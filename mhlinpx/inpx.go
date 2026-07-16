package mhlinpx

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/internal/inpxutil"
	"metabib/model"
)

const fieldSep = "\x04"

type Format string

const (
	Format2X   Format = "2x"
	FormatRUKS Format = "ruks"
)

type SequenceMode string

const (
	SequenceAuthor    SequenceMode = "author"
	SequencePublisher SequenceMode = "publisher"
	SequenceIgnore    SequenceMode = "ignore"
)

type FB2Preference string

const (
	PreferIgnore     FB2Preference = "ignore"
	PreferMerge      FB2Preference = "merge"
	PreferComplement FB2Preference = "complement"
	PreferReplace    FB2Preference = "replace"
)

type Limits struct {
	AuthorName   int
	AuthorMiddle int
	AuthorFamily int
	Title        int
	Keywords     int
	Sequence     int
}

type Options struct {
	InputPrefix     string
	OutputPrefix    string
	Format          Format
	SequenceMode    SequenceMode
	FB2Preference   FB2Preference
	QuickFix        bool
	Limits          Limits
	CommentTemplate string
	VersionTemplate string
	Log             *zap.Logger
}

type Stats struct {
	OutputPath string
	DumpDate   string
	Archives   int
	Files      int
	Records    int64
	DBRecords  int64
	FB2Records int64
	Dummy      int64
}

type ArchiveStats struct {
	Name       string
	Files      int
	Records    int64
	DBRecords  int64
	FB2Records int64
	Dummy      int64
	Elapsed    time.Duration
}

func DefaultLimits() Limits {
	return Limits{AuthorName: 128, AuthorMiddle: 128, AuthorFamily: 128, Title: 150, Keywords: 255, Sequence: 80}
}

func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "2x", "2.x":
		return Format2X, nil
	case "ruks":
		return FormatRUKS, nil
	default:
		return "", fmt.Errorf("invalid INPX format %q", value)
	}
}

func ParseSequenceMode(value string) (SequenceMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "author":
		return SequenceAuthor, nil
	case "publisher":
		return SequencePublisher, nil
	case "ignore":
		return SequenceIgnore, nil
	default:
		return "", fmt.Errorf("invalid INPX sequence mode %q", value)
	}
}

func ParseFB2Preference(value string) (FB2Preference, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "complement":
		return PreferComplement, nil
	case "ignore":
		return PreferIgnore, nil
	case "merge":
		return PreferMerge, nil
	case "replace":
		return PreferReplace, nil
	default:
		return "", fmt.Errorf("invalid INPX FB2 preference %q", value)
	}
}

func Generate(ctx context.Context, opts Options) (Stats, error) {
	stats := Stats{}
	if opts.InputPrefix == "" {
		return stats, errors.New("INPX input prefix is required")
	}
	if opts.OutputPrefix == "" {
		return stats, errors.New("INPX output prefix is required")
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits()
	}
	var stream *streamINPXWriter
	var tmpPath string
	cleanupTemp := true
	defer func() {
		if cleanupTemp && tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	var meta inpxutil.Metadata
	_, loaded, err := inpxutil.StreamDatasetInput(
		ctx,
		opts.InputPrefix,
		opts.Log,
		func(dataset model.Dataset) error {
			meta = inpxutil.DatasetMetadata(dataset)
			inpxutil.EnsureDumpDate(&meta, opts.Log)
			stats.DumpDate = meta.DumpDate
			outputPath, err := inpxutil.OutputPath(opts.OutputPrefix, meta)
			if err != nil {
				return err
			}
			stats.OutputPath = outputPath
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				return fmt.Errorf("create INPX output directory: %w", err)
			}
			tmpFile, err := os.CreateTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+"-*.tmp")
			if err != nil {
				return fmt.Errorf("create temporary INPX output: %w", err)
			}
			tmpPath = tmpFile.Name()
			if err := tmpFile.Close(); err != nil {
				return fmt.Errorf("close temporary INPX output %q: %w", tmpPath, err)
			}
			if _, err := os.Stat(outputPath); err == nil && opts.Log != nil {
				opts.Log.Warn("Overwriting existing INPX output", zap.String("file", outputPath))
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("stat INPX output %q: %w", outputPath, err)
			}
			if opts.Log != nil {
				opts.Log.Info("INPX creation started", zap.String("file", outputPath), zap.Int("archives", len(dataset.Archives)))
			}
			stream, err = newStreamINPXWriter(tmpPath, meta, dataset, opts)
			return err
		},
		func(rec model.DatasetRecord) error {
			if stream == nil {
				return errors.New("INPX dataset record arrived before header")
			}
			return stream.WriteRecord(rec)
		},
	)
	if err != nil {
		if stream != nil {
			_ = stream.Close()
		}
		return stats, err
	}
	if stream == nil {
		return stats, errors.New("INPX dataset input is missing header")
	}
	writeStats, err := stream.Finish()
	if err != nil {
		return stats, err
	}
	if opts.Log != nil {
		opts.Log.Info("INPX records streamed", zap.Int64("records", loaded), zap.Int("archives", len(stream.archives)))
	}
	stats.Archives = writeStats.Archives
	stats.Files = writeStats.Files
	stats.Records = writeStats.Records
	stats.DBRecords = writeStats.DBRecords
	stats.FB2Records = writeStats.FB2Records
	stats.Dummy = writeStats.Dummy
	if err := fileutil.ReplaceOutputFile(tmpPath, stats.OutputPath); err != nil {
		return stats, fmt.Errorf("replace INPX output %q: %w", stats.OutputPath, err)
	}
	cleanupTemp = false
	return stats, nil
}

type streamINPXWriter struct {
	path        string
	meta        inpxutil.Metadata
	opts        Options
	zw          *zip.Writer
	f           *os.File
	archives    []*inpxutil.DatasetArchiveRows
	archiveByID map[string]int
	nextArchive int
	active      int
	activeIndex int
	activeStart time.Time
	activeStats Stats
	bw          *bufio.Writer
	stats       Stats
}

func newStreamINPXWriter(path string, meta inpxutil.Metadata, dataset model.Dataset, opts Options) (*streamINPXWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(zipComment(meta))
	archives := inpxutil.DatasetArchiveRowsList(dataset)
	archiveByID := make(map[string]int, len(archives))
	for idx, archive := range archives {
		archiveByID[archive.Meta.ID] = idx
	}
	return &streamINPXWriter{
		path:        path,
		meta:        meta,
		opts:        opts,
		zw:          zw,
		f:           f,
		archives:    archives,
		archiveByID: archiveByID,
		active:      -1,
		stats:       Stats{DumpDate: meta.DumpDate},
	}, nil
}

func (w *streamINPXWriter) WriteRecord(rec model.DatasetRecord) error {
	target, index, ok, err := w.recordTarget(rec)
	if err != nil || !ok {
		return err
	}
	if err := w.advanceTo(target); err != nil {
		return err
	}
	if index < w.activeIndex {
		return fmt.Errorf("INPX record index %d arrived after index %d in archive %q", index, w.activeIndex, w.archives[w.active].Meta.ID)
	}
	for w.activeIndex < index {
		if err := w.writeMissing(w.activeIndex); err != nil {
			return err
		}
		w.activeIndex++
	}
	if inpxutil.InRanges(w.archives[w.active].Meta.Ignored, index) {
		w.activeIndex++
		return nil
	}
	return w.writeRecordAt(rec, index)
}

func (w *streamINPXWriter) recordTarget(rec model.DatasetRecord) (int, int, bool, error) {
	locator := rec.Record.Locator
	if locator.Kind != "archive_entry" {
		if _, ok := w.archiveByID[inpxutil.OnlineArchivePath]; !ok {
			return 0, 0, false, nil
		}
		archive := w.archives[w.archiveByID[inpxutil.OnlineArchivePath]]
		index := archive.Meta.Entries
		archive.Meta.Entries++
		return w.archiveByID[inpxutil.OnlineArchivePath], index, true, nil
	}
	if locator.Index == nil {
		return 0, 0, false, fmt.Errorf("INPX archive record for source %q has no index", locator.Source)
	}
	target, ok := w.archiveByID[locator.Source]
	if !ok {
		return 0, 0, false, fmt.Errorf("INPX record references undeclared archive source %q", locator.Source)
	}
	return target, *locator.Index, true, nil
}

func (w *streamINPXWriter) advanceTo(target int) error {
	for w.active != target {
		if w.active != -1 {
			if err := w.finishActive(); err != nil {
				return err
			}
		}
		if w.nextArchive > target {
			return fmt.Errorf("INPX records are out of archive order: target archive %d after %d", target, w.nextArchive-1)
		}
		if err := w.openNext(); err != nil {
			return err
		}
	}
	return nil
}

func (w *streamINPXWriter) openNext() error {
	if w.nextArchive >= len(w.archives) {
		return errors.New("INPX record references archive past declared list")
	}
	archive := w.archives[w.nextArchive]
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	zw, err := w.zw.Create(name)
	if err != nil {
		return fmt.Errorf("create INPX entry %q: %w", name, err)
	}
	w.bw = bufio.NewWriter(zw)
	w.active = w.nextArchive
	w.activeIndex = 0
	w.activeStart = time.Now()
	w.activeStats = w.stats
	w.nextArchive++
	return nil
}

func (w *streamINPXWriter) writeMissing(index int) error {
	archive := w.archives[w.active]
	if inpxutil.InRanges(archive.Meta.Ignored, index) {
		return nil
	}
	w.stats.Files++
	w.stats.Dummy++
	if _, err := w.bw.WriteString(dummyLine(index + 1)); err != nil {
		name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
		return fmt.Errorf("write INPX entry %q: %w", name, err)
	}
	return nil
}

func (w *streamINPXWriter) writeRecordAt(rec model.DatasetRecord, index int) error {
	archive := w.archives[w.active]
	w.stats.Files++
	line, view, err := recordLine(rec, w.opts)
	if err != nil {
		return err
	}
	if line == "" {
		line = dummyLine(index + 1)
		w.stats.Dummy++
	} else {
		w.stats.Records++
		if view.HasDatabase {
			w.stats.DBRecords++
		} else {
			w.stats.FB2Records++
		}
	}
	if _, err := w.bw.WriteString(line); err != nil {
		name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
		return fmt.Errorf("write INPX entry %q: %w", name, err)
	}
	w.activeIndex++
	return nil
}

func (w *streamINPXWriter) finishActive() error {
	archive := w.archives[w.active]
	for w.activeIndex < archive.Meta.Entries {
		if err := w.writeMissing(w.activeIndex); err != nil {
			return err
		}
		w.activeIndex++
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if w.opts.Log != nil {
		archiveStats := w.statsSinceActiveStart()
		w.opts.Log.Info(
			"INPX entry created",
			zap.String("entry", strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name))+".inp"),
			zap.String("archive", archive.Meta.Name),
			zap.Int64("records", archiveStats.DBRecords),
			zap.Int64("fb2_records", archiveStats.FB2Records),
			zap.Int64("dummy_records", archiveStats.Dummy),
			zap.Int("files", archiveStats.Files),
			zap.Duration("elapsed", time.Since(w.activeStart)),
		)
	}
	w.stats.Archives++
	w.active = -1
	w.bw = nil
	return nil
}

func (w *streamINPXWriter) statsSinceActiveStart() Stats {
	return Stats{
		Files:      w.stats.Files - w.activeStats.Files,
		Records:    w.stats.Records - w.activeStats.Records,
		DBRecords:  w.stats.DBRecords - w.activeStats.DBRecords,
		FB2Records: w.stats.FB2Records - w.activeStats.FB2Records,
		Dummy:      w.stats.Dummy - w.activeStats.Dummy,
	}
}

func (w *streamINPXWriter) Finish() (Stats, error) {
	if w.active != -1 {
		if err := w.finishActive(); err != nil {
			w.Close()
			return w.stats, err
		}
	}
	for w.nextArchive < len(w.archives) {
		if err := w.openNext(); err != nil {
			w.Close()
			return w.stats, err
		}
		if err := w.finishActive(); err != nil {
			w.Close()
			return w.stats, err
		}
	}
	collection, err := collectionInfo(w.meta, w.opts)
	if err != nil {
		w.Close()
		return w.stats, err
	}
	if err := inpxutil.WriteZipText(w.zw, "collection.info", collection); err != nil {
		w.Close()
		return w.stats, err
	}
	version, err := versionInfo(w.meta, w.opts)
	if err != nil {
		w.Close()
		return w.stats, err
	}
	if err := inpxutil.WriteZipText(w.zw, "version.info", version); err != nil {
		w.Close()
		return w.stats, err
	}
	return w.stats, w.Close()
}

func (w *streamINPXWriter) Close() error {
	if w.zw != nil {
		if err := w.zw.Close(); err != nil {
			w.f.Close()
			return fmt.Errorf("close INPX zip %q: %w", w.path, err)
		}
		w.zw = nil
	}
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			return fmt.Errorf("close INPX %q: %w", w.path, err)
		}
		w.f = nil
	}
	return nil
}

func recordLine(rec model.DatasetRecord, opts Options) (string, inpxutil.DatasetRecordView, error) {
	view, err := inpxutil.DatasetRecordClaims(rec)
	if err != nil {
		return "", view, err
	}
	if !view.HasDatabase && !view.HasFB2 {
		return "", view, nil
	}
	title := view.Database.Title
	if title == "" {
		title = view.FB2.Title
	}
	authors := authorsString(view.HasDatabase, view.Database.Authors, view.FB2.Authors, opts)
	genres := genresString(view.Database.Genres, view.FB2.Genres)
	sequence, seqNum := sequenceString(view.Database.Sequences, view.FB2.Sequences, opts)
	fileName := strings.TrimSuffix(view.Artifact.Name, filepath.Ext(view.Artifact.Name))
	if fileName == "" {
		fileName = datasetBookID(rec)
	}
	ext := view.Catalog.FileType
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(view.Artifact.Name), ".")
	}
	date := dateOnly(view.Catalog.Time)
	if date == "" {
		date = view.Artifact.Date
	}
	lang := view.Database.Language
	if lang == "" {
		lang = view.FB2.Language
	}
	keywords := view.Database.Keywords
	if keywords == "" {
		keywords = view.FB2.Keywords
	}
	fields := []string{
		authors,
		genres,
		fix(title, opts.QuickFix, opts.Limits.Title),
		fix(sequence, opts.QuickFix, opts.Limits.Sequence),
		seqNum,
		fileName,
		strconv.FormatUint(view.Artifact.Size, 10),
		datasetBookID(rec),
		view.Catalog.Deleted,
		ext,
		date,
		strings.TrimSpace(lang),
		ruksRate(view.Catalog.Rating),
		fix(keywords, opts.QuickFix, opts.Limits.Keywords),
	}
	if opts.Format == FormatRUKS {
		fields = append(fields, view.Catalog.MD5, datasetReplacedBy(rec))
	}
	return joinINPFields(fields), view, nil
}

func authorsString(dbPresent bool, authors []model.PersonValue, fb2Authors []model.PersonValue, opts Options) string {
	if opts.FB2Preference == PreferReplace && len(fb2Authors) > 0 {
		return peopleString(fb2Authors, opts)
	}
	if dbPresent && len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	if len(authors) == 0 && len(fb2Authors) > 0 {
		return peopleString(fb2Authors, opts)
	}
	if len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	return peopleString(authors, opts)
}

func peopleString(people []model.PersonValue, opts Options) string {
	var b strings.Builder
	for _, person := range people {
		lastName := fix(inpxutil.CleanseAuthorComponent(person.LastName), opts.QuickFix, opts.Limits.AuthorFamily)
		firstName := fix(inpxutil.CleanseAuthorComponent(person.FirstName), opts.QuickFix, opts.Limits.AuthorName)
		middleName := fix(inpxutil.CleanseAuthorComponent(person.MiddleName), opts.QuickFix, opts.Limits.AuthorMiddle)
		if lastName == "" && firstName == "" && middleName == "" {
			continue
		}
		b.WriteString(lastName)
		b.WriteByte(',')
		b.WriteString(firstName)
		b.WriteByte(',')
		b.WriteString(middleName)
		b.WriteByte(':')
	}
	if b.Len() == 0 {
		return "неизвестный,автор,:"
	}
	return b.String()
}

func genresString(genres []model.GenreValue, fb2Genres []model.GenreValue) string {
	if len(genres) > 0 {
		var b strings.Builder
		for _, genre := range genres {
			code := inpxutil.CleanseGenreCode(genre.Code)
			if code == "" {
				continue
			}
			b.WriteString(code)
			b.WriteByte(':')
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	if len(fb2Genres) > 0 {
		var b strings.Builder
		for _, genre := range fb2Genres {
			code := inpxutil.CleanseGenreCode(genre.Code)
			if code == "" {
				continue
			}
			b.WriteString(code)
			b.WriteByte(':')
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return "other:"
}

func sequenceString(sequences []model.SequenceValue, fb2Sequences []model.SequenceValue, opts Options) (string, string) {
	dbName, dbNum := dbSequence(sequences, opts.SequenceMode)
	fbName, fbNum := fb2Sequence(fb2Sequences)
	switch opts.FB2Preference {
	case PreferReplace:
		if fbName != "" {
			return fbName, fbNum
		}
	case PreferMerge:
		if fbName != "" {
			return fbName, fbNum
		}
	case PreferComplement:
		if dbName == "" && fbName != "" {
			return fbName, fbNum
		}
	}
	return dbName, dbNum
}

func dbSequence(sequences []model.SequenceValue, mode SequenceMode) (string, string) {
	if mode == SequenceIgnore || len(sequences) == 0 {
		return "", ""
	}
	sequences = slices.DeleteFunc(slices.Clone(sequences), func(seq model.SequenceValue) bool { return seq.Type == nil })
	if len(sequences) == 0 {
		return "", ""
	}
	slices.SortFunc(sequences, func(a, b model.SequenceValue) int {
		if *a.Type != *b.Type {
			if mode == SequencePublisher {
				return int(*b.Type - *a.Type)
			}
			return int(*a.Type - *b.Type)
		}
		if sequenceLevel(a) != sequenceLevel(b) {
			return int(sequenceLevel(a) - sequenceLevel(b))
		}
		return strings.Compare(a.Name, b.Name)
	})
	seq := sequences[0]
	return seq.Name, sequenceNumber(seq.Number)
}

func fb2Sequence(sequences []model.SequenceValue) (string, string) {
	if len(sequences) == 0 {
		return "", ""
	}
	seq := sequences[0]
	return seq.Name, sequenceNumber(seq.Number)
}

func dummyLine(index int) string {
	fields := []string{"dummy:", "other:", "dummy record", "", "", "", "1", strconv.Itoa(index), "1", "EXT", "2000-01-01", "en", "0", ""}
	return joinINPFields(fields)
}

func sequenceLevel(seq model.SequenceValue) int64 {
	if seq.Level == nil {
		return 0
	}
	return *seq.Level
}

func sequenceNumber(value *model.NumberValue) string {
	if value == nil {
		return ""
	}
	if value.Value != nil {
		return strconv.Itoa(int(*value.Value))
	}
	return value.Text
}

func datasetBookID(rec model.DatasetRecord) string {
	return inpxutil.DatasetBookID(rec)
}

func datasetReplacedBy(rec model.DatasetRecord) string {
	for _, relation := range rec.Relations {
		if relation.Type == "replaced_by" && relation.Target != nil {
			return relation.Target.Value
		}
	}
	return ""
}

func ruksRate(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value
	}
	return strconv.FormatInt(int64(parsed), 10)
}

func joinINPFields(fields []string) string {
	for i := range fields {
		fields[i] = inpxutil.Cleanse(fields[i])
	}
	return strings.Join(fields, fieldSep) + fieldSep + "\r\n"
}

func zipComment(meta inpxutil.Metadata) string {
	return inpxutil.ZipComment(meta)
}

func collectionInfo(meta inpxutil.Metadata, opts Options) (string, error) {
	return inpxutil.CollectionInfo(meta, inpxutil.TemplateOptions{CommentTemplate: opts.CommentTemplate})
}

func versionInfo(meta inpxutil.Metadata, opts Options) (string, error) {
	return inpxutil.VersionInfo(meta, inpxutil.TemplateOptions{VersionTemplate: opts.VersionTemplate})
}

func fix(value string, enabled bool, maxLen int) string {
	value = inpxutil.Cleanse(value)
	if !enabled || maxLen <= 0 {
		return value
	}
	runes := []rune(value)
	limit := max(maxLen-1, 0)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimRight(string(runes[:limit]), " \t")
}

func dateOnly(value string) string {
	if len(value) >= 10 {
		if _, err := time.Parse("2006-01-02", value[:10]); err == nil {
			return value[:10]
		}
	}
	return value
}
