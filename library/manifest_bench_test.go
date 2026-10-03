package library

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"metabib/model"
)

// METABIB_BENCH_MANIFEST optionally supplies real records from an existing manifest.
func BenchmarkManifestWriter(b *testing.B) {
	records := manifestBenchmarkRecords(b)
	var uncompressedSize int64
	for _, rec := range records {
		data, err := jsonv2.Marshal(rec)
		if err != nil {
			b.Fatal(err)
		}
		uncompressedSize += int64(len(data) + 1)
	}
	dir := b.TempDir()
	path := filepath.Join(dir, "database.manifest.zst")
	header := databaseManifestHeader{Schema: databaseManifestSchema, Records: int64(len(records))}
	var finalization time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		w, err := newManifestWriter(path)
		if err != nil {
			b.Fatal(err)
		}
		for _, rec := range records {
			if err := w.Write(rec); err != nil {
				_ = w.Abort()
				b.Fatal(err)
			}
		}
		start := time.Now()
		if err := w.Close(header); err != nil {
			_ = w.Abort()
			b.Fatal(err)
		}
		finalization += time.Since(start)
	}
	b.StopTimer()
	b.ReportMetric(float64(finalization.Nanoseconds())/float64(b.N), "finalize-ns/op")
	b.ReportMetric(float64(uncompressedSize), "uncompressed-bytes/op")
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(info.Size()), "compressed-bytes/op")
	index := 0
	count, err := ForEachManifestRecord(context.Background(), path, func(rec model.Record) error {
		if index >= len(records) || rec.ID.BookID != records[index].ID.BookID {
			return fmt.Errorf("manifest record order changed at record %d", index)
		}
		index++
		return nil
	})
	if err != nil || count != int64(len(records)) {
		b.Fatalf("read back manifest: count=%d error=%v", count, err)
	}
}

func manifestBenchmarkRecords(b *testing.B) []model.Record {
	b.Helper()
	const count = 100000
	records := make([]model.Record, 0, count)
	if path := os.Getenv("METABIB_BENCH_MANIFEST"); path != "" {
		stop := errors.New("benchmark sample complete")
		_, err := ForEachManifestRecord(context.Background(), path, func(rec model.Record) error {
			records = append(records, rec)
			if len(records) == count {
				return stop
			}
			return nil
		})
		if err != nil && !errors.Is(err, stop) {
			b.Fatal(err)
		}
		if len(records) == 0 {
			b.Fatal("benchmark manifest has no records")
		}
		return records
	}
	for idx := range count {
		id := int64(idx + 1)
		records = append(records, model.Record{
			Schema: recordSchema,
			ID:     model.RecordID{Library: "flibusta", BookID: id},
			Source: model.RecordSources{Database: model.DatabaseSource{
				Present: true,
				Book:    &model.DBBook{BookID: id, Title: fmt.Sprintf("Book %d", id), FileType: "fb2", Lang: "ru"},
				Authors: []model.Contributor{{ID: id % 1000, FirstName: "Иван", LastName: "Иванов"}},
				Annotations: []model.DBAnnotation{{
					NID: id, Title: "Annotation", Body: strings.Repeat("A book description with metadata and publication details. ", 20),
				}},
			}},
		})
	}
	return records
}
