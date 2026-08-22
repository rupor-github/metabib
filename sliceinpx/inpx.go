package sliceinpx

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	jsonv2 "encoding/json/v2"
	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/internal/inpxutil"
	"metabib/model"
)

const structureInfo = "AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;YEAR;"

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
	InputPrefix         string
	OutputPrefix        string
	Additional          bool
	SequenceMode        SequenceMode
	FB2Preference       FB2Preference
	FlattenMode         FlattenMode
	DedupMode           DedupMode
	FB2PathSeparator    string
	Where               string
	SplitBy             string
	DisambiguateAuthors bool
	Language            *inpxutil.LanguageResolver
	CommentTemplate     string
	VersionTemplate     string
	Log                 *zap.Logger
	Verbose             bool
	AuthorDisambiguator *inpxutil.AuthorDisambiguator
}

type Stats struct {
	OutputPath               string
	AdditionalOutputPath     string
	CompilationsOutputPath   string
	DumpDate                 string
	Archives                 int
	Files                    int64
	Records                  int64
	DBRecords                int64
	FB2Records               int64
	FilteredRecords          int64
	SkippedNonArchiveRecords int64
	SkippedIgnoredRecords    int64
	SkippedInvalidRecords    int64
	DisambiguatedAuthorBooks int64
	DisambiguatedAuthors     int64
	CanonicalizedLangBooks   int64
	Splits                   []SplitStats
}

type SplitStats struct {
	Entry   string
	Records int64
	Books   int64
}

type sequence struct {
	Name   string
	Number string
	Source string
}

type recordFields struct {
	Author   string
	Genre    string
	Title    string
	File     string
	Size     string
	LibID    string
	Deleted  string
	Ext      string
	Date     string
	InsNo    string
	Folder   string
	Lang     string
	LibRate  string
	Keywords string
	Year     string
}

type entryDiagnostics struct {
	DisambiguatedAuthorBooks int64
	DisambiguatedAuthors     int64
	CanonicalizedLangBooks   int64
}

type FilterRecord struct {
	del string

	InputRecord  int64
	AcceptedBook int64
	AcceptedRow  int64
	BookRow      int

	Author   string
	Genre    string
	Title    string
	Series   string
	SerNo    string
	File     string
	Size     string
	LibID    string
	Deleted  bool
	Ext      string
	Date     string
	InsNo    int
	Folder   string
	Lang     string
	LibRate  string
	Keywords string
	Year     string

	Authors   []Person
	Genres    []string
	Sequences []Sequence

	HasDatabase bool
	HasFB2      bool
	ArchiveID   string
	ArchiveName string
}

type Person struct {
	FirstName  string
	MiddleName string
	LastName   string
	NickName   string
	ID         string
}

type Sequence struct {
	Name   string
	Number string
	Source string
}

type row struct {
	line string
	ctx  FilterRecord
}

type splitWriter struct {
	key     string
	entry   string
	path    string
	file    *os.File
	bw      *bufio.Writer
	records int64
	books   int64
}

type streamINPXWriter struct {
	path                   string
	meta                   inpxutil.Metadata
	opts                   Options
	where                  *inpxutil.RecordTemplate
	splitBy                *inpxutil.RecordTemplate
	zw                     *zip.Writer
	f                      *os.File
	archives               []*inpxutil.DatasetArchiveRows
	archiveByID            map[string]int
	splits                 map[string]*splitWriter
	splitOrder             []string
	stats                  Stats
	activeDiag             entryDiagnostics
	inputRecord            int64
	acceptedBooks          int64
	acceptedRows           int64
	annotations            *annotationCollector
	compilations           *compilationCollector
	compilationsOutputPath string
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
		return "", fmt.Errorf("invalid INPX slice sequence mode %q", value)
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
		return "", fmt.Errorf("invalid INPX slice FB2 preference %q", value)
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
		return "", fmt.Errorf("invalid INPX slice FB2 flatten mode %q", value)
	}
}

func ParseDedupMode(value string) (DedupMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "case-insensitive":
		return DedupCaseInsensitive, nil
	case "case-sensitive":
		return DedupCaseSensitive, nil
	default:
		return "", fmt.Errorf("invalid INPX slice sequence dedup mode %q", value)
	}
}

func Generate(ctx context.Context, opts Options) (Stats, error) {
	stats := Stats{}
	if opts.InputPrefix == "" {
		return stats, errors.New("INPX slice input prefix is required")
	}
	if opts.OutputPrefix == "" {
		return stats, errors.New("INPX slice output prefix is required")
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
	where, err := optionalTemplate("where", opts.Where)
	if err != nil {
		return stats, err
	}
	splitBy, err := optionalTemplate("split-by", opts.SplitBy)
	if err != nil {
		return stats, err
	}

	var stream *streamINPXWriter
	var tmpPath string
	var annotationsTmpPath string
	var compilationsTmpPath string
	cleanupTemp := true
	defer func() {
		if cleanupTemp && tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
		if cleanupTemp && annotationsTmpPath != "" {
			_ = os.Remove(annotationsTmpPath)
		}
		if cleanupTemp && compilationsTmpPath != "" {
			_ = os.Remove(compilationsTmpPath)
		}
	}()

	var meta inpxutil.Metadata
	_, loaded, err := inpxutil.StreamDatasetInput(
		ctx,
		opts.InputPrefix,
		opts.Log,
		func(dataset model.Dataset) error {
			if len(dataset.Archives) == 0 {
				return errors.New("INPX slice requires archive-backed dataset input")
			}
			meta = inpxutil.DatasetMetadata(dataset)
			inpxutil.EnsureDumpDate(&meta, opts.Log)
			if opts.DisambiguateAuthors && dataset.Database != nil {
				opts.AuthorDisambiguator = inpxutil.NewAuthorDisambiguator(dataset.Database.INPX, opts.Log, opts.Verbose)
			}
			stats.DumpDate = meta.DumpDate
			outputPath, err := inpxutil.OutputPath(opts.OutputPrefix, meta)
			if err != nil {
				return err
			}
			stats.OutputPath = outputPath
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				return fmt.Errorf("create INPX slice output directory: %w", err)
			}
			tmpFile, err := fileutil.CreateHiddenTemp(filepath.Dir(outputPath), filepath.Base(outputPath))
			if err != nil {
				return fmt.Errorf("create temporary INPX slice output: %w", err)
			}
			tmpPath = tmpFile.Name()
			if err := tmpFile.Close(); err != nil {
				return fmt.Errorf("close temporary INPX slice output %q: %w", tmpPath, err)
			}
			if _, err := os.Stat(outputPath); err == nil && opts.Log != nil {
				opts.Log.Warn("Overwriting existing INPX slice output", zap.String("file", outputPath))
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("stat INPX slice output %q: %w", outputPath, err)
			}
			if opts.Additional {
				additionalOutputPath := annotationsOutputPath(outputPath)
				stats.AdditionalOutputPath = additionalOutputPath
				additionalTmpFile, err := fileutil.CreateHiddenTemp(filepath.Dir(additionalOutputPath), filepath.Base(additionalOutputPath))
				if err != nil {
					return fmt.Errorf("create temporary INPX slice additional output: %w", err)
				}
				annotationsTmpPath = additionalTmpFile.Name()
				if err := additionalTmpFile.Close(); err != nil {
					return fmt.Errorf("close temporary INPX slice additional output %q: %w", annotationsTmpPath, err)
				}
				if _, err := os.Stat(additionalOutputPath); err == nil && opts.Log != nil {
					opts.Log.Warn("Overwriting existing INPX slice additional output", zap.String("file", additionalOutputPath))
				} else if err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("stat INPX slice additional output %q: %w", additionalOutputPath, err)
				}
				if dataset.Processing.FB2BodyFingerprints == nil || dataset.Processing.FB2BodyFingerprints.Coverage == model.FB2BodyFingerprintCoverageNone {
					if opts.Log != nil {
						opts.Log.Warn("Skipping INPX slice compilations output because dataset has no FB2 body fingerprints")
					}
				} else {
					compilationsOutputPath := compilationsOutputPathFor(outputPath)
					stats.CompilationsOutputPath = compilationsOutputPath
					compilationsTmpFile, err := fileutil.CreateHiddenTemp(filepath.Dir(compilationsOutputPath), filepath.Base(compilationsOutputPath))
					if err != nil {
						return fmt.Errorf("create temporary INPX slice compilations output: %w", err)
					}
					compilationsTmpPath = compilationsTmpFile.Name()
					if err := compilationsTmpFile.Close(); err != nil {
						return fmt.Errorf("close temporary INPX slice compilations output %q: %w", compilationsTmpPath, err)
					}
					if _, err := os.Stat(compilationsOutputPath); err == nil && opts.Log != nil {
						opts.Log.Warn("Overwriting existing INPX slice compilations output", zap.String("file", compilationsOutputPath))
					} else if err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("stat INPX slice compilations output %q: %w", compilationsOutputPath, err)
					}
				}
			}
			if opts.Log != nil {
				opts.Log.Info("INPX slice creation started", zap.String("file", outputPath), zap.Int("archives", len(dataset.Archives)))
			}
			stream, err = newStreamINPXWriter(tmpPath, annotationsTmpPath, compilationsTmpPath, stats.CompilationsOutputPath, meta, dataset, opts, where, splitBy)
			return err
		},
		func(rec model.DatasetRecord) error {
			if stream == nil {
				return errors.New("INPX slice dataset record arrived before header")
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
		return stats, errors.New("INPX slice dataset input is missing header")
	}
	writeStats, err := stream.Finish()
	if err != nil {
		return stats, err
	}
	stats.Archives = writeStats.Archives
	stats.Files = writeStats.Files
	stats.Records = writeStats.Records
	stats.DBRecords = writeStats.DBRecords
	stats.FB2Records = writeStats.FB2Records
	stats.FilteredRecords = writeStats.FilteredRecords
	stats.SkippedNonArchiveRecords = writeStats.SkippedNonArchiveRecords
	stats.SkippedIgnoredRecords = writeStats.SkippedIgnoredRecords
	stats.SkippedInvalidRecords = writeStats.SkippedInvalidRecords
	stats.DisambiguatedAuthorBooks = writeStats.DisambiguatedAuthorBooks
	stats.DisambiguatedAuthors = writeStats.DisambiguatedAuthors
	stats.CanonicalizedLangBooks = writeStats.CanonicalizedLangBooks
	stats.Splits = writeStats.Splits
	if err := fileutil.ReplaceOutputFile(tmpPath, stats.OutputPath); err != nil {
		return stats, fmt.Errorf("replace INPX slice output %q: %w", stats.OutputPath, err)
	}
	if stats.AdditionalOutputPath != "" {
		if err := fileutil.ReplaceOutputFile(annotationsTmpPath, stats.AdditionalOutputPath); err != nil {
			return stats, fmt.Errorf("replace INPX slice additional output %q: %w", stats.AdditionalOutputPath, err)
		}
	}
	if stats.CompilationsOutputPath != "" && compilationsTmpPath != "" {
		if _, err := os.Stat(compilationsTmpPath); os.IsNotExist(err) {
			stats.CompilationsOutputPath = ""
		} else if err != nil {
			return stats, fmt.Errorf("stat INPX slice compilations output %q: %w", compilationsTmpPath, err)
		}
	}
	if stats.CompilationsOutputPath != "" && compilationsTmpPath != "" {
		if err := fileutil.ReplaceOutputFile(compilationsTmpPath, stats.CompilationsOutputPath); err != nil {
			return stats, fmt.Errorf("replace INPX slice compilations output %q: %w", stats.CompilationsOutputPath, err)
		}
	}
	cleanupTemp = false
	logSummary(opts.Log, loaded, stats)
	return stats, nil
}

func optionalTemplate(name string, text string) (*inpxutil.RecordTemplate, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	tmpl, err := inpxutil.NewRecordTemplate(name, text)
	if err != nil {
		return nil, err
	}
	return tmpl, nil
}

func newStreamINPXWriter(
	path string,
	annotationsPath string,
	compilationsPath string,
	compilationsOutputPath string,
	meta inpxutil.Metadata,
	dataset model.Dataset,
	opts Options,
	where *inpxutil.RecordTemplate,
	splitBy *inpxutil.RecordTemplate,
) (*streamINPXWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create INPX slice %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(meta))
	archives := inpxutil.DatasetArchiveRowsList(dataset)
	archiveByID := make(map[string]int, len(archives))
	for idx, archive := range archives {
		archiveByID[archive.Meta.ID] = idx
	}
	var annotations *annotationCollector
	if annotationsPath != "" {
		annotations = newAnnotationCollector(annotationsPath, meta)
	}
	var compilations *compilationCollector
	if compilationsPath != "" {
		compilations = newCompilationCollector(compilationsPath, meta, opts.Log)
	}
	return &streamINPXWriter{
		path:                   path,
		meta:                   meta,
		opts:                   opts,
		where:                  where,
		splitBy:                splitBy,
		zw:                     zw,
		f:                      f,
		archives:               archives,
		archiveByID:            archiveByID,
		splits:                 make(map[string]*splitWriter),
		stats:                  Stats{DumpDate: meta.DumpDate},
		annotations:            annotations,
		compilations:           compilations,
		compilationsOutputPath: compilationsOutputPath,
	}, nil
}

func (w *streamINPXWriter) WriteRecord(rec model.DatasetRecord) error {
	w.inputRecord++
	archive, index, ok, err := w.recordTarget(rec)
	if err != nil {
		return err
	}
	if !ok {
		w.stats.SkippedNonArchiveRecords++
		return nil
	}
	if inpxutil.InRanges(archive.Meta.Ignored, index) {
		w.stats.SkippedIgnoredRecords++
		return nil
	}
	fields, view, sequences, diagnostics, ok, err := w.buildRecordRows(rec, archive.Meta, index)
	if err != nil {
		return err
	}
	if !ok {
		w.stats.SkippedInvalidRecords++
		return nil
	}
	rows := make([]row, 0, len(sequences))
	for seqIdx, seq := range sequences {
		ctx := filterRecord(rec, view, fields, seq, archive.Meta, index, w.inputRecord, seqIdx+1, sequences)
		keep := true
		if w.where != nil {
			keep, err = w.where.ExecuteBool(ctx)
			if err != nil {
				return fmt.Errorf("evaluate INPX slice filter for book %q: %w", datasetBookID(rec), err)
			}
		}
		if !keep {
			w.stats.FilteredRecords++
			if w.opts.Log != nil && w.opts.Verbose {
				w.opts.Log.Debug(
					"Filtered INPX slice record",
					zap.String("book_id", datasetBookID(rec)),
					zap.String("lang", fields.Lang),
					zap.String("archive", archive.Meta.Name),
					zap.Int("index", index),
				)
			}
			continue
		}
		rows = append(rows, row{line: recordLine(ctx), ctx: ctx})
	}
	if len(rows) == 0 {
		return nil
	}
	acceptedBook := w.acceptedBooks + 1
	acceptedRow := w.acceptedRows + 1
	for idx := range rows {
		rows[idx].ctx.AcceptedBook = acceptedBook
		rows[idx].ctx.AcceptedRow = acceptedRow + int64(idx)
	}
	splitKey := "books"
	if w.splitBy != nil {
		value, err := w.splitBy.ExecuteString(rows[0].ctx)
		if err != nil {
			return fmt.Errorf("evaluate INPX slice split for book %q: %w", datasetBookID(rec), err)
		}
		splitKey = value
	}
	split, err := w.openSplit(splitKey)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := split.bw.WriteString(row.line); err != nil {
			return fmt.Errorf("write INPX slice entry %q: %w", split.entry, err)
		}
		split.records++
		w.stats.Records++
		if view.HasDatabase {
			w.stats.DBRecords++
		} else {
			w.stats.FB2Records++
		}
	}
	w.acceptedBooks++
	w.acceptedRows += int64(len(rows))
	split.books++
	w.stats.Files++
	w.activeDiag.add(diagnostics)
	w.stats.DisambiguatedAuthorBooks += diagnostics.DisambiguatedAuthorBooks
	w.stats.DisambiguatedAuthors += diagnostics.DisambiguatedAuthors
	w.stats.CanonicalizedLangBooks += diagnostics.CanonicalizedLangBooks
	if w.annotations != nil {
		name := fields.File
		if fields.Ext != "" {
			name += "." + fields.Ext
		}
		if err := w.annotations.WriteRecord(archive.Meta.Name, name, fb2Annotation(rec)); err != nil {
			return err
		}
	}
	if w.compilations != nil {
		fileName := fields.File
		if fields.Ext != "" {
			fileName += "." + fields.Ext
		}
		w.compilations.AddRecord(rec, archive.Meta.Name, fileName)
	}
	return nil
}

func (w *streamINPXWriter) recordTarget(rec model.DatasetRecord) (*inpxutil.DatasetArchiveRows, int, bool, error) {
	locator := rec.Record.Locator
	if locator.Kind != "archive_entry" {
		return nil, 0, false, nil
	}
	if locator.Index == nil {
		return nil, 0, false, fmt.Errorf("INPX slice archive record for source %q has no index", locator.Source)
	}
	idx, ok := w.archiveByID[locator.Source]
	if !ok {
		return nil, 0, false, fmt.Errorf("INPX slice record references undeclared archive source %q", locator.Source)
	}
	return w.archives[idx], *locator.Index, true, nil
}

func (w *streamINPXWriter) buildRecordRows(
	rec model.DatasetRecord,
	archive model.DatasetArchive,
	index int,
) (recordFields, inpxutil.DatasetRecordView, []sequence, entryDiagnostics, bool, error) {
	view, err := inpxutil.DatasetRecordClaims(rec)
	if err != nil {
		return recordFields{}, view, nil, entryDiagnostics{}, false, err
	}
	diagnostics := entryDiagnostics{}
	ext := view.Catalog.FileType
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(view.Artifact.Name), ".")
	}
	if !strings.EqualFold(strings.TrimPrefix(ext, "."), "fb2") {
		return recordFields{}, view, nil, diagnostics, false, nil
	}
	title := view.Database.Title
	if title == "" {
		title = view.FB2.Title
	}
	if title == "" {
		return recordFields{}, view, nil, diagnostics, false, nil
	}
	fileName := strings.TrimSuffix(view.Artifact.Name, filepath.Ext(view.Artifact.Name))
	if fileName == "" {
		fileName = datasetBookID(rec)
	}
	date := inpxutil.DateOnly(view.Catalog.Time)
	if date == "" {
		date = view.Artifact.Date
	}
	lang, languageSelection := recordLanguage(rec, view, w.opts)
	if languageSelection.Canonicalized {
		diagnostics.CanonicalizedLangBooks = 1
	}
	keywords := view.Database.Keywords
	if keywords == "" {
		keywords = view.FB2.Keywords
	}
	year := view.DatabasePublication.Year
	if year == "" {
		year = view.FB2Publication.Year
	}
	authors := authorsString(view.HasDatabase, view.Database.Authors, view.FB2.Authors, w.opts)
	if count := logDisambiguatedDBAuthors(rec, view, authors, w.opts); count > 0 {
		diagnostics.DisambiguatedAuthorBooks = 1
		diagnostics.DisambiguatedAuthors = int64(count)
	}
	sequences := recordSequences(rec, view, w.opts)
	if len(sequences) == 0 {
		sequences = []sequence{{}}
	}
	fields := recordFields{
		Author:   authors,
		Genre:    genresString(view.Database.Genres, view.FB2.Genres),
		Title:    inpxutil.Cleanse(title),
		File:     inpxutil.Cleanse(fileName),
		Size:     strconv.FormatUint(view.Artifact.Size, 10),
		LibID:    datasetBookID(rec),
		Deleted:  inpxutil.Cleanse(view.Catalog.Deleted),
		Ext:      inpxutil.Cleanse(strings.TrimPrefix(ext, ".")),
		Date:     inpxutil.Cleanse(date),
		InsNo:    strconv.Itoa(index + 1),
		Folder:   inpxutil.Cleanse(archive.Name),
		Lang:     inpxutil.Cleanse(strings.TrimSpace(lang)),
		LibRate:  view.Catalog.Rating,
		Keywords: keywordsString(keywords),
		Year:     inpxutil.Cleanse(year),
	}
	return fields, view, sequences, diagnostics, true, nil
}

func (w *streamINPXWriter) openSplit(key string) (*splitWriter, error) {
	entry, err := splitEntryName(key)
	if err != nil {
		return nil, err
	}
	if split, ok := w.splits[entry]; ok {
		return split, nil
	}
	tmpFile, err := fileutil.CreateHiddenTemp(filepath.Dir(w.path), "inpx-split")
	if err != nil {
		return nil, fmt.Errorf("create temporary INPX split %q: %w", entry, err)
	}
	split := &splitWriter{key: key, entry: entry, path: tmpFile.Name(), file: tmpFile, bw: bufio.NewWriter(tmpFile)}
	w.splits[entry] = split
	w.splitOrder = append(w.splitOrder, entry)
	return split, nil
}

func (w *streamINPXWriter) Finish() (Stats, error) {
	if err := w.flushSplits(); err != nil {
		w.Close()
		return w.stats, err
	}
	for _, entry := range w.splitOrder {
		split := w.splits[entry]
		if err := w.writeSplit(split); err != nil {
			w.Close()
			return w.stats, err
		}
		w.stats.Splits = append(w.stats.Splits, SplitStats{Entry: split.entry, Records: split.records, Books: split.books})
	}
	if err := inpxutil.WriteZipText(w.zw, "structure.info", structureInfo); err != nil {
		w.Close()
		return w.stats, err
	}
	collection, err := inpxutil.CollectionInfo(w.meta, inpxutil.TemplateOptions{CommentTemplate: w.opts.CommentTemplate})
	if err != nil {
		w.Close()
		return w.stats, err
	}
	if err := inpxutil.WriteZipText(w.zw, "collection.info", collection); err != nil {
		w.Close()
		return w.stats, err
	}
	version, err := inpxutil.VersionInfo(w.meta, inpxutil.TemplateOptions{VersionTemplate: w.opts.VersionTemplate})
	if err != nil {
		w.Close()
		return w.stats, err
	}
	if err := inpxutil.WriteZipText(w.zw, "version.info", version); err != nil {
		w.Close()
		return w.stats, err
	}
	if w.annotations != nil {
		if err := w.annotations.Write(); err != nil {
			w.Close()
			return w.stats, err
		}
	}
	if w.compilations != nil {
		if err := w.compilations.Write(); err != nil {
			w.Close()
			return w.stats, err
		}
	}
	w.stats.Archives = len(w.archives)
	return w.stats, w.Close()
}

func (w *streamINPXWriter) flushSplits() error {
	for _, split := range w.splits {
		if split.bw != nil {
			if err := split.bw.Flush(); err != nil {
				return err
			}
			split.bw = nil
		}
		if split.file != nil {
			if err := split.file.Close(); err != nil {
				return fmt.Errorf("close temporary INPX split %q: %w", split.path, err)
			}
			split.file = nil
		}
	}
	return nil
}

func (w *streamINPXWriter) writeSplit(split *splitWriter) error {
	zw, err := w.zw.Create(split.entry)
	if err != nil {
		return fmt.Errorf("create INPX split entry %q: %w", split.entry, err)
	}
	f, err := os.Open(split.path)
	if err != nil {
		return fmt.Errorf("open temporary INPX split %q: %w", split.path, err)
	}
	defer f.Close()
	if _, err := io.Copy(zw, f); err != nil {
		return fmt.Errorf("copy INPX split entry %q: %w", split.entry, err)
	}
	return nil
}

func (w *streamINPXWriter) Close() error {
	var errs []error
	for _, split := range w.splits {
		if split.bw != nil {
			errs = append(errs, split.bw.Flush())
			split.bw = nil
		}
		if split.file != nil {
			errs = append(errs, split.file.Close())
			split.file = nil
		}
		if split.path != "" {
			if err := os.Remove(split.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove temporary INPX split %q: %w", split.path, err))
			}
			split.path = ""
		}
	}
	if w.zw != nil {
		if err := w.zw.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close INPX slice zip %q: %w", w.path, err))
		}
		w.zw = nil
	}
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close INPX slice %q: %w", w.path, err))
		}
		w.f = nil
	}
	if w.annotations != nil {
		errs = append(errs, w.annotations.Close())
		w.annotations = nil
	}
	return errors.Join(errs...)
}

func splitEntryName(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "other"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range key {
		invalid := r == '/' || r == '\\' || unicode.IsControl(r)
		if invalid {
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
			continue
		}
		b.WriteRune(r)
		lastUnderscore = false
	}
	key = strings.Trim(strings.TrimSpace(b.String()), ". ")
	if key == "" || key == "." || key == ".." {
		key = "other"
	}
	return key + ".inp", nil
}

func filterRecord(
	rec model.DatasetRecord,
	view inpxutil.DatasetRecordView,
	fields recordFields,
	seq sequence,
	archive model.DatasetArchive,
	index int,
	inputRecord int64,
	bookRow int,
	sequences []sequence,
) FilterRecord {
	return FilterRecord{
		InputRecord: inputRecord,
		BookRow:     bookRow,
		Author:      fields.Author,
		Genre:       fields.Genre,
		Title:       fields.Title,
		Series:      inpxutil.Cleanse(seq.Name),
		SerNo:       inpxutil.Cleanse(seq.Number),
		File:        fields.File,
		Size:        fields.Size,
		LibID:       fields.LibID,
		del:         fields.Deleted,
		Deleted:     isDeleted(fields.Deleted),
		Ext:         fields.Ext,
		Date:        fields.Date,
		InsNo:       index + 1,
		Folder:      fields.Folder,
		Lang:        fields.Lang,
		LibRate:     fields.LibRate,
		Keywords:    fields.Keywords,
		Year:        fields.Year,
		Authors:     selectedAuthors(view),
		Genres:      selectedGenres(view),
		Sequences:   contextSequences(sequences),
		HasDatabase: view.HasDatabase,
		HasFB2:      view.HasFB2,
		ArchiveID:   archive.ID,
		ArchiveName: archive.Name,
	}
}

func isDeleted(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "deleted":
		return true
	default:
		return false
	}
}

func recordLine(ctx FilterRecord) string {
	values := []string{
		ctx.Author,
		ctx.Genre,
		ctx.Title,
		ctx.Series,
		ctx.SerNo,
		ctx.File,
		ctx.Size,
		ctx.LibID,
		ctx.del,
		ctx.Ext,
		ctx.Date,
		strconv.Itoa(ctx.InsNo),
		ctx.Folder,
		ctx.Lang,
		ctx.LibRate,
		ctx.Keywords,
		ctx.Year,
	}
	return strings.Join(values, inpxutil.FieldSep) + inpxutil.FieldSep + "\r\n"
}

func recordLanguage(rec model.DatasetRecord, view inpxutil.DatasetRecordView, opts Options) (string, inpxutil.LanguageSelection) {
	if opts.Language != nil {
		return opts.Language.SelectLanguageWithReport(rec, view)
	}
	lang := view.Database.Language
	if lang == "" {
		lang = view.FB2.Language
	}
	return lang, inpxutil.LanguageSelection{}
}

func authorsString(dbPresent bool, authors []model.PersonValue, fb2Authors []model.PersonValue, opts Options) string {
	if opts.FB2Preference == PreferReplace && len(fb2Authors) > 0 {
		return peopleStringWithDisambiguation(fb2Authors, opts.AuthorDisambiguator)
	}
	if dbPresent && len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	if len(authors) == 0 && len(fb2Authors) > 0 {
		return peopleStringWithDisambiguation(fb2Authors, opts.AuthorDisambiguator)
	}
	if len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	return peopleStringWithDisambiguation(authors, opts.AuthorDisambiguator)
}

func peopleStringWithDisambiguation(people []model.PersonValue, disambiguator *inpxutil.AuthorDisambiguator) string {
	var b strings.Builder
	for _, person := range people {
		lastName := authorLastName(person, disambiguator.Suffix(person))
		firstName := inpxutil.CleanseAuthorComponent(person.FirstName)
		middleName := inpxutil.CleanseAuthorComponent(person.MiddleName)
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

func authorLastName(person model.PersonValue, suffix string) string {
	lastName := inpxutil.CleanseAuthorComponent(person.LastName)
	suffix = inpxutil.CleanseAuthorComponent(suffix)
	if suffix == "" {
		return lastName
	}
	return strings.TrimSpace(lastName + " " + suffix)
}

func logDisambiguatedDBAuthors(rec model.DatasetRecord, view inpxutil.DatasetRecordView, renderedAuthors string, opts Options) int {
	if opts.AuthorDisambiguator == nil || !dbAuthorsSelected(view.Database.Authors, view.FB2.Authors, opts) {
		return 0
	}
	count := 0
	for _, person := range view.Database.Authors {
		suffix := opts.AuthorDisambiguator.Suffix(person)
		if suffix == "" {
			continue
		}
		count++
		if opts.Log == nil || !opts.Verbose {
			continue
		}
		fields := []zap.Field{
			zap.String("book_id", datasetBookID(rec)),
			zap.String("flibusta_person_id", inpxutil.FlibustaPersonID(person)),
			zap.String("first_name", person.FirstName),
			zap.String("middle_name", person.MiddleName),
			zap.String("last_name", person.LastName),
			zap.String("nick_name", person.NickName),
			zap.String("suffix", suffix),
			zap.String("rendered_last_name", authorLastName(person, suffix)),
			zap.String("rendered_authors", renderedAuthors),
			zap.String("locator_kind", rec.Record.Locator.Kind),
			zap.String("locator_source", rec.Record.Locator.Source),
			zap.String("artifact", view.Artifact.Name),
		}
		if person.Position != nil {
			fields = append(fields, zap.Int64("position", *person.Position))
		}
		if rec.Record.Locator.Index != nil {
			fields = append(fields, zap.Int("locator_index", *rec.Record.Locator.Index))
		}
		opts.Log.Debug("Disambiguated INPX DB author", fields...)
	}
	return count
}

func dbAuthorsSelected(authors []model.PersonValue, fb2Authors []model.PersonValue, opts Options) bool {
	return len(authors) > 0 && !(opts.FB2Preference == PreferReplace && len(fb2Authors) > 0)
}

func genresString(genres []model.GenreValue, fb2Genres []model.GenreValue) string {
	selected := selectedGenreValues(genres, fb2Genres)
	if len(selected) == 0 {
		return "other:"
	}
	var b strings.Builder
	for _, genre := range selected {
		b.WriteString(genre)
		b.WriteByte(':')
	}
	return b.String()
}

func selectedGenres(view inpxutil.DatasetRecordView) []string {
	return selectedGenreValues(view.Database.Genres, view.FB2.Genres)
}

func selectedGenreValues(genres []model.GenreValue, fb2Genres []model.GenreValue) []string {
	if values := genreValues(genres); len(values) > 0 {
		return values
	}
	if values := genreValues(fb2Genres); len(values) > 0 {
		return values
	}
	return []string{"other"}
}

func genreValues(genres []model.GenreValue) []string {
	values := make([]string, 0, len(genres))
	for _, genre := range genres {
		code := inpxutil.CleanseGenreCode(genre.Code)
		if code != "" {
			values = append(values, code)
		}
	}
	return values
}

func selectedAuthors(view inpxutil.DatasetRecordView) []Person {
	people := view.Database.Authors
	if len(people) == 0 && len(view.FB2.Authors) > 0 {
		people = view.FB2.Authors
	}
	result := make([]Person, 0, len(people))
	for _, person := range people {
		result = append(result, Person{
			FirstName:  person.FirstName,
			MiddleName: person.MiddleName,
			LastName:   person.LastName,
			NickName:   person.NickName,
			ID:         inpxutil.FlibustaPersonID(person),
		})
	}
	return result
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
					fields = append(fields, zap.Int("archive_index", *rec.Record.Locator.Index))
				}
				opts.Log.Debug("Dropped duplicate INPX slice sequence", fields...)
			}
			continue
		}
		seen[key] = seq
		result = append(result, seq)
	}
	return result
}

func contextSequences(sequences []sequence) []Sequence {
	result := make([]Sequence, 0, len(sequences))
	for _, seq := range sequences {
		result = append(result, Sequence(seq))
	}
	return result
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

func (d *entryDiagnostics) add(other entryDiagnostics) {
	d.DisambiguatedAuthorBooks += other.DisambiguatedAuthorBooks
	d.DisambiguatedAuthors += other.DisambiguatedAuthors
	d.CanonicalizedLangBooks += other.CanonicalizedLangBooks
}

func datasetBookID(rec model.DatasetRecord) string {
	return inpxutil.DatasetBookID(rec)
}

func logSummary(log *zap.Logger, loaded int64, stats Stats) {
	if log == nil {
		return
	}
	if stats.Records == 0 {
		log.Warn(
			"INPX slice wrote no records",
			zap.Int64("loaded_records", loaded),
			zap.Int64("filtered_records", stats.FilteredRecords),
			zap.Int64("skipped_non_archive_records", stats.SkippedNonArchiveRecords),
			zap.Int64("skipped_ignored_records", stats.SkippedIgnoredRecords),
			zap.Int64("skipped_invalid_records", stats.SkippedInvalidRecords),
		)
	}
	log.Info(
		"INPX slice records streamed",
		zap.Int64("loaded_records", loaded),
		zap.Int64("written_records", stats.Records),
		zap.Int64("written_books", stats.Files),
		zap.Int64("filtered_records", stats.FilteredRecords),
		zap.Int("split_entries", len(stats.Splits)),
		zap.Int64("skipped_non_archive_records", stats.SkippedNonArchiveRecords),
		zap.Int64("skipped_ignored_records", stats.SkippedIgnoredRecords),
		zap.Int64("skipped_invalid_records", stats.SkippedInvalidRecords),
		zap.Int64("disambiguated_author_books", stats.DisambiguatedAuthorBooks),
		zap.Int64("disambiguated_authors", stats.DisambiguatedAuthors),
		zap.Int64("canonicalized_language_books", stats.CanonicalizedLangBooks),
	)
	for _, split := range stats.Splits {
		log.Info(
			"INPX slice split written",
			zap.String("entry", split.Entry),
			zap.Int64("written_records", split.Records),
			zap.Int64("books", split.Books),
		)
	}
}

func annotationsOutputPath(outputPath string) string {
	ext := filepath.Ext(outputPath)
	return strings.TrimSuffix(outputPath, ext) + "_annotation.zip"
}

func compilationsOutputPathFor(outputPath string) string {
	ext := filepath.Ext(outputPath)
	return strings.TrimSuffix(outputPath, ext) + "_compilations.zip"
}

func fb2Annotation(rec model.DatasetRecord) string {
	if rec.Claims.Bibliographic == nil {
		return ""
	}
	for _, claim := range rec.Claims.Bibliographic.Annotation {
		if claim.Observation != "fb2" {
			continue
		}
		if annotation, ok := claim.Value.(string); ok {
			return annotation
		}
	}
	return ""
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}

type annotationCollector struct {
	path     string
	meta     inpxutil.Metadata
	archives map[string]*annotationArchive
	order    []string
}

type annotationArchive struct {
	name string
	path string
	file *os.File
	bw   *bufio.Writer
}

func newAnnotationCollector(path string, meta inpxutil.Metadata) *annotationCollector {
	return &annotationCollector{path: path, meta: meta, archives: make(map[string]*annotationArchive)}
}

func (c *annotationCollector) WriteRecord(archiveName string, name string, annotation string) error {
	if strings.TrimSpace(annotation) == "" {
		return nil
	}
	archive, err := c.openArchive(archiveName)
	if err != nil {
		return err
	}
	if _, err := archive.bw.WriteString("\t<file name=\""); err != nil {
		return err
	}
	if _, err := archive.bw.WriteString(xmlEscape(name)); err != nil {
		return err
	}
	if _, err := archive.bw.WriteString("\">\n\t\t<p>"); err != nil {
		return err
	}
	if _, err := archive.bw.WriteString(xmlEscape(strings.TrimSpace(annotation))); err != nil {
		return err
	}
	_, err = archive.bw.WriteString("</p>\n\t</file>\n")
	return err
}

func (c *annotationCollector) openArchive(name string) (*annotationArchive, error) {
	if archive, ok := c.archives[name]; ok {
		return archive, nil
	}
	tmpFile, err := fileutil.CreateHiddenTemp(filepath.Dir(c.path), "inpx-annotation")
	if err != nil {
		return nil, fmt.Errorf("create temporary INPX slice annotation %q: %w", name, err)
	}
	archive := &annotationArchive{name: name, path: tmpFile.Name(), file: tmpFile, bw: bufio.NewWriter(tmpFile)}
	if _, err := archive.bw.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<folder name=\""); err != nil {
		_ = archive.closeAndRemove()
		return nil, err
	}
	if _, err := archive.bw.WriteString(xmlEscape(name)); err != nil {
		_ = archive.closeAndRemove()
		return nil, err
	}
	if _, err := archive.bw.WriteString("\">\n"); err != nil {
		_ = archive.closeAndRemove()
		return nil, err
	}
	c.archives[name] = archive
	c.order = append(c.order, name)
	return archive, nil
}

func (c *annotationCollector) Write() error {
	if err := c.flush(); err != nil {
		return err
	}
	f, err := os.Create(c.path)
	if err != nil {
		return fmt.Errorf("create INPX slice additional output %q: %w", c.path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(c.meta))
	for _, name := range c.order {
		archive := c.archives[name]
		if err := writeZipFile(zw, archive.name, archive.path); err != nil {
			_ = zw.Close()
			_ = f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("close INPX slice additional zip %q: %w", c.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close INPX slice additional output %q: %w", c.path, err)
	}
	return c.cleanup()
}

func (c *annotationCollector) flush() error {
	for _, archive := range c.archives {
		if archive.bw != nil {
			if _, err := archive.bw.WriteString("</folder>\n"); err != nil {
				return err
			}
			if err := archive.bw.Flush(); err != nil {
				return err
			}
			archive.bw = nil
		}
		if archive.file != nil {
			if err := archive.file.Close(); err != nil {
				return fmt.Errorf("close temporary INPX slice annotation %q: %w", archive.path, err)
			}
			archive.file = nil
		}
	}
	return nil
}

func (c *annotationCollector) cleanup() error {
	var errs []error
	for _, archive := range c.archives {
		errs = append(errs, archive.closeAndRemove())
	}
	return errors.Join(errs...)
}

func (c *annotationCollector) Close() error {
	return c.cleanup()
}

func (a *annotationArchive) closeAndRemove() error {
	var errs []error
	if a.bw != nil {
		if err := a.bw.Flush(); err != nil {
			errs = append(errs, err)
		}
		a.bw = nil
	}
	if a.file != nil {
		if err := a.file.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close temporary INPX slice annotation %q: %w", a.path, err))
		}
		a.file = nil
	}
	if a.path != "" {
		if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove temporary INPX slice annotation %q: %w", a.path, err))
		}
		a.path = ""
	}
	return errors.Join(errs...)
}

func writeZipFile(zw *zip.Writer, name string, path string) error {
	out, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create INPX slice additional entry %q: %w", name, err)
	}
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open temporary INPX slice additional entry %q: %w", path, err)
	}
	defer in.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy INPX slice additional entry %q: %w", name, err)
	}
	return nil
}

type compilationCollector struct {
	path    string
	meta    inpxutil.Metadata
	log     *zap.Logger
	books   []compilationBook
	missing int
}

type compilationBook struct {
	folder string
	file   string
	root   *compilationSection
}

type compilationSection struct {
	key      string
	leaf     bool
	children []*compilationSection
}

type compilationOutput struct {
	Folder      string                  `json:"folder"`
	File        string                  `json:"file"`
	Compilation []compilationOutputPart `json:"compilation"`
	Covered     bool                    `json:"covered"`
}

type compilationOutputPart struct {
	Part   int    `json:"part"`
	Folder string `json:"folder"`
	File   string `json:"file"`
}

func newCompilationCollector(path string, meta inpxutil.Metadata, log *zap.Logger) *compilationCollector {
	return &compilationCollector{path: path, meta: meta, log: log}
}

func (c *compilationCollector) AddRecord(rec model.DatasetRecord, folder string, file string) {
	fingerprint := recordFB2BodyFingerprint(rec)
	if fingerprint == nil || len(fingerprint.Sections) == 0 {
		c.missing++
		return
	}
	root := compilationSectionTree(fingerprint.Sections)
	if root == nil {
		c.missing++
		return
	}
	c.books = append(c.books, compilationBook{folder: folder, file: file, root: root})
}

func (c *compilationCollector) Write() error {
	if c.missing > 0 && c.log != nil {
		c.log.Warn("Some INPX slice records lack FB2 body fingerprints", zap.Int("records", c.missing))
	}
	outputs := c.compilations()
	if len(outputs) == 0 {
		if c.log != nil {
			c.log.Warn("Skipping INPX slice compilations output because no compilations were detected")
		}
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove empty INPX slice compilations output %q: %w", c.path, err)
		}
		return nil
	}
	data, err := jsonv2.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("marshal INPX slice compilations JSON: %w", err)
	}
	f, err := os.Create(c.path)
	if err != nil {
		return fmt.Errorf("create INPX slice compilations output %q: %w", c.path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(c.meta))
	if err := inpxutil.WriteZipText(zw, "compilations.json", string(data)); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("close INPX slice compilations zip %q: %w", c.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close INPX slice compilations output %q: %w", c.path, err)
	}
	return nil
}

func (c *compilationCollector) compilations() []compilationOutput {
	rootToBooks := make(map[string][]int, len(c.books))
	for idx, book := range c.books {
		rootToBooks[book.root.key] = append(rootToBooks[book.root.key], idx)
	}
	outputs := make([]compilationOutput, 0)
	for idx, book := range c.books {
		parts, covered, found := c.compilationParts(idx, rootToBooks)
		if found <= 1 {
			continue
		}
		outputs = append(outputs, compilationOutput{Folder: book.folder, File: book.file, Compilation: parts, Covered: covered})
	}
	return outputs
}

func (c *compilationCollector) compilationParts(owner int, rootToBooks map[string][]int) ([]compilationOutputPart, bool, int) {
	book := c.books[owner]
	found := make(map[string]struct{})
	notFound := make(map[string]struct{})
	var parts []compilationOutputPart
	var walk func([]*compilationSection)
	walk = func(sections []*compilationSection) {
		for _, section := range sections {
			if section.key == book.root.key {
				walk(section.children)
				continue
			}
			matches := rootToBooks[section.key]
			if len(matches) > 0 {
				part := len(found)
				for _, match := range matches {
					matched := c.books[match]
					parts = append(parts, compilationOutputPart{Part: part, Folder: matched.folder, File: matched.file})
				}
				found[section.key] = struct{}{}
				continue
			}
			if section.leaf {
				notFound[section.key] = struct{}{}
				continue
			}
			walk(section.children)
		}
	}
	walk(book.root.children)
	return parts, len(notFound) == 0, len(found)
}

func recordFB2BodyFingerprint(rec model.DatasetRecord) *model.FB2BodyFingerprint {
	for _, artifact := range rec.Artifacts {
		if artifact.Fingerprints != nil && artifact.Fingerprints.FB2Body != nil {
			return artifact.Fingerprints.FB2Body
		}
	}
	return nil
}

func compilationSectionTree(sections []model.FB2BodySectionFingerprint) *compilationSection {
	var root *compilationSection
	stack := make([]*compilationSection, 0)
	for _, section := range sections {
		node := &compilationSection{key: section.Key, leaf: section.Leaf}
		if section.Depth == 0 {
			if root != nil {
				return nil
			}
			root = node
			stack = []*compilationSection{node}
			continue
		}
		if root == nil || section.Depth > len(stack) {
			return nil
		}
		stack = stack[:section.Depth]
		parent := stack[len(stack)-1]
		parent.children = append(parent.children, node)
		stack = append(stack, node)
	}
	return root
}
