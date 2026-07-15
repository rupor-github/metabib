package mhlinpx

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"metabib/jsonl"
	"metabib/model"
)

func BenchmarkGenerateArchiveEntry(b *testing.B) {
	const recordCount = 10000
	dir := b.TempDir()
	prefix := filepath.Join(dir, "all")
	records := make([]model.DatasetRecord, recordCount)
	for idx := range records {
		entry := strconv.Itoa(idx+1) + ".fb2"
		records[idx] = mhlDatasetRecord("archive-0001", idx, entry, int64(idx+1))
	}
	writeBenchDataset(b, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      recordCount,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives: []model.DatasetArchive{{
			ID:      "archive-0001",
			Name:    "fb2-0000000001-0000010000.zip",
			Entries: recordCount,
		}},
		Ordering: model.DatasetOrdering{
			Mode:       "archive_entry",
			ArchiveKey: "ordinal",
			EntryKey:   "index",
			Direction:  "ascending",
		},
	}, records...)

	opts := Options{
		InputPrefix:     prefix,
		Format:          Format2X,
		SequenceMode:    SequenceAuthor,
		FB2Preference:   PreferComplement,
		QuickFix:        true,
		Limits:          DefaultLimits(),
		CommentTemplate: "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate: "{{ .DumpDate }}\r\n",
	}

	b.ReportAllocs()
	b.ResetTimer()
	idx := 0
	for b.Loop() {
		opts.OutputPrefix = filepath.Join(dir, "out", strconv.Itoa(idx), "flibusta")
		if _, err := Generate(context.Background(), opts); err != nil {
			b.Fatalf("Generate() error = %v", err)
		}
		idx++
	}
}

func writeBenchDataset(b *testing.B, prefix string, dataset model.Dataset, records ...model.DatasetRecord) {
	b.Helper()
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		b.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.WriteValue(dataset); err != nil {
		b.Fatalf("WriteValue(dataset) error = %v", err)
	}
	for _, rec := range records {
		if err := w.WriteValue(rec); err != nil {
			b.Fatalf("WriteValue(record) error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatalf("Close() error = %v", err)
	}
}
