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
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
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
	if stats.Archives != 1 || stats.Files != 2 || stats.Records != 1 || stats.DBRecords != 1 ||
		stats.FB2Records != 0 || stats.Dummy != 1 || stats.DumpDate != "20260603" {
		t.Fatalf("stats = %#v", stats)
	}
	zr, err := zip.OpenReader(stats.OutputPath)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	if string(zr.Comment) != "flibusta - 2026-06-03" {
		t.Fatalf("comment = %q", zr.Comment)
	}
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
	if !strings.Contains(entries["fb2-0000000001-0000000003.inp"], "Last,First,") {
		t.Fatalf("inp = %q", entries["fb2-0000000001-0000000003.inp"])
	}
	if !strings.Contains(entries["fb2-0000000001-0000000003.inp"], "dummy record") {
		t.Fatalf("inp missing dummy = %q", entries["fb2-0000000001-0000000003.inp"])
	}
	if strings.TrimSpace(entries["version.info"]) != "20260603" {
		t.Fatalf("version.info = %q", entries["version.info"])
	}
	if !strings.HasPrefix(entries["collection.info"], "\ufeff") {
		t.Fatalf("collection.info missing UTF-8 BOM: %q", entries["collection.info"])
	}
	if !strings.Contains(entries["collection.info"], "flibusta_20260603") {
		t.Fatalf("collection.info = %q", entries["collection.info"])
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

	got := cleanse("a" + fieldSep + "b\rc\r\nd\ne\u00a0f")
	if strings.Contains(got, fieldSep) || strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("cleanse() = %q, still contains layout characters", got)
	}
	if got != "a b c de f" {
		t.Fatalf("cleanse() = %q", got)
	}
}

func TestRecordLineSanitizesFieldSeparators(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		ID: model.RecordID{BookID: 1, FileName: "file" + fieldSep + "name", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: 1, Title: "bad" + fieldSep + "title", FileType: "fb2", Keywords: "bad\rkeywords"},
			Authors: []model.Contributor{{FirstName: "First" + fieldSep + "Name", LastName: "Last\rName"}},
			Genres:  []model.DBGenre{{Code: "sf" + fieldSep + "bad"}},
		}},
	}
	line := recordLine(rec, Options{Format: Format2X, QuickFix: true, Limits: DefaultLimits()})
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), fieldSep)
	if len(fields) != 15 {
		t.Fatalf("field count = %d fields=%#v line=%q", len(fields), fields, line)
	}
	for idx, field := range fields[:len(fields)-1] {
		if strings.Contains(field, fieldSep) || strings.Contains(field, "\r") {
			t.Fatalf("field %d = %q contains unsanitized layout character", idx, field)
		}
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
