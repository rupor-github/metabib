package inpxutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/jsonl"
	"metabib/model"
)

func TestDiscoverInputPartsUsesMetadataPartsAndWarnsUnlisted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	listed := filepath.Join(dir, "all.0000000001-0000000001.jsonl")
	extra := filepath.Join(dir, "all.0000000002-0000000002.jsonl")
	for _, path := range []string{listed, extra} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	core, logs := observer.New(zap.WarnLevel)
	parts, err := DiscoverInputParts(
		prefix,
		filepath.Join(dir, "all.meta.json.zst"),
		model.MergeMetadata{Parts: []string{filepath.Base(listed)}},
		zap.New(core),
	)
	if err != nil {
		t.Fatalf("DiscoverInputParts() error = %v", err)
	}
	if len(parts) != 1 || parts[0] != listed {
		t.Fatalf("parts = %#v, want %q", parts, listed)
	}
	if logs.FilterMessage("Ignoring JSONL input part not listed in merge metadata").Len() != 1 {
		t.Fatalf("logs = %#v, want one unlisted-part warning", logs.All())
	}
}

func TestDiscoverInputPartsRequiresMetadataParts(t *testing.T) {
	t.Parallel()

	_, err := DiscoverInputParts("all", "all.meta.json.zst", model.MergeMetadata{}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not list JSONL parts") {
		t.Fatalf("DiscoverInputParts() error = %v, want missing parts error", err)
	}
}

func TestCleanse(t *testing.T) {
	t.Parallel()

	if got := Cleanse("plain text"); got != "plain text" {
		t.Fatalf("Cleanse(no-op) = %q", got)
	}
	got := Cleanse("a" + FieldSep + "b\rc\r\nd\ne\u00a0f")
	if strings.Contains(got, FieldSep) || strings.Contains(got, "\r") || strings.Contains(got, "\n") || strings.Contains(got, "\u00a0") {
		t.Fatalf("Cleanse() = %q, still contains layout characters", got)
	}
	if got != "a b c de f" {
		t.Fatalf("Cleanse() = %q, want %q", got, "a b c de f")
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
		testRecord(archivePath, "1.fb2", 0, 1),
		testRecord(archivePath, "2.fb2", 0, 2),
		testRecord(archivePath, "3.fb2", 2, 3),
		testRecord(archivePath, "4.fb2", 2, 4),
	} {
		if err := w.Write(rec); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	part := filepath.Join(dir, "all.0000000001-0000000004.jsonl")
	archives := map[string]*ArchiveRows{
		archivePath: {Meta: model.MergeArchiveMetadata{Path: archivePath, Name: filepath.Base(archivePath)}, Records: make(map[int]model.Record)},
	}
	core, logs := observer.New(zap.WarnLevel)

	loaded, err := ReadRecords(context.Background(), []string{part}, archives, zap.New(core))
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
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

func testRecord(archivePath string, entry string, index int, bookID int64) model.Record {
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
