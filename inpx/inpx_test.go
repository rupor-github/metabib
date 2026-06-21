package inpx

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

	out, err := Generate(context.Background(), Options{InputPrefix: prefix, OutputPrefix: filepath.Join(dir, "flibusta"), Format: Format2X, SequenceMode: SequenceAuthor, FB2Preference: PreferComplement, QuickFix: true, Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if filepath.Base(out) != "flibusta_20260603.inpx" {
		t.Fatalf("output = %q", out)
	}
	zr, err := zip.OpenReader(out)
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
