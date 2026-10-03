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
	"strconv"
	"strings"
	"unicode"

	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/internal/inpxutil"
	"metabib/model"
)

const structureInfo = "AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;YEAR;"

type SequenceMode = inpxutil.SequenceMode

const (
	SequenceAuthor    = inpxutil.SequenceAuthor
	SequencePublisher = inpxutil.SequencePublisher
	SequenceAll       = inpxutil.SequenceAll
	SequenceIgnore    = inpxutil.SequenceIgnore
)

type FB2Preference = inpxutil.FB2Preference

const (
	PreferIgnore     = inpxutil.PreferIgnore
	PreferMerge      = inpxutil.PreferMerge
	PreferComplement = inpxutil.PreferComplement
	PreferReplace    = inpxutil.PreferReplace
)

type FlattenMode = inpxutil.FlattenMode

const (
	FlattenAll      = inpxutil.FlattenAll
	FlattenLeaf     = inpxutil.FlattenLeaf
	FlattenPath     = inpxutil.FlattenPath
	FlattenPathLeaf = inpxutil.FlattenPathLeaf
)

type DedupMode = inpxutil.DedupMode

const (
	DedupCaseInsensitive = inpxutil.DedupCaseInsensitive
	DedupCaseSensitive   = inpxutil.DedupCaseSensitive
)

type Options struct {
	InputPrefix         string
	OutputPrefix        string
	ContentMode         inpxutil.ContentMode
	Additional          bool
	SequenceMode        SequenceMode
	FB2Preference       FB2Preference
	FlattenMode         FlattenMode
	DedupMode           DedupMode
	FB2PathSeparator    string
	Where               string
	SplitBy             string
	DisambiguateAuthors bool
	DisambiguationField inpxutil.AuthorDisambiguationField
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

type sequence = inpxutil.Sequence

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

type entryDiagnostics = inpxutil.EntryDiagnostics

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

type Sequence = inpxutil.Sequence

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
	compilations           *inpxutil.CompilationCollector
	compilationsOutputPath string
}

func ParseSequenceMode(value string) (SequenceMode, error) {
	return inpxutil.ParseSequenceMode(value, "INPX slice")
}

func ParseFB2Preference(value string) (FB2Preference, error) {
	return inpxutil.ParseFB2Preference(value, "INPX slice")
}

func ParseFlattenMode(value string) (FlattenMode, error) {
	return inpxutil.ParseFlattenMode(value, "INPX slice")
}

func ParseDedupMode(value string) (DedupMode, error) {
	return inpxutil.ParseDedupMode(value, "INPX slice")
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
	contentMode, err := inpxutil.ParseContentMode(string(opts.ContentMode), inpxutil.ContentAll)
	if err != nil {
		return stats, err
	}
	opts.ContentMode = contentMode
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
				opts.AuthorDisambiguator = inpxutil.NewAuthorDisambiguator(
					inpxutil.MetadataForContent(dataset.Database.INPX, opts.ContentMode),
					opts.DisambiguationField,
					opts.Log,
					opts.Verbose,
				)
			}
			stats.DumpDate = meta.DumpDate
			outputPath, err := inpxutil.OutputPath(opts.OutputPrefix, meta)
			if err != nil {
				return err
			}
			stats.OutputPath = outputPath
			tmpPath, err = inpxutil.PrepareOutput(outputPath, "INPX slice", opts.Log)
			if err != nil {
				return err
			}
			if opts.Additional {
				additionalOutputPath := inpxutil.AnnotationsOutputPath(outputPath)
				stats.AdditionalOutputPath = additionalOutputPath
				annotationsTmpPath, err = inpxutil.PrepareOutput(additionalOutputPath, "INPX slice additional", opts.Log)
				if err != nil {
					return err
				}
				if dataset.Processing.FB2BodyFingerprints == nil || dataset.Processing.FB2BodyFingerprints.Coverage == model.FB2BodyFingerprintCoverageNone {
					if opts.Log != nil {
						opts.Log.Warn("Skipping INPX slice compilations output because dataset has no FB2 body fingerprints")
					}
				} else {
					compilationsOutputPath := inpxutil.CompilationsOutputPath(outputPath)
					stats.CompilationsOutputPath = compilationsOutputPath
					compilationsTmpPath, err = inpxutil.PrepareOutput(compilationsOutputPath, "INPX slice compilations", opts.Log)
					if err != nil {
						return err
					}
				}
			}
			if opts.Log != nil {
				opts.Log.Info("INPX slice creation started", zap.String("file", outputPath), zap.Int("archives", len(dataset.Archives)))
			}
			stream, err = newStreamINPXWriter(
				tmpPath,
				annotationsTmpPath,
				compilationsTmpPath,
				stats.CompilationsOutputPath,
				meta,
				dataset,
				opts,
				where,
				splitBy,
			)
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
	archives, archiveByID := inpxutil.DatasetArchiveIndex(dataset)
	var annotations *annotationCollector
	if annotationsPath != "" {
		annotations = newAnnotationCollector(annotationsPath, meta)
	}
	var compilations *inpxutil.CompilationCollector
	if compilationsPath != "" {
		compilations = inpxutil.NewCompilationCollector(compilationsPath, meta, opts.Log)
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
	contentKeep, err := inpxutil.RecordMatchesContent(w.opts.ContentMode, rec)
	if err != nil {
		return err
	}
	if !contentKeep {
		w.stats.FilteredRecords++
		return nil
	}
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
				return fmt.Errorf("evaluate INPX slice filter for book %q: %w", inpxutil.DatasetBookID(rec), err)
			}
		}
		if !keep {
			w.stats.FilteredRecords++
			if w.opts.Log != nil && w.opts.Verbose {
				w.opts.Log.Debug(
					"Filtered INPX slice record",
					zap.String("book_id", inpxutil.DatasetBookID(rec)),
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
			return fmt.Errorf("evaluate INPX slice split for book %q: %w", inpxutil.DatasetBookID(rec), err)
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
	w.activeDiag.Add(diagnostics)
	w.stats.DisambiguatedAuthorBooks += diagnostics.DisambiguatedAuthorBooks
	w.stats.DisambiguatedAuthors += diagnostics.DisambiguatedAuthors
	w.stats.CanonicalizedLangBooks += diagnostics.CanonicalizedLangBooks
	if w.annotations != nil {
		name := fields.File
		if fields.Ext != "" {
			name += "." + fields.Ext
		}
		if err := w.annotations.WriteRecord(archive.Meta.Name, name, inpxutil.RecordAnnotation(rec, w.opts.FB2Preference)); err != nil {
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
	selected, diagnostics, ok := inpxutil.PrepareRecordFields(rec, view, authorOptions(w.opts), w.opts.Language, w.opts.Log)
	if !ok {
		return recordFields{}, view, nil, diagnostics, false, nil
	}
	sequences := recordSequences(rec, view, w.opts)
	if len(sequences) == 0 {
		sequences = []sequence{{}}
	}
	fields := recordFields{
		Author:   selected.Authors,
		Genre:    selected.Genres,
		Title:    inpxutil.Cleanse(selected.Title),
		File:     inpxutil.CleanseFileName(selected.File),
		Size:     strconv.FormatUint(view.Artifact.Size, 10),
		LibID:    inpxutil.DatasetBookID(rec),
		Deleted:  inpxutil.Cleanse(view.Catalog.Deleted),
		Ext:      inpxutil.CleanseFileName(strings.TrimPrefix(selected.Ext, ".")),
		Date:     inpxutil.Cleanse(selected.Date),
		InsNo:    strconv.Itoa(index + 1),
		Folder:   inpxutil.Cleanse(archive.Name),
		Lang:     inpxutil.Cleanse(strings.TrimSpace(selected.Language)),
		LibRate:  view.Catalog.Rating,
		Keywords: inpxutil.KeywordsString(selected.Keywords),
		Year:     inpxutil.Cleanse(selected.Year),
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
	if err := inpxutil.WriteInfoEntries(w.zw, w.meta, structureInfo, inpxutil.TemplateOptions{
		CommentTemplate: w.opts.CommentTemplate, VersionTemplate: w.opts.VersionTemplate,
	}); err != nil {
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
		Genres:      inpxutil.SelectedGenreValues(view.Database.Genres, view.FB2.Genres),
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
	return inpxutil.JoinINPFields(values)
}

func authorOptions(opts Options) inpxutil.AuthorOptions {
	return inpxutil.AuthorOptions{
		Preference:          opts.FB2Preference,
		Disambiguator:       opts.AuthorDisambiguator,
		DisambiguationField: opts.DisambiguationField,
		Verbose:             opts.Verbose,
	}
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
	return inpxutil.RecordSequences(rec, view, inpxutil.SequenceOptions{
		Mode:                opts.SequenceMode,
		Preference:          opts.FB2Preference,
		Flatten:             opts.FlattenMode,
		Dedup:               opts.DedupMode,
		PathSeparator:       opts.FB2PathSeparator,
		DuplicateLogMessage: "Dropped duplicate INPX slice sequence",
	}, opts.Log)
}

func contextSequences(sequences []sequence) []Sequence {
	result := make([]Sequence, 0, len(sequences))
	for _, seq := range sequences {
		result = append(result, Sequence(seq))
	}
	return result
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
	return inpxutil.WriteAnnotationRecord(archive.bw, name, annotation)
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
	if err := inpxutil.WriteAnnotationHeader(archive.bw, name); err != nil {
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
