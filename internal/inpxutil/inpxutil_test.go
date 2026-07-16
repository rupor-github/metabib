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

func TestLoadDatasetInputInitializesArchiveRowsFromHeader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      3,
		Archives: []model.DatasetArchive{{
			ID:       "archive-0001",
			Name:     "books.zip",
			PathHint: "/archives/books.zip",
			Entries:  10,
		}},
	}
	if err := writeDatasetInput(prefix, dataset,
		testDatasetArchiveRecord("archive-0001", 2, "2.fb2"),
		testDatasetArchiveRecord("archive-0001", 2, "duplicate.fb2"),
		testDatasetArchiveRecord("archive-0001", 4, "4.fb2"),
	); err != nil {
		t.Fatalf("writeDatasetInput() error = %v", err)
	}
	core, logs := observer.New(zap.WarnLevel)

	loadedDataset, archives, loaded, err := LoadDatasetInput(context.Background(), prefix, zap.New(core))
	if err != nil {
		t.Fatalf("LoadDatasetInput() error = %v", err)
	}
	if loadedDataset.RecordSchema != model.DatasetRecordSchemaV1 || loaded != 3 {
		t.Fatalf("loaded dataset = %#v, records = %d", loadedDataset, loaded)
	}
	archive := archives["archive-0001"]
	if archive == nil || archive.Meta.PathHint != "/archives/books.zip" || archive.Meta.Entries != 10 {
		t.Fatalf("archive rows = %#v", archives)
	}
	if len(archive.Records) != 2 || archive.Records[2].Artifacts[0].Name != "2.fb2" || archive.Records[4].Artifacts[0].Name != "4.fb2" {
		t.Fatalf("records = %#v", archive.Records)
	}
	if logs.FilterMessage("Duplicate archive index in INPX dataset input; keeping first record").Len() != 1 {
		t.Fatalf("logs = %#v, want one duplicate warning", logs.All())
	}
}

func TestDatasetArchiveListUsesHeaderOrdinalOrder(t *testing.T) {
	t.Parallel()

	archives := map[string]*DatasetArchiveRows{
		"archive-0001": {Meta: model.DatasetArchive{ID: "archive-0001", Ordinal: 0, Name: "z.zip"}},
		"archive-0002": {Meta: model.DatasetArchive{ID: "archive-0002", Ordinal: 1, Name: "a.zip"}},
		"archive-0003": {Meta: model.DatasetArchive{ID: "archive-0003", Ordinal: 1, Name: "b.zip"}},
	}

	got := DatasetArchiveList(archives)
	want := []string{"archive-0001", "archive-0002", "archive-0003"}
	if len(got) != len(want) {
		t.Fatalf("DatasetArchiveList() length = %d, want %d", len(got), len(want))
	}
	for idx, archive := range got {
		if archive.Meta.ID != want[idx] {
			t.Fatalf("DatasetArchiveList()[%d] = %q, want %q", idx, archive.Meta.ID, want[idx])
		}
	}
}

func TestLoadDatasetInputBucketsDatabaseOnlyRecords(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      1,
		Database:     &model.DatasetDatabase{ID: "database"},
	}
	rec := model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "database_book", Source: "database"},
		},
		Observations: []model.Observation{{ID: "db", Source: "database", Kind: "database_book", Status: "present"}},
	}
	if err := writeDatasetInput(prefix, dataset, rec); err != nil {
		t.Fatalf("writeDatasetInput() error = %v", err)
	}

	_, archives, loaded, err := LoadDatasetInput(context.Background(), prefix, nil)
	if err != nil {
		t.Fatalf("LoadDatasetInput() error = %v", err)
	}
	if loaded != 1 {
		t.Fatalf("loaded = %d, want 1", loaded)
	}
	online := archives[OnlineArchivePath]
	if online == nil || online.Meta.Name != OnlineArchiveName || online.Meta.Entries != 1 || online.Records[0].Record.Locator.Kind != "database_book" {
		t.Fatalf("online archive = %#v", online)
	}
}

func TestLoadDatasetInputRejectsUnknownArchiveSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      1,
		Archives:     []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip"}},
	}
	if err := writeDatasetInput(prefix, dataset, testDatasetArchiveRecord("missing", 1, "1.fb2")); err != nil {
		t.Fatalf("writeDatasetInput() error = %v", err)
	}

	_, _, _, err := LoadDatasetInput(context.Background(), prefix, nil)
	if err == nil || !strings.Contains(err.Error(), "undeclared archive source") {
		t.Fatalf("LoadDatasetInput() error = %v, want undeclared archive source", err)
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

func TestCleanseAuthorComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "trailing colon", value: "Ливадный:", want: "Ливадный"},
		{name: "url and email", value: "(http://marsexxx.com, marsexxx@ya.ru)", want: "(http：//marsexxx.com， marsexxx@ya.ru)"},
		{name: "date time", value: "Ср, 13 окт 2010 11:41", want: "Ср， 13 окт 2010 11：41"},
		{name: "minimal", value: ":", want: ""},
		{name: "corrupt", value: "�����:������", want: ""},
		{name: "layout", value: " Last,\u00a0Jr: \r\n First" + FieldSep + "  Middle ", want: "Last， Jr： First Middle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CleanseAuthorComponent(tt.value); got != tt.want {
				t.Fatalf("CleanseAuthorComponent() = %q, want %q", got, tt.want)
			}
		})
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

			got, err := OutputPath(tt.prefix, Metadata{DumpDate: tt.date})
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

func TestDatasetMetadataNormalizesISODumpDate(t *testing.T) {
	t.Parallel()

	meta := DatasetMetadata(model.Dataset{
		Library:  "flibusta",
		Database: &model.DatasetDatabase{DumpDate: "2026-07-13"},
	})
	if meta.Library != "flibusta" || meta.DumpDate != "20260713" || meta.DumpDateISO != "2026-07-13" {
		t.Fatalf("DatasetMetadata() = %#v", meta)
	}
	path, err := OutputPath("all_mhl", meta)
	if err != nil {
		t.Fatalf("OutputPath() error = %v", err)
	}
	if path != "all_mhl_20260713.inpx" {
		t.Fatalf("OutputPath() = %q", path)
	}
}

func TestEnsureDumpDateUsesCurrentDateAndWarns(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	meta := Metadata{}
	before := time.Now().UTC().Format("20060102")
	EnsureDumpDate(&meta, zap.New(core))
	after := time.Now().UTC().Format("20060102")

	if meta.DumpDate != before && meta.DumpDate != after {
		t.Fatalf("DumpDate = %q, want current date %q or %q", meta.DumpDate, before, after)
	}
	if meta.DumpDateISO == "" {
		t.Fatal("DumpDateISO is empty")
	}
	if logs.FilterMessage("INPX input metadata has empty dump date; using current date").Len() != 1 {
		t.Fatalf("logs = %#v, want one empty-date warning", logs.All())
	}
}

func TestEnsureDumpDateKeepsExistingDate(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	meta := Metadata{DumpDate: "20260603", DumpDateISO: "2026-06-03"}
	EnsureDumpDate(&meta, zap.New(core))

	if meta.DumpDate != "20260603" || meta.DumpDateISO != "2026-06-03" {
		t.Fatalf("metadata changed: %#v", meta)
	}
	if logs.Len() != 0 {
		t.Fatalf("logs = %#v, want no warnings", logs.All())
	}
}

func writeDatasetInput(prefix string, dataset model.Dataset, records ...model.DatasetRecord) error {
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		return err
	}
	if err := w.WriteValue(dataset); err != nil {
		w.Abort()
		return err
	}
	for _, rec := range records {
		if err := w.WriteValue(rec); err != nil {
			w.Abort()
			return err
		}
	}
	return w.Close()
}

func testDatasetArchiveRecord(source string, index int, entry string) model.DatasetRecord {
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index},
		},
		Artifacts: []model.Artifact{{Name: entry}},
		Observations: []model.Observation{{
			ID:     "archive",
			Source: source,
			Kind:   "archive_entry",
			Status: "present",
		}},
	}
}
