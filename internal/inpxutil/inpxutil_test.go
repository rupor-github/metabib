package inpxutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDiscoverDatasetInputUsesExactPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "all.jsonl.zst")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := DiscoverDatasetInput(path)
	if err != nil {
		t.Fatalf("DiscoverDatasetInput() error = %v", err)
	}
	if got != path {
		t.Fatalf("DiscoverDatasetInput() = %q, want %q", got, path)
	}
}

func TestDiscoverDatasetInputUsesPrefixCandidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	path := prefix + ".jsonl.gz"
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := DiscoverDatasetInput(prefix)
	if err != nil {
		t.Fatalf("DiscoverDatasetInput() error = %v", err)
	}
	if got != path {
		t.Fatalf("DiscoverDatasetInput() = %q, want %q", got, path)
	}
}

func TestDiscoverDatasetInputRejectsMissingAndAmbiguousInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	if _, err := DiscoverDatasetInput(prefix); err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("DiscoverDatasetInput(missing) error = %v, want found 0", err)
	}
	for _, path := range []string{prefix + ".jsonl", prefix + ".jsonl.zst"} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	if _, err := DiscoverDatasetInput(prefix); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("DiscoverDatasetInput(ambiguous) error = %v, want found 2", err)
	}
}

func TestDiscoverDatasetInputRejectsMissingExactPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "all.jsonl.zip")
	if _, err := DiscoverDatasetInput(path); err == nil || !strings.Contains(err.Error(), "stat dataset input") {
		t.Fatalf("DiscoverDatasetInput() error = %v, want stat error", err)
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

func TestOutputPathValidatesCompactDumpDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prefix  string
		date    string
		want    string
		wantErr bool
	}{
		{
			name:   "empty date",
			prefix: "out/books",
			want:   "out/books.inpx",
		},
		{
			name:   "valid compact date",
			prefix: "out/books",
			date:   "20260603",
			want:   "out/books_20260603.inpx",
		},
		{
			name:   "already suffixed",
			prefix: "out/books_20260603",
			date:   "20260603",
			want:   "out/books_20260603.inpx",
		},
		{
			name:    "iso date",
			prefix:  "out/books",
			date:    "2026-06-03",
			wantErr: true,
		},
		{
			name:    "short date",
			prefix:  "out/books",
			date:    "2026063",
			wantErr: true,
		},
		{
			name:    "long date",
			prefix:  "out/books",
			date:    "202606030",
			wantErr: true,
		},
		{
			name:    "non-digit",
			prefix:  "out/books",
			date:    "2026060x",
			wantErr: true,
		},
		{
			name:    "path separator",
			prefix:  "out/books",
			date:    "202606/3",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := OutputPath(tt.prefix, model.MergeMetadata{Database: model.MergeDatabaseMetadata{DumpDate: tt.date}})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("OutputPath() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("OutputPath() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("OutputPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureDumpDateUsesCurrentDateAndWarns(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	meta := model.MergeMetadata{}
	before := time.Now().UTC().Format("20060102")
	EnsureDumpDate(&meta, zap.New(core))
	after := time.Now().UTC().Format("20060102")

	if meta.Database.DumpDate != before && meta.Database.DumpDate != after {
		t.Fatalf("DumpDate = %q, want current date %q or %q", meta.Database.DumpDate, before, after)
	}
	if meta.Database.DumpDateISO == "" {
		t.Fatal("DumpDateISO is empty")
	}
	if logs.FilterMessage("INPX input metadata has empty dump date; using current date").Len() != 1 {
		t.Fatalf("logs = %#v, want one empty-date warning", logs.All())
	}
}

func TestEnsureDumpDateKeepsExistingDate(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	meta := model.MergeMetadata{Database: model.MergeDatabaseMetadata{DumpDate: "20260603", DumpDateISO: "2026-06-03"}}
	EnsureDumpDate(&meta, zap.New(core))

	if meta.Database.DumpDate != "20260603" || meta.Database.DumpDateISO != "2026-06-03" {
		t.Fatalf("metadata changed: %#v", meta.Database)
	}
	if logs.Len() != 0 {
		t.Fatalf("logs = %#v, want no warnings", logs.All())
	}
}

func TestReadRecordsWarnsAndKeepsFirstDuplicateArchiveIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	archivePath := filepath.Join(dir, "books.zip")
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
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
	part := filepath.Join(dir, "all.jsonl")
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

func TestReadRecordsIgnoresArchiveLessRecordsWhenArchivesExist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	archivePath := filepath.Join(dir, "books.zip")
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	for _, rec := range []model.Record{
		testRecord(archivePath, "1.fb2", 0, 1),
		{Schema: "metabib.record/1", ID: model.RecordID{BookID: 2}},
	} {
		if err := w.Write(rec); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	part := filepath.Join(dir, "all.jsonl")
	archives := map[string]*ArchiveRows{
		archivePath: {Meta: model.MergeArchiveMetadata{Path: archivePath, Name: filepath.Base(archivePath)}, Records: make(map[int]model.Record)},
	}

	loaded, err := ReadRecords(context.Background(), []string{part}, archives, nil)
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}
	if loaded != 2 {
		t.Fatalf("loaded = %d, want 2", loaded)
	}
	if archives[archivePath].Records[0].ID.BookID != 1 {
		t.Fatalf("archive record = %#v", archives[archivePath].Records[0])
	}
	if _, ok := archives[OnlineArchivePath]; ok {
		t.Fatalf("created online archive for mixed input: %#v", archives[OnlineArchivePath])
	}
}

func TestReadRecordsBucketsArchiveLessRecordsForDatabaseOnlyInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 1}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	part := filepath.Join(dir, "all.jsonl")
	archives := map[string]*ArchiveRows{OnlineArchivePath: newOnlineArchive()}

	loaded, err := ReadRecords(context.Background(), []string{part}, archives, nil)
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}
	if loaded != 1 {
		t.Fatalf("loaded = %d, want 1", loaded)
	}
	online := archives[OnlineArchivePath]
	if online.Meta.Name != OnlineArchiveName || online.Meta.Entries != 1 || online.Records[0].ID.BookID != 1 {
		t.Fatalf("online archive = %#v", online)
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
