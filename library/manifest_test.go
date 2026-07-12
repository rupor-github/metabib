package library

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

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
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "records.manifest.zst*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("manifest temp matches after close = %#v err=%v, want none", matches, err)
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

func TestForEachManifestRecordRejectsUnexpectedSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	w, err := newManifestWriter(path)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
	}
	if err := w.Close(map[string]any{"schema": "bad", "records": 0}); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := ForEachManifestRecord(context.Background(), path, nil); err == nil {
		t.Fatal("ForEachManifestRecord() error = nil, want schema error")
	}
}

func TestManifestWritersUseUniqueTempPaths(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	first, err := newManifestWriter(path)
	if err != nil {
		t.Fatalf("newManifestWriter(first) error = %v", err)
	}
	second, err := newManifestWriter(path)
	if err != nil {
		first.Abort()
		t.Fatalf("newManifestWriter(second) error = %v", err)
	}
	defer first.Abort()
	defer second.Abort()
	if first.tmpRecords == second.tmpRecords {
		t.Fatalf("manifest records temp paths collided: %q", first.tmpRecords)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "records.manifest.zst.records-*.tmp")); err != nil || len(matches) != 2 {
		t.Fatalf("manifest records temp matches = %#v err=%v, want 2", matches, err)
	}
}

func TestForEachManifestRecordRejectsRecordCountMismatch(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	w, err := newManifestWriter(path)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: recordSchema, ID: model.RecordID{BookID: 10}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(archiveManifestHeader{Schema: archiveManifestSchema, Records: 2}); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	count, err := ForEachManifestRecord(context.Background(), path, nil)
	if err == nil {
		t.Fatal("ForEachManifestRecord() error = nil, want record count mismatch")
	}
	if count != 1 {
		t.Fatalf("count = %d, want decoded count 1", count)
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

	plan, ready, err := PlanArchives(ctx, cfg, []string{archive}, false, nil, false)
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

	_, reports, err := ValidateArchiveManifests(ctx, cfg, []string{archive}, true, nil, false)
	if err != nil {
		t.Fatalf("ValidateArchiveManifests() error = %v", err)
	}
	if len(reports) != 1 || !reports[0].Ready(false) || !reports[0].ChecksumVerified {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestArchiveManifestReadyLogsRequireVerbose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	archive := filepath.Join(dir, "books.zip")
	writeZip(t, archive, map[string]string{"1.fb2": `<FictionBook/>`})
	cfg := manifestTestConfig()
	plan, _, err := PlanArchives(ctx, cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	writer, err := newManifestWriter(plan[0].ManifestPath)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
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

	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	_, ready, err := PlanArchives(ctx, cfg, []string{archive}, false, logger, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if !ready {
		t.Fatal("PlanArchives() ready = false")
	}
	_, _, err = ValidateArchiveManifests(ctx, cfg, []string{archive}, false, logger, false)
	if err != nil {
		t.Fatalf("ValidateArchiveManifests() error = %v", err)
	}
	if logs.FilterMessage("Archive manifest selected").Len() != 0 || logs.FilterMessage("Manifest ready").Len() != 0 {
		t.Fatalf("non-verbose archive manifest logs = %#v", logs.AllUntimed())
	}

	core, logs = observer.New(zap.DebugLevel)
	logger = zap.New(core)
	_, ready, err = PlanArchives(ctx, cfg, []string{archive}, false, logger, true)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if !ready {
		t.Fatal("PlanArchives() ready = false")
	}
	_, _, err = ValidateArchiveManifests(ctx, cfg, []string{archive}, false, logger, true)
	if err != nil {
		t.Fatalf("ValidateArchiveManifests() error = %v", err)
	}
	if logs.FilterMessage("Archive manifest selected").Len() != 1 || logs.FilterMessage("Manifest ready").Len() != 1 {
		t.Fatalf("verbose archive manifest logs = %#v", logs.AllUntimed())
	}
}

func TestArchiveManifestIgnoresAbsoluteSourcePath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	sourceArchive := filepath.Join(sourceDir, "books.zip")
	targetArchive := filepath.Join(targetDir, "books.zip")
	writeZip(t, sourceArchive, map[string]string{"1.fb2": `<FictionBook/>`})
	writeZip(t, targetArchive, map[string]string{"1.fb2": `<FictionBook/>`})
	archiveTime := time.Now().Add(-time.Hour).Round(0)
	if err := os.Chtimes(sourceArchive, archiveTime, archiveTime); err != nil {
		t.Fatalf("Chtimes(source) error = %v", err)
	}
	if err := os.Chtimes(targetArchive, archiveTime, archiveTime); err != nil {
		t.Fatalf("Chtimes(target) error = %v", err)
	}
	cfg := manifestTestConfig()

	plan, _, err := PlanArchives(ctx, cfg, []string{sourceArchive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	targetManifest := archiveManifestPath(cfg, targetArchive)
	writer, err := newManifestWriter(targetManifest)
	if err != nil {
		t.Fatalf("newManifestWriter() error = %v", err)
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
	if err := os.Chtimes(targetManifest, future, future); err != nil {
		t.Fatalf("Chtimes(manifest) error = %v", err)
	}

	_, reports, err := ValidateArchiveManifests(ctx, cfg, []string{targetArchive}, false, nil, false)
	if err != nil {
		t.Fatalf("ValidateArchiveManifests() error = %v", err)
	}
	if len(reports) != 1 || !reports[0].Ready(false) {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestArchiveManifestPathsQualifyOnlyCollisionsInArchiveDir(t *testing.T) {
	t.Parallel()

	cfg := manifestTestConfig()
	cfg.Processing.Manifests.ArchiveDir = t.TempDir()
	leftArchive := filepath.Join(t.TempDir(), "books.zip")
	rightArchive := filepath.Join(t.TempDir(), "books.zip")
	otherArchive := filepath.Join(t.TempDir(), "other.zip")
	paths := archiveManifestPaths(cfg, []string{leftArchive, rightArchive, otherArchive}, nil)
	if paths[leftArchive] == paths[rightArchive] {
		t.Fatalf("archiveManifestPaths() collision for same basename: %q", paths[leftArchive])
	}
	if filepath.Base(paths[leftArchive]) != "books.manifest.zst" {
		t.Fatalf("first manifest path = %q, want unchanged basename", paths[leftArchive])
	}
	if filepath.Base(paths[otherArchive]) != "other.manifest.zst" {
		t.Fatalf("non-colliding manifest path = %q, want unchanged basename", paths[otherArchive])
	}
	if filepath.Base(paths[rightArchive]) == "books.manifest.zst" {
		t.Fatalf("second manifest path = %q, want source-qualified basename", paths[rightArchive])
	}
	if filepath.Dir(paths[leftArchive]) != cfg.Processing.Manifests.ArchiveDir || filepath.Dir(paths[rightArchive]) != cfg.Processing.Manifests.ArchiveDir {
		t.Fatalf("manifest paths are not in archive_dir: %#v", paths)
	}
}

func TestSourcePathLabelIncludesVolume(t *testing.T) {
	t.Parallel()

	label := sourcePathLabel(`C:\data\flibusta\daily\books.zip`)
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(label, "C") {
			t.Fatalf("sourcePathLabel() = %q, want drive prefix", label)
		}
		return
	}
	if label == "" {
		t.Fatal("sourcePathLabel() returned empty label")
	}
}

func TestSourcePathLabelPreservesComponentBoundaries(t *testing.T) {
	t.Parallel()

	if label := sourcePathLabel(filepath.Join(string(os.PathSeparator), "data", "flibusta-daily", "books.zip")); label != "data--flibusta-daily" {
		t.Fatalf("sourcePathLabel(hyphen component) = %q", label)
	}
	if label := sourcePathLabel(filepath.Join(string(os.PathSeparator), "data", "flibusta", "daily", "books.zip")); label != "data--flibusta--daily" {
		t.Fatalf("sourcePathLabel(split component) = %q", label)
	}
}

func TestSourcePathLabelWindowsLikePaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		`\\server\share\flibusta-daily\books.zip`,
		`\\?\C:\flibusta-daily\books.zip`,
		`\\.\C:\flibusta-daily\books.zip`,
	} {
		if label := sourcePathLabel(path); label == "" || strings.Contains(label, string(os.PathSeparator)) || strings.Contains(label, "\\") {
			t.Fatalf("sourcePathLabel(%q) = %q, want safe non-empty label", path, label)
		}
	}
}

func TestDatabaseManifestIgnoresAbsoluteSourcePaths(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manifestDir := t.TempDir()
	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	sourceDump := writeManifestTestFile(t, sourceDir, "a.sql", "dump")
	targetDump := writeManifestTestFile(t, targetDir, "a.sql", "dump")
	dumpTime := time.Now().Add(-time.Hour).Round(0)
	if err := os.Chtimes(sourceDump, dumpTime, dumpTime); err != nil {
		t.Fatalf("Chtimes(source) error = %v", err)
	}
	if err := os.Chtimes(targetDump, dumpTime, dumpTime); err != nil {
		t.Fatalf("Chtimes(target) error = %v", err)
	}
	cfg := manifestTestConfig()
	cfg.Processing.Manifests.DatabaseDir = manifestDir
	sourceDumps := []db.DumpFile{{Path: sourceDump, Name: "a.sql", DumpDate: "2026-06-20", DumpCompleted: "2026-06-20T02:19:33"}}
	targetDumps := []db.DumpFile{{Path: targetDump, Name: "a.sql", DumpDate: "2026-06-20", DumpCompleted: "2026-06-20T02:19:33"}}

	decision, err := PlanDatabaseManifest(ctx, cfg, sourceDir, sourceDumps, "2026-06-20", false, nil)
	if err != nil {
		t.Fatalf("PlanDatabaseManifest() error = %v", err)
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
		t.Fatalf("Chtimes(manifest) error = %v", err)
	}

	_, report, err := ValidateDatabaseManifest(ctx, cfg, targetDir, targetDumps, "2026-06-20", true, nil)
	if err != nil {
		t.Fatalf("ValidateDatabaseManifest() error = %v", err)
	}
	if !report.Ready(false) || !report.ChecksumVerified {
		t.Fatalf("report = %#v", report)
	}
}

func TestArchiveManifestToleratesSubMicrosecondMTimeDrift(t *testing.T) {
	t.Parallel()

	cfg := manifestTestConfig()
	stored := time.Date(2016, 6, 19, 20, 21, 39, 798633814, time.FixedZone("test", -4*60*60))
	header := archiveManifestHeader{
		Source:     ArchiveManifestSource{Path: "/source/books.zip", Modified: stored.Format(time.RFC3339Nano)},
		Processing: processingManifest(cfg),
	}
	if !archiveManifestLightMatches(header, cfg, "/target/books.zip", stored.Add(-14*time.Nanosecond), true) {
		t.Fatal("archiveManifestLightMatches() = false for sub-microsecond mtime drift")
	}
	if archiveManifestLightMatches(header, cfg, "/target/books.zip", stored.Add(-2*time.Microsecond), true) {
		t.Fatal("archiveManifestLightMatches() = true for multi-microsecond mtime drift")
	}
}

func TestDatabaseManifestToleratesSubMicrosecondMTimeDrift(t *testing.T) {
	t.Parallel()

	cfg := manifestTestConfig()
	stored := time.Date(2026, 7, 12, 8, 0, 13, 374015409, time.FixedZone("test", -4*60*60))
	header := databaseManifestHeader{
		Source: DatabaseManifestSource{
			DumpDate: "2026-07-12",
			Dumps: []DumpManifestSource{{
				Name:          "lib.libavtor.sql",
				DumpDate:      "2026-07-12",
				DumpCompleted: "2026-07-12T02:17:36",
				Modified:      stored.Format(time.RFC3339Nano),
			}},
		},
		Processing: processingManifest(cfg),
	}
	current := []DumpManifestSource{{
		Name:          "lib.libavtor.sql",
		DumpDate:      "2026-07-12",
		DumpCompleted: "2026-07-12T02:17:36",
		Modified:      stored.Add(-9 * time.Nanosecond).Format(time.RFC3339Nano),
	}}
	if !databaseManifestLightMatches(header, cfg, "2026-07-12", current, true) {
		t.Fatal("databaseManifestLightMatches() = false for sub-microsecond mtime drift")
	}
	current[0].Modified = stored.Add(-2 * time.Microsecond).Format(time.RFC3339Nano)
	if databaseManifestLightMatches(header, cfg, "2026-07-12", current, true) {
		t.Fatal("databaseManifestLightMatches() = true for multi-microsecond mtime drift")
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
