package flibinpx

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

const structureInfo = "AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;YEAR;SOURCELIB;"

type SequenceMode string

const (
	SequenceAuthor    SequenceMode = "author"
	SequencePublisher SequenceMode = "publisher"
	SequenceAll       SequenceMode = "all"
	SequenceIgnore    SequenceMode = "ignore"
)

type FB2Preference string

const (
	PreferIgnore     FB2Preference = "ignore"
	PreferMerge      FB2Preference = "merge"
	PreferComplement FB2Preference = "complement"
	PreferReplace    FB2Preference = "replace"
)

type FlattenMode string

const (
	FlattenAll      FlattenMode = "all"
	FlattenLeaf     FlattenMode = "leaf"
	FlattenPath     FlattenMode = "path"
	FlattenPathLeaf FlattenMode = "path-leaf"
)

type DedupMode string

const (
	DedupCaseInsensitive DedupMode = "case-insensitive"
	DedupCaseSensitive   DedupMode = "case-sensitive"
)

type Options struct {
	InputPrefix      string
	OutputPrefix     string
	SequenceMode     SequenceMode
	FB2Preference    FB2Preference
	FlattenMode      FlattenMode
	DedupMode        DedupMode
	FB2PathSeparator string
	SourceLib        string
	CommentTemplate  string
	VersionTemplate  string
	Log              *zap.Logger
}

type Stats = inpxutil.Stats

type sequence struct {
	Name   string
	Number string
	Source string
}

type recordFields struct {
	Authors  string
	Genres   string
	Title    string
	File     string
	Size     string
	LibID    string
	Deleted  string
	Ext      string
	Date     string
	Folder   string
	Lang     string
	Rate     string
	Keywords string
	Year     string
	Source   string
}

func ParseSequenceMode(value string) (SequenceMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "author":
		return SequenceAuthor, nil
	case "publisher":
		return SequencePublisher, nil
	case "all":
		return SequenceAll, nil
	case "ignore":
		return SequenceIgnore, nil
	default:
		return "", fmt.Errorf("invalid FLibrary INPX sequence mode %q", value)
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
		return "", fmt.Errorf("invalid FLibrary INPX FB2 preference %q", value)
	}
}

func ParseFlattenMode(value string) (FlattenMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return FlattenAll, nil
	case "leaf":
		return FlattenLeaf, nil
	case "path":
		return FlattenPath, nil
	case "path-leaf":
		return FlattenPathLeaf, nil
	default:
		return "", fmt.Errorf("invalid FLibrary INPX FB2 flatten mode %q", value)
	}
}

func ParseDedupMode(value string) (DedupMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "case-insensitive":
		return DedupCaseInsensitive, nil
	case "case-sensitive":
		return DedupCaseSensitive, nil
	default:
		return "", fmt.Errorf("invalid FLibrary INPX sequence dedup mode %q", value)
	}
}

func Generate(ctx context.Context, opts Options) (Stats, error) {
	stats := Stats{}
	if opts.InputPrefix == "" {
		return stats, errors.New("FLibrary INPX input prefix is required")
	}
	if opts.OutputPrefix == "" {
		return stats, errors.New("FLibrary INPX output prefix is required")
	}
	if opts.SequenceMode == "" {
		opts.SequenceMode = SequenceAuthor
	}
	if opts.FB2Preference == "" {
		opts.FB2Preference = PreferComplement
	}
	if opts.FlattenMode == "" {
		opts.FlattenMode = FlattenAll
	}
	if opts.DedupMode == "" {
		opts.DedupMode = DedupCaseInsensitive
	}
	if opts.FB2PathSeparator == "" {
		opts.FB2PathSeparator = " / "
	}
	dataset, archives, _, err := inpxutil.LoadDatasetInput(ctx, opts.InputPrefix, opts.Log)
	if err != nil {
		return stats, err
	}
	meta := inpxutil.DatasetMetadata(dataset)
	inpxutil.EnsureDumpDate(&meta, opts.Log)
	if opts.SourceLib == "" {
		opts.SourceLib = dataset.Library
	}
	stats.DumpDate = meta.DumpDate
	outputPath, err := inpxutil.OutputPath(opts.OutputPrefix, meta)
	if err != nil {
		return stats, err
	}
	stats.OutputPath = outputPath
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return stats, fmt.Errorf("create FLibrary INPX output directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+"-*.tmp")
	if err != nil {
		return stats, fmt.Errorf("create temporary FLibrary INPX output: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return stats, fmt.Errorf("close temporary FLibrary INPX output %q: %w", tmpPath, err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := os.Stat(outputPath); err == nil && opts.Log != nil {
		opts.Log.Warn("Overwriting existing FLibrary INPX output", zap.String("file", outputPath))
	} else if err != nil && !os.IsNotExist(err) {
		return stats, fmt.Errorf("stat FLibrary INPX output %q: %w", outputPath, err)
	}
	if opts.Log != nil {
		opts.Log.Info("FLibrary INPX creation started", zap.String("file", outputPath), zap.Int("archives", len(archives)))
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
		return stats, fmt.Errorf("replace FLibrary INPX output %q: %w", outputPath, err)
	}
	cleanupTemp = false
	return stats, nil
}

func writeINPX(
	ctx context.Context,
	path string,
	meta inpxutil.Metadata,
	archives map[string]*inpxutil.DatasetArchiveRows,
	opts Options,
) (Stats, error) {
	stats := Stats{DumpDate: meta.DumpDate}
	f, err := os.Create(path)
	if err != nil {
		return stats, fmt.Errorf("create FLibrary INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(meta))
	for _, archive := range inpxutil.DatasetArchiveList(archives) {
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
	}
	if err := inpxutil.WriteZipText(zw, "structure.info", structureInfo); err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	collection, err := inpxutil.CollectionInfo(meta, templateOptions(opts))
	if err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	if err := inpxutil.WriteZipText(zw, "collection.info", collection); err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	version, err := inpxutil.VersionInfo(meta, templateOptions(opts))
	if err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	if err := inpxutil.WriteZipText(zw, "version.info", version); err != nil {
		zw.Close()
		f.Close()
		return stats, err
	}
	return stats, inpxutil.CloseZipFile(path, zw, f)
}

func templateOptions(opts Options) inpxutil.TemplateOptions {
	return inpxutil.TemplateOptions{CommentTemplate: opts.CommentTemplate, VersionTemplate: opts.VersionTemplate}
}

func writeArchiveINP(zw *zip.Writer, archive *inpxutil.DatasetArchiveRows, opts Options) (Stats, error) {
	start := time.Now()
	stats := Stats{}
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	w, err := zw.Create(name)
	if err != nil {
		return stats, fmt.Errorf("create FLibrary INPX entry %q: %w", name, err)
	}
	bw := bufio.NewWriter(w)
	indexes := make([]int, 0, len(archive.Records))
	for idx := range archive.Records {
		indexes = append(indexes, idx)
	}
	slices.Sort(indexes)
	insNo := 0
	for _, idx := range indexes {
		if inpxutil.InRanges(archive.Meta.Ignored, idx) {
			continue
		}
		rec := archive.Records[idx]
		fields, view, ok, err := buildRecordFields(rec, archive.Meta, opts)
		if err != nil {
			return stats, err
		}
		if !ok {
			continue
		}
		stats.Files++
		sequences := recordSequences(rec, view, opts)
		if len(sequences) == 0 {
			sequences = []sequence{{}}
		}
		for _, seq := range sequences {
			insNo++
			if _, err := bw.WriteString(recordLine(fields, seq, insNo)); err != nil {
				return stats, fmt.Errorf("write FLibrary INPX entry %q: %w", name, err)
			}
			stats.Records++
			if view.HasDatabase {
				stats.DBRecords++
			} else {
				stats.FB2Records++
			}
		}
	}
	if err := bw.Flush(); err != nil {
		return stats, err
	}
	if opts.Log != nil {
		opts.Log.Info(
			"FLibrary INPX entry created",
			zap.String("entry", name),
			zap.String("archive", archive.Meta.Name),
			zap.Int64("records", stats.DBRecords),
			zap.Int64("fb2_records", stats.FB2Records),
			zap.Int("files", stats.Files),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return stats, nil
}

func buildRecordFields(
	rec model.DatasetRecord,
	archive model.DatasetArchive,
	opts Options,
) (recordFields, inpxutil.DatasetRecordView, bool, error) {
	view, err := inpxutil.DatasetRecordClaims(rec)
	if err != nil {
		return recordFields{}, view, false, err
	}
	ext := view.Catalog.FileType
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(view.Artifact.Name), ".")
	}
	if !strings.EqualFold(strings.TrimPrefix(ext, "."), "fb2") {
		return recordFields{}, view, false, nil
	}
	title := view.Database.Title
	if title == "" {
		title = view.FB2.Title
	}
	if title == "" {
		return recordFields{}, view, false, nil
	}
	fileName := strings.TrimSuffix(view.Artifact.Name, filepath.Ext(view.Artifact.Name))
	if fileName == "" {
		fileName = datasetBookID(rec)
	}
	size := view.Artifact.Size
	date := inpxutil.DateOnly(view.Catalog.Time)
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
	year := view.DatabasePublication.Year
	if year == "" {
		year = view.FB2Publication.Year
	}
	folder := filepath.Base(archive.PathHint)
	if folder == "." || folder == string(filepath.Separator) || folder == "" {
		folder = archive.Name
	}
	return recordFields{
		Authors:  authorsString(view.HasDatabase, view.Database.Authors, view.FB2.Authors, opts),
		Genres:   genresString(view.Database.Genres, view.FB2.Genres),
		Title:    inpxutil.Cleanse(title),
		File:     inpxutil.Cleanse(fileName),
		Size:     strconv.FormatUint(size, 10),
		LibID:    datasetBookID(rec),
		Deleted:  inpxutil.Cleanse(view.Catalog.Deleted),
		Ext:      inpxutil.Cleanse(strings.TrimPrefix(ext, ".")),
		Date:     inpxutil.Cleanse(date),
		Folder:   inpxutil.Cleanse(folder),
		Lang:     inpxutil.Cleanse(strings.TrimSpace(lang)),
		Rate:     view.Catalog.Rating,
		Keywords: keywordsString(keywords),
		Year:     inpxutil.Cleanse(year),
		Source:   inpxutil.Cleanse(opts.SourceLib),
	}, view, true, nil
}

func recordLine(fields recordFields, seq sequence, insNo int) string {
	values := []string{
		fields.Authors,
		fields.Genres,
		fields.Title,
		inpxutil.Cleanse(seq.Name),
		inpxutil.Cleanse(seq.Number),
		fields.File,
		fields.Size,
		fields.LibID,
		fields.Deleted,
		fields.Ext,
		fields.Date,
		strconv.Itoa(insNo),
		fields.Folder,
		fields.Lang,
		fields.Rate,
		fields.Keywords,
		fields.Year,
		fields.Source,
	}
	return strings.Join(values, inpxutil.FieldSep) + inpxutil.FieldSep + "\r\n"
}

func authorsString(dbPresent bool, authors []model.PersonValue, fb2Authors []model.PersonValue, opts Options) string {
	if opts.FB2Preference == PreferReplace && len(fb2Authors) > 0 {
		return peopleString(fb2Authors)
	}
	if dbPresent && len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	if len(authors) == 0 && len(fb2Authors) > 0 {
		return peopleString(fb2Authors)
	}
	if len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	return peopleString(authors)
}

func peopleString(people []model.PersonValue) string {
	var b strings.Builder
	for _, person := range people {
		b.WriteString(inpxutil.Cleanse(person.LastName))
		b.WriteByte(',')
		b.WriteString(inpxutil.Cleanse(person.FirstName))
		b.WriteByte(',')
		b.WriteString(inpxutil.Cleanse(person.MiddleName))
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
			b.WriteString(inpxutil.Cleanse(genre.Code))
			b.WriteByte(':')
		}
		return b.String()
	}
	if len(fb2Genres) > 0 {
		var b strings.Builder
		for _, genre := range fb2Genres {
			b.WriteString(inpxutil.Cleanse(genre.Code))
			b.WriteByte(':')
		}
		return b.String()
	}
	return "other:"
}

func recordSequences(rec model.DatasetRecord, view inpxutil.DatasetRecordView, opts Options) []sequence {
	dbSeqs := dbSequences(view.Database.Sequences, opts.SequenceMode)
	fb2Seqs := fb2Sequences(view.FB2.Sequences, view.FB2Publication.Sequences, opts)
	var selected []sequence
	switch opts.FB2Preference {
	case PreferIgnore:
		selected = dbSeqs
	case PreferMerge:
		selected = append(append([]sequence{}, dbSeqs...), fb2Seqs...)
	case PreferReplace:
		if len(fb2Seqs) > 0 {
			selected = fb2Seqs
		} else {
			selected = dbSeqs
		}
	default:
		if len(dbSeqs) > 0 {
			selected = dbSeqs
		} else {
			selected = fb2Seqs
		}
	}
	return dedupSequences(rec, selected, opts)
}

func dbSequences(sequences []model.SequenceValue, mode SequenceMode) []sequence {
	if mode == SequenceIgnore || len(sequences) == 0 {
		return nil
	}
	filtered := slices.DeleteFunc(slices.Clone(sequences), func(seq model.SequenceValue) bool {
		if seq.Type == nil {
			return true
		}
		switch mode {
		case SequenceAuthor:
			return *seq.Type != 0
		case SequencePublisher:
			return *seq.Type != 1
		default:
			return *seq.Type != 0 && *seq.Type != 1
		}
	})
	slices.SortFunc(filtered, func(a, b model.SequenceValue) int {
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
	result := make([]sequence, 0, len(filtered))
	for _, seq := range filtered {
		result = append(result, sequence{Name: seq.Name, Number: sequenceNumber(seq.Number), Source: "db"})
	}
	return result
}

func fb2Sequences(titleSequences []model.SequenceValue, publicationSequences []model.SequenceValue, opts Options) []sequence {
	var result []sequence
	if opts.SequenceMode == SequenceAuthor || opts.SequenceMode == SequenceAll || opts.SequenceMode == SequenceIgnore {
		result = append(result, flattenFB2Sequences(titleSequences, opts.FlattenMode, opts.FB2PathSeparator)...)
	}
	if opts.SequenceMode == SequencePublisher || opts.SequenceMode == SequenceAll {
		result = append(result, flattenFB2Sequences(publicationSequences, opts.FlattenMode, opts.FB2PathSeparator)...)
	}
	return result
}

func flattenFB2Sequences(sequences []model.SequenceValue, mode FlattenMode, separator string) []sequence {
	var result []sequence
	var walk func(seq model.SequenceValue, path []string)
	walk = func(seq model.SequenceValue, path []string) {
		name := strings.TrimSpace(seq.Name)
		if name == "" {
			return
		}
		path = append(path, name)
		isLeaf := len(seq.Sequences) == 0
		number := sequenceNumber(seq.Number)
		switch mode {
		case FlattenLeaf:
			if isLeaf {
				result = append(result, sequence{Name: name, Number: number, Source: "fb2"})
			}
		case FlattenPath:
			if isLeaf {
				result = append(result, sequence{Name: strings.Join(path, separator), Number: number, Source: "fb2"})
			}
		case FlattenPathLeaf:
			if isLeaf {
				result = append(result, sequence{Name: strings.Join(path, separator), Number: number, Source: "fb2"})
				result = append(result, sequence{Name: name, Number: number, Source: "fb2"})
			}
		default:
			result = append(result, sequence{Name: name, Number: number, Source: "fb2"})
		}
		for _, nested := range seq.Sequences {
			walk(nested, path)
		}
	}
	for _, seq := range sequences {
		walk(seq, nil)
	}
	return result
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

func dedupSequences(rec model.DatasetRecord, sequences []sequence, opts Options) []sequence {
	seen := make(map[string]sequence, len(sequences))
	result := make([]sequence, 0, len(sequences))
	for _, seq := range sequences {
		seq.Name = strings.TrimSpace(seq.Name)
		if seq.Name == "" {
			continue
		}
		key := seq.Name
		if opts.DedupMode == DedupCaseInsensitive {
			key = strings.ToLower(key)
		}
		if kept, ok := seen[key]; ok {
			if opts.Log != nil {
				fields := []zap.Field{
					zap.String("book_id", datasetBookID(rec)),
					zap.String("locator_kind", rec.Record.Locator.Kind),
					zap.String("locator_source", rec.Record.Locator.Source),
					zap.String("name", seq.Name),
					zap.String("number", seq.Number),
					zap.String("source", seq.Source),
					zap.String("kept_name", kept.Name),
					zap.String("kept_number", kept.Number),
					zap.String("kept_source", kept.Source),
				}
				if rec.Record.Locator.Index != nil {
					fields = append(
						fields,
						zap.Int("archive_index", *rec.Record.Locator.Index),
					)
				}
				opts.Log.Debug("Dropped duplicate FLibrary sequence", fields...)
			}
			continue
		}
		seen[key] = seq
		result = append(result, seq)
	}
	return result
}

func sequenceLevel(seq model.SequenceValue) int64 {
	if seq.Level == nil {
		return 0
	}
	return *seq.Level
}

func datasetBookID(rec model.DatasetRecord) string {
	return inpxutil.DatasetBookID(rec)
}

func keywordsString(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ',', ';', '/', '.', '(', ')', '[', ']', ':':
			return true
		default:
			return false
		}
	})
	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(inpxutil.Cleanse(part))
		if part == "" {
			continue
		}
		b.WriteString(part)
		b.WriteByte(':')
	}
	return b.String()
}
