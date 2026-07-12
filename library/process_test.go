package library

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"

	"metabib/config"
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

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
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

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
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
	slices.Sort(ids)
	if count != 2 || len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("count=%d ids=%v, want [1 2]", count, ids)
	}
}

func TestProcessArchiveBatchLooksUpNonNumericFB2Filename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "books.zip")
	writeZip(t, archive, map[string]string{"Some.Book.fb2": `<FictionBook/>`})
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	repo := &fakeArchiveRepo{
		idsByFilename: map[string]int64{"Some.Book.fb2": 42},
		sourcesByID: map[int64]model.DatabaseSource{
			42: {Present: true, Book: &model.DBBook{BookID: 42, Title: "DB title"}},
		},
	}
	records, _, err := processArchiveBatch(context.Background(), repo, &config.Config{Database: config.DatabaseConfig{Name: "lib"}}, archive, []archiveEntry{{Index: 0, File: zr.File[0], Ext: "fb2"}})
	if err != nil {
		t.Fatalf("processArchiveBatch() error = %v", err)
	}
	if len(records) != 1 || records[0].ID.BookID != 42 || !records[0].Source.Database.Present {
		t.Fatalf("records = %#v, want DB fallback source", records)
	}
	if repo.filenameLookups != 1 {
		t.Fatalf("filenameLookups = %d, want 1", repo.filenameLookups)
	}
}

func TestShouldLookupArchiveFilename(t *testing.T) {
	t.Parallel()

	repo := &fakeArchiveRepo{}
	if !shouldLookupArchiveFilename(repo, model.DatabaseSource{}, 0, "fb2") {
		t.Fatal("shouldLookupArchiveFilename(non-numeric fb2) = false")
	}
	if shouldLookupArchiveFilename(repo, model.DatabaseSource{}, 42, "fb2") {
		t.Fatal("shouldLookupArchiveFilename(numeric fb2) = true")
	}
	if !shouldLookupArchiveFilename(repo, model.DatabaseSource{}, 42, "txt") {
		t.Fatal("shouldLookupArchiveFilename(non-fb2) = false")
	}
	if shouldLookupArchiveFilename(repo, model.DatabaseSource{Present: true}, 0, "fb2") {
		t.Fatal("shouldLookupArchiveFilename(present DB source) = true")
	}
	if shouldLookupArchiveFilename(nil, model.DatabaseSource{}, 0, "fb2") {
		t.Fatal("shouldLookupArchiveFilename(nil repo) = true")
	}
}

type fakeArchiveRepo struct {
	idsByFilename   map[string]int64
	sourcesByID     map[int64]model.DatabaseSource
	filenameLookups int
}

func (r *fakeArchiveRepo) BookSourcesByIDs(context.Context, []int64) (map[int64]model.DatabaseSource, error) {
	return nil, errors.New("BookSourcesByIDs should not be called")
}

func (r *fakeArchiveRepo) BookIDByFilename(_ context.Context, filename string) (int64, error) {
	r.filenameLookups++
	return r.idsByFilename[filename], nil
}

func (r *fakeArchiveRepo) BookByID(_ context.Context, id int64) (model.DatabaseSource, error) {
	return r.sourcesByID[id], nil
}

type stringsReader string

func (r stringsReader) Read(p []byte) (int, error) {
	if len(r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, string(r))
	return n, nil
}
