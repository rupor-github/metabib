package inpxutil

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	sprig "github.com/go-task/slim-sprig/v3"
	"go.uber.org/zap"

	"metabib/jsonl"
	"metabib/model"
)

const FieldSep = "\x04"

const (
	OnlineArchivePath = "online"
	OnlineArchiveName = "online.zip"
)

var cleanseReplacer = strings.NewReplacer(
	"\r\n", " ",
	"\r", " ",
	"\n", "",
	FieldSep, " ",
	"\u00a0", " ",
)

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

type DatasetArchiveRows struct {
	Meta    model.DatasetArchive
	Records map[int]model.DatasetRecord
}

type TemplateOptions struct {
	CommentTemplate string
	VersionTemplate string
}

type Metadata struct {
	Library     string
	DumpDate    string
	DumpDateISO string
}

func DatasetMetadata(dataset model.Dataset) Metadata {
	meta := Metadata{Library: dataset.Library}
	if dataset.Database == nil {
		return meta
	}
	meta.DumpDate = dataset.Database.DumpDate
	if len(meta.DumpDate) == 8 {
		meta.DumpDateISO = meta.DumpDate[:4] + "-" + meta.DumpDate[4:6] + "-" + meta.DumpDate[6:8]
	}
	return meta
}

func LoadDatasetInput(ctx context.Context, inputPrefix string, log *zap.Logger) (model.Dataset, map[string]*DatasetArchiveRows, int64, error) {
	inputPath, err := DiscoverDatasetInput(inputPrefix)
	if err != nil {
		return model.Dataset{}, nil, 0, err
	}
	if log != nil {
		log.Info("INPX dataset input selected", zap.String("input", inputPath))
	}

	var dataset model.Dataset
	var archives map[string]*DatasetArchiveRows
	var records int64
	for value, err := range jsonl.DatasetValues(ctx, inputPath) {
		if err != nil {
			return dataset, archives, records, err
		}
		if value.Header {
			dataset = value.Dataset
			archives = datasetArchiveRows(dataset)
			continue
		}
		records++
		if err := addDatasetRecord(inputPath, archives, value.Record, log); err != nil {
			return dataset, archives, records, err
		}
	}
	if log != nil {
		log.Info(
			"INPX dataset records loaded",
			zap.Int64("records", records),
			zap.Int("archives", len(archives)),
		)
	}
	return dataset, archives, records, nil
}

func datasetArchiveRows(dataset model.Dataset) map[string]*DatasetArchiveRows {
	archives := make(map[string]*DatasetArchiveRows, len(dataset.Archives))
	for _, archive := range dataset.Archives {
		archives[archive.ID] = &DatasetArchiveRows{Meta: archive, Records: make(map[int]model.DatasetRecord)}
	}
	if len(dataset.Archives) == 0 {
		archives[OnlineArchivePath] = newOnlineDatasetArchive()
	}
	return archives
}

func DatasetArchiveList(archives map[string]*DatasetArchiveRows) []*DatasetArchiveRows {
	archiveList := make([]*DatasetArchiveRows, 0, len(archives))
	for _, archive := range archives {
		archiveList = append(archiveList, archive)
	}
	slices.SortFunc(archiveList, func(a, b *DatasetArchiveRows) int {
		if a.Meta.Ordinal != b.Meta.Ordinal {
			return a.Meta.Ordinal - b.Meta.Ordinal
		}
		return strings.Compare(a.Meta.Name, b.Meta.Name)
	})
	return archiveList
}

func addDatasetRecord(path string, archives map[string]*DatasetArchiveRows, rec model.DatasetRecord, log *zap.Logger) error {
	locator := rec.Record.Locator
	if locator.Kind != "archive_entry" {
		if online := archives[OnlineArchivePath]; online != nil {
			idx := online.Meta.Entries
			online.Records[idx] = rec
			online.Meta.Entries++
		}
		return nil
	}
	if locator.Index == nil {
		return fmt.Errorf("dataset JSONL %q archive record for source %q has no index", path, locator.Source)
	}
	archive := archives[locator.Source]
	if archive == nil {
		return fmt.Errorf("dataset JSONL %q record references undeclared archive source %q", path, locator.Source)
	}
	if existing, ok := archive.Records[*locator.Index]; ok {
		logDuplicateDatasetArchiveIndex(log, path, locator.Source, *locator.Index, existing, rec)
		return nil
	}
	archive.Records[*locator.Index] = rec
	return nil
}

func DiscoverDatasetInput(input string) (string, error) {
	if isDatasetInputPath(input) {
		if _, err := os.Stat(input); err != nil {
			return "", fmt.Errorf("stat dataset input %q: %w", input, err)
		}
		return filepath.Clean(input), nil
	}
	candidates := []string{
		input + ".jsonl",
		input + ".jsonl.zst",
		input + ".jsonl.gz",
		input + ".jsonl.zip",
	}
	matches := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			matches = append(matches, filepath.Clean(candidate))
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat dataset input %q: %w", candidate, err)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one dataset JSONL input for %q, found %d", input, len(matches))
	}
	return matches[0], nil
}

func isDatasetInputPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".jsonl") ||
		strings.HasSuffix(lower, ".jsonl.zst") ||
		strings.HasSuffix(lower, ".jsonl.gz") ||
		strings.HasSuffix(lower, ".jsonl.zip")
}

func EnsureDumpDate(meta *Metadata, log *zap.Logger) {
	if meta == nil || meta.DumpDate != "" {
		return
	}
	now := time.Now().UTC()
	meta.DumpDate = now.Format("20060102")
	meta.DumpDateISO = now.Format("2006-01-02")
	if log != nil {
		log.Warn(
			"INPX input metadata has empty dump date; using current date",
			zap.String("dump_date", meta.DumpDate),
			zap.String("display_date", meta.DumpDateISO),
		)
	}
}

func newOnlineDatasetArchive() *DatasetArchiveRows {
	return &DatasetArchiveRows{
		Meta:    model.DatasetArchive{ID: OnlineArchivePath, Name: OnlineArchiveName},
		Records: make(map[int]model.DatasetRecord),
	}
}

func logDuplicateDatasetArchiveIndex(
	log *zap.Logger,
	path string,
	archiveSource string,
	index int,
	existing model.DatasetRecord,
	duplicate model.DatasetRecord,
) {
	if log == nil {
		return
	}
	log.Warn(
		"Duplicate archive index in INPX dataset input; keeping first record",
		zap.String("input", path),
		zap.String("archive", archiveSource),
		zap.Int("archive_index", index),
		zap.String("existing_kind", existing.Record.Locator.Kind),
		zap.String("duplicate_kind", duplicate.Record.Locator.Kind),
	)
}

func OutputPath(prefix string, meta Metadata) (string, error) {
	base := prefix
	date := meta.DumpDate
	if date != "" && !isCompactDumpDate(date) {
		return "", fmt.Errorf("invalid compact dump date %q: expected exactly 8 digits", date)
	}
	if date != "" && !strings.HasSuffix(base, "_"+date) {
		base += "_" + date
	}
	return base + ".inpx", nil
}

func isCompactDumpDate(value string) bool {
	if len(value) != 8 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func ZipComment(meta Metadata) string {
	return meta.Library + " - " + DisplayDate(meta)
}

func CollectionInfo(meta Metadata, opts TemplateOptions) (string, error) {
	if opts.CommentTemplate == "" {
		return "", errors.New("collection.info comment template is empty")
	}
	return RenderInfoTemplate("comment_template", opts.CommentTemplate, meta)
}

func VersionInfo(meta Metadata, opts TemplateOptions) (string, error) {
	if opts.VersionTemplate == "" {
		return "", errors.New("version.info template is empty")
	}
	return RenderInfoTemplate("version_template", opts.VersionTemplate, meta)
}

func RenderInfoTemplate(name string, text string, meta Metadata) (string, error) {
	tmpl, err := template.New(name).Funcs(sprig.FuncMap()).Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	values := struct {
		DatabaseName string
		DumpDate     string
		DisplayDate  string
	}{
		DatabaseName: meta.Library,
		DumpDate:     meta.DumpDate,
		DisplayDate:  DisplayDate(meta),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("execute %s: %w", name, err)
	}
	return buf.String(), nil
}

func WriteZipText(zw *zip.Writer, name string, text string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create INPX entry %q: %w", name, err)
	}
	_, err = io.WriteString(w, text)
	return err
}

func InRanges(ranges []model.IndexRange, idx int) bool {
	for _, r := range ranges {
		if idx >= r.Start && idx <= r.End {
			return true
		}
	}
	return false
}

func Cleanse(value string) string {
	return cleanseReplacer.Replace(value)
}

func DateOnly(value string) string {
	if len(value) >= 10 {
		if _, err := time.Parse("2006-01-02", value[:10]); err == nil {
			return value[:10]
		}
	}
	return value
}

func CloseZipFile(path string, zw *zip.Writer, f *os.File) error {
	if err := zw.Close(); err != nil {
		f.Close()
		return fmt.Errorf("close INPX zip %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close INPX %q: %w", path, err)
	}
	return nil
}

func DisplayDate(meta Metadata) string {
	if meta.DumpDateISO != "" {
		return meta.DumpDateISO
	}
	date := meta.DumpDate
	if len(date) == 8 {
		return date[:4] + "-" + date[4:6] + "-" + date[6:8]
	}
	return date
}
