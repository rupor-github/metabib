package fetch

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	regexp2 "github.com/dlclark/regexp2/v2"
	"go.uber.org/zap"

	"metabib/config"
	"metabib/internal/fileutil"
	"metabib/misc"
)

const NewArchivesExitCode = 2

const profilePatternTimeout = time.Second

const (
	archiveFamilyFB2 = "fb2"
	archiveFamilyUSR = "usr"
)

type Options struct {
	Library       config.FetchLibraryConfig
	ArchiveDir    string
	SQLDir        string
	NoArchives    bool
	DownloadSQL   bool
	Retry         int
	Timeout       time.Duration
	ChunkSize     int64
	Continue      bool
	Sticky        bool
	Verbose       bool
	Log           *zap.Logger
	UserAgentName string
}

type Result struct {
	LibraryName string
	LastBookID  int
	Archives    int
	SQLTables   int
	SQLDir      string
}

type archiveHighWater struct {
	Begin int
	End   int
	Daily bool
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Retry < 1 {
		return Result{}, fmt.Errorf("retry must be at least 1")
	}
	if opts.Timeout <= 0 {
		return Result{}, fmt.Errorf("timeout must be positive")
	}
	if opts.ChunkSize <= 0 {
		return Result{}, fmt.Errorf("chunk size must be positive")
	}
	if opts.NoArchives && !opts.DownloadSQL {
		return Result{}, errors.New("nothing to fetch: --noarchives and --nosql cannot be used together")
	}
	if !opts.NoArchives && opts.ArchiveDir == "" {
		return Result{}, errors.New("archive output directory is required")
	}

	var err error
	archiveDir := ""
	if !opts.NoArchives {
		archiveDir, err = filepath.Abs(opts.ArchiveDir)
		if err != nil {
			return Result{}, fmt.Errorf("resolve archive output directory: %w", err)
		}
	}
	libraryName := opts.Library.LibraryName
	if libraryName == "" {
		libraryName = opts.Library.Name
	}
	sqlDir := opts.SQLDir
	if sqlDir == "" {
		sqlDir = libraryName + "_" + time.Now().UTC().Format("20060102_150405")
	}
	sqlDir, err = filepath.Abs(sqlDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve SQL output directory: %w", err)
	}

	client, err := newHTTPClient(opts)
	if err != nil {
		return Result{}, err
	}
	defer client.CloseIdleConnections()
	f := fetcher{opts: opts, client: client, userAgent: userAgent(opts)}

	highWater := map[string]archiveHighWater{archiveFamilyFB2: {}, archiveFamilyUSR: {}}
	if !opts.NoArchives {
		if err := os.MkdirAll(archiveDir, 0o777); err != nil {
			return Result{}, fmt.Errorf("create archive output directory %q: %w", archiveDir, err)
		}
		var err error
		highWater, err = getArchiveHighWater(archiveDir)
		if err != nil {
			return Result{}, err
		}
	}
	if opts.Log != nil {
		opts.Log.Info(
			"Processing fetch profile",
			zap.String("library", libraryName),
			zap.String("profile", opts.Library.Name),
			zap.String("archive_content", archiveContent(opts.Library)),
			zap.Int("fb2_last_book_id", highWater[archiveFamilyFB2].End),
			zap.Int("usr_last_book_id", highWater[archiveFamilyUSR].End),
		)
	}

	archiveLinks := []string(nil)
	if !opts.NoArchives {
		var err error
		archiveLinks, err = f.archiveLinks(ctx, opts.Library.ArchiveURL, opts.Library.ArchivePattern, archiveDir, highWater)
		if err != nil {
			return Result{}, err
		}
		if err := f.files(ctx, archiveLinks, opts.Library.ArchiveURL, archiveDir); err != nil {
			return Result{}, err
		}
	}

	res := Result{
		LibraryName: libraryName,
		LastBookID:  max(highWater[archiveFamilyFB2].End, highWater[archiveFamilyUSR].End),
		Archives:    len(archiveLinks),
		SQLDir:      sqlDir,
	}
	if opts.Log != nil && !opts.NoArchives {
		opts.Log.Info("Archive fetch completed", zap.Int("archives", res.Archives), zap.String("directory", archiveDir))
	}
	if !opts.DownloadSQL {
		return res, nil
	}

	sqlLinks, err := f.links(ctx, opts.Library.SQLURL, opts.Library.SQLPattern)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(sqlDir, 0o777); err != nil {
		return Result{}, fmt.Errorf("create SQL output directory %q: %w", sqlDir, err)
	}
	if err := f.files(ctx, sqlLinks, opts.Library.SQLURL, sqlDir); err != nil {
		return Result{}, err
	}
	res.SQLTables = len(sqlLinks)
	if opts.Log != nil {
		opts.Log.Info("SQL fetch completed", zap.Int("tables", res.SQLTables), zap.String("directory", sqlDir))
	}
	return res, nil
}

type fetcher struct {
	opts      Options
	client    *http.Client
	userAgent string
}

func (f fetcher) archiveLinks(
	ctx context.Context, baseURL string, pattern string, dest string, highWater map[string]archiveHighWater,
) ([]string, error) {
	links, err := f.links(ctx, baseURL, pattern)
	if err != nil {
		return nil, err
	}
	content := archiveContent(f.opts.Library)
	selected := make([]string, 0, len(links))
	for _, link := range links {
		family, _, second, ok, err := classifyUpdateName(link)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("archive pattern selected unsupported update name %q", link)
		}
		if !archiveContentAccepts(content, family) {
			return nil, fmt.Errorf("archive pattern selected %s update %q for %s profile", family, link, content)
		}
		last := highWater[family]
		if last.End < second || (last.End == second && last.Daily && !fileExists(filepath.Join(dest, link))) {
			selected = append(selected, link)
		}
	}
	return selected, nil
}

func (f fetcher) links(ctx context.Context, baseURL string, pattern string) ([]string, error) {
	body, err := f.fetchString(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return nil, fmt.Errorf("compile link pattern: %w", err)
	}
	re.MatchTimeout = profilePatternTimeout
	match, err := re.FindStringMatch(body)
	if err != nil {
		return nil, fmt.Errorf("match links at %s: %w", baseURL, err)
	}
	if match == nil {
		return nil, fmt.Errorf("no suitable links found at %s", baseURL)
	}
	links := make([]string, 0)
	for match != nil {
		if match.GroupCount() < 2 {
			return nil, fmt.Errorf("link pattern must capture file name as first group")
		}
		links = append(links, match.GroupByNumber(1).String())
		match, err = re.FindNextMatch(match)
		if err != nil {
			return nil, fmt.Errorf("match links at %s: %w", baseURL, err)
		}
	}
	return links, nil
}

func (f fetcher) fetchString(ctx context.Context, sourceURL string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= f.opts.Retry; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if f.opts.Log != nil {
			f.opts.Log.Info("Downloading index", zap.String("url", sourceURL), zap.Int("attempt", attempt), zap.Int("attempts", f.opts.Retry))
		}
		resp, err := f.request(ctx, http.MethodGet, sourceURL, 0)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr == nil && closeErr == nil {
				return string(body), nil
			}
			if readErr != nil {
				err = readErr
			} else {
				err = closeErr
			}
		}
		lastErr = err
		if !retryable(err) {
			break
		}
		if f.opts.Log != nil {
			f.opts.Log.Warn("Downloading index failed", zap.String("url", sourceURL), zap.Int("attempt", attempt),
				zap.Int("attempts", f.opts.Retry), zap.Error(err))
		}
		if attempt < f.opts.Retry {
			if err := f.waitRetry(ctx, attempt, lastErr); err != nil {
				return "", err
			}
		}
	}
	return "", fmt.Errorf("download index %q: %w", sourceURL, lastErr)
}

func (f fetcher) files(ctx context.Context, files []string, baseURL string, dest string) error {
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.file(ctx, file, baseURL, dest); err != nil {
			return err
		}
	}
	return nil
}

func getLastBookID(path string) (int, error) {
	highWater, err := getArchiveHighWater(path)
	if err != nil {
		return 0, err
	}
	return highWater[archiveFamilyFB2].End, nil
}

func getArchiveHighWater(path string) (map[string]archiveHighWater, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read archive directory %q: %w", path, err)
	}
	highWater := map[string]archiveHighWater{archiveFamilyFB2: {}, archiveFamilyUSR: {}}
	mergeBegin := map[string]int{archiveFamilyFB2: 0, archiveFamilyUSR: 0}
	mergeEnd := map[string]int{archiveFamilyFB2: 0, archiveFamilyUSR: 0}
	mergeCount := map[string]int{archiveFamilyFB2: 0, archiveFamilyUSR: 0}
	for _, entry := range entries {
		name := entry.Name()
		family, first, second, daily, ok, err := classifyArchiveRangeName(name)
		if err != nil {
			return nil, err
		}
		if ok && highWater[family].End < second {
			highWater[family] = archiveHighWater{Begin: first, End: second, Daily: daily}
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if family, first, second, ok, err := classifyMergeName(name); err != nil {
			return nil, err
		} else if ok {
			mergeBegin[family] = first
			mergeEnd[family] = second
			mergeCount[family]++
		}
	}
	for _, family := range []string{archiveFamilyFB2, archiveFamilyUSR} {
		if mergeCount[family] > 1 {
			return nil, fmt.Errorf("there could only be single %s merge archive", family)
		}
		if mergeCount[family] == 0 {
			continue
		}
		last := highWater[family]
		if mergeBegin[family] < last.Begin ||
			(mergeBegin[family] > last.Begin && mergeBegin[family] <= last.End) ||
			mergeEnd[family] < last.End {
			return nil, fmt.Errorf(
				"%s merge (%d:%d) and last (%d:%d) archive do not match",
				family,
				mergeBegin[family],
				mergeEnd[family],
				last.Begin,
				last.End,
			)
		}
		highWater[family] = archiveHighWater{Begin: mergeBegin[family], End: mergeEnd[family]}
	}
	return highWater, nil
}

var (
	mergeNameRE         = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.merging$`)
	localRangeRE        = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.zip$`)
	plainFB2UpdateRE    = regexp.MustCompile(`(?i)^([0-9]+)-([0-9]+)\.zip$`)
	flibustaFB2UpdateRE = regexp.MustCompile(`(?i)^f(?:\.fb2)?\.([0-9]+)-([0-9]+)\.zip$`)
	flibustaAnyUpdateRE = regexp.MustCompile(`(?i)^f\.([^.]+)\.([0-9]+)-([0-9]+)\.zip$`)
	librusecAnyUpdateRE = regexp.MustCompile(`(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.([^.]+)\.zip$`)
)

func classifyMergeName(name string) (string, int, int, bool, error) {
	match := mergeNameRE.FindStringSubmatch(name)
	if match == nil || !knownArchiveFamily(match[1]) {
		return "", 0, 0, false, nil
	}
	first, second, err := parseRange(match[2], match[3], name)
	return strings.ToLower(match[1]), first, second, true, err
}

func classifyArchiveRangeName(name string) (string, int, int, bool, bool, error) {
	if family, first, second, ok, err := classifyLocalRangeName(name); ok || err != nil {
		return family, first, second, false, ok, err
	}
	family, first, second, ok, err := classifyUpdateName(name)
	return family, first, second, true, ok, err
}

func classifyRangeName(name string) (string, int, int, bool, error) {
	family, first, second, _, ok, err := classifyArchiveRangeName(name)
	return family, first, second, ok, err
}

func classifyLocalRangeName(name string) (string, int, int, bool, error) {
	match := localRangeRE.FindStringSubmatch(name)
	if match == nil || !knownArchiveFamily(match[1]) {
		return "", 0, 0, false, nil
	}
	first, second, err := parseRange(match[2], match[3], name)
	return strings.ToLower(match[1]), first, second, true, err
}

func classifyUpdateName(name string) (string, int, int, bool, error) {
	if match := plainFB2UpdateRE.FindStringSubmatch(name); match != nil {
		first, second, err := parseRange(match[1], match[2], name)
		return archiveFamilyFB2, first, second, true, err
	}
	if match := flibustaFB2UpdateRE.FindStringSubmatch(name); match != nil {
		first, second, err := parseRange(match[1], match[2], name)
		return archiveFamilyFB2, first, second, true, err
	}
	if match := flibustaAnyUpdateRE.FindStringSubmatch(name); match != nil {
		if strings.EqualFold(match[1], archiveFamilyFB2) {
			first, second, err := parseRange(match[2], match[3], name)
			return archiveFamilyFB2, first, second, true, err
		}
		first, second, err := parseRange(match[2], match[3], name)
		return archiveFamilyUSR, first, second, true, err
	}
	match := librusecAnyUpdateRE.FindStringSubmatch(name)
	if match == nil {
		return "", 0, 0, false, nil
	}
	family := archiveFamilyUSR
	if strings.EqualFold(match[3], archiveFamilyFB2) {
		family = archiveFamilyFB2
	}
	first, second, err := parseRange(match[1], match[2], name)
	return family, first, second, true, err
}

func dissectRange(name string) (bool, int, int, error) {
	_, first, second, ok, err := classifyRangeName(name)
	return ok, first, second, err
}

func parseRange(firstText string, secondText string, name string) (int, int, error) {
	first, err := strconv.Atoi(firstText)
	if err != nil {
		return 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	second, err := strconv.Atoi(secondText)
	if err != nil {
		return 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	return first, second, nil
}

func knownArchiveFamily(family string) bool {
	family = strings.ToLower(family)
	return family == archiveFamilyFB2 || family == archiveFamilyUSR
}

func archiveContent(lib config.FetchLibraryConfig) string {
	if lib.ArchiveContent == "" {
		return archiveFamilyFB2
	}
	return strings.ToLower(lib.ArchiveContent)
}

func archiveContentAccepts(content string, family string) bool {
	return content == "all" || content == family
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func joinURL(baseURL string, file string) string {
	if strings.HasSuffix(baseURL, "/") {
		return baseURL + file
	}
	return baseURL + "/" + file
}

func processFile(tmp string, file string) error {
	switch ext := strings.ToLower(filepath.Ext(file)); ext {
	case ".zip":
		if err := checkZip(tmp); err != nil {
			return invalidDownloadError{err: err}
		}
		return copyFileContents(tmp, file)
	case ".gz":
		return ungzipFile(tmp, strings.TrimSuffix(file, ext))
	default:
		return fmt.Errorf("unknown downloaded file extension %q", ext)
	}
}

type invalidDownloadError struct {
	err error
}

func (e invalidDownloadError) Error() string { return e.err.Error() }

func (e invalidDownloadError) Unwrap() error { return e.err }

func checkZip(file string) error {
	r, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("check zip %q: %w", file, err)
	}
	defer r.Close()
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("check zip entry %q: %w", f.Name, err)
		}
		_, readErr := io.Copy(io.Discard, rc)
		if err := errors.Join(readErr, rc.Close()); err != nil {
			return fmt.Errorf("check zip entry %q: %w", f.Name, err)
		}
	}
	return nil
}

func copyFileContents(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return publishDownload(in, dst)
}

func ungzipFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open gzip %q: %w", src, err)
	}
	defer in.Close()
	r, err := gzip.NewReader(in)
	if err != nil {
		return invalidDownloadError{err: fmt.Errorf("read gzip %q: %w", src, err)}
	}
	defer r.Close()
	err = publishDownload(r, dst)
	if errors.Is(err, gzip.ErrChecksum) || errors.Is(err, gzip.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF) {
		return invalidDownloadError{err: err}
	}
	return err
}

func publishDownload(in io.Reader, dst string) error {
	out, err := fileutil.CreateHiddenTemp(filepath.Dir(dst), filepath.Base(dst))
	if err != nil {
		return fmt.Errorf("create output for %q: %w", dst, err)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(out.Name())
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("write downloaded file %q: %w", dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync downloaded file %q: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close downloaded file %q: %w", dst, err)
	}
	if err := fileutil.ReplaceOutputFile(out.Name(), dst); err != nil {
		return fmt.Errorf("publish downloaded file %q: %w", dst, err)
	}
	return nil
}

func userAgent(opts Options) string {
	name := opts.UserAgentName
	if name == "" {
		name = misc.GetAppName()
	}
	version := misc.GetVersion()
	if opts.Library.UserAgentSuffix != "" {
		return fmt.Sprintf("%s/%s %s", name, version, opts.Library.UserAgentSuffix)
	}
	return fmt.Sprintf("%s/%s", name, version)
}
