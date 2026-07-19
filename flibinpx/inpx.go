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
	InputPrefix         string
	OutputPrefix        string
	Additional          bool
	SequenceMode        SequenceMode
	FB2Preference       FB2Preference
	FlattenMode         FlattenMode
	DedupMode           DedupMode
	FB2PathSeparator    string
	SourceLib           string
	DisambiguateAuthors bool
	Language            *inpxutil.LanguageResolver
	CommentTemplate     string
	VersionTemplate     string
	Log                 *zap.Logger
	Verbose             bool
	AuthorDisambiguator *inpxutil.AuthorDisambiguator
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

type entryDiagnostics struct {
	DisambiguatedAuthorBooks int64
	DisambiguatedAuthors     int64
	CanonicalizedLangBooks   int64
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
	var stream *streamINPXWriter
	var tmpPath string
	var additionalTmpPath string
	cleanupTemp := true
	defer func() {
		if cleanupTemp && tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
		if cleanupTemp && additionalTmpPath != "" {
			_ = os.Remove(additionalTmpPath)
		}
	}()

	var meta inpxutil.Metadata
	_, _, err := inpxutil.StreamDatasetInput(
		ctx,
		opts.InputPrefix,
		opts.Log,
		func(dataset model.Dataset) error {
			meta = inpxutil.DatasetMetadata(dataset)
			inpxutil.EnsureDumpDate(&meta, opts.Log)
			if opts.SourceLib == "" {
				opts.SourceLib = dataset.Library
			}
			if opts.DisambiguateAuthors && dataset.Database != nil {
				opts.AuthorDisambiguator = inpxutil.NewAuthorDisambiguator(dataset.Database.INPX, opts.Log, opts.Verbose)
			}
			stats.DumpDate = meta.DumpDate
			outputPath, err := inpxutil.OutputPath(opts.OutputPrefix, meta)
			if err != nil {
				return err
			}
			stats.OutputPath = outputPath
			if opts.Additional && len(dataset.Archives) == 0 {
				if opts.Log != nil {
					opts.Log.Warn("Skipping FLibrary additional artifacts for database-only input")
				}
				opts.Additional = false
			}
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				return fmt.Errorf("create FLibrary INPX output directory: %w", err)
			}
			tmpFile, err := os.CreateTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+"-*.tmp")
			if err != nil {
				return fmt.Errorf("create temporary FLibrary INPX output: %w", err)
			}
			tmpPath = tmpFile.Name()
			if err := tmpFile.Close(); err != nil {
				return fmt.Errorf("close temporary FLibrary INPX output %q: %w", tmpPath, err)
			}
			if _, err := os.Stat(outputPath); err == nil && opts.Log != nil {
				opts.Log.Warn("Overwriting existing FLibrary INPX output", zap.String("file", outputPath))
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("stat FLibrary INPX output %q: %w", outputPath, err)
			}
			if opts.Additional {
				additionalOutputPath := annotationsOutputPath(outputPath)
				stats.AdditionalOutputPath = additionalOutputPath
				additionalTmpFile, err := os.CreateTemp(filepath.Dir(additionalOutputPath), filepath.Base(additionalOutputPath)+"-*.tmp")
				if err != nil {
					return fmt.Errorf("create temporary FLibrary additional output: %w", err)
				}
				additionalTmpPath = additionalTmpFile.Name()
				if err := additionalTmpFile.Close(); err != nil {
					return fmt.Errorf("close temporary FLibrary additional output %q: %w", additionalTmpPath, err)
				}
				if _, err := os.Stat(additionalOutputPath); err == nil && opts.Log != nil {
					opts.Log.Warn("Overwriting existing FLibrary additional output", zap.String("file", additionalOutputPath))
				} else if err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("stat FLibrary additional output %q: %w", additionalOutputPath, err)
				}
			}
			if opts.Log != nil {
				opts.Log.Info("FLibrary INPX creation started", zap.String("file", outputPath), zap.Int("archives", len(dataset.Archives)))
			}
			stream, err = newStreamINPXWriter(tmpPath, additionalTmpPath, meta, dataset, opts)
			return err
		},
		func(rec model.DatasetRecord) error {
			if stream == nil {
				return errors.New("FLibrary INPX dataset record arrived before header")
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
		return stats, errors.New("FLibrary INPX dataset input is missing header")
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
	stats.Dummy = writeStats.Dummy
	if err := fileutil.ReplaceOutputFile(tmpPath, stats.OutputPath); err != nil {
		return stats, fmt.Errorf("replace FLibrary INPX output %q: %w", stats.OutputPath, err)
	}
	if stats.AdditionalOutputPath != "" {
		if err := fileutil.ReplaceOutputFile(additionalTmpPath, stats.AdditionalOutputPath); err != nil {
			return stats, fmt.Errorf("replace FLibrary additional output %q: %w", stats.AdditionalOutputPath, err)
		}
	}
	cleanupTemp = false
	return stats, nil
}

func annotationsOutputPath(outputPath string) string {
	return strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + "-annotations.zip"
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
	activeStart time.Time
	activeStats Stats
	activeDiag  entryDiagnostics
	insNo       int
	bw          *bufio.Writer
	stats       Stats
	annotations *annotationWriter
}

func newStreamINPXWriter(path string, annotationsPath string, meta inpxutil.Metadata, dataset model.Dataset, opts Options) (*streamINPXWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create FLibrary INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(meta))
	archives := inpxutil.DatasetArchiveRowsList(dataset)
	archiveByID := make(map[string]int, len(archives))
	for idx, archive := range archives {
		archiveByID[archive.Meta.ID] = idx
	}
	var annotations *annotationWriter
	if opts.Additional {
		annotations, err = newAnnotationWriter(annotationsPath, meta)
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			return nil, err
		}
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
		annotations: annotations,
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
	archive := w.archives[w.active]
	if inpxutil.InRanges(archive.Meta.Ignored, index) {
		return nil
	}
	fields, view, diagnostics, ok, err := buildRecordFields(rec, archive.Meta, w.opts)
	if err != nil || !ok {
		return err
	}
	if w.annotations != nil {
		name := fields.File
		if fields.Ext != "" {
			name += "." + fields.Ext
		}
		if err := w.annotations.WriteRecord(name, fb2Annotation(rec)); err != nil {
			return err
		}
	}
	w.stats.Files++
	w.activeDiag.add(diagnostics)
	sequences := recordSequences(rec, view, w.opts)
	if len(sequences) == 0 {
		sequences = []sequence{{}}
	}
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	for _, seq := range sequences {
		w.insNo++
		if _, err := w.bw.WriteString(recordLine(fields, seq, w.insNo)); err != nil {
			return fmt.Errorf("write FLibrary INPX entry %q: %w", name, err)
		}
		w.stats.Records++
		if view.HasDatabase {
			w.stats.DBRecords++
		} else {
			w.stats.FB2Records++
		}
	}
	return nil
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
		return 0, 0, false, fmt.Errorf("FLibrary INPX archive record for source %q has no index", locator.Source)
	}
	target, ok := w.archiveByID[locator.Source]
	if !ok {
		return 0, 0, false, fmt.Errorf("FLibrary INPX record references undeclared archive source %q", locator.Source)
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
			return fmt.Errorf("FLibrary INPX records are out of archive order: target archive %d after %d", target, w.nextArchive-1)
		}
		if err := w.openNext(); err != nil {
			return err
		}
	}
	return nil
}

func (w *streamINPXWriter) openNext() error {
	if w.nextArchive >= len(w.archives) {
		return errors.New("FLibrary INPX record references archive past declared list")
	}
	archive := w.archives[w.nextArchive]
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	zw, err := w.zw.Create(name)
	if err != nil {
		return fmt.Errorf("create FLibrary INPX entry %q: %w", name, err)
	}
	w.bw = bufio.NewWriter(zw)
	if w.annotations != nil {
		if err := w.annotations.OpenArchive(archive.Meta); err != nil {
			return err
		}
	}
	w.active = w.nextArchive
	w.activeStart = time.Now()
	w.activeStats = w.stats
	w.activeDiag = entryDiagnostics{}
	w.insNo = 0
	w.nextArchive++
	return nil
}

func (w *streamINPXWriter) finishActive() error {
	archive := w.archives[w.active]
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if w.annotations != nil {
		if err := w.annotations.FinishArchive(); err != nil {
			return err
		}
	}
	if w.opts.Log != nil {
		archiveStats := w.statsSinceActiveStart()
		w.opts.Log.Info(
			"FLibrary INPX entry created",
			zap.String("entry", strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name))+".inp"),
			zap.String("archive", archive.Meta.Name),
			zap.Int64("records", archiveStats.DBRecords),
			zap.Int64("fb2_records", archiveStats.FB2Records),
			zap.Int64("disambiguated_author_books", w.activeDiag.DisambiguatedAuthorBooks),
			zap.Int64("disambiguated_authors", w.activeDiag.DisambiguatedAuthors),
			zap.Int64("canonicalized_language_books", w.activeDiag.CanonicalizedLangBooks),
			zap.Int("files", archiveStats.Files),
			zap.Duration("elapsed", time.Since(w.activeStart)),
		)
	}
	w.stats.Archives++
	w.active = -1
	w.bw = nil
	return nil
}

func (d *entryDiagnostics) add(other entryDiagnostics) {
	d.DisambiguatedAuthorBooks += other.DisambiguatedAuthorBooks
	d.DisambiguatedAuthors += other.DisambiguatedAuthors
	d.CanonicalizedLangBooks += other.CanonicalizedLangBooks
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
	if err := inpxutil.WriteZipText(w.zw, "structure.info", structureInfo); err != nil {
		w.Close()
		return w.stats, err
	}
	collection, err := inpxutil.CollectionInfo(w.meta, templateOptions(w.opts))
	if err != nil {
		w.Close()
		return w.stats, err
	}
	if err := inpxutil.WriteZipText(w.zw, "collection.info", collection); err != nil {
		w.Close()
		return w.stats, err
	}
	version, err := inpxutil.VersionInfo(w.meta, templateOptions(w.opts))
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
	var errs []error
	if w.zw != nil {
		if err := w.zw.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close FLibrary INPX zip %q: %w", w.path, err))
		}
		w.zw = nil
	}
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close FLibrary INPX %q: %w", w.path, err))
		}
		w.f = nil
	}
	if w.annotations != nil {
		errs = append(errs, w.annotations.Close())
		w.annotations = nil
	}
	return errors.Join(errs...)
}

type annotationWriter struct {
	path string
	zw   *zip.Writer
	f    *os.File
	bw   *bufio.Writer
}

func newAnnotationWriter(path string, meta inpxutil.Metadata) (*annotationWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create FLibrary additional output %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(meta))
	return &annotationWriter{path: path, zw: zw, f: f}, nil
}

func (w *annotationWriter) OpenArchive(archive model.DatasetArchive) error {
	zw, err := w.zw.Create(archive.Name)
	if err != nil {
		return fmt.Errorf("create FLibrary additional entry %q: %w", archive.Name, err)
	}
	w.bw = bufio.NewWriter(zw)
	if _, err := w.bw.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<folder name=\""); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(xmlEscape(archive.Name)); err != nil {
		return err
	}
	_, err = w.bw.WriteString("\">\n")
	return err
}

func (w *annotationWriter) WriteRecord(name string, annotation string) error {
	if strings.TrimSpace(annotation) == "" {
		return nil
	}
	if _, err := w.bw.WriteString("\t<file name=\""); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(xmlEscape(name)); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\">\n\t\t<p>"); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(xmlEscape(strings.TrimSpace(annotation))); err != nil {
		return err
	}
	_, err := w.bw.WriteString("</p>\n\t</file>\n")
	return err
}

func (w *annotationWriter) FinishArchive() error {
	if w.bw == nil {
		return nil
	}
	if _, err := w.bw.WriteString("</folder>\n"); err != nil {
		return err
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	w.bw = nil
	return nil
}

func (w *annotationWriter) Close() error {
	var errs []error
	if w.zw != nil {
		if err := w.zw.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close FLibrary additional zip %q: %w", w.path, err))
		}
		w.zw = nil
	}
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close FLibrary additional output %q: %w", w.path, err))
		}
		w.f = nil
	}
	return errors.Join(errs...)
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

func templateOptions(opts Options) inpxutil.TemplateOptions {
	return inpxutil.TemplateOptions{CommentTemplate: opts.CommentTemplate, VersionTemplate: opts.VersionTemplate}
}

func buildRecordFields(
	rec model.DatasetRecord,
	archive model.DatasetArchive,
	opts Options,
) (recordFields, inpxutil.DatasetRecordView, entryDiagnostics, bool, error) {
	view, err := inpxutil.DatasetRecordClaims(rec)
	if err != nil {
		return recordFields{}, view, entryDiagnostics{}, false, err
	}
	diagnostics := entryDiagnostics{}
	ext := view.Catalog.FileType
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(view.Artifact.Name), ".")
	}
	if !strings.EqualFold(strings.TrimPrefix(ext, "."), "fb2") {
		return recordFields{}, view, entryDiagnostics{}, false, nil
	}
	title := view.Database.Title
	if title == "" {
		title = view.FB2.Title
	}
	if title == "" {
		return recordFields{}, view, entryDiagnostics{}, false, nil
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
	lang, languageSelection := recordLanguage(rec, view, opts)
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
	folder := filepath.Base(archive.PathHint)
	if folder == "." || folder == string(filepath.Separator) || folder == "" {
		folder = archive.Name
	}
	authors := authorsString(view.HasDatabase, view.Database.Authors, view.FB2.Authors, opts)
	if count := logDisambiguatedDBAuthors(rec, view, authors, opts); count > 0 {
		diagnostics.DisambiguatedAuthorBooks = 1
		diagnostics.DisambiguatedAuthors = int64(count)
	}
	return recordFields{
		Authors:  authors,
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
	}, view, diagnostics, true, nil
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

func peopleString(people []model.PersonValue) string {
	var b strings.Builder
	for _, person := range people {
		lastName := inpxutil.CleanseAuthorComponent(person.LastName)
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
