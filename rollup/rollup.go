package rollup

import (
	"archive/zip"
	"context"
	"encoding/json"
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
	"time"

	regexp2 "github.com/dlclark/regexp2/v2"
	"go.uber.org/zap"

	"metabib/internal/fileutil"
)

const NewArchiveExitCode = 2

const (
	updatePatternTimeout = time.Second
	stateFileName        = "rollup-state.json"
)

type FinalizationPolicy string

const (
	FinalizationPolicySize     FinalizationPolicy = "size"
	FinalizationPolicyRolling  FinalizationPolicy = "rolling"
	FinalizationPolicyCalendar FinalizationPolicy = "calendar"
)

type CalendarBucket string

const (
	CalendarBucketISOWeek   CalendarBucket = "iso-week"
	CalendarBucketISOBiweek CalendarBucket = "iso-biweek"
	CalendarBucketMonth     CalendarBucket = "month"
)

type FinalizationReason string

const (
	FinalizationReasonSize              FinalizationReason = "size"
	FinalizationReasonRollingDeadline   FinalizationReason = "rolling_deadline"
	FinalizationReasonCalendarBucketEnd FinalizationReason = "calendar_bucket_end"
)

type FinalizationOptions struct {
	Policy          FinalizationPolicy
	RollingDuration time.Duration
	RollingText     string
	CalendarBucket  CalendarBucket
}

type Options struct {
	ArchiveDir      string
	UpdateDirs      []string
	TargetSizeBytes map[string]int64
	Finalization    FinalizationOptions
	ValidateCRC     bool
	UpdatePatterns  []UpdatePattern
	Now             func() time.Time
	Log             *zap.Logger
}

type UpdatePattern struct {
	Name    string
	Family  string
	Pattern string
}

type Result struct {
	Updates           int
	Finalized         int
	ActiveMerge       string
	ActiveMerges      []string
	FinalizedArchives []string
}

type archiveFamily struct {
	Name   string
	Prefix string
}

type compiledUpdatePattern struct {
	Name    string
	Family  archiveFamily
	Pattern string
	re      *regexp2.Regexp
}

type archive struct {
	dir   string
	info  os.FileInfo
	begin int
	end   int
}

type rollupState struct {
	Version  int                           `json:"version"`
	Lineages map[string]rollupLineageState `json:"lineages,omitempty"`
}

type rollupLineageState struct {
	ActiveMerge string             `json:"active_merge"`
	FirstBook   int                `json:"first_book"`
	LastBook    int                `json:"last_book"`
	Policy      FinalizationPolicy `json:"policy"`
	Rolling     string             `json:"rolling,omitempty"`
	StartedAt   *time.Time         `json:"started_at,omitempty"`
	Deadline    *time.Time         `json:"deadline,omitempty"`
	Calendar    CalendarBucket     `json:"calendar,omitempty"`
	BucketStart *time.Time         `json:"bucket_start,omitempty"`
	BucketEnd   *time.Time         `json:"bucket_end,omitempty"`
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
	finalization := normalizeFinalization(opts.Finalization)
	if err := validateFinalizationOptions(finalization, opts.TargetSizeBytes); err != nil {
		return Result{}, err
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
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
	updatePatterns, err := compileUpdatePatterns(opts.UpdatePatterns)
	if err != nil {
		return Result{}, err
	}
	var state *rollupState
	statePath := filepath.Join(archiveDir, stateFileName)
	if finalization.Policy != FinalizationPolicySize {
		state, err = loadRollupState(statePath)
		if err != nil {
			return Result{}, err
		}
	}

	var res Result
	for _, family := range archiveFamilies() {
		familyRes, err := runFamily(ctx, opts, archiveDir, allFiles, family, updatePatterns, finalization, statePath, state, now, copyEntry)
		if err != nil {
			return Result{}, err
		}
		res.Updates += familyRes.Updates
		res.Finalized += familyRes.Finalized
		res.FinalizedArchives = append(res.FinalizedArchives, familyRes.FinalizedArchives...)
		if familyRes.ActiveMerge != "" {
			if res.ActiveMerge == "" {
				res.ActiveMerge = familyRes.ActiveMerge
			}
			res.ActiveMerges = append(res.ActiveMerges, familyRes.ActiveMerge)
		}
	}
	return res, nil
}

func runFamily(
	ctx context.Context,
	opts Options,
	archiveDir string,
	allFiles []archive,
	family archiveFamily,
	updatePatterns []compiledUpdatePattern,
	finalization FinalizationOptions,
	statePath string,
	state *rollupState,
	now func() time.Time,
	copyEntry copyEntryFunc,
) (Result, error) {
	targetSize := opts.TargetSizeBytes[family.Name]
	last, err := getLastArchive(allFiles, family)
	if err != nil {
		return Result{}, err
	}
	if last.info == nil {
		last.dir = archiveDir
		if opts.Log != nil {
			opts.Log.Info("No finalized archive found; using archive directory", zap.String("family", family.Name), zap.String("directory", archiveDir))
		}
	} else if opts.Log != nil {
		opts.Log.Info(
			"Last archive detected",
			zap.String("family", family.Name),
			zap.String("file", filepath.Join(last.dir, last.info.Name())),
			zap.Int("begin", last.begin),
			zap.Int("end", last.end),
			zap.Int64("size", last.info.Size()),
		)
	}

	merge, err := getMergeArchive(allFiles, family)
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
				zap.String("family", family.Name),
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
	var res Result
	var lineageState *rollupLineageState
	if finalization.Policy != FinalizationPolicySize {
		lineageState, err = validatePeriodState(state, family, merge, finalization)
		if err != nil {
			return Result{}, err
		}
		if merge.info == nil && removeRollupLineageState(state, family.Name) {
			if err := saveRollupState(statePath, state); err != nil {
				return Result{}, err
			}
		}
		if merge.info != nil && periodExpired(*lineageState, now()) {
			nameWidth := archiveNameWidth(last, merge, family)
			finalized, err := finalizeMergeArchive(merge, family, nameWidth, finalization.Policy, finalizationReason(finalization), opts.Log)
			if err != nil {
				return Result{}, err
			}
			removeRollupLineageState(state, family.Name)
			if err := saveRollupState(statePath, state); err != nil {
				return Result{}, err
			}
			res.Finalized++
			res.FinalizedArchives = append(res.FinalizedArchives, filepath.Join(finalized.dir, finalized.info.Name()))
			last = finalized
			merge = archive{begin: last.begin, end: last.end}
			lineageState = nil
		}
	}

	updates, err := getUpdates(allFiles, merge.end, family, updatePatterns)
	if err != nil {
		return Result{}, err
	}
	updates, err = refineUpdatesByContents(updates, merge.end, opts.Log)
	if err != nil {
		return Result{}, err
	}
	if len(updates) == 0 {
		if opts.Log != nil {
			opts.Log.Info("No archive updates found", zap.String("family", family.Name))
		}
		return res, nil
	}
	if opts.Log != nil {
		opts.Log.Info("Archive updates found", zap.String("family", family.Name), zap.Int("updates", len(updates)))
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

	familyRes, err := processUpdates(
		ctx,
		opts,
		last,
		merge,
		updates,
		archiveNameWidth(last, merge, family),
		family,
		targetSize,
		finalization,
		statePath,
		state,
		lineageState,
		now,
		copyEntry,
	)
	if err != nil {
		return Result{}, err
	}
	familyRes.Finalized += res.Finalized
	familyRes.FinalizedArchives = append(res.FinalizedArchives, familyRes.FinalizedArchives...)
	return familyRes, nil
}

func validateTargetSizeBytes(sizes map[string]int64) error {
	for _, family := range archiveFamilies() {
		if sizes[family.Name] <= 0 {
			return fmt.Errorf("rollup target size for %s must be positive", family.Name)
		}
	}
	return nil
}

func normalizeFinalization(finalization FinalizationOptions) FinalizationOptions {
	if finalization.Policy == "" {
		finalization.Policy = FinalizationPolicySize
	}
	return finalization
}

func validateFinalizationOptions(finalization FinalizationOptions, targetSizeBytes map[string]int64) error {
	switch finalization.Policy {
	case FinalizationPolicySize:
		return validateTargetSizeBytes(targetSizeBytes)
	case FinalizationPolicyRolling:
		if finalization.RollingDuration <= 0 {
			return errors.New("rollup rolling finalization duration must be positive")
		}
		const day = 24 * time.Hour
		if finalization.RollingDuration%day != 0 {
			return errors.New("rollup rolling finalization duration must be whole days")
		}
	case FinalizationPolicyCalendar:
		switch finalization.CalendarBucket {
		case CalendarBucketISOWeek, CalendarBucketISOBiweek, CalendarBucketMonth:
			return nil
		default:
			return fmt.Errorf("unsupported rollup calendar bucket %q", finalization.CalendarBucket)
		}
	default:
		return fmt.Errorf("unsupported rollup finalization policy %q", finalization.Policy)
	}
	return nil
}

func ParseRollingDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("duration is required")
	}
	unit := value[len(value)-1]
	multiplier := 24 * time.Hour
	switch unit {
	case 'd':
	case 'w':
		multiplier *= 7
	default:
		return 0, fmt.Errorf("duration %q must use whole-day or whole-week units", value)
	}
	amount, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("duration %q must be a positive integer followed by d or w", value)
	}
	if amount > int64(1<<63-1)/int64(multiplier) {
		return 0, fmt.Errorf("duration %q is too large", value)
	}
	return time.Duration(amount) * multiplier, nil
}

func formatRollingDuration(duration time.Duration) string {
	const day = 24 * time.Hour
	if duration%(7*day) == 0 {
		return fmt.Sprintf("%dw", duration/(7*day))
	}
	return fmt.Sprintf("%dd", duration/day)
}

func loadRollupState(path string) (*rollupState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &rollupState{Version: 1, Lineages: make(map[string]rollupLineageState)}, nil
		}
		return nil, fmt.Errorf("read rollup state %q: %w", path, err)
	}
	var state rollupState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode rollup state %q: %w", path, err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("rollup state %q has unsupported version %d", path, state.Version)
	}
	if state.Lineages == nil {
		state.Lineages = make(map[string]rollupLineageState)
	}
	return &state, nil
}

func saveRollupState(path string, state *rollupState) error {
	if state == nil {
		return nil
	}
	if state.Version == 0 {
		state.Version = 1
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rollup state %q: %w", path, err)
	}
	data = append(data, '\n')
	tmp, err := fileutil.CreateHiddenTemp(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return fmt.Errorf("create rollup state temp in %q: %w", filepath.Dir(path), err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write rollup state %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close rollup state %q: %w", tmpPath, err)
	}
	if err := fileutil.ReplaceOutputFile(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish rollup state %q: %w", path, err)
	}
	return nil
}

func validatePeriodState(
	state *rollupState,
	family archiveFamily,
	merge archive,
	finalization FinalizationOptions,
) (*rollupLineageState, error) {
	if merge.info == nil {
		return nil, nil
	}
	if state == nil || state.Lineages == nil {
		return nil, fmt.Errorf("rollup %s merge archive exists but rollup state is missing", family.Name)
	}
	lineage, ok := state.Lineages[family.Name]
	if !ok {
		return nil, fmt.Errorf("rollup %s merge archive exists but rollup state has no lineage data", family.Name)
	}
	if lineage.ActiveMerge != merge.info.Name() {
		return nil, fmt.Errorf("rollup %s state active merge %q does not match %q", family.Name, lineage.ActiveMerge, merge.info.Name())
	}
	if lineage.FirstBook != merge.begin || lineage.LastBook != merge.end {
		return nil, fmt.Errorf(
			"rollup %s state range %d:%d does not match merge range %d:%d",
			family.Name,
			lineage.FirstBook,
			lineage.LastBook,
			merge.begin,
			merge.end,
		)
	}
	if lineage.Policy != finalization.Policy {
		return nil, fmt.Errorf("rollup %s state policy %q does not match configured policy %q", family.Name, lineage.Policy, finalization.Policy)
	}
	switch finalization.Policy {
	case FinalizationPolicyRolling:
		if lineage.Rolling == "" || lineage.StartedAt == nil || lineage.Deadline == nil {
			return nil, fmt.Errorf("rollup %s rolling state is incomplete", family.Name)
		}
		duration, err := ParseRollingDuration(lineage.Rolling)
		if err != nil {
			return nil, fmt.Errorf("rollup %s rolling state duration is invalid: %w", family.Name, err)
		}
		if duration != finalization.RollingDuration {
			return nil, fmt.Errorf("rollup %s state rolling duration %q does not match configured duration", family.Name, lineage.Rolling)
		}
	case FinalizationPolicyCalendar:
		if lineage.Calendar != finalization.CalendarBucket || lineage.BucketStart == nil || lineage.BucketEnd == nil {
			return nil, fmt.Errorf("rollup %s calendar state is incomplete or does not match configured bucket", family.Name)
		}
	}
	return &lineage, nil
}

func removeRollupLineageState(state *rollupState, family string) bool {
	if state == nil || state.Lineages == nil {
		return false
	}
	if _, ok := state.Lineages[family]; !ok {
		return false
	}
	delete(state.Lineages, family)
	return true
}

func setRollupLineageState(state *rollupState, family string, lineage rollupLineageState) {
	if state.Lineages == nil {
		state.Lineages = make(map[string]rollupLineageState)
	}
	state.Lineages[family] = lineage
}

func createRollupLineageState(
	activeMerge string,
	firstBook int,
	lastBook int,
	finalization FinalizationOptions,
	startedAt time.Time,
) rollupLineageState {
	startedAt = startedAt.UTC()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	lineage := rollupLineageState{
		ActiveMerge: activeMerge,
		FirstBook:   firstBook,
		LastBook:    lastBook,
		Policy:      finalization.Policy,
	}
	switch finalization.Policy {
	case FinalizationPolicyRolling:
		rolling := finalization.RollingText
		if rolling == "" {
			rolling = formatRollingDuration(finalization.RollingDuration)
		}
		deadline := startedAt.Add(finalization.RollingDuration).UTC()
		lineage.Rolling = rolling
		lineage.StartedAt = timePtr(startedAt)
		lineage.Deadline = timePtr(deadline)
	case FinalizationPolicyCalendar:
		bucketStart, bucketEnd := calendarBucketRange(startedAt, finalization.CalendarBucket)
		lineage.Calendar = finalization.CalendarBucket
		lineage.BucketStart = timePtr(bucketStart)
		lineage.BucketEnd = timePtr(bucketEnd)
	}
	return lineage
}

func periodExpired(lineage rollupLineageState, now time.Time) bool {
	now = now.UTC()
	switch lineage.Policy {
	case FinalizationPolicyRolling:
		return lineage.Deadline != nil && !now.Before(*lineage.Deadline)
	case FinalizationPolicyCalendar:
		return lineage.BucketEnd != nil && !now.Before(*lineage.BucketEnd)
	default:
		return false
	}
}

func finalizationReason(finalization FinalizationOptions) FinalizationReason {
	switch finalization.Policy {
	case FinalizationPolicyRolling:
		return FinalizationReasonRollingDeadline
	case FinalizationPolicyCalendar:
		return FinalizationReasonCalendarBucketEnd
	default:
		return FinalizationReasonSize
	}
}

func finalizeMergeArchive(
	merge archive,
	family archiveFamily,
	nameWidth int,
	policy FinalizationPolicy,
	reason FinalizationReason,
	log *zap.Logger,
) (archive, error) {
	format := fmt.Sprintf("%s-%%0%dd-%%0%dd.zip", family.Prefix, nameWidth, nameWidth)
	finalName := filepath.Join(merge.dir, fmt.Sprintf(format, merge.begin, merge.end))
	mergeName := filepath.Join(merge.dir, merge.info.Name())
	if err := fileutil.ReplaceOutputFile(mergeName, finalName); err != nil {
		return archive{}, fmt.Errorf("rename archive %q to %q: %w", mergeName, finalName, err)
	}
	if log != nil {
		log.Info(
			"Archive finalized",
			zap.String("file", finalName),
			zap.Int("begin", merge.begin),
			zap.Int("end", merge.end),
			zap.String("policy", string(policy)),
			zap.String("reason", string(reason)),
		)
	}
	info, err := os.Stat(finalName)
	if err != nil {
		return archive{}, fmt.Errorf("stat finalized archive %q: %w", finalName, err)
	}
	return archive{dir: filepath.Dir(finalName), info: info, begin: merge.begin, end: merge.end}, nil
}

func calendarBucketRange(now time.Time, bucket CalendarBucket) (time.Time, time.Time) {
	now = now.UTC()
	switch bucket {
	case CalendarBucketISOWeek:
		start := isoWeekStart(now)
		return start, start.AddDate(0, 0, 7)
	case CalendarBucketISOBiweek:
		start := isoWeekStart(now)
		_, week := start.ISOWeek()
		if week == 53 {
			return start, start.AddDate(0, 0, 7)
		}
		if week%2 == 0 {
			start = start.AddDate(0, 0, -7)
			return start, start.AddDate(0, 0, 14)
		}
		return start, start.AddDate(0, 0, 14)
	case CalendarBucketMonth:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	default:
		return now, now
	}
}

func isoWeekStart(now time.Time) time.Time {
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekday := int(date.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return date.AddDate(0, 0, 1-weekday)
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func processUpdates(
	ctx context.Context,
	opts Options,
	last archive,
	merge archive,
	updates []archive,
	nameWidth int,
	family archiveFamily,
	targetSize int64,
	finalization FinalizationOptions,
	statePath string,
	state *rollupState,
	lineageState *rollupLineageState,
	now func() time.Time,
	copyEntry copyEntryFunc,
) (Result, error) {
	format := fmt.Sprintf("%s-%%0%dd-%%0%dd", family.Prefix, nameWidth, nameWidth)
	res := Result{Updates: len(updates)}
	work, err := openWorkArchive(opts, last, merge, targetSize, finalization.Policy, &updates)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if work != nil {
			work.cleanup()
		}
	}()
	firstUpdateIsExisting := work.firstUpdateIsExisting

	leftBytes := targetSize - work.existingSize
	firstBook := work.firstBook
	lastBook := work.lastBook
	existingEnd := merge.end
	copiedNewEntry := false
	newPeriodStartedAt := time.Time{}
	if lineageState != nil && lineageState.StartedAt != nil {
		newPeriodStartedAt = *lineageState.StartedAt
	}
	activeWorkIDs := make(map[string]struct{})
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
			if diff := nameToID(a.FileInfo().Name()) - nameToID(b.FileInfo().Name()); diff != 0 {
				return diff
			}
			return strings.Compare(a.FileInfo().Name(), b.FileInfo().Name())
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
				key := archiveEntryKey(file, family)
				if _, ok := activeWorkIDs[key]; ok {
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
				activeWorkIDs[archiveEntryKey(file, family)] = struct{}{}
				if !copiedNewEntry {
					newPeriodStartedAt = now().UTC()
				}
				copiedNewEntry = true
			}
			if finalization.Policy == FinalizationPolicySize {
				if !updateIsExisting {
					leftBytes -= int64(file.CompressedSize64)
				}
				if leftBytes > 0 {
					continue
				}
				finalName := filepath.Join(last.dir, fmt.Sprintf(format+".zip", firstBook, lastBook))
				if err := work.finishAs(finalName); err != nil {
					rc.Close()
					return Result{}, err
				}
				res.Finalized++
				res.FinalizedArchives = append(res.FinalizedArchives, finalName)
				if opts.Log != nil {
					opts.Log.Info(
						"Archive finalized",
						zap.String("file", finalName),
						zap.Int("begin", firstBook),
						zap.Int("end", lastBook),
						zap.String("policy", string(FinalizationPolicySize)),
						zap.String("reason", string(FinalizationReasonSize)),
					)
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
				leftBytes = targetSize
				firstBook = 0
				lastBook = 0
				copiedNewEntry = false
				activeWorkIDs = make(map[string]struct{})
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
		if finalization.Policy != FinalizationPolicySize {
			lineage := createRollupLineageState(filepath.Base(mergeName), firstBook, lastBook, finalization, newPeriodStartedAt)
			if lineageState != nil {
				lineage = *lineageState
				lineage.ActiveMerge = filepath.Base(mergeName)
				lineage.FirstBook = firstBook
				lineage.LastBook = lastBook
			}
			setRollupLineageState(state, family.Name, lineage)
			if err := saveRollupState(statePath, state); err != nil {
				return Result{}, err
			}
		}
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

func openWorkArchive(
	opts Options,
	last archive,
	merge archive,
	targetSize int64,
	policy FinalizationPolicy,
	updates *[]archive,
) (*workArchive, error) {
	if merge.info != nil {
		return rewriteExistingMergeArchive(filepath.Join(merge.dir, merge.info.Name()), merge.begin, merge.end, merge.info.Size())
	}
	work, err := createNewWorkArchive(last.dir)
	if err != nil {
		return nil, err
	}
	if policy == FinalizationPolicySize && last.info != nil && targetSize-last.info.Size() > 0 {
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
	f, err := fileutil.CreateHiddenTemp(dir, "rollup")
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
	tmp, err := fileutil.CreateHiddenTemp(filepath.Dir(path), "rollup")
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
	localArchiveNameRE = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.zip$`)
	localMergeNameRE   = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.merging$`)
	localNameRE        = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.(?:zip|merging)$`)
)

func archiveFamilies() []archiveFamily {
	return []archiveFamily{{Name: "fb2", Prefix: "fb2"}, {Name: "usr", Prefix: "usr"}}
}

func defaultUpdatePatterns() []UpdatePattern {
	return []UpdatePattern{
		{Name: "flibusta-fb2", Family: "fb2", Pattern: `(?i)^f(?:\.fb2)?\.([0-9]+)-([0-9]+)\.zip$`},
		{Name: "flibusta-usr", Family: "usr", Pattern: `(?i)^f\.(?!fb2\.)[^.]+\.([0-9]+)-([0-9]+)\.zip$`},
		{Name: "librusec-fb2", Family: "fb2", Pattern: `(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.fb2\.zip$`},
		{Name: "librusec-usr", Family: "usr", Pattern: `(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.(?!fb2\.)[^.]+\.zip$`},
	}
}

func compileUpdatePatterns(patterns []UpdatePattern) ([]compiledUpdatePattern, error) {
	if len(patterns) == 0 {
		patterns = defaultUpdatePatterns()
	}
	res := make([]compiledUpdatePattern, 0, len(patterns))
	for _, pattern := range patterns {
		family, ok := archiveFamilyByName(pattern.Family)
		if !ok {
			return nil, fmt.Errorf("rollup update pattern %q has unsupported family %q", patternLabel(pattern), pattern.Family)
		}
		re, err := regexp2.Compile(pattern.Pattern, regexp2.None)
		if err != nil {
			return nil, fmt.Errorf("compile rollup update pattern %q: %w", patternLabel(pattern), err)
		}
		re.MatchTimeout = updatePatternTimeout
		res = append(res, compiledUpdatePattern{Name: patternLabel(pattern), Family: family, Pattern: pattern.Pattern, re: re})
	}
	return res, nil
}

func archiveFamilyByName(name string) (archiveFamily, bool) {
	for _, family := range archiveFamilies() {
		if strings.EqualFold(family.Name, name) {
			return family, true
		}
	}
	return archiveFamily{}, false
}

func patternLabel(pattern UpdatePattern) string {
	if pattern.Name != "" {
		return pattern.Name
	}
	return pattern.Pattern
}

func archiveNameWidth(last archive, merge archive, family archiveFamily) int {
	for _, item := range []archive{merge, last} {
		if item.info == nil {
			continue
		}
		match := localNameRE.FindStringSubmatch(item.info.Name())
		if len(match) >= 4 && strings.EqualFold(match[1], family.Prefix) {
			return max(len(match[2]), len(match[3]))
		}
	}
	return 10
}

func getLastArchive(files []archive, family archiveFamily) (archive, error) {
	var res archive
	for _, file := range files {
		ok, first, second, err := dissectLocalName(localArchiveNameRE, file.info.Name(), family)
		if err != nil {
			return archive{}, err
		}
		if ok && res.end < second {
			res = archive{dir: file.dir, info: file.info, begin: first, end: second}
		}
	}
	return res, nil
}

func getMergeArchive(files []archive, family archiveFamily) (archive, error) {
	var res archive
	var count int
	for _, file := range files {
		ok, first, second, err := dissectLocalName(localMergeNameRE, file.info.Name(), family)
		if err != nil {
			return archive{}, err
		}
		if ok {
			res = archive{dir: file.dir, info: file.info, begin: first, end: second}
			count++
		}
	}
	if count > 1 {
		return archive{}, fmt.Errorf("there could only be single %s merge archive", family.Name)
	}
	return res, nil
}

func getUpdates(files []archive, last int, family archiveFamily, patterns []compiledUpdatePattern) ([]archive, error) {
	updates := make([]archive, 0)
	for _, file := range files {
		matchedFamily, first, second, ok, err := matchUpdateName(file.info.Name(), patterns)
		if err != nil {
			return nil, err
		}
		if ok && matchedFamily.Name == family.Name && last < second {
			updates = append(updates, archive{dir: file.dir, info: file.info, begin: first, end: second})
		}
	}
	return updates, nil
}

func refineUpdatesByContents(updates []archive, last int, log *zap.Logger) ([]archive, error) {
	res := make([]archive, 0, len(updates))
	for _, update := range updates {
		actualBegin, actualEnd, err := updateArchiveActualRange(update)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(update.dir, update.info.Name())
		if actualEnd == 0 {
			if log != nil {
				log.Warn(
					"Skipping archive update with no usable entries",
					zap.String("file", path),
					zap.Int("existing_end", last),
				)
			}
			continue
		}
		if actualBegin != update.begin || actualEnd != update.end {
			if log != nil {
				log.Warn(
					"Archive update range adjusted to actual entries",
					zap.String("file", path),
					zap.Int("begin", update.begin),
					zap.Int("end", update.end),
					zap.Int("actual_begin", actualBegin),
					zap.Int("actual_end", actualEnd),
				)
			}
			update.begin = actualBegin
			update.end = actualEnd
		}
		if update.end <= last {
			if log != nil {
				log.Warn(
					"Skipping archive update with no new entries",
					zap.String("file", path),
					zap.Int("end", update.end),
					zap.Int("existing_end", last),
				)
			}
			continue
		}
		res = append(res, update)
	}
	return res, nil
}

func updateArchiveActualRange(update archive) (int, int, error) {
	path := filepath.Join(update.dir, update.info.Name())
	rc, err := zip.OpenReader(path)
	if err != nil {
		return 0, 0, fmt.Errorf("open update archive %q: %w", path, err)
	}
	begin, end := 0, 0
	for _, file := range rc.File {
		if file.FileInfo().Size() == 0 {
			continue
		}
		id := nameToID(file.FileInfo().Name())
		if id <= 0 {
			continue
		}
		if begin == 0 || id < begin {
			begin = id
		}
		if id > end {
			end = id
		}
	}
	if err := rc.Close(); err != nil {
		return 0, 0, fmt.Errorf("close update archive %q: %w", path, err)
	}
	return begin, end, nil
}

func dissectLocalName(re *regexp.Regexp, name string, family archiveFamily) (bool, int, int, error) {
	match := re.FindStringSubmatch(name)
	if match == nil {
		return false, 0, 0, nil
	}
	if !strings.EqualFold(match[1], family.Prefix) {
		return false, 0, 0, nil
	}
	first, err := strconv.Atoi(match[2])
	if err != nil {
		return true, 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	second, err := strconv.Atoi(match[3])
	if err != nil {
		return true, 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	return true, first, second, nil
}

func matchUpdateName(name string, patterns []compiledUpdatePattern) (archiveFamily, int, int, bool, error) {
	var matched *compiledUpdatePattern
	var first, second int
	for i := range patterns {
		match, err := patterns[i].re.FindStringMatch(name)
		if err != nil {
			return archiveFamily{}, 0, 0, false, fmt.Errorf("match update file %q with rollup pattern %q: %w", name, patterns[i].Name, err)
		}
		if match == nil {
			continue
		}
		if matched != nil {
			return archiveFamily{}, 0, 0, false, fmt.Errorf(
				"update file %q matches multiple rollup patterns: %q and %q",
				name,
				matched.Name,
				patterns[i].Name,
			)
		}
		if match.GroupCount() < 3 {
			return archiveFamily{}, 0, 0, false, fmt.Errorf("rollup pattern %q must capture range begin and end", patterns[i].Name)
		}
		first, second, err = parseUpdateRange(match.GroupByNumber(1).String(), match.GroupByNumber(2).String(), name, patterns[i].Name)
		if err != nil {
			return archiveFamily{}, 0, 0, false, err
		}
		matched = &patterns[i]
	}
	if matched == nil {
		return archiveFamily{}, 0, 0, false, nil
	}
	return matched.Family, first, second, true, nil
}

func parseUpdateRange(firstText string, secondText string, name string, pattern string) (int, int, error) {
	first, err := strconv.Atoi(firstText)
	if err != nil {
		return 0, 0, fmt.Errorf("dissect update file %q with rollup pattern %q: %w", name, pattern, err)
	}
	second, err := strconv.Atoi(secondText)
	if err != nil {
		return 0, 0, fmt.Errorf("dissect update file %q with rollup pattern %q: %w", name, pattern, err)
	}
	return first, second, nil
}

func nameToID(name string) int {
	base := filepath.Base(name)
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	id, err := strconv.Atoi(base)
	if err != nil {
		return -1
	}
	return id
}

func archiveEntryKey(file *zip.File, family archiveFamily) string {
	name := file.FileInfo().Name()
	if family.Name == "fb2" {
		return strconv.Itoa(nameToID(name))
	}
	return strings.ToLower(filepath.Base(name))
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
