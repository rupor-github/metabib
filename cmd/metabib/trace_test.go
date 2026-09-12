package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	jsonstd "encoding/json"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"metabib/jsonl"
	"metabib/model"
)

func TestTraceDatasetFindsManifestRecordsCompactDefault(t *testing.T) {
	archivePath, archiveManifestPath, databaseManifestPath, datasetPrefix := writeTraceFixture(t)
	var out bytes.Buffer
	err := traceDataset(context.Background(), traceOptions{Input: datasetPrefix, BookID: 42}, &out)
	if err != nil {
		t.Fatalf("traceDataset() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Trace",
		"merged: record=1 archive_entry archive-0001 index=1",
		"Steps",
		"archive_manifest   found  " + archiveManifestPath,
		"database_manifest  found  " + databaseManifestPath,
		"archive            found  " + archivePath,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("trace output = %q, missing %q", text, want)
		}
	}
	if strings.Contains(text, `"schema": "metabib.dataset_record/1"`) {
		t.Fatalf("compact trace output includes full merged record: %q", text)
	}
}

func TestTraceDatasetJSONOmitsFullRecordsByDefault(t *testing.T) {
	_, _, _, datasetPrefix := writeTraceFixture(t)
	var out bytes.Buffer
	if err := traceDataset(context.Background(), traceOptions{Input: datasetPrefix, BookID: 42, JSON: true}, &out); err != nil {
		t.Fatalf("traceDataset(json) error = %v", err)
	}
	var result map[string]any
	if err := jsonstd.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal(trace json) error = %v", err)
	}
	if result["schema"] != traceSchemaV1 {
		t.Fatalf("trace schema = %v, want %q", result["schema"], traceSchemaV1)
	}
	if strings.Contains(out.String(), `"merged_record":`) || strings.Contains(out.String(), `"archive_manifest_record":`) {
		t.Fatalf("compact trace JSON includes full records: %q", out.String())
	}
}

func TestTraceDatasetRecordsFull(t *testing.T) {
	_, _, _, datasetPrefix := writeTraceFixture(t)
	var out bytes.Buffer
	if err := traceDataset(context.Background(), traceOptions{Input: datasetPrefix, BookID: 42, RecordsFull: true}, &out); err != nil {
		t.Fatalf("traceDataset(records full) error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"--- merged dataset record ---", "--- archive manifest record ---", `"schema": "metabib.dataset_record/1"`, `"id": {`, `"book_id": 42`} {
		if !strings.Contains(text, want) {
			t.Fatalf("full trace output = %q, missing %q", text, want)
		}
	}
}

func TestTraceDatasetAmbiguousSelector(t *testing.T) {
	dataset := inspectTestDataset()
	dataset.Records = 2
	left := inspectTestRecord()
	right := inspectTestRecord()
	idx := 0
	right.Record.Locator.Index = &idx
	right.Artifacts[0].Occurrences[0].Index = idx
	prefix := writeInspectDatasetRecords(t, dataset, []model.DatasetRecord{left, right})
	var out bytes.Buffer
	err := traceDataset(context.Background(), traceOptions{Input: prefix, BookID: 42}, &out)
	if err == nil || !strings.Contains(err.Error(), "matched 2 records") {
		t.Fatalf("traceDataset(ambiguous) error = %v, want ambiguous match", err)
	}
}

func TestTraceDatasetDeepZipVerification(t *testing.T) {
	_, _, _, datasetPrefix := writeTraceFixture(t)
	var out bytes.Buffer
	if err := traceDataset(context.Background(), traceOptions{Input: datasetPrefix, BookID: 42, Deep: true}, &out); err != nil {
		t.Fatalf("traceDataset(deep) error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "deep_archive       found") || !strings.Contains(text, "archive entry verified") {
		t.Fatalf("deep trace output = %q, want verified archive entry", text)
	}
}

func TestTraceDatasetDirectoryOverrides(t *testing.T) {
	archiveDir := t.TempDir()
	archiveManifestDir := t.TempDir()
	databaseManifestDir := t.TempDir()
	datasetDir := t.TempDir()
	archivePath := filepath.Join(archiveDir, "books.zip")
	writeTraceZip(t, archivePath)
	contentMD5 := traceTestMD5("hello")
	dataset := inspectTestDataset()
	dataset.Database.DumpDirHint = "/volume4/backup/library/flibusta_20260910_080033"
	dataset.Archives[0].Name = "books.zip"
	dataset.Archives[0].PathHint = "/volume4/backup/library/flibusta/books.zip"
	record := inspectTestRecord()
	record.Artifacts[0].Occurrences[0].UncompressedSize = uint64(len("hello"))
	record.Artifacts[0].Checksums = append(record.Artifacts[0].Checksums, model.ArtifactChecksum{
		Observation: "archive",
		Algorithm:   "md5",
		Scope:       "content",
		Origin:      "calculated",
		Value:       contentMD5,
	})
	datasetPrefix := writeTraceDatasetRecordsInDir(t, datasetDir, dataset, []model.DatasetRecord{record})
	archiveManifestPath := filepath.Join(archiveManifestDir, "books"+traceManifestExt)
	writeTraceManifest(t, archiveManifestPath, map[string]any{
		"schema":  traceArchiveManifestV2,
		"source":  map[string]any{"path": "/volume4/backup/library/flibusta/books.zip"},
		"scope":   "fb2",
		"created": "2026-07-15T00:00:00Z",
		"records": 1,
	}, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			BookID:    42,
			FileName:  "42",
			Extension: "fb2",
			Archive: &model.ArchiveInfo{
				Path:             "/volume4/backup/library/flibusta/books.zip",
				Entry:            "42.fb2",
				Index:            1,
				UncompressedSize: uint64(len("hello")),
				ContentMD5:       contentMD5,
			},
		},
		Source: model.RecordSources{FB2: model.FB2Source{Present: true}},
	})
	databaseManifestPath := filepath.Join(databaseManifestDir, "database"+traceManifestExt)
	dumpPath := filepath.Join(databaseManifestDir, "lib.libbook.sql")
	if err := os.WriteFile(dumpPath, []byte("dump"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", dumpPath, err)
	}
	writeTraceManifest(t, databaseManifestPath, map[string]any{
		"schema":  traceDatabaseManifestV2,
		"source":  map[string]any{"dump_dir": "/volume4/backup/library/flibusta_20260910_080033", "dumps": []any{map[string]any{"name": "lib.libbook.sql", "path": "/volume4/backup/library/flibusta_20260910_080033/lib.libbook.sql"}}},
		"created": "2026-07-15T00:00:00Z",
		"records": 1,
	}, model.Record{
		Schema: "metabib.record/1",
		ID:     model.RecordID{Library: "flibusta", BookID: 42, FileName: "42", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 42, Title: "Title"}}},
	})
	var out bytes.Buffer
	err := traceDataset(context.Background(), traceOptions{
		Input:               datasetPrefix,
		BookID:              42,
		ArchiveDir:          archiveDir,
		ArchiveManifestDir:  archiveManifestDir,
		DatabaseManifestDir: databaseManifestDir,
		Deep:                true,
	}, &out)
	if err != nil {
		t.Fatalf("traceDataset(directory overrides) error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"archive            found  " + archivePath,
		"archive_manifest   found  " + archiveManifestPath,
		"database_manifest  found  " + databaseManifestPath,
		"database_dump      found  " + databaseManifestDir,
		"database_dump      found  " + dumpPath,
		"deep_archive       found",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("trace output = %q, missing %q", text, want)
		}
	}
}

func writeTraceFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "books.zip")
	writeTraceZip(t, archivePath)
	contentMD5 := traceTestMD5("hello")
	dataset := inspectTestDataset()
	dataset.Database.DumpDirHint = dir
	dataset.Archives[0].PathHint = archivePath
	record := inspectTestRecord()
	record.Artifacts[0].Occurrences[0].UncompressedSize = uint64(len("hello"))
	record.Artifacts[0].Checksums = append(record.Artifacts[0].Checksums, model.ArtifactChecksum{
		Observation: "archive",
		Algorithm:   "md5",
		Scope:       "content",
		Origin:      "calculated",
		Value:       contentMD5,
	})
	datasetPrefix := writeInspectDatasetRecords(t, dataset, []model.DatasetRecord{record})
	archiveManifestPath := filepath.Join(dir, "books"+traceManifestExt)
	writeTraceManifest(t, archiveManifestPath, map[string]any{
		"schema":  traceArchiveManifestV2,
		"source":  map[string]any{"path": archivePath, "modified": "2026-07-15T00:00:00Z", "md5": "container-md5"},
		"scope":   "fb2",
		"created": "2026-07-15T00:00:00Z",
		"records": 1,
	}, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			BookID:    42,
			FileName:  "42",
			Extension: "fb2",
			Archive: &model.ArchiveInfo{
				Path:             archivePath,
				Entry:            "42.fb2",
				Index:            1,
				UncompressedSize: uint64(len("hello")),
				ContentMD5:       contentMD5,
			},
		},
		Source: model.RecordSources{FB2: model.FB2Source{Present: true}},
	})
	databaseManifestPath := filepath.Join(dir, "database"+traceManifestExt)
	writeTraceManifest(t, databaseManifestPath, map[string]any{
		"schema":  traceDatabaseManifestV2,
		"source":  map[string]any{"dump_dir": dir, "dump_date": "20260715", "database_format": "flibusta-current", "dumps": []any{}},
		"created": "2026-07-15T00:00:00Z",
		"records": 1,
	}, model.Record{
		Schema: "metabib.record/1",
		ID:     model.RecordID{Library: "flibusta", BookID: 42, FileName: "42", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: 42, Title: "Title", FileSize: int64(len("hello")), FileType: "fb2"},
		}},
	})
	return archivePath, archiveManifestPath, databaseManifestPath, datasetPrefix
}

func writeTraceDatasetRecordsInDir(t *testing.T, dir string, dataset model.Dataset, records []model.DatasetRecord) string {
	t.Helper()
	prefix := filepath.Join(dir, "combined")
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.WriteValue(dataset); err != nil {
		_ = w.Abort()
		t.Fatalf("WriteValue(dataset) error = %v", err)
	}
	for _, record := range records {
		if err := w.WriteValue(record); err != nil {
			_ = w.Abort()
			t.Fatalf("WriteValue(record) error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return prefix
}

func writeTraceManifest(t *testing.T, path string, header any, records ...model.Record) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", path, err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		f.Close()
		t.Fatalf("NewWriter(%q) error = %v", path, err)
	}
	if err := jsonv2.MarshalWrite(zw, header); err != nil {
		zw.Close()
		f.Close()
		t.Fatalf("MarshalWrite(header) error = %v", err)
	}
	if _, err := zw.Write([]byte{'\n'}); err != nil {
		zw.Close()
		f.Close()
		t.Fatalf("Write(header newline) error = %v", err)
	}
	for _, record := range records {
		if err := jsonv2.MarshalWrite(zw, record); err != nil {
			zw.Close()
			f.Close()
			t.Fatalf("MarshalWrite(record) error = %v", err)
		}
		if _, err := zw.Write([]byte{'\n'}); err != nil {
			zw.Close()
			f.Close()
			t.Fatalf("Write(record newline) error = %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		t.Fatalf("Close(zstd) error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%q) error = %v", path, err)
	}
}

func writeTraceZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", path, err)
	}
	zw := zip.NewWriter(f)
	for _, file := range []struct {
		name    string
		content string
	}{
		{name: "dummy.txt", content: "dummy"},
		{name: "42.fb2", content: "hello"},
	} {
		entry, err := zw.Create(file.name)
		if err != nil {
			zw.Close()
			f.Close()
			t.Fatalf("Create zip entry %q error = %v", file.name, err)
		}
		if _, err := entry.Write([]byte(file.content)); err != nil {
			zw.Close()
			f.Close()
			t.Fatalf("Write zip entry %q error = %v", file.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		t.Fatalf("Close zip error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%q) error = %v", path, err)
	}
}

func traceTestMD5(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
