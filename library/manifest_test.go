package library

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"metabib/config"
	"metabib/db"
	"metabib/model"
)

func TestManifestWriterAndIterator(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	w, err := newManifestWriter(path)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: recordSchema, ID: model.RecordID{BookID: 10}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(archiveManifestHeader{Schema: archiveManifestSchema, Records: 1}); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var ids []int64
	count, err := ForEachManifestRecord(context.Background(), path, func(rec model.Record) error {
		ids = append(ids, rec.ID.BookID)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachManifestRecord() error = %v", err)
	}
	if count != 1 || len(ids) != 1 || ids[0] != 10 {
		t.Fatalf("count=%d ids=%v", count, ids)
	}
	if _, err := readArchiveManifestHeader(path); err != nil {
		t.Fatalf("readArchiveManifestHeader() error = %v", err)
	}
}

func TestPlanDatabaseManifestMissingAndFresh(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	dump := writeManifestTestFile(t, dir, "a.sql", "dump")
	dumps := []db.DumpFile{{Path: dump, Name: "a.sql", DumpDate: "2026-06-20", DumpCompleted: "2026-06-20T02:19:33"}}
	cfg := manifestTestConfig()

	decision, err := PlanDatabaseManifest(ctx, cfg, dir, dumps, "2026-06-20", false, nil)
	if err != nil {
		t.Fatalf("PlanDatabaseManifest(missing) error = %v", err)
	}
	if !decision.Create || decision.Use || decision.ManifestPath == "" || decision.Dumps[0].MD5 == "" {
		t.Fatalf("missing decision = %#v", decision)
	}

	writer, err := newManifestWriter(decision.ManifestPath)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
	}
	if err := writer.Close(databaseManifestHeaderFor(cfg, decision, 0)); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(decision.ManifestPath, future, future); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	decision, err = PlanDatabaseManifest(ctx, cfg, dir, dumps, "2026-06-20", true, nil)
	if err != nil {
		t.Fatalf("PlanDatabaseManifest(fresh) error = %v", err)
	}
	if !decision.Use || decision.Create {
		t.Fatalf("fresh decision = %#v", decision)
	}
}

func TestPlanArchiveManifestMissingAndValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	archive := filepath.Join(dir, "books.zip")
	writeZip(t, archive, map[string]string{"1.fb2": `<FictionBook/>`, "skip.txt": "x"})
	cfg := manifestTestConfig()

	plan, ready, err := PlanArchives(ctx, cfg, []string{archive}, false, nil)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if ready || len(plan) != 1 || !plan[0].Create {
		t.Fatalf("plan=%#v ready=%v", plan, ready)
	}

	writer, err := newManifestWriter(plan[0].ManifestPath)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
	}
	plan[0].ArchiveMD5, err = fileMD5(ctx, archive)
	if err != nil {
		t.Fatalf("fileMD5() error = %v", err)
	}
	if err := writer.Write(model.Record{Schema: recordSchema, ID: model.RecordID{BookID: 1}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	header, err := archiveManifestHeaderFor(cfg, plan[0], 1)
	if err != nil {
		t.Fatalf("archiveManifestHeaderFor() error = %v", err)
	}
	if err := writer.Close(header); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(plan[0].ManifestPath, future, future); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	_, reports, err := ValidateArchiveManifests(ctx, cfg, []string{archive}, true, nil)
	if err != nil {
		t.Fatalf("ValidateArchiveManifests() error = %v", err)
	}
	if len(reports) != 1 || !reports[0].Ready(false) || !reports[0].ChecksumVerified {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestExpandArchivesFromDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "b.zip"), map[string]string{"1.fb2": ""})
	writeZip(t, filepath.Join(dir, "a.zip"), map[string]string{"2.fb2": ""})
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ignored: %v", err)
	}
	archives, err := expandArchives([]string{dir})
	if err != nil {
		t.Fatalf("expandArchives() error = %v", err)
	}
	if len(archives) != 2 || filepath.Base(archives[0]) != "a.zip" || filepath.Base(archives[1]) != "b.zip" {
		t.Fatalf("archives = %#v", archives)
	}
}

func manifestTestConfig() *config.Config {
	return &config.Config{
		Database: config.DatabaseConfig{Name: "lib"},
		Processing: config.ProcessingConfig{
			ParseFB2:           true,
			FB2DescriptionTree: true,
			ArchiveContentMD5:  true,
		},
	}
}

func writeManifestTestFile(t *testing.T, dir string, name string, data string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	return path
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}
