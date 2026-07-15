package jsonl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metabib/model"
)

func TestDatasetValuesHeaderOnly(t *testing.T) {
	t.Parallel()

	path := writeDatasetStream(
		t,
		CompressionNone,
		model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 0},
	)
	dataset, records, err := readDatasetValues(context.Background(), path)
	if err != nil {
		t.Fatalf("DatasetValues() error = %v", err)
	}
	if dataset.Schema != model.DatasetSchemaV1 || records != 0 {
		t.Fatalf("dataset=%#v records=%d", dataset, records)
	}
}

func TestDatasetValuesValidatesRecordCount(t *testing.T) {
	t.Parallel()

	path := writeDatasetStream(t,
		CompressionNone,
		model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 2},
		model.DatasetRecord{Schema: model.DatasetRecordSchemaV1},
	)
	_, records, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "record count mismatch") || records != 1 {
		t.Fatalf("DatasetValues() records=%d error=%v, want count mismatch after one record", records, err)
	}
}

func TestDatasetValuesRejectsTrailingData(t *testing.T) {
	t.Parallel()

	path := writeDatasetStream(t,
		CompressionNone,
		model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 0},
		model.DatasetRecord{Schema: model.DatasetRecordSchemaV1},
	)
	_, _, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "contains data after declared 0 records") {
		t.Fatalf("DatasetValues() error=%v, want trailing data error", err)
	}
}

func TestDatasetValuesRejectsMissingHeader(t *testing.T) {
	t.Parallel()

	path := writeDatasetStream(t, CompressionNone, model.DatasetRecord{Schema: model.DatasetRecordSchemaV1})
	_, _, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "has schema") {
		t.Fatalf("DatasetValues() error=%v, want schema error", err)
	}
}

func TestDatasetValuesRejectsLegacyV1Record(t *testing.T) {
	t.Parallel()

	path := writeDatasetStream(t, CompressionNone, map[string]any{"schema": "metabib.record/1"})
	_, _, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), model.DatasetSchemaV1) || !strings.Contains(err.Error(), "metabib.record/1") {
		t.Fatalf("DatasetValues() error=%v, want v1 record rejection", err)
	}
}

func TestDatasetValuesRejectsDuplicateHeader(t *testing.T) {
	t.Parallel()

	header := model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 1}
	path := writeDatasetStream(t, CompressionNone, header, header)
	_, _, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "record 1 has schema") {
		t.Fatalf("DatasetValues() error=%v, want duplicate header schema error", err)
	}
}

func TestDatasetValuesRejectsInvalidArchiveHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		archives []model.DatasetArchive
		want     string
	}{
		{
			name:     "empty id",
			archives: []model.DatasetArchive{{Ordinal: 0}},
			want:     "empty ID",
		},
		{
			name: "duplicate id",
			archives: []model.DatasetArchive{
				{ID: "archive-0001", Ordinal: 0},
				{ID: "archive-0001", Ordinal: 1},
			},
			want: "duplicate archive ID",
		},
		{
			name:     "non contiguous ordinal",
			archives: []model.DatasetArchive{{ID: "archive-0001", Ordinal: 1}},
			want:     "has ordinal 1, want 0",
		},
		{
			name: "array order mismatch",
			archives: []model.DatasetArchive{
				{ID: "archive-0002", Ordinal: 1},
				{ID: "archive-0001", Ordinal: 0},
			},
			want: "has ordinal 1, want 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dataset := model.Dataset{
				Schema:       model.DatasetSchemaV1,
				RecordSchema: model.DatasetRecordSchemaV1,
				Records:      0,
				Archives:     tt.archives,
			}
			path := writeDatasetStream(t, CompressionNone, dataset)
			_, _, err := readDatasetValues(context.Background(), path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DatasetValues() error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestDatasetValuesRejectsInvalidOrderingHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dataset model.Dataset
		want    string
	}{
		{
			name: "unsupported mode",
			dataset: model.Dataset{
				Schema:       model.DatasetSchemaV1,
				RecordSchema: model.DatasetRecordSchemaV1,
				Ordering:     model.DatasetOrdering{Mode: "unknown", Direction: "ascending"},
			},
			want: "unsupported ordering mode",
		},
		{
			name: "unsupported direction",
			dataset: model.Dataset{
				Schema:       model.DatasetSchemaV1,
				RecordSchema: model.DatasetRecordSchemaV1,
				Ordering:     model.DatasetOrdering{Mode: "database_book_id", Source: "database", Direction: "descending"},
			},
			want: "ordering direction",
		},
		{
			name: "archive ordering without archives",
			dataset: model.Dataset{
				Schema:       model.DatasetSchemaV1,
				RecordSchema: model.DatasetRecordSchemaV1,
				Ordering:     model.DatasetOrdering{Mode: "archive_entry", ArchiveKey: "ordinal", EntryKey: "index", Direction: "ascending"},
			},
			want: "archive_entry ordering without archives",
		},
		{
			name: "archive ordering wrong key",
			dataset: model.Dataset{
				Schema:       model.DatasetSchemaV1,
				RecordSchema: model.DatasetRecordSchemaV1,
				Archives:     []model.DatasetArchive{{ID: "archive-0001", Ordinal: 0}},
				Ordering:     model.DatasetOrdering{Mode: "archive_entry", ArchiveKey: "name", EntryKey: "index", Direction: "ascending"},
			},
			want: "archive key",
		},
		{
			name: "database ordering wrong source",
			dataset: model.Dataset{
				Schema:       model.DatasetSchemaV1,
				RecordSchema: model.DatasetRecordSchemaV1,
				Ordering:     model.DatasetOrdering{Mode: "database_book_id", Source: "catalog", Direction: "ascending"},
			},
			want: "database_book_id source",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeDatasetStream(t, CompressionNone, tt.dataset)
			_, _, err := readDatasetValues(context.Background(), path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DatasetValues() error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestDatasetValuesValidatesArchiveOrder(t *testing.T) {
	t.Parallel()

	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      4,
		Archives: []model.DatasetArchive{
			{ID: "archive-0001", Ordinal: 0, Name: "z.zip"},
			{ID: "archive-0002", Ordinal: 1, Name: "a.zip"},
		},
		Ordering: model.DatasetOrdering{Mode: "archive_entry", ArchiveKey: "ordinal", EntryKey: "index", Direction: "ascending"},
	}
	path := writeDatasetStream(t,
		CompressionNone,
		dataset,
		datasetArchiveRecord("archive-0001", 0),
		datasetArchiveRecord("archive-0001", 1),
		datasetArchiveRecord("archive-0001", 1),
		datasetArchiveRecord("archive-0002", 0),
	)
	_, records, err := readDatasetValues(context.Background(), path)
	if err != nil {
		t.Fatalf("DatasetValues() error = %v", err)
	}
	if records != 4 {
		t.Fatalf("records = %d, want 4", records)
	}
}

func TestDatasetValuesRejectsOutOfArchiveOrder(t *testing.T) {
	t.Parallel()

	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      2,
		Archives: []model.DatasetArchive{
			{ID: "archive-0001", Ordinal: 0, Name: "z.zip"},
			{ID: "archive-0002", Ordinal: 1, Name: "a.zip"},
		},
		Ordering: model.DatasetOrdering{Mode: "archive_entry", ArchiveKey: "ordinal", EntryKey: "index", Direction: "ascending"},
	}
	path := writeDatasetStream(t,
		CompressionNone,
		dataset,
		datasetArchiveRecord("archive-0002", 0),
		datasetArchiveRecord("archive-0001", 0),
	)
	_, records, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "out of archive order") || records != 1 {
		t.Fatalf("DatasetValues() records=%d error=%v, want archive order error after one record", records, err)
	}
}

func TestDatasetValuesRejectsOutOfDatabaseBookIDOrder(t *testing.T) {
	t.Parallel()

	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      2,
		Ordering:     model.DatasetOrdering{Mode: "database_book_id", Source: "database", Direction: "ascending"},
	}
	path := writeDatasetStream(t,
		CompressionNone,
		dataset,
		datasetDatabaseRecord(20),
		datasetDatabaseRecord(10),
	)
	_, records, err := readDatasetValues(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "out of database book ID order") || records != 1 {
		t.Fatalf("DatasetValues() records=%d error=%v, want database order error after one record", records, err)
	}
}

func TestDatasetValuesCompressedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		compression Compression
	}{
		{name: "none", compression: CompressionNone},
		{name: "gzip", compression: CompressionGzip},
		{name: "zstd", compression: CompressionZstd},
		{name: "zip", compression: CompressionZip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeDatasetStream(t, tt.compression,
				model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 1},
				model.DatasetRecord{Schema: model.DatasetRecordSchemaV1},
			)
			_, records, err := readDatasetValues(context.Background(), path)
			if err != nil {
				t.Fatalf("DatasetValues() error = %v", err)
			}
			if records != 1 {
				t.Fatalf("records=%d, want 1", records)
			}
		})
	}
}

func readDatasetValues(ctx context.Context, path string) (model.Dataset, int64, error) {
	var dataset model.Dataset
	var records int64
	for value, err := range DatasetValues(ctx, path) {
		if err != nil {
			return dataset, records, err
		}
		if value.Header {
			dataset = value.Dataset
			continue
		}
		records++
	}
	return dataset, records, nil
}

func datasetArchiveRecord(source string, index int) model.DatasetRecord {
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Locator: model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index},
		},
	}
}

func datasetDatabaseRecord(bookID int64) model.DatasetRecord {
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: &bookID},
		},
	}
}

func writeDatasetStream(t *testing.T, compression Compression, values ...any) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "all")
	w, err := CreateCompressed(base, compression)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	for _, value := range values {
		if err := w.WriteValue(value); err != nil {
			_ = w.Abort()
			t.Fatalf("WriteValue() error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(base), "all.jsonl*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want one", matches)
	}
	if _, err := os.Stat(matches[0]); err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	return matches[0]
}
