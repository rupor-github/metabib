package mhlinpx

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	sprig "github.com/go-task/slim-sprig/v3"
	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/internal/inpxutil"
	"metabib/jsonl"
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

type archiveRows struct {
	Meta    model.MergeArchiveMetadata
	Records map[int]model.Record
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
	dataset, archives, loaded, err := inpxutil.LoadDatasetInput(ctx, opts.InputPrefix, opts.Log)
	if err != nil {
		return stats, err
	}
	meta := datasetMetadata(dataset)
	inpxutil.EnsureDumpDate(&meta, opts.Log)
	stats.DumpDate = meta.Database.DumpDate
	if opts.Log != nil {
		opts.Log.Info("INPX records loaded", zap.Int64("records", loaded), zap.Int("archives", len(archives)))
	}
	outputPath, err := inpxutil.OutputPath(opts.OutputPrefix, meta)
	if err != nil {
		return stats, err
	}
	stats.OutputPath = outputPath
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return stats, fmt.Errorf("create INPX output directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+"-*.tmp")
	if err != nil {
		return stats, fmt.Errorf("create temporary INPX output: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return stats, fmt.Errorf("close temporary INPX output %q: %w", tmpPath, err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := os.Stat(outputPath); err == nil && opts.Log != nil {
		opts.Log.Warn("Overwriting existing INPX output", zap.String("file", outputPath))
	} else if err != nil && !os.IsNotExist(err) {
		return stats, fmt.Errorf("stat INPX output %q: %w", outputPath, err)
	}
	if opts.Log != nil {
		opts.Log.Info("INPX creation started", zap.String("file", outputPath), zap.Int("archives", len(archives)))
	}
	writeStats, err := writeINPX(ctx, tmpPath, meta, archives, opts)
	if err != nil {
		return stats, err
	}
	stats.Archives = writeStats.Archives
	stats.Files = writeStats.Files
	stats.Records = writeStats.Records
	stats.DBRecords = writeStats.DBRecords
	stats.FB2Records = writeStats.FB2Records
	stats.Dummy = writeStats.Dummy
	if err := fileutil.ReplaceOutputFile(tmpPath, outputPath); err != nil {
		return stats, fmt.Errorf("replace INPX output %q: %w", outputPath, err)
	}
	cleanupTemp = false
	return stats, nil
}

func discoverMetadata(prefix string) (string, error) {
	matches, err := filepath.Glob(prefix + ".meta.json*")
	if err != nil {
		return "", err
	}
	matches = slices.DeleteFunc(matches, func(path string) bool { return strings.HasSuffix(path, ".tmp") })
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one metadata sidecar for %q, found %d", prefix, len(matches))
	}
	return matches[0], nil
}

func discoverInputParts(prefix string, metaPath string, meta model.MergeMetadata, log *zap.Logger) ([]string, error) {
	if len(meta.Parts) == 0 {
		return nil, fmt.Errorf("merge metadata %q does not list JSONL parts; rerun metabib merge", metaPath)
	}
	baseDir := filepath.Dir(metaPath)
	parts := make([]string, 0, len(meta.Parts))
	listed := make(map[string]struct{}, len(meta.Parts))
	for _, part := range meta.Parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("merge metadata %q contains an empty JSONL part path", metaPath)
		}
		path := part
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		path = filepath.Clean(path)
		parts = append(parts, path)
		listed[comparablePath(path)] = struct{}{}
	}
	warnUnlistedInputParts(prefix, listed, log)
	return parts, nil
}

func warnUnlistedInputParts(prefix string, listed map[string]struct{}, log *zap.Logger) {
	if log == nil {
		return
	}
	matches, err := filepath.Glob(prefix + ".*.jsonl*")
	if err != nil {
		log.Warn("Unable to scan for unlisted JSONL input parts", zap.String("prefix", prefix), zap.Error(err))
		return
	}
	matches = slices.DeleteFunc(matches, func(path string) bool {
		base := filepath.Base(path)
		return strings.Contains(base, ".meta.json") || strings.HasSuffix(base, ".tmp")
	})
	sort.Strings(matches)
	for _, match := range matches {
		if _, ok := listed[comparablePath(match)]; ok {
			continue
		}
		log.Warn("Ignoring JSONL input part not listed in merge metadata", zap.String("file", match))
	}
}

func comparablePath(path string) string {
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func readMetadata(path string) (model.MergeMetadata, error) {
	r, err := jsonl.OpenCompressedFile(path)
	if err != nil {
		return model.MergeMetadata{}, err
	}
	defer r.Close()
	var meta model.MergeMetadata
	if err := jsonv2.UnmarshalRead(r, &meta); err != nil {
		return meta, fmt.Errorf("decode merge metadata %q: %w", path, err)
	}
	return meta, nil
}

func readRecords(ctx context.Context, parts []string, archives map[string]*archiveRows, log *zap.Logger) (int64, error) {
	var records int64
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		r, err := jsonl.OpenCompressedFile(part)
		if err != nil {
			return records, err
		}
		dec := jsontext.NewDecoder(r)
		for {
			var rec model.Record
			if err := jsonv2.UnmarshalDecode(dec, &rec); err != nil {
				if err == io.EOF {
					break
				}
				r.Close()
				return records, fmt.Errorf("decode JSONL part %q: %w", part, err)
			}
			records++
			if rec.ID.Archive == nil {
				if online := archives[inpxutil.OnlineArchivePath]; online != nil {
					idx := online.Meta.Entries
					online.Records[idx] = rec
					online.Meta.Entries++
				}
				continue
			}
			archive := archives[rec.ID.Archive.Path]
			if archive == nil {
				r.Close()
				return records, fmt.Errorf(
					"record references archive %q not listed in merge metadata; rebuild merge output before generating MyHomeLib INPX",
					rec.ID.Archive.Path,
				)
			}
			if existing, ok := archive.Records[rec.ID.Archive.Index]; ok {
				logDuplicateArchiveIndex(log, part, rec.ID.Archive.Path, rec.ID.Archive.Index, existing, rec)
				continue
			}
			archive.Records[rec.ID.Archive.Index] = rec
		}
		if err := r.Close(); err != nil {
			return records, err
		}
	}
	return records, nil
}

func newOnlineArchive() *archiveRows {
	return &archiveRows{
		Meta:    model.MergeArchiveMetadata{Path: inpxutil.OnlineArchivePath, Name: inpxutil.OnlineArchiveName},
		Records: make(map[int]model.Record),
	}
}

func logDuplicateArchiveIndex(log *zap.Logger, part string, archivePath string, index int, existing model.Record, duplicate model.Record) {
	if log == nil {
		return
	}
	fields := []zap.Field{
		zap.String("part", part),
		zap.String("archive", archivePath),
		zap.Int("archive_index", index),
		zap.Int64("existing_book_id", existing.ID.BookID),
		zap.Int64("duplicate_book_id", duplicate.ID.BookID),
	}
	if existing.ID.Archive != nil {
		fields = append(fields, zap.String("existing_archive_entry", existing.ID.Archive.Entry))
	}
	if duplicate.ID.Archive != nil {
		fields = append(fields, zap.String("duplicate_archive_entry", duplicate.ID.Archive.Entry))
	}
	log.Warn("Duplicate archive index in INPX input; keeping first record", fields...)
}

func writeINPX(
	ctx context.Context,
	path string,
	meta model.MergeMetadata,
	archives map[string]*inpxutil.DatasetArchiveRows,
	opts Options,
) (Stats, error) {
	stats := Stats{DumpDate: meta.Database.DumpDate}
	f, err := os.Create(path)
	if err != nil {
		return stats, fmt.Errorf("create INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(zipComment(meta))
	archiveList := make([]*inpxutil.DatasetArchiveRows, 0, len(archives))
	for _, archive := range archives {
		archiveList = append(archiveList, archive)
	}
	sort.Slice(archiveList, func(i, j int) bool { return archiveList[i].Meta.Name < archiveList[j].Meta.Name })
	for _, archive := range archiveList {
		if err := ctx.Err(); err != nil {
			zw.Close()
			f.Close()
			return stats, err
		}
		archiveStats, err := writeArchiveINP(zw, archive, opts)
		if err != nil {
			zw.Close()
			f.Close()
			return stats, err
		}
		stats.Archives++
		stats.Files += archiveStats.Files
		stats.Records += archiveStats.Records
		stats.DBRecords += archiveStats.DBRecords
		stats.FB2Records += archiveStats.FB2Records
		stats.Dummy += archiveStats.Dummy
	}
	collection, err := collectionInfo(meta, opts)
	if err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	if err := writeZipText(zw, "collection.info", collection); err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	version, err := versionInfo(meta, opts)
	if err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	if err := writeZipText(zw, "version.info", version); err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return stats, fmt.Errorf("close INPX zip %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return stats, fmt.Errorf("close INPX %q: %w", path, err)
	}
	return stats, nil
}

func writeArchiveINP(zw *zip.Writer, archive *inpxutil.DatasetArchiveRows, opts Options) (Stats, error) {
	start := time.Now()
	stats := Stats{}
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	w, err := zw.Create(name)
	if err != nil {
		return stats, fmt.Errorf("create INPX entry %q: %w", name, err)
	}
	bw := bufio.NewWriter(w)
	for idx := 0; idx < archive.Meta.Entries; idx++ {
		if inRanges(archive.Meta.Ignored, idx) {
			continue
		}
		stats.Files++
		rec, ok := archive.Records[idx]
		line := ""
		var view inpxutil.DatasetRecordView
		if ok {
			var err error
			line, view, err = recordLine(rec, opts)
			if err != nil {
				return stats, err
			}
		}
		if line == "" {
			line = dummyLine(idx + 1)
			stats.Dummy++
		} else {
			stats.Records++
			if view.HasDatabase {
				stats.DBRecords++
			} else {
				stats.FB2Records++
			}
		}
		if _, err := bw.WriteString(line); err != nil {
			return stats, fmt.Errorf("write INPX entry %q: %w", name, err)
		}
	}
	if err := bw.Flush(); err != nil {
		return stats, err
	}
	if opts.Log != nil {
		opts.Log.Info(
			"INPX entry created",
			zap.String("entry", name),
			zap.String("archive", archive.Meta.Name),
			zap.Int64("records", stats.DBRecords),
			zap.Int64("fb2_records", stats.FB2Records),
			zap.Int64("dummy_records", stats.Dummy),
			zap.Int("files", stats.Files),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return stats, nil
}

func writeZipText(zw *zip.Writer, name string, text string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create INPX entry %q: %w", name, err)
	}
	_, err = io.WriteString(w, text)
	return err
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
		b.WriteString(fix(person.LastName, opts.QuickFix, opts.Limits.AuthorFamily))
		b.WriteByte(',')
		b.WriteString(fix(person.FirstName, opts.QuickFix, opts.Limits.AuthorName))
		b.WriteByte(',')
		b.WriteString(fix(person.MiddleName, opts.QuickFix, opts.Limits.AuthorMiddle))
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
			b.WriteString(genre.Code)
			b.WriteByte(':')
		}
		return b.String()
	}
	if len(fb2Genres) > 0 {
		var b strings.Builder
		for _, genre := range fb2Genres {
			b.WriteString(genre.Code)
			b.WriteByte(':')
		}
		return b.String()
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
	if rec.Record.Locator.BookID != nil {
		return strconv.FormatInt(*rec.Record.Locator.BookID, 10)
	}
	if rec.Identities == nil {
		return ""
	}
	for _, identity := range rec.Identities.Catalog {
		if identity.Scheme == "flibusta.book" {
			return identity.Value
		}
	}
	return ""
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

func datasetMetadata(dataset model.Dataset) model.MergeMetadata {
	meta := model.MergeMetadata{Library: dataset.Library}
	if dataset.Database != nil {
		meta.Database.DumpDate = dataset.Database.DumpDate
		if len(dataset.Database.DumpDate) == 8 {
			meta.Database.DumpDateISO = dataset.Database.DumpDate[:4] + "-" + dataset.Database.DumpDate[4:6] + "-" + dataset.Database.DumpDate[6:8]
		}
	}
	return meta
}

func joinINPFields(fields []string) string {
	for i := range fields {
		fields[i] = inpxutil.Cleanse(fields[i])
	}
	return strings.Join(fields, fieldSep) + fieldSep + "\r\n"
}

func inRanges(ranges []model.IndexRange, idx int) bool {
	for _, r := range ranges {
		if idx >= r.Start && idx <= r.End {
			return true
		}
	}
	return false
}

func zipComment(meta model.MergeMetadata) string {
	return meta.Library + " - " + displayDate(meta)
}

type commentTemplateContext struct {
	DatabaseName string
	DumpDate     string
	DisplayDate  string
}

func collectionInfo(meta model.MergeMetadata, opts Options) (string, error) {
	if opts.CommentTemplate == "" {
		return "", errors.New("collection.info comment template is empty")
	}
	return renderInfoTemplate("comment_template", opts.CommentTemplate, meta)
}

func versionInfo(meta model.MergeMetadata, opts Options) (string, error) {
	if opts.VersionTemplate == "" {
		return "", errors.New("version.info template is empty")
	}
	return renderInfoTemplate("version_template", opts.VersionTemplate, meta)
}

func renderInfoTemplate(name string, text string, meta model.MergeMetadata) (string, error) {
	tmpl, err := template.New(name).Funcs(sprig.FuncMap()).Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	values := commentTemplateContext{
		DatabaseName: meta.Library,
		DumpDate:     meta.Database.DumpDate,
		DisplayDate:  displayDate(meta),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("execute %s: %w", name, err)
	}
	return buf.String(), nil
}

func displayDate(meta model.MergeMetadata) string {
	if meta.Database.DumpDateISO != "" {
		return meta.Database.DumpDateISO
	}
	date := meta.Database.DumpDate
	if len(date) == 8 {
		return date[:4] + "-" + date[4:6] + "-" + date[6:8]
	}
	return date
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
