package rollup

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"metabib/internal/fileutil"
)

const NewArchiveExitCode = 2

type Options struct {
	ArchiveDir  string
	UpdateDirs  []string
	SizeBytes   int64
	ValidateCRC bool
	Log         *zap.Logger
}

type Result struct {
	Updates           int
	Finalized         int
	ActiveMerge       string
	FinalizedArchives []string
}

type archive struct {
	dir   string
	info  os.FileInfo
	begin int
	end   int
}

type byName []archive

func (a byName) Len() int { return len(a) }

func (a byName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

func (a byName) Less(i, j int) bool { return a[i].info.Name() < a[j].info.Name() }

type copyEntryFunc func(*zip.Writer, *zip.File) error

func Run(ctx context.Context, opts Options) (Result, error) {
	return run(ctx, opts, (*zip.Writer).Copy)
}

func run(ctx context.Context, opts Options, copyEntry copyEntryFunc) (Result, error) {
	if opts.ArchiveDir == "" {
		return Result{}, errors.New("archive directory is required")
	}
	if opts.SizeBytes <= 0 {
		return Result{}, errors.New("archive size must be positive")
	}
	archiveDir, err := filepath.Abs(opts.ArchiveDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve archive directory: %w", err)
	}
	if err := os.MkdirAll(archiveDir, 0o777); err != nil {
		return Result{}, fmt.Errorf("create archive directory %q: %w", archiveDir, err)
	}
	updateDirs, err := updateDirectories(archiveDir, opts.UpdateDirs)
	if err != nil {
		return Result{}, err
	}
	allFiles, err := collectArchives(append([]string{archiveDir}, updateDirs...))
	if err != nil {
		return Result{}, err
	}
	sort.Sort(byName(allFiles))

	last, err := getLastArchive(allFiles)
	if err != nil {
		return Result{}, err
	}
	if last.info == nil {
		last.dir = archiveDir
		if opts.Log != nil {
			opts.Log.Info("No finalized archive found; using archive directory", zap.String("directory", archiveDir))
		}
	} else if opts.Log != nil {
		opts.Log.Info(
			"Last archive detected",
			zap.String("file", filepath.Join(last.dir, last.info.Name())),
			zap.Int("begin", last.begin),
			zap.Int("end", last.end),
			zap.Int64("size", last.info.Size()),
		)
	}

	merge, err := getMergeArchive(allFiles)
	if err != nil {
		return Result{}, err
	}
	if merge.info != nil {
		if merge.begin < last.begin || (merge.begin > last.begin && merge.begin <= last.end) || merge.end < last.end {
			return Result{}, fmt.Errorf(
				"merge archive (%s) and last archive (%s) do not match",
				filepath.Join(merge.dir, merge.info.Name()),
				filepath.Join(last.dir, last.info.Name()),
			)
		}
		if opts.Log != nil {
			opts.Log.Info(
				"Merge archive detected",
				zap.String("file", filepath.Join(merge.dir, merge.info.Name())),
				zap.Int("begin", merge.begin),
				zap.Int("end", merge.end),
				zap.Int64("size", merge.info.Size()),
			)
		}
	} else {
		merge.begin = last.begin
		merge.end = last.end
	}

	updates, err := getUpdates(allFiles, merge.end)
	if err != nil {
		return Result{}, err
	}
	if len(updates) == 0 {
		if opts.Log != nil {
			opts.Log.Info("No archive updates found")
		}
		return Result{}, nil
	}
	if opts.Log != nil {
		opts.Log.Info("Archive updates found", zap.Int("updates", len(updates)))
		for _, update := range updates {
			fields := []zap.Field{
				zap.String("file", filepath.Join(update.dir, update.info.Name())),
				zap.Int("begin", update.begin),
				zap.Int("end", update.end),
			}
			if update.begin <= merge.end {
				opts.Log.Warn("Overlapping archive update selected", append(fields, zap.Int("existing_end", merge.end))...)
				continue
			}
			opts.Log.Debug("Archive update selected", fields...)
		}
	}

	return processUpdates(ctx, opts, last, merge, updates, archiveNameWidth(last, merge), copyEntry)
}

func processUpdates(
	ctx context.Context,
	opts Options,
	last archive,
	merge archive,
	updates []archive,
	nameWidth int,
	copyEntry copyEntryFunc,
) (Result, error) {
	format := fmt.Sprintf("fb2-%%0%dd-%%0%dd", nameWidth, nameWidth)
	res := Result{Updates: len(updates)}
	work, err := openWorkArchive(opts, last, merge, &updates)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if work != nil {
			work.cleanup()
		}
	}()
	firstUpdateIsExisting := work.firstUpdateIsExisting

	leftBytes := opts.SizeBytes - work.existingSize
	firstBook := work.firstBook
	lastBook := work.lastBook
	existingEnd := merge.end
	copiedNewEntry := false
	activeWorkIDs := make(map[int]struct{})
	for updateIndex, update := range updates {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		updatePath := filepath.Join(update.dir, update.info.Name())
		updateIsExisting := updateIndex == 0 && firstUpdateIsExisting
		rc, err := zip.OpenReader(updatePath)
		if err != nil {
			return Result{}, fmt.Errorf("open update archive %q: %w", updatePath, err)
		}
		if opts.Log != nil {
			opts.Log.Info("Processing update archive", zap.String("file", updatePath))
		}
		files := slices.SortedFunc(slices.Values(rc.File), func(a, b *zip.File) int {
			return nameToID(a.FileInfo().Name()) - nameToID(b.FileInfo().Name())
		})
		for _, file := range files {
			if file.FileInfo().Size() == 0 {
				if opts.Log != nil {
					opts.Log.Warn("Skipping empty archive entry", zap.String("file", updatePath), zap.String("entry", file.Name))
				}
				continue
			}
			id := nameToID(file.FileInfo().Name())
			if id <= 0 {
				if opts.Log != nil {
					opts.Log.Warn("Skipping entry with non-numeric name", zap.String("file", updatePath), zap.String("entry", file.Name))
				}
				continue
			}
			if !updateIsExisting && id <= existingEnd {
				if opts.Log != nil {
					opts.Log.Warn(
						"Skipping already finalized archive entry from overlapping update",
						zap.String("file", updatePath),
						zap.String("entry", file.Name),
						zap.Int("book_id", id),
						zap.Int("existing_end", existingEnd),
					)
				}
				continue
			}
			if !updateIsExisting {
				if _, ok := activeWorkIDs[id]; ok {
					if opts.Log != nil {
						opts.Log.Warn(
							"Skipping duplicate archive entry from overlapping update",
							zap.String("file", updatePath),
							zap.String("entry", file.Name),
							zap.Int("book_id", id),
						)
					}
					continue
				}
			}
			if opts.ValidateCRC {
				if err := validateEntryCRC(file); err != nil {
					return Result{}, errors.Join(
						fmt.Errorf("validate CRC for update archive entry %q from %q: %w", file.Name, updatePath, err),
						closeUpdateArchive(rc, updatePath),
					)
				}
			}
			if err := copyEntry(work.writer, file); err != nil {
				return Result{}, errors.Join(
					fmt.Errorf("copy update archive entry %q from %q: %w", file.Name, updatePath, err),
					closeUpdateArchive(rc, updatePath),
				)
			}
			if firstBook == 0 || id < firstBook {
				firstBook = id
			}
			if id > lastBook {
				lastBook = id
			}
			if !updateIsExisting {
				activeWorkIDs[id] = struct{}{}
				copiedNewEntry = true
				leftBytes -= int64(file.CompressedSize64)
			}
			if leftBytes <= 0 {
				finalName := filepath.Join(last.dir, fmt.Sprintf(format+".zip", firstBook, lastBook))
				if err := work.finishAs(finalName); err != nil {
					rc.Close()
					return Result{}, err
				}
				res.Finalized++
				res.FinalizedArchives = append(res.FinalizedArchives, finalName)
				if opts.Log != nil {
					opts.Log.Info("Archive finalized", zap.String("file", finalName), zap.Int("begin", firstBook), zap.Int("end", lastBook))
				}
				lastInfo, err := os.Stat(finalName)
				if err != nil {
					rc.Close()
					return Result{}, fmt.Errorf("stat finalized archive %q: %w", finalName, err)
				}
				last = archive{dir: filepath.Dir(finalName), info: lastInfo, begin: firstBook, end: lastBook}
				existingEnd = last.end
				work, err = createNewWorkArchive(last.dir)
				if err != nil {
					rc.Close()
					return Result{}, err
				}
				leftBytes = opts.SizeBytes
				firstBook = 0
				lastBook = 0
				copiedNewEntry = false
				activeWorkIDs = make(map[int]struct{})
			}
		}
		if err := rc.Close(); err != nil {
			return Result{}, fmt.Errorf("close update archive %q: %w", updatePath, err)
		}
	}

	if firstBook == 0 || !copiedNewEntry {
		if err := work.remove(); err != nil {
			return Result{}, err
		}
	} else {
		mergeName := filepath.Join(last.dir, fmt.Sprintf(format+".merging", firstBook, lastBook))
		if err := work.finishAs(mergeName); err != nil {
			return Result{}, err
		}
		res.ActiveMerge = mergeName
		if opts.Log != nil {
			opts.Log.Info("Merge archive updated", zap.String("file", mergeName), zap.Int("begin", firstBook), zap.Int("end", lastBook))
		}
	}
	return res, nil
}

func validateEntryCRC(file *zip.File) error {
	r, err := file.Open()
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, r)
	return errors.Join(readErr, r.Close())
}

func closeUpdateArchive(rc *zip.ReadCloser, path string) error {
	if err := rc.Close(); err != nil {
		return fmt.Errorf("close update archive %q: %w", path, err)
	}
	return nil
}

type workArchive struct {
	file                  *os.File
	writer                *zip.Writer
	path                  string
	closed                bool
	cleanupTemp           bool
	removeOnCleanup       bool
	existingSize          int64
	firstBook             int
	lastBook              int
	oldPath               string
	firstUpdateIsExisting bool
}

func openWorkArchive(opts Options, last archive, merge archive, updates *[]archive) (*workArchive, error) {
	if merge.info != nil {
		return rewriteExistingMergeArchive(filepath.Join(merge.dir, merge.info.Name()), merge.begin, merge.end, merge.info.Size())
	}
	work, err := createNewWorkArchive(last.dir)
	if err != nil {
		return nil, err
	}
	if last.info != nil && opts.SizeBytes-last.info.Size() > 0 {
		lastPath := filepath.Join(last.dir, last.info.Name())
		if opts.Log != nil {
			opts.Log.Info("Merging last archive", zap.String("file", lastPath))
		}
		mergedUpdates := make([]archive, len(*updates)+1)
		mergedUpdates[0] = last
		copy(mergedUpdates[1:], *updates)
		*updates = mergedUpdates
		work.oldPath = lastPath
		work.firstBook = last.begin
		work.lastBook = last.end
		work.firstUpdateIsExisting = true
	}
	return work, nil
}

func createNewWorkArchive(dir string) (*workArchive, error) {
	f, err := os.CreateTemp(dir, "rollup-")
	if err != nil {
		return nil, fmt.Errorf("create temporary archive in %q: %w", dir, err)
	}
	return &workArchive{file: f, writer: zip.NewWriter(f), path: f.Name(), cleanupTemp: true, removeOnCleanup: true}, nil
}

func rewriteExistingMergeArchive(path string, firstBook int, lastBook int, existingSize int64) (*workArchive, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open merge archive %q: %w", path, err)
	}
	defer reader.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), "rollup-")
	if err != nil {
		return nil, fmt.Errorf("create temporary archive in %q: %w", filepath.Dir(path), err)
	}
	work := &workArchive{
		file:            tmp,
		writer:          zip.NewWriter(tmp),
		path:            tmp.Name(),
		cleanupTemp:     true,
		removeOnCleanup: true,
		existingSize:    existingSize,
		firstBook:       firstBook,
		lastBook:        lastBook,
		oldPath:         path,
	}
	for _, file := range reader.File {
		if err := work.writer.Copy(file); err != nil {
			work.cleanup()
			return nil, fmt.Errorf("copy existing merge entry %q: %w", file.Name, err)
		}
	}
	return work, nil
}

func (w *workArchive) close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	var errs []error
	if w.writer != nil {
		errs = append(errs, w.writer.Close())
	}
	if w.file != nil {
		errs = append(errs, w.file.Close())
	}
	return errors.Join(errs...)
}

func (w *workArchive) finishAs(path string) error {
	if err := w.close(); err != nil {
		return fmt.Errorf("finish archive %q: %w", w.path, err)
	}
	if err := fileutil.ReplaceOutputFile(w.path, path); err != nil {
		return fmt.Errorf("rename archive %q to %q: %w", w.path, path, err)
	}
	if w.oldPath != "" && w.oldPath != path {
		if err := os.Remove(w.oldPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old merge archive %q: %w", w.oldPath, err)
		}
	}
	w.removeOnCleanup = false
	return nil
}

func (w *workArchive) remove() error {
	if err := w.close(); err != nil {
		return fmt.Errorf("finish empty archive %q: %w", w.path, err)
	}
	w.removeOnCleanup = false
	if err := os.Remove(w.path); err != nil {
		return fmt.Errorf("remove empty archive %q: %w", w.path, err)
	}
	return nil
}

func (w *workArchive) cleanup() {
	_ = w.close()
	if w.cleanupTemp && w.removeOnCleanup && w.path != "" {
		_ = os.Remove(w.path)
	}
}

func updateDirectories(archiveDir string, updateDirs []string) ([]string, error) {
	if len(updateDirs) == 0 {
		return []string{archiveDir}, nil
	}
	res := make([]string, 0, len(updateDirs))
	for _, dir := range updateDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve update directory %q: %w", dir, err)
		}
		res = append(res, abs)
	}
	return res, nil
}

func collectArchives(dirs []string) ([]archive, error) {
	seen := make(map[string]struct{}, len(dirs))
	var archives []archive
	for _, dir := range dirs {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read directory %q: %w", dir, err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("read file info %q: %w", filepath.Join(dir, entry.Name()), err)
			}
			archives = append(archives, archive{dir: dir, info: info})
		}
	}
	return archives, nil
}

var (
	archiveNameRE        = regexp.MustCompile(`(?i)^fb2-([0-9]+)-([0-9]+)\.zip$`)
	mergeNameRE          = regexp.MustCompile(`(?i)^fb2-([0-9]+)-([0-9]+)\.merging$`)
	updateNameRE         = regexp.MustCompile(`(?i)^f(?:\.fb2)?\.([0-9]+)-([0-9]+)\.zip$`)
	librusecUpdateNameRE = regexp.MustCompile(`(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.fb2\.zip$`)
	localNameRE          = regexp.MustCompile(`(?i)^fb2-([0-9]+)-([0-9]+)\.(?:zip|merging)$`)
)

func archiveNameWidth(last archive, merge archive) int {
	for _, item := range []archive{merge, last} {
		if item.info == nil {
			continue
		}
		match := localNameRE.FindStringSubmatch(item.info.Name())
		if len(match) >= 3 {
			return max(len(match[1]), len(match[2]))
		}
	}
	return 10
}

func getLastArchive(files []archive) (archive, error) {
	var res archive
	for _, file := range files {
		ok, first, second, err := dissect(archiveNameRE, file.info.Name())
		if err != nil {
			return archive{}, err
		}
		if ok && res.end < second {
			res = archive{dir: file.dir, info: file.info, begin: first, end: second}
		}
	}
	return res, nil
}

func getMergeArchive(files []archive) (archive, error) {
	var res archive
	var count int
	for _, file := range files {
		ok, first, second, err := dissect(mergeNameRE, file.info.Name())
		if err != nil {
			return archive{}, err
		}
		if ok {
			res = archive{dir: file.dir, info: file.info, begin: first, end: second}
			count++
		}
	}
	if count > 1 {
		return archive{}, errors.New("there could only be single merge archive")
	}
	return res, nil
}

func getUpdates(files []archive, last int) ([]archive, error) {
	updates := make([]archive, 0)
	for _, file := range files {
		ok, first, second, err := dissectUpdateName(file.info.Name())
		if err != nil {
			return nil, err
		}
		if ok && last < second {
			updates = append(updates, archive{dir: file.dir, info: file.info, begin: first, end: second})
		}
	}
	return updates, nil
}

func dissectUpdateName(name string) (bool, int, int, error) {
	for _, re := range []*regexp.Regexp{updateNameRE, librusecUpdateNameRE} {
		ok, first, second, err := dissect(re, name)
		if err != nil || ok {
			return ok, first, second, err
		}
	}
	return false, 0, 0, nil
}

func dissect(re *regexp.Regexp, name string) (bool, int, int, error) {
	match := re.FindStringSubmatch(name)
	if match == nil {
		return false, 0, 0, nil
	}
	first, err := strconv.Atoi(match[1])
	if err != nil {
		return true, 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	second, err := strconv.Atoi(match[2])
	if err != nil {
		return true, 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	return true, first, second, nil
}

func nameToID(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	id, err := strconv.Atoi(base)
	if err != nil {
		return -1
	}
	return id
}

func countZipEntries(path string) (int, error) {
	rc, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	for _, file := range rc.File {
		reader, err := file.Open()
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		if err != nil {
			return 0, err
		}
		if closeErr != nil {
			return 0, closeErr
		}
	}
	return len(rc.File), nil
}
