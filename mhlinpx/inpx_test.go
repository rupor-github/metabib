package mhlinpx

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "fb2-0000000001-0000000003.zip")
	prefix := filepath.Join(dir, "all")
	writeMetadata(t, prefix, model.MergeMetadata{
		Schema:  "metabib.merge_metadata/1",
		Library: "flibusta",
		Database: model.MergeDatabaseMetadata{
			DumpDate:    "20260603",
			DumpDateISO: "2026-06-03",
		},
		Archives: []model.MergeArchiveMetadata{{
			Path:    archivePath,
			Name:    filepath.Base(archivePath),
			Entries: 3,
			Ignored: []model.IndexRange{{Start: 1, End: 1}},
		}},
		Parts: []string{"all.0000000001-0000000001.jsonl"},
	})
	w, err := jsonl.CreateCompressed(prefix, 0, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			BookID:    1,
			FileName:  "1",
			Extension: "fb2",
			Archive:   &model.ArchiveInfo{Path: archivePath, Entry: "1.fb2", Index: 0, UncompressedSize: 123, Modified: "2026-06-03T00:00:00Z"},
		},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: 1, FileSize: 123, Title: "Title", FileType: "fb2", Time: "2026-06-03T00:00:00Z", Lang: "ru", Deleted: "1"},
			Authors: []model.Contributor{{FirstName: "First", LastName: "Last"}},
			Genres:  []model.DBGenre{{Code: "sf"}},
		}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Write(model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			BookID:    1,
			FileName:  "online-only",
			Extension: "fb2",
		},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: 1, FileSize: 321, Title: "Online Only", FileType: "fb2", Time: "2026-06-03T00:00:00Z"},
		}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	fixedTmp := filepath.Join(dir, "flibusta_20260603.inpx.tmp")
	if err := os.WriteFile(fixedTmp, []byte("stale fixed tmp"), 0o644); err != nil {
		t.Fatalf("write fixed temp file: %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:   prefix,
		OutputPrefix:  filepath.Join(dir, "flibusta"),
		Format:        Format2X,
		SequenceMode:  SequenceAuthor,
		FB2Preference: PreferComplement,
		QuickFix:      true,
		Limits:        DefaultLimits(),
		CommentTemplate: strings.Join([]string{
			"\ufeff{{ .DatabaseName }} FB2 - {{ .DisplayDate }}",
			"{{ .DatabaseName }}_{{ .DumpDate }}",
			"65536",
			"Локальные архивы библиотеки {{ .DatabaseName }} (FB2) {{ .DisplayDate }}",
		}, "\r\n"),
		VersionTemplate: "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if filepath.Base(stats.OutputPath) != "flibusta_20260603.inpx" {
		t.Fatalf("output = %q", stats.OutputPath)
	}
	if data, err := os.ReadFile(fixedTmp); err != nil || string(data) != "stale fixed tmp" {
		t.Fatalf("fixed temp file = %q, %v", data, err)
	}
	if stats.Archives != 1 || stats.Files != 2 || stats.Records != 1 || stats.DBRecords != 1 ||
		stats.FB2Records != 0 || stats.Dummy != 1 || stats.DumpDate != "20260603" {
		t.Fatalf("stats = %#v", stats)
	}
	entries, comment := readZipEntries(t, stats.OutputPath)
	if comment != "flibusta - 2026-06-03" {
		t.Fatalf("comment = %q", comment)
	}
	if !strings.Contains(entries["fb2-0000000001-0000000003.inp"], "Last,First,") {
		t.Fatalf("inp = %q", entries["fb2-0000000001-0000000003.inp"])
	}
	if !strings.Contains(entries["fb2-0000000001-0000000003.inp"], "dummy record") {
		t.Fatalf("inp missing dummy = %q", entries["fb2-0000000001-0000000003.inp"])
	}
	if strings.TrimSpace(entries["version.info"]) != "20260603" {
		t.Fatalf("version.info = %q", entries["version.info"])
	}
	if _, ok := entries["online.inp"]; ok {
		t.Fatalf("unexpected online.inp for mixed archive input: %#v", entries)
	}
	if !strings.HasPrefix(entries["collection.info"], "\ufeff") {
		t.Fatalf("collection.info missing UTF-8 BOM: %q", entries["collection.info"])
	}
	if !strings.Contains(entries["collection.info"], "flibusta_20260603") {
		t.Fatalf("collection.info = %q", entries["collection.info"])
	}
}

func TestGenerateDatabaseOnlyWritesOnlineINP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "online")
	writeMetadata(t, prefix, model.MergeMetadata{
		Schema:  "metabib.merge_metadata/1",
		Library: "librusec",
		Database: model.MergeDatabaseMetadata{
			DumpDate:    "20260713",
			DumpDateISO: "2026-07-13",
		},
		Parts: []string{"online.0000000001-0000000001.jsonl"},
	})
	w, err := jsonl.CreateCompressed(prefix, 0, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "librusec",
			BookID:    1,
			FileName:  "1",
			Extension: "fb2",
		},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: 1, FileSize: 123, Title: "Online Title", FileType: "fb2", Time: "2026-07-13T00:00:00Z", Lang: "ru"},
			Authors: []model.Contributor{{FirstName: "First", LastName: "Last"}},
			Genres:  []model.DBGenre{{Code: "sf"}},
		}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:     prefix,
		OutputPrefix:    filepath.Join(dir, "librusec"),
		Format:          Format2X,
		SequenceMode:    SequenceAuthor,
		FB2Preference:   PreferComplement,
		QuickFix:        true,
		Limits:          DefaultLimits(),
		CommentTemplate: "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate: "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.Archives != 1 || stats.Files != 1 || stats.Records != 1 || stats.DBRecords != 1 || stats.Dummy != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	entries, _ := readZipEntries(t, stats.OutputPath)
	if !strings.Contains(entries["online.inp"], "Last,First,") || !strings.Contains(entries["online.inp"], "Online Title") {
		t.Fatalf("online.inp = %q", entries["online.inp"])
	}
}

func TestInfoTemplates(t *testing.T) {
	t.Parallel()

	meta := model.MergeMetadata{
		Library: "flibusta",
		Database: model.MergeDatabaseMetadata{
			DumpDate:    "20260603",
			DumpDateISO: "2026-06-03",
		},
	}
	got, err := collectionInfo(meta, Options{CommentTemplate: "\ufeff{{ .DatabaseName | upper }} {{ .DumpDate }} {{ .DisplayDate }}"})
	if err != nil {
		t.Fatalf("collectionInfo() error = %v", err)
	}
	if got != "\ufeffFLIBUSTA 20260603 2026-06-03" {
		t.Fatalf("collectionInfo() = %q", got)
	}
	got, err = versionInfo(meta, Options{VersionTemplate: "{{ .DumpDate }} {{ .DatabaseName | upper }}\r\n"})
	if err != nil {
		t.Fatalf("versionInfo() error = %v", err)
	}
	if got != "20260603 FLIBUSTA\r\n" {
		t.Fatalf("versionInfo() = %q", got)
	}
}

func TestDBSequenceEmitsZeroNumber(t *testing.T) {
	t.Parallel()

	name, num := dbSequence([]model.DBSequence{{Name: "Series", Number: 0}}, SequenceAuthor)
	if name != "Series" || num != "0" {
		t.Fatalf("dbSequence() = %q, %q", name, num)
	}
}

func TestAuthorsStringDBPresentWithoutAuthors(t *testing.T) {
	t.Parallel()

	titleInfo := &model.FB2TitleInfo{Authors: []model.FB2Person{{FirstName: "неизвестен", LastName: "Автор"}}}
	got := authorsString(true, nil, titleInfo, Options{FB2Preference: PreferComplement, Limits: DefaultLimits()})
	if got != "неизвестный,автор,:" {
		t.Fatalf("authorsString() = %q", got)
	}
}

func TestRecordLineRUKSAppendsMD5AndReplacement(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		ID: model.RecordID{BookID: 1, FileName: "1", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book: &model.DBBook{
				BookID:     1,
				FileSize:   123,
				Title:      "Title",
				FileType:   "fb2",
				MD5:        "0123456789abcdef0123456789abcdef",
				ReplacedBy: 42,
			},
			Authors: []model.Contributor{{FirstName: "First", LastName: "Last"}},
			Genres:  []model.DBGenre{{Code: "sf"}},
		}},
	}
	line := recordLine(rec, Options{Format: FormatRUKS, QuickFix: true, Limits: DefaultLimits()})
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), fieldSep)
	if len(fields) != 17 {
		t.Fatalf("field count = %d fields=%#v line=%q", len(fields), fields, line)
	}
	if fields[14] != "0123456789abcdef0123456789abcdef" || fields[15] != "42" || fields[16] != "" {
		t.Fatalf("RUKS tail fields = %#v", fields[14:])
	}
}

func TestCleanseRemovesINPLayoutCharacters(t *testing.T) {
	t.Parallel()

	got := inpxutil.Cleanse("a" + fieldSep + "b\rc\r\nd\ne\u00a0f")
	if strings.Contains(got, fieldSep) || strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("Cleanse() = %q, still contains layout characters", got)
	}
	if got != "a b c de f" {
		t.Fatalf("Cleanse() = %q", got)
	}
}

func TestRecordLineSanitizesFieldSeparators(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		ID: model.RecordID{BookID: 1, FileName: "file" + fieldSep + "name", Extension: "fb2" + fieldSep + "bad"},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book: &model.DBBook{
				BookID:   1,
				Title:    "bad" + fieldSep + "title",
				Time:     "bad" + fieldSep + "date",
				Lang:     "en" + fieldSep + "ru",
				Deleted:  "bad\rdelete",
				Keywords: "bad\rkeywords",
				MD5:      "md5" + fieldSep + "bad",
			},
			Authors: []model.Contributor{{FirstName: "First" + fieldSep + "Name", LastName: "Last\rName"}},
			Genres:  []model.DBGenre{{Code: "sf" + fieldSep + "bad"}},
			Sequences: []model.DBSequence{{
				Name:   "seq" + fieldSep + "bad",
				Number: 7,
			}},
		}},
	}
	line := recordLine(rec, Options{Format: FormatRUKS, QuickFix: true, Limits: DefaultLimits()})
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), fieldSep)
	if len(fields) != 17 {
		t.Fatalf("field count = %d fields=%#v line=%q", len(fields), fields, line)
	}
	for idx, field := range fields[:len(fields)-1] {
		if strings.Contains(field, fieldSep) || strings.Contains(field, "\r") || strings.Contains(field, "\n") {
			t.Fatalf("field %d = %q contains unsanitized layout character", idx, field)
		}
	}
}

func TestReadRecordsWarnsAndKeepsFirstDuplicateArchiveIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	archivePath := filepath.Join(dir, "books.zip")
	w, err := jsonl.CreateCompressed(prefix, 0, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	for _, rec := range []model.Record{
		mhlTestRecord(archivePath, "1.fb2", 0, 1),
		mhlTestRecord(archivePath, "2.fb2", 0, 2),
		mhlTestRecord(archivePath, "3.fb2", 2, 3),
		mhlTestRecord(archivePath, "4.fb2", 2, 4),
	} {
		if err := w.Write(rec); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	part := filepath.Join(dir, "all.0000000001-0000000004.jsonl")
	archives := map[string]*archiveRows{
		archivePath: {Meta: model.MergeArchiveMetadata{Path: archivePath, Name: filepath.Base(archivePath)}, Records: make(map[int]model.Record)},
	}
	core, logs := observer.New(zap.WarnLevel)

	loaded, err := readRecords(context.Background(), []string{part}, archives, zap.New(core))
	if err != nil {
		t.Fatalf("readRecords() error = %v", err)
	}
	if loaded != 4 {
		t.Fatalf("loaded = %d, want 4", loaded)
	}
	if archives[archivePath].Records[0].ID.BookID != 1 || archives[archivePath].Records[2].ID.BookID != 3 {
		t.Fatalf("records = %#v, want first record for each duplicate index", archives[archivePath].Records)
	}
	if logs.FilterMessage("Duplicate archive index in INPX input; keeping first record").Len() != 2 {
		t.Fatalf("logs = %#v, want two duplicate warnings", logs.All())
	}
}

func TestReadRecordsRejectsArchiveMissingFromMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	archivePath := filepath.Join(dir, "books.zip")
	w, err := jsonl.CreateCompressed(prefix, 0, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(mhlTestRecord(archivePath, "1.fb2", 0, 1)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	part := filepath.Join(dir, "all.0000000001-0000000001.jsonl")

	_, err = readRecords(context.Background(), []string{part}, map[string]*archiveRows{}, nil)
	if err == nil || !strings.Contains(err.Error(), "rebuild merge output") {
		t.Fatalf("readRecords() error = %v, want rebuild guidance", err)
	}
}

func mhlTestRecord(archivePath string, entry string, index int, bookID int64) model.Record {
	return model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			BookID: bookID,
			Archive: &model.ArchiveInfo{
				Path:  archivePath,
				Entry: entry,
				Index: index,
			},
		},
	}
}

func writeMetadata(t *testing.T, prefix string, meta model.MergeMetadata) {
	t.Helper()
	data, err := jsonv2.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(prefix+".meta.json", append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readZipEntries(t *testing.T, path string) (map[string]string, string) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	entries := map[string]string{}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatalf("Open(%q) error = %v", file.Name, err)
		}
		buf := new(strings.Builder)
		if _, err := io.Copy(buf, r); err != nil {
			r.Close()
			t.Fatalf("ReadFrom(%q) error = %v", file.Name, err)
		}
		r.Close()
		entries[file.Name] = buf.String()
	}
	return entries, string(zr.Comment)
}
