package flibinpx

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/internal/inpxutil"
	"metabib/model"
)

const structureInfo = "AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;LANG;LIBRATE;KEYWORDS;YEAR;SOURCELIB;"

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
	SourceLib           string
	DisambiguateAuthors bool
	DisambiguationField inpxutil.AuthorDisambiguationField
	Language            *inpxutil.LanguageResolver
	CommentTemplate     string
	VersionTemplate     string
	Log                 *zap.Logger
	Verbose             bool
	AuthorDisambiguator *inpxutil.AuthorDisambiguator
}

type Stats = inpxutil.Stats

type sequence = inpxutil.Sequence

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
	Lang     string
	Rate     string
	Keywords string
	Year     string
	Source   string
}

type entryDiagnostics = inpxutil.EntryDiagnostics

func ParseSequenceMode(value string) (SequenceMode, error) {
	return inpxutil.ParseSequenceMode(value, "FLibrary INPX")
}

func ParseFB2Preference(value string) (FB2Preference, error) {
	return inpxutil.ParseFB2Preference(value, "FLibrary INPX")
}

func ParseFlattenMode(value string) (FlattenMode, error) {
	return inpxutil.ParseFlattenMode(value, "FLibrary INPX")
}

func ParseDedupMode(value string) (DedupMode, error) {
	return inpxutil.ParseDedupMode(value, "FLibrary INPX")
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
	contentMode, err := inpxutil.ParseContentMode(string(opts.ContentMode), inpxutil.ContentFB2)
	if err != nil {
		return stats, err
	}
	opts.ContentMode = contentMode
	var stream *streamINPXWriter
	var tmpPath string
	var annotationsTmpPath string
	var compilationsTmpPath string
	var compilationsOutputPath string
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
	_, _, err = inpxutil.StreamDatasetInput(
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
			if opts.Additional && len(dataset.Archives) == 0 {
				if opts.Log != nil {
					opts.Log.Warn("Skipping FLibrary additional artifacts for database-only input")
				}
				opts.Additional = false
			}
			tmpPath, err = inpxutil.PrepareOutput(outputPath, "FLibrary INPX", opts.Log)
			if err != nil {
				return err
			}
			if opts.Additional {
				additionalOutputPath := inpxutil.AnnotationsOutputPath(outputPath)
				stats.AdditionalOutputPath = additionalOutputPath
				annotationsTmpPath, err = inpxutil.PrepareOutput(additionalOutputPath, "FLibrary additional", opts.Log)
				if err != nil {
					return err
				}
				if dataset.Processing.FB2BodyFingerprints == nil || dataset.Processing.FB2BodyFingerprints.Coverage == model.FB2BodyFingerprintCoverageNone {
					if opts.Log != nil {
						opts.Log.Warn("Skipping FLibrary compilations output because dataset has no FB2 body fingerprints")
					}
				} else {
					if dataset.Processing.FB2BodyFingerprints.Coverage == model.FB2BodyFingerprintCoveragePartial && opts.Log != nil {
						opts.Log.Warn("Generating FLibrary compilations output from partial FB2 body fingerprint coverage")
					}
					compilationsOutputPath = inpxutil.CompilationsOutputPath(outputPath)
					compilationsTmpPath, err = inpxutil.PrepareOutput(compilationsOutputPath, "FLibrary compilations", opts.Log)
					if err != nil {
						return err
					}
				}
			}
			if opts.Log != nil {
				opts.Log.Info("FLibrary INPX creation started", zap.String("file", outputPath), zap.Int("archives", len(dataset.Archives)))
			}
			stream, err = newStreamINPXWriter(tmpPath, annotationsTmpPath, compilationsTmpPath, meta, dataset, opts)
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
		if err := fileutil.ReplaceOutputFile(annotationsTmpPath, stats.AdditionalOutputPath); err != nil {
			return stats, fmt.Errorf("replace FLibrary additional output %q: %w", stats.AdditionalOutputPath, err)
		}
	}
	if compilationsOutputPath != "" && compilationsTmpPath != "" {
		if _, err := os.Stat(compilationsTmpPath); err == nil {
			if err := fileutil.ReplaceOutputFile(compilationsTmpPath, compilationsOutputPath); err != nil {
				return stats, fmt.Errorf("replace FLibrary compilations output %q: %w", compilationsOutputPath, err)
			}
			compilationsTmpPath = ""
		} else if !os.IsNotExist(err) {
			return stats, fmt.Errorf("stat FLibrary compilations output %q: %w", compilationsTmpPath, err)
		}
	}
	cleanupTemp = false
	return stats, nil
}

type streamINPXWriter struct {
	path         string
	meta         inpxutil.Metadata
	opts         Options
	zw           *zip.Writer
	f            *os.File
	archives     []*inpxutil.DatasetArchiveRows
	archiveByID  map[string]int
	nextArchive  int
	active       int
	activeStart  time.Time
	activeStats  Stats
	activeDiag   entryDiagnostics
	bw           *bufio.Writer
	stats        Stats
	annotations  *annotationWriter
	compilations *inpxutil.CompilationCollector
}

func newStreamINPXWriter(
	path string,
	annotationsPath string,
	compilationsPath string,
	meta inpxutil.Metadata,
	dataset model.Dataset,
	opts Options,
) (*streamINPXWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create FLibrary INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(inpxutil.ZipComment(meta))
	archives, archiveByID := inpxutil.DatasetArchiveIndex(dataset)
	var annotations *annotationWriter
	if opts.Additional {
		annotations, err = newAnnotationWriter(annotationsPath, meta)
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			return nil, err
		}
	}
	var compilations *inpxutil.CompilationCollector
	if compilationsPath != "" {
		compilations = inpxutil.NewCompilationCollector(compilationsPath, meta, opts.Log)
	}
	return &streamINPXWriter{
		path:         path,
		meta:         meta,
		opts:         opts,
		zw:           zw,
		f:            f,
		archives:     archives,
		archiveByID:  archiveByID,
		active:       -1,
		stats:        Stats{DumpDate: meta.DumpDate},
		annotations:  annotations,
		compilations: compilations,
	}, nil
}

func (w *streamINPXWriter) WriteRecord(rec model.DatasetRecord) error {
	keep, err := inpxutil.RecordMatchesContent(w.opts.ContentMode, rec)
	if err != nil {
		return err
	}
	if !keep {
		return nil
	}
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
	fields, view, diagnostics, ok, err := buildRecordFields(rec, w.opts)
	if err != nil || !ok {
		return err
	}
	if err := w.ensureActiveWriter(); err != nil {
		return err
	}
	if w.annotations != nil {
		name := fields.File
		if fields.Ext != "" {
			name += "." + fields.Ext
		}
		if err := w.annotations.WriteRecord(name, inpxutil.RecordAnnotation(rec, w.opts.FB2Preference)); err != nil {
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
	w.stats.Files++
	w.activeDiag.Add(diagnostics)
	sequences := recordSequences(rec, view, w.opts)
	if len(sequences) == 0 {
		sequences = []sequence{{}}
	}
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	for _, seq := range sequences {
		if _, err := w.bw.WriteString(recordLine(fields, seq)); err != nil {
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
	w.active = w.nextArchive
	w.activeStart = time.Now()
	w.activeStats = w.stats
	w.activeDiag = entryDiagnostics{}
	w.nextArchive++
	return nil
}

func (w *streamINPXWriter) ensureActiveWriter() error {
	if w.bw != nil {
		return nil
	}
	archive := w.archives[w.active]
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
	return nil
}

func (w *streamINPXWriter) finishActive() error {
	archive := w.archives[w.active]
	if w.bw == nil {
		w.active = -1
		return nil
	}
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
	if err := inpxutil.WriteInfoEntries(w.zw, w.meta, structureInfo, inpxutil.TemplateOptions{
		CommentTemplate: w.opts.CommentTemplate, VersionTemplate: w.opts.VersionTemplate,
	}); err != nil {
		w.Close()
		return w.stats, err
	}
	if w.compilations != nil {
		if err := w.compilations.Write(); err != nil {
			w.Close()
			return w.stats, err
		}
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
	return inpxutil.WriteAnnotationHeader(w.bw, archive.Name)
}

func (w *annotationWriter) WriteRecord(name string, annotation string) error {
	return inpxutil.WriteAnnotationRecord(w.bw, name, annotation)
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

func buildRecordFields(
	rec model.DatasetRecord,
	opts Options,
) (recordFields, inpxutil.DatasetRecordView, entryDiagnostics, bool, error) {
	view, err := inpxutil.DatasetRecordClaims(rec)
	if err != nil {
		return recordFields{}, view, entryDiagnostics{}, false, err
	}
	selected, diagnostics, ok := inpxutil.PrepareRecordFields(rec, view, authorOptions(opts), opts.Language, opts.Log)
	if !ok {
		return recordFields{}, view, diagnostics, false, nil
	}
	return recordFields{
		Authors:  selected.Authors,
		Genres:   selected.Genres,
		Title:    inpxutil.Cleanse(selected.Title),
		File:     inpxutil.CleanseFileName(selected.File),
		Size:     strconv.FormatUint(view.Artifact.Size, 10),
		LibID:    inpxutil.DatasetBookID(rec),
		Deleted:  inpxutil.Cleanse(view.Catalog.Deleted),
		Ext:      inpxutil.CleanseFileName(strings.TrimPrefix(selected.Ext, ".")),
		Date:     inpxutil.Cleanse(selected.Date),
		Lang:     inpxutil.Cleanse(strings.TrimSpace(selected.Language)),
		Rate:     view.Catalog.Rating,
		Keywords: inpxutil.KeywordsString(selected.Keywords),
		Year:     inpxutil.Cleanse(selected.Year),
		Source:   inpxutil.Cleanse(opts.SourceLib),
	}, view, diagnostics, true, nil
}

func recordLine(fields recordFields, seq sequence) string {
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
		fields.Lang,
		fields.Rate,
		fields.Keywords,
		fields.Year,
		fields.Source,
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

func recordSequences(rec model.DatasetRecord, view inpxutil.DatasetRecordView, opts Options) []sequence {
	return inpxutil.RecordSequences(rec, view, inpxutil.SequenceOptions{
		Mode:                opts.SequenceMode,
		Preference:          opts.FB2Preference,
		Flatten:             opts.FlattenMode,
		Dedup:               opts.DedupMode,
		PathSeparator:       opts.FB2PathSeparator,
		DuplicateLogMessage: "Dropped duplicate FLibrary sequence",
	}, opts.Log)
}
