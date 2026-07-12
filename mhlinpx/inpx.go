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
	metaPath, err := discoverMetadata(opts.InputPrefix)
	if err != nil {
		return stats, err
	}
	if opts.Log != nil {
		opts.Log.Info("INPX metadata selected", zap.String("metadata", metaPath))
	}
	meta, err := readMetadata(metaPath)
	if err != nil {
		return stats, err
	}
	stats.DumpDate = meta.Database.DumpDate
	parts, err := discoverInputParts(opts.InputPrefix, metaPath, meta, opts.Log)
	if err != nil {
		return stats, err
	}
	if opts.Log != nil {
		opts.Log.Info(
			"INPX input parts selected",
			zap.Int("parts", len(parts)),
			zap.Int("archives", len(meta.Archives)),
			zap.String("dump_date", meta.Database.DumpDate),
		)
	}
	archives := make(map[string]*archiveRows, len(meta.Archives))
	for _, archive := range meta.Archives {
		archives[archive.Path] = &archiveRows{Meta: archive, Records: make(map[int]model.Record)}
	}
	loadStart := time.Now()
	loaded, err := readRecords(ctx, parts, archives, opts.Log)
	if err != nil {
		return stats, err
	}
	if opts.Log != nil {
		opts.Log.Info("INPX records loaded", zap.Int64("records", loaded), zap.Int("parts", len(parts)), zap.Duration("elapsed", time.Since(loadStart)))
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

func writeINPX(ctx context.Context, path string, meta model.MergeMetadata, archives map[string]*archiveRows, opts Options) (Stats, error) {
	stats := Stats{DumpDate: meta.Database.DumpDate}
	f, err := os.Create(path)
	if err != nil {
		return stats, fmt.Errorf("create INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(zipComment(meta))
	archiveList := make([]*archiveRows, 0, len(archives))
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

func writeArchiveINP(zw *zip.Writer, archive *archiveRows, opts Options) (Stats, error) {
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
		if ok {
			line = recordLine(rec, opts)
		}
		if line == "" {
			line = dummyLine(idx + 1)
			stats.Dummy++
		} else {
			stats.Records++
			if rec.Source.Database.Present {
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

func recordLine(rec model.Record, opts Options) string {
	db := rec.Source.Database
	fb2 := rec.Source.FB2
	titleInfo := fb2TitleInfo(fb2)
	book := db.Book
	if book == nil && titleInfo == nil {
		return ""
	}
	title := ""
	if book != nil {
		title = book.Title
	}
	if title == "" && titleInfo != nil {
		title = titleInfo.Title
	}
	authors := authorsString(db.Present, db.Authors, titleInfo, opts)
	genres := genresString(db.Genres, titleInfo)
	sequence, seqNum := sequenceString(db.Sequences, titleInfo, opts)
	fileName := rec.ID.FileName
	if fileName == "" && rec.ID.BookID > 0 {
		fileName = strconv.FormatInt(rec.ID.BookID, 10)
	}
	size := int64(0)
	deleted := ""
	ext := rec.ID.Extension
	date := ""
	lang := ""
	rate := ""
	keywords := ""
	md5 := ""
	replaced := ""
	if book != nil {
		size = book.FileSize
		deleted = book.Deleted
		if ext == "" {
			ext = book.FileType
		}
		date = dateOnly(book.Time)
		lang = book.Lang
		keywords = book.Keywords
		md5 = book.MD5
		if book.ReplacedBy > 0 {
			replaced = strconv.FormatInt(book.ReplacedBy, 10)
		}
	}
	if size == 0 && rec.ID.Archive != nil {
		size = int64(rec.ID.Archive.UncompressedSize)
	}
	if date == "" && rec.ID.Archive != nil {
		date = dateOnly(rec.ID.Archive.Modified)
	}
	if lang == "" && titleInfo != nil {
		lang = titleInfo.Language
	}
	if keywords == "" && titleInfo != nil {
		keywords = titleInfo.Keywords
	}
	if db.Rating != nil && db.Rating.Count > 0 {
		rate = strconv.FormatInt(int64(db.Rating.Average), 10)
	}
	fields := []string{
		authors,
		genres,
		fix(title, opts.QuickFix, opts.Limits.Title),
		fix(sequence, opts.QuickFix, opts.Limits.Sequence),
		seqNum,
		fileName,
		strconv.FormatInt(size, 10),
		strconv.FormatInt(rec.ID.BookID, 10),
		deleted,
		ext,
		date,
		strings.TrimSpace(lang),
		rate,
		fix(keywords, opts.QuickFix, opts.Limits.Keywords),
	}
	if opts.Format == FormatRUKS {
		fields = append(fields, md5, replaced)
	}
	return joinINPFields(fields)
}

func fb2TitleInfo(src model.FB2Source) *model.FB2TitleInfo {
	if src.Description == nil {
		return nil
	}
	return src.Description.TitleInfo
}

func authorsString(dbPresent bool, authors []model.Contributor, titleInfo *model.FB2TitleInfo, opts Options) string {
	if opts.FB2Preference == PreferReplace && titleInfo != nil && len(titleInfo.Authors) > 0 {
		return fb2AuthorsString(titleInfo.Authors, opts)
	}
	if dbPresent && len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	if len(authors) == 0 && titleInfo != nil && len(titleInfo.Authors) > 0 {
		return fb2AuthorsString(titleInfo.Authors, opts)
	}
	if len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	var b strings.Builder
	for _, author := range authors {
		b.WriteString(fix(author.LastName, opts.QuickFix, opts.Limits.AuthorFamily))
		b.WriteByte(',')
		b.WriteString(fix(author.FirstName, opts.QuickFix, opts.Limits.AuthorName))
		b.WriteByte(',')
		b.WriteString(fix(author.MiddleName, opts.QuickFix, opts.Limits.AuthorMiddle))
		b.WriteByte(':')
	}
	return b.String()
}

func fb2AuthorsString(authors []model.FB2Person, opts Options) string {
	var b strings.Builder
	for _, author := range authors {
		b.WriteString(fix(author.LastName, opts.QuickFix, opts.Limits.AuthorFamily))
		b.WriteByte(',')
		b.WriteString(fix(author.FirstName, opts.QuickFix, opts.Limits.AuthorName))
		b.WriteByte(',')
		b.WriteString(fix(author.MiddleName, opts.QuickFix, opts.Limits.AuthorMiddle))
		b.WriteByte(':')
	}
	if b.Len() == 0 {
		return "неизвестный,автор,:"
	}
	return b.String()
}

func genresString(genres []model.DBGenre, titleInfo *model.FB2TitleInfo) string {
	if len(genres) > 0 {
		var b strings.Builder
		for _, genre := range genres {
			b.WriteString(genre.Code)
			b.WriteByte(':')
		}
		return b.String()
	}
	if titleInfo != nil && len(titleInfo.Genres) > 0 {
		var b strings.Builder
		for _, genre := range titleInfo.Genres {
			b.WriteString(genre.Code)
			b.WriteByte(':')
		}
		return b.String()
	}
	return "other:"
}

func sequenceString(sequences []model.DBSequence, titleInfo *model.FB2TitleInfo, opts Options) (string, string) {
	dbName, dbNum := dbSequence(sequences, opts.SequenceMode)
	fbName, fbNum := fb2Sequence(titleInfo)
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

func dbSequence(sequences []model.DBSequence, mode SequenceMode) (string, string) {
	if mode == SequenceIgnore || len(sequences) == 0 {
		return "", ""
	}
	slices.SortFunc(sequences, func(a, b model.DBSequence) int {
		if a.Type != b.Type {
			if mode == SequencePublisher {
				return int(b.Type - a.Type)
			}
			return int(a.Type - b.Type)
		}
		if a.Level != b.Level {
			return int(a.Level - b.Level)
		}
		return strings.Compare(a.Name, b.Name)
	})
	seq := sequences[0]
	num := strconv.FormatInt(seq.Number, 10)
	return seq.Name, num
}

func fb2Sequence(titleInfo *model.FB2TitleInfo) (string, string) {
	if titleInfo == nil || len(titleInfo.Sequences) == 0 {
		return "", ""
	}
	seq := titleInfo.Sequences[0]
	num := ""
	if seq.Number != "" {
		if value, err := strconv.ParseFloat(seq.Number, 64); err == nil {
			num = strconv.Itoa(int(value))
		} else {
			num = seq.Number
		}
	}
	return seq.Name, num
}

func dummyLine(index int) string {
	fields := []string{"dummy:", "other:", "dummy record", "", "", "", "1", strconv.Itoa(index), "1", "EXT", "2000-01-01", "en", "0", ""}
	return joinINPFields(fields)
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
