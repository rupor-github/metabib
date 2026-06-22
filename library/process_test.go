package library

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"metabib/model"
)

func TestArchiveEntryHelpers(t *testing.T) {
	t.Parallel()

	if !isFB2Entry("Book.FB2") {
		t.Fatal("isFB2Entry() = false")
	}
	if isFB2Entry("Book.txt") {
		t.Fatal("isFB2Entry(txt) = true")
	}
	if !isBackup("x.ORG") {
		t.Fatal("isBackup() = false")
	}
	id, ext := entryIdentity("dir/123.fb2")
	if id != 123 || ext != "fb2" {
		t.Fatalf("entryIdentity() = %d, %q", id, ext)
	}
}

func TestBufferedContextReaderCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := bufferedContextReader(ctx, stringsReader("data"), 4)
	_, err := r.Read(make([]byte, 4))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v, want context.Canceled", err)
	}
}

func TestArchiveEntryMD5(t *testing.T) {
	t.Parallel()

	archive := filepath.Join(t.TempDir(), "one.zip")
	writeZip(t, archive, map[string]string{"1.fb2": "hello"})
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	got, err := archiveEntryMD5(context.Background(), zr.File[0], 1024)
	if err != nil {
		t.Fatalf("archiveEntryMD5() error = %v", err)
	}
	if got != "5d41402abc4b2a76b9719d911017c592" {
		t.Fatalf("archiveEntryMD5() = %q", got)
	}
}

func TestBuildArchiveManifestsProcessesZip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "books.zip")
	writeZip(t, archive, map[string]string{
		"1.fb2": `<FictionBook><description><title-info><book-title>A</book-title></title-info></description></FictionBook>`,
	})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, nil, false, plan); err != nil {
		t.Fatalf("BuildArchiveManifests() error = %v", err)
	}
	var recs []model.Record
	count, err := ForEachManifestRecord(context.Background(), plan[0].ManifestPath, func(rec model.Record) error {
		recs = append(recs, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachManifestRecord() error = %v", err)
	}
	if count != 1 || len(recs) != 1 || !recs[0].Source.FB2.Present {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
}

func TestBuildArchiveManifestsKeepsBatchesAfterSkippedEntries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "books.zip")
	writeZip(t, archive, map[string]string{
		"000.txt": "skip",
		"001.txt": "skip",
		"1.fb2":   `<FictionBook><description><title-info><book-title>A</book-title></title-info></description></FictionBook>`,
		"2.fb2":   `<FictionBook><description><title-info><book-title>B</book-title></title-info></description></FictionBook>`,
	})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, nil, false, plan); err != nil {
		t.Fatalf("BuildArchiveManifests() error = %v", err)
	}
	var ids []int64
	count, err := ForEachManifestRecord(context.Background(), plan[0].ManifestPath, func(rec model.Record) error {
		ids = append(ids, rec.ID.BookID)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachManifestRecord() error = %v", err)
	}
	if count != 2 || len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("count=%d ids=%v, want [1 2]", count, ids)
	}
}

type stringsReader string

func (r stringsReader) Read(p []byte) (int, error) {
	if len(r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, string(r))
	return n, nil
}
