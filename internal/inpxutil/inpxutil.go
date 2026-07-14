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
	"sort"
	"strings"
	"text/template"
	"time"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
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

type ArchiveRows struct {
	Meta    model.MergeArchiveMetadata
	Records map[int]model.Record
}

type TemplateOptions struct {
	CommentTemplate string
	VersionTemplate string
}

func LoadInput(ctx context.Context, inputPrefix string, log *zap.Logger) (model.MergeMetadata, map[string]*ArchiveRows, int64, error) {
	metaPath, err := DiscoverMetadata(inputPrefix)
	if err != nil {
		return model.MergeMetadata{}, nil, 0, err
	}
	if log != nil {
		log.Info("INPX metadata selected", zap.String("metadata", metaPath))
	}
	meta, err := ReadMetadata(metaPath)
	if err != nil {
		return model.MergeMetadata{}, nil, 0, err
	}
	parts, err := DiscoverInputParts(inputPrefix, metaPath, meta, log)
	if err != nil {
		return meta, nil, 0, err
	}
	if log != nil {
		log.Info(
			"INPX input parts selected",
			zap.Int("parts", len(parts)),
			zap.Int("archives", len(meta.Archives)),
			zap.String("dump_date", meta.Database.DumpDate),
		)
	}
	archives := make(map[string]*ArchiveRows, len(meta.Archives))
	for _, archive := range meta.Archives {
		archives[archive.Path] = &ArchiveRows{Meta: archive, Records: make(map[int]model.Record)}
	}
	if len(meta.Archives) == 0 {
		archives[OnlineArchivePath] = newOnlineArchive()
	}
	loadStart := time.Now()
	loaded, err := ReadRecords(ctx, parts, archives, log)
	if err != nil {
		return meta, nil, loaded, err
	}
	if log != nil {
		log.Info("INPX records loaded", zap.Int64("records", loaded), zap.Int("parts", len(parts)), zap.Duration("elapsed", time.Since(loadStart)))
	}
	return meta, archives, loaded, nil
}

func DiscoverMetadata(prefix string) (string, error) {
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

func DiscoverInputParts(prefix string, metaPath string, meta model.MergeMetadata, log *zap.Logger) ([]string, error) {
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

func ReadMetadata(path string) (model.MergeMetadata, error) {
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

func EnsureDumpDate(meta *model.MergeMetadata, log *zap.Logger) {
	if meta == nil || meta.Database.DumpDate != "" {
		return
	}
	now := time.Now().UTC()
	meta.Database.DumpDate = now.Format("20060102")
	meta.Database.DumpDateISO = now.Format("2006-01-02")
	if log != nil {
		log.Warn(
			"INPX input metadata has empty dump date; using current date",
			zap.String("dump_date", meta.Database.DumpDate),
			zap.String("display_date", meta.Database.DumpDateISO),
		)
	}
}

func ReadRecords(ctx context.Context, parts []string, archives map[string]*ArchiveRows, log *zap.Logger) (int64, error) {
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
				if online := archives[OnlineArchivePath]; online != nil {
					idx := online.Meta.Entries
					online.Records[idx] = rec
					online.Meta.Entries++
				}
				continue
			}
			archive := archives[rec.ID.Archive.Path]
			if archive == nil {
				archive = &ArchiveRows{
					Meta:    model.MergeArchiveMetadata{Path: rec.ID.Archive.Path, Name: filepath.Base(rec.ID.Archive.Path)},
					Records: make(map[int]model.Record),
				}
				archives[rec.ID.Archive.Path] = archive
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

func newOnlineArchive() *ArchiveRows {
	return &ArchiveRows{
		Meta:    model.MergeArchiveMetadata{Path: OnlineArchivePath, Name: OnlineArchiveName},
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

func ArchiveList(archives map[string]*ArchiveRows) []*ArchiveRows {
	archiveList := make([]*ArchiveRows, 0, len(archives))
	for _, archive := range archives {
		archiveList = append(archiveList, archive)
	}
	sort.Slice(archiveList, func(i, j int) bool { return archiveList[i].Meta.Name < archiveList[j].Meta.Name })
	return archiveList
}

func OutputPath(prefix string, meta model.MergeMetadata) (string, error) {
	base := prefix
	date := meta.Database.DumpDate
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

func ZipComment(meta model.MergeMetadata) string {
	return meta.Library + " - " + DisplayDate(meta)
}

func CollectionInfo(meta model.MergeMetadata, opts TemplateOptions) (string, error) {
	if opts.CommentTemplate == "" {
		return "", errors.New("collection.info comment template is empty")
	}
	return RenderInfoTemplate("comment_template", opts.CommentTemplate, meta)
}

func VersionInfo(meta model.MergeMetadata, opts TemplateOptions) (string, error) {
	if opts.VersionTemplate == "" {
		return "", errors.New("version.info template is empty")
	}
	return RenderInfoTemplate("version_template", opts.VersionTemplate, meta)
}

func RenderInfoTemplate(name string, text string, meta model.MergeMetadata) (string, error) {
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
		DumpDate:     meta.Database.DumpDate,
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

func DisplayDate(meta model.MergeMetadata) string {
	if meta.Database.DumpDateISO != "" {
		return meta.Database.DumpDateISO
	}
	date := meta.Database.DumpDate
	if len(date) == 8 {
		return date[:4] + "-" + date[4:6] + "-" + date[6:8]
	}
	return date
}
