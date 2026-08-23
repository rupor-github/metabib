package library

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rardecode "github.com/nwaples/rardecode/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/config"
	"metabib/fb2"
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
	if !isUSRFB2Entry("Book.fb2.zip") {
		t.Fatal("isUSRFB2Entry(Book.fb2.zip) = false")
	}
	if isUSRFB2Entry("Book.pdf.zip") {
		t.Fatal("isUSRFB2Entry(Book.pdf.zip) = true")
	}
	if !isBackup("x.ORG") {
		t.Fatal("isBackup() = false")
	}
	for _, name := range []string{"__MACOSX/book.fbd", "._book.fbd", ".hidden/book.fbd", "dir/.DS_Store"} {
		if !isIgnoredArchiveEntry(name) {
			t.Fatalf("isIgnoredArchiveEntry(%q) = false", name)
		}
	}
	if isIgnoredArchiveEntry("dir/book.fbd") {
		t.Fatal("isIgnoredArchiveEntry(dir/book.fbd) = true")
	}
	id, ext := entryIdentity("dir/123.fb2")
	if id != 123 || ext != "fb2" {
		t.Fatalf("entryIdentity() = %d, %q", id, ext)
	}

	for _, tt := range []struct {
		name         string
		bookID       int64
		fileName     string
		ext          string
		containerExt string
	}{
		{name: "dir/592481.djvu.zip", bookID: 592481, fileName: "592481", ext: "djvu", containerExt: "zip"},
		{name: "dir/592481.djvu.tar.gz", bookID: 592481, fileName: "592481", ext: "djvu", containerExt: "tar.gz"},
		{name: "dir/592481.tgz", bookID: 592481, fileName: "592481", containerExt: "tgz"},
		{name: "dir/Something.7z", fileName: "Something", containerExt: "7z"},
		{name: "dir/592481.djvu.7z.001", bookID: 592481, fileName: "592481", ext: "djvu", containerExt: "7z"},
	} {
		bookID, fileName, ext, containerExt := entryIdentityParts(tt.name)
		if bookID != tt.bookID || fileName != tt.fileName || ext != tt.ext || containerExt != tt.containerExt {
			t.Fatalf("entryIdentityParts(%q) = %d, %q, %q, %q, want %#v", tt.name, bookID, fileName, ext, containerExt, tt)
		}
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

func TestFB2LimitReaderRejectsOversizedEntry(t *testing.T) {
	t.Parallel()

	data, err := io.ReadAll(&fb2LimitReader{reader: strings.NewReader("ab"), remaining: 2})
	if err != nil {
		t.Fatalf("ReadAll(exact limit) error = %v", err)
	}
	if string(data) != "ab" {
		t.Fatalf("ReadAll(exact limit) data = %q, want ab", data)
	}

	data, err = io.ReadAll(&fb2LimitReader{reader: strings.NewReader("abc"), remaining: 2})
	if !errors.Is(err, fb2.ErrLimitExceeded) {
		t.Fatalf("ReadAll() error = %v, want ErrLimitExceeded", err)
	}
	if string(data) != "ab" {
		t.Fatalf("ReadAll() data = %q, want ab", data)
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
	header, err := readArchiveManifestHeader(plan[0].ManifestPath)
	if err != nil {
		t.Fatalf("readArchiveManifestHeader() error = %v", err)
	}
	if header.Schema != archiveManifestSchema || header.Scope != archiveScopeFB2 {
		t.Fatalf("header schema/scope = %q/%q", header.Schema, header.Scope)
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

func TestBuildArchiveManifestsLogsIgnoredNonFB2InFB2Scope(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "books.zip")
	writeZip(t, archive, map[string]string{
		"1.fb2":    `<FictionBook><description><title-info><book-title>A</book-title></title-info></description></FictionBook>`,
		"note.pdf": "book bytes",
	})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	core, logs := observer.New(zap.InfoLevel)

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Scope != archiveScopeFB2 {
		t.Fatalf("plan = %#v, want FB2 scope", plan)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, zap.New(core), false, plan); err != nil {
		t.Fatalf("BuildArchiveManifests() error = %v", err)
	}
	count, err := ForEachManifestRecord(context.Background(), plan[0].ManifestPath, func(rec model.Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachManifestRecord() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want only FB2 record", count)
	}
	entries := logs.FilterMessage("Archive manifest created").All()
	if len(entries) != 1 {
		t.Fatalf("created logs = %#v", logs.All())
	}
	fields := entries[0].ContextMap()
	if fields["fb2_non_fb2_entries_ignored"] != int64(1) || fields["usr_fb2_entries_ignored"] != int64(0) {
		t.Fatalf("created log fields = %#v, want one ignored non-FB2 entry", fields)
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

func TestBuildArchiveManifestsProcessesUSRSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	writeZip(t, archive, map[string]string{
		"42.pdf": "book bytes",
		"42.fbd": `<FictionBook><description><title-info><book-title>Sidecar title</book-title></title-info></description></FictionBook>`,
	})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Scope != archiveScopeUSR {
		t.Fatalf("plan = %#v, want USR scope", plan)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, nil, false, plan); err != nil {
		t.Fatalf("BuildArchiveManifests() error = %v", err)
	}
	header, err := readArchiveManifestHeader(plan[0].ManifestPath)
	if err != nil {
		t.Fatalf("readArchiveManifestHeader() error = %v", err)
	}
	if header.Schema != archiveManifestSchema || header.Scope != archiveScopeUSR {
		t.Fatalf("header schema/scope = %q/%q", header.Schema, header.Scope)
	}
	var recs []model.Record
	count, err := ForEachManifestRecord(context.Background(), plan[0].ManifestPath, func(rec model.Record) error {
		recs = append(recs, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachManifestRecord() error = %v", err)
	}
	if count != 1 || len(recs) != 1 {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
	rec := recs[0]
	if rec.ID.BookID != 42 || rec.ID.FileName != "42" || rec.ID.Extension != "pdf" || rec.Source.FB2.Present {
		t.Fatalf("record identity/source = %#v", rec)
	}
	if len(rec.Source.Sidecars) != 1 || !rec.Source.Sidecars[0].Present || rec.Source.Sidecars[0].Description.TitleInfo.Title != "Sidecar title" {
		t.Fatalf("sidecars = %#v", rec.Source.Sidecars)
	}
}

func TestBuildArchiveManifestsIgnoresNestedFB2ContainerInUSRScope(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	writeZip(t, archive, map[string]string{
		"42.fb2.zip": "nested fb2 container bytes",
		"43.pdf":     "book bytes",
	})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	core, logs := observer.New(zap.InfoLevel)

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Scope != archiveScopeUSR {
		t.Fatalf("plan = %#v, want USR scope", plan)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, zap.New(core), false, plan); err != nil {
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
	if count != 1 || len(recs) != 1 || recs[0].ID.FileName != "43" || recs[0].ID.Extension != "pdf" {
		t.Fatalf("count=%d recs=%#v, want only 43.pdf", count, recs)
	}
	entries := logs.FilterMessage("Archive manifest created").All()
	if len(entries) != 1 {
		t.Fatalf("created logs = %#v", logs.All())
	}
	if fields := entries[0].ContextMap(); fields["usr_fb2_entries_ignored"] != int64(1) {
		t.Fatalf("created log fields = %#v, want one ignored FB2 entry", fields)
	}
}

func TestBuildArchiveManifestsSanitizesInvalidUTF8EntryExtension(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	invalidExt := string([]byte{0xff})
	entryName := "42." + invalidExt
	writeZip(t, archive, map[string]string{entryName: "book bytes"})
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
	if count != 1 || len(recs) != 1 {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
	if recs[0].ID.Extension != validUTF8(invalidExt) || recs[0].ID.Archive.Entry != validUTF8(entryName) {
		t.Fatalf("record identity = %#v", recs[0].ID)
	}
}

func TestBuildArchiveManifestsUsesUnicodePathExtraName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	nested := writeZipBytes(t, map[string]string{
		"book.fb2": `<FictionBook><description><title-info><book-title>Nested FB2</book-title></title-info></description></FictionBook>`,
	})
	writeZipRawNameEntry(t, archive, []byte("568397.\xa0\xa82.zip"), "568397.аи2.zip", nested)
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = true
	core, logs := observer.New(zap.WarnLevel)

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, zap.New(core), false, plan); err != nil {
		t.Fatalf("BuildArchiveManifests() error = %v", err)
	}
	entries := logs.FilterMessage("FB2 archive entry ignored in USR scope").All()
	if len(entries) != 1 {
		t.Fatalf("ignored FB2 logs = %#v", logs.All())
	}
	if got := entries[0].ContextMap()["entry"]; got != "568397.аи2.zip" {
		t.Fatalf("entry log field = %#v, want decoded Unicode Path extra name", got)
	}
}

func TestBuildArchiveManifestsProcessesNestedTarGZSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	nested := writeTarGZBytes(t, map[string]string{
		"book.pdf": "book bytes",
		"book.fbd": `<FictionBook><description><title-info><book-title>Nested sidecar</book-title></title-info></description></FictionBook>`,
	})
	writeZip(t, archive, map[string]string{"42.tar.gz": string(nested)})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = true
	cfg.Processing.NestedArchiveInspection.MaxCompressedSizeMiB = 1

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
	if count != 1 || len(recs) != 1 {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
	rec := recs[0]
	if rec.ID.BookID != 42 || rec.ID.FileName != "42" || rec.ID.Extension != "pdf" || rec.ID.ContainerExtension != "tar.gz" {
		t.Fatalf("record identity = %#v", rec.ID)
	}
	if len(rec.Source.Sidecars) != 1 || rec.Source.Sidecars[0].Entry != "42.tar.gz" {
		t.Fatalf("sidecars = %#v", rec.Source.Sidecars)
	}
	if got := rec.Source.Sidecars[0].Description.TitleInfo.Title; got != "Nested sidecar" {
		t.Fatalf("nested sidecar title = %q", got)
	}
}

func TestBuildArchiveManifestsAutodetectsNestedZipWithWrongExtension(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	nested := writeZipBytes(t, map[string]string{
		"book.pdf": "book bytes",
		"book.fbd": `<FictionBook><description><title-info><book-title>Zip sidecar</book-title></title-info></description></FictionBook>`,
	})
	writeZip(t, archive, map[string]string{"42.rar": string(nested)})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = true
	cfg.Processing.NestedArchiveInspection.MaxCompressedSizeMiB = 1

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
	if count != 1 || len(recs) != 1 {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
	rec := recs[0]
	if rec.ID.BookID != 42 || rec.ID.Extension != "pdf" || rec.ID.ContainerExtension != "rar" {
		t.Fatalf("record identity = %#v", rec.ID)
	}
	if len(rec.Source.Sidecars) != 1 || rec.Source.Sidecars[0].Description.TitleInfo.Title != "Zip sidecar" {
		t.Fatalf("sidecars = %#v", rec.Source.Sidecars)
	}
}

func TestBuildArchiveManifestsIgnoresNestedMacOSMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	nested := writeZipBytes(t, map[string]string{
		"book.pdf":          "book bytes",
		"book.fbd":          `<FictionBook><description><title-info><book-title>Valid sidecar</book-title></title-info></description></FictionBook>`,
		"__MACOSX/book.fbd": "not XML",
	})
	writeZip(t, archive, map[string]string{"42.zip": string(nested)})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = true
	cfg.Processing.NestedArchiveInspection.MaxCompressedSizeMiB = 1

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
	if count != 1 || len(recs) != 1 {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
	if len(recs[0].Source.Sidecars) != 1 || recs[0].Source.Sidecars[0].Description.TitleInfo.Title != "Valid sidecar" {
		t.Fatalf("sidecars = %#v", recs[0].Source.Sidecars)
	}
}

func TestBuildArchiveManifestsIgnoresInspectedNestedFB2InUSRScope(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	nested := writeZipBytes(t, map[string]string{
		"book.fb2": `<FictionBook><description><title-info><book-title>Nested FB2</book-title></title-info></description></FictionBook>`,
	})
	writeZip(t, archive, map[string]string{"42.zip": string(nested)})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = true
	cfg.Processing.NestedArchiveInspection.MaxCompressedSizeMiB = 1
	core, logs := observer.New(zap.InfoLevel)

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Scope != archiveScopeUSR {
		t.Fatalf("plan = %#v, want USR scope", plan)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, zap.New(core), false, plan); err != nil {
		t.Fatalf("BuildArchiveManifests() error = %v", err)
	}
	count, err := ForEachManifestRecord(context.Background(), plan[0].ManifestPath, func(rec model.Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachManifestRecord() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("count=%d, want no records", count)
	}
	entries := logs.FilterMessage("Archive manifest created").All()
	if len(entries) != 1 {
		t.Fatalf("created logs = %#v", logs.All())
	}
	if fields := entries[0].ContextMap(); fields["usr_fb2_entries_ignored"] != int64(1) {
		t.Fatalf("created log fields = %#v, want one ignored FB2 entry", fields)
	}
}

func TestBuildArchiveManifestsKeepsOpaqueNestedArchiveWhenInspectionDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	nested := writeZipBytes(t, map[string]string{
		"book.fb2": `<FictionBook><description><title-info><book-title>Nested FB2</book-title></title-info></description></FictionBook>`,
	})
	writeZip(t, archive, map[string]string{"42.zip": string(nested)})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = false
	core, logs := observer.New(zap.InfoLevel)

	plan, _, err := PlanArchives(context.Background(), cfg, []string{archive}, false, nil, false)
	if err != nil {
		t.Fatalf("PlanArchives() error = %v", err)
	}
	if err := BuildArchiveManifests(context.Background(), cfg, zap.New(core), false, plan); err != nil {
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
	if count != 1 || len(recs) != 1 || recs[0].ID.FileName != "42" || recs[0].ID.Extension != "" || recs[0].ID.ContainerExtension != "zip" {
		t.Fatalf("count=%d recs=%#v, want opaque zip record", count, recs)
	}
	entries := logs.FilterMessage("Archive manifest created").All()
	if len(entries) != 1 {
		t.Fatalf("created logs = %#v", logs.All())
	}
	if fields := entries[0].ContextMap(); fields["usr_fb2_entries_ignored"] != int64(0) {
		t.Fatalf("created log fields = %#v, want no ignored FB2 entries without inspection", fields)
	}
}

func TestBuildArchiveManifestsRecordsNestedInspectionIssue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	writeZip(t, archive, map[string]string{"42.zip": "not a zip"})
	cfg := manifestTestConfig()
	cfg.Processing.ArchiveWorkers = 1
	cfg.Processing.ArchiveBatchSize = 1
	cfg.Processing.ArchiveReadBuffer = 1024
	cfg.Processing.NestedArchiveInspection.Enabled = true
	cfg.Processing.NestedArchiveInspection.MaxCompressedSizeMiB = 1

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
	if count != 1 || len(recs) != 1 {
		t.Fatalf("count=%d recs=%#v", count, recs)
	}
	if len(recs[0].Issues) != 1 {
		t.Fatalf("issues = %#v", recs[0].Issues)
	}
	issue := recs[0].Issues[0]
	if issue.Stage != nestedInspectionStage || issue.Code != "nested_archive_invalid" || issue.Path != "42.zip" {
		t.Fatalf("issue = %#v", issue)
	}
	if issue.Details["declared_format"] != "zip" || issue.Details["detected_format"] != "zip" || issue.Details["behavior"] != nestedInspectionBehavior {
		t.Fatalf("issue details = %#v", issue.Details)
	}
	wantMD5 := md5.Sum([]byte("not a zip"))
	if got := recs[0].ID.Archive.ContentMD5; got != hex.EncodeToString(wantMD5[:]) {
		t.Fatalf("content MD5 = %q, want nested inspection read hash", got)
	}
}

func TestNestedArchiveInspectionClassifiesMultiVolumeRAR(t *testing.T) {
	t.Parallel()

	if got := nestedArchiveInspectionErrorCode(rardecode.ErrMultiVolume, false, true); got != "nested_archive_multivolume" {
		t.Fatalf("nestedArchiveInspectionErrorCode(grouped ErrMultiVolume) = %q", got)
	}
	if got := nestedArchiveInspectionErrorCode(errors.New("unexpected EOF"), false, true); got != "nested_archive_multivolume" {
		t.Fatalf("nestedArchiveInspectionErrorCode(grouped generic error) = %q", got)
	}
	if got := nestedArchiveInspectionErrorCode(rardecode.ErrMultiVolume, false, false); got != "nested_archive_invalid" {
		t.Fatalf("nestedArchiveInspectionErrorCode(incomplete ErrMultiVolume) = %q", got)
	}
	if got := nestedArchiveInspectionErrorCode(rardecode.ErrInvalidFileBlock, true, false); got != "nested_archive_multivolume" {
		t.Fatalf("nestedArchiveInspectionErrorCode(continuation ErrInvalidFileBlock) = %q", got)
	}
	if got := nestedArchiveInspectionErrorCode(rardecode.ErrInvalidFileBlock, false, false); got == "nested_archive_multivolume" {
		t.Fatalf("nestedArchiveInspectionErrorCode(ErrInvalidFileBlock) = %q, want non-multivolume", got)
	}
}

func TestLikelyRARVolumeContinuation(t *testing.T) {
	t.Parallel()

	names := map[string]bool{
		"book.rar":   true,
		"book1.rar":  true,
		"part01.rar": true,
		"part02.rar": true,
	}
	for _, name := range []string{"book1.rar", "part02.rar"} {
		if !likelyRARVolumeContinuation(name, names) {
			t.Fatalf("likelyRARVolumeContinuation(%q) = false", name)
		}
	}
	for _, name := range []string{"book.rar", "lonely1.rar", "note.txt"} {
		if likelyRARVolumeContinuation(name, names) {
			t.Fatalf("likelyRARVolumeContinuation(%q) = true", name)
		}
	}
}

func TestArchiveEntriesCoalescesProvidedNestedRARVolumeNames(t *testing.T) {
	t.Parallel()

	const (
		gita5Part1    = "Gita_Ayengar_Yoga_Ayengar_chast_5_partiya_1_.rar"
		gita5Part2    = "Gita_Ayengar_Yoga_Ayengar_chast_5_partiya_2_.rar"
		gita6Part1    = "Gita_Ayengar_Yoga_Ayengar_chast_6_partiya_1_.rar"
		gita6Part2    = "Gita_Ayengar_Yoga_Ayengar_chast_6_partiya_2_.rar"
		gita6Part3    = "Gita_Ayengar_Yoga_Ayengar_chast_6_partiya_3_.rar"
		gita7Part1    = "Gita_Ayengar_Yoga_Ayengar_chast_7_partiya_1_.rar"
		gita7Part2    = "Gita_Ayengar_Yoga_Ayengar_chast_7_partiya_2_.rar"
		gitaOrphan    = "Gita_Ayengar_Yoga_Ayengar_chast_partiya_4_.rar"
		valdemarRoot  = "Valdemar Lysyak_Zacharovannye ostrova.rar"
		valdemarPart1 = "Valdemar Lysyak_Zacharovannye ostrova1.rar"
	)
	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	writeZip(t, archive, map[string]string{
		gita5Part1:    "volume root",
		gita5Part2:    "volume continuation",
		gita6Part1:    "volume root",
		gita6Part2:    "volume continuation",
		gita6Part3:    "volume continuation",
		gita7Part1:    "volume root",
		gita7Part2:    "volume continuation",
		gitaOrphan:    "orphan continuation",
		valdemarRoot:  "volume root",
		valdemarPart1: "volume continuation",
	})
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()

	entries, _, err := archiveEntries(context.Background(), archive, zr.File, archiveScopeUSR, nil, false)
	if err != nil {
		t.Fatalf("archiveEntries() error = %v", err)
	}
	byName := make(map[string]archiveEntry, len(entries))
	for _, entry := range entries {
		byName[entry.File.Name] = entry
	}
	if len(byName) != 5 {
		t.Fatalf("entries = %#v, want four grouped roots plus orphan", byName)
	}
	for _, skipped := range []string{gita5Part2, gita6Part2, gita6Part3, gita7Part2, valdemarPart1} {
		if byName[skipped].File != nil {
			t.Fatalf("entry %q was not skipped: %#v", skipped, byName[skipped])
		}
	}
	for root, want := range map[string][]string{
		gita5Part1:   {gita5Part2},
		gita6Part1:   {gita6Part2, gita6Part3},
		gita7Part1:   {gita7Part2},
		valdemarRoot: {valdemarPart1},
	} {
		if got := byName[root].NestedVolumeEntries; !slices.Equal(got, want) {
			t.Fatalf("%q volume entries = %#v, want %#v", root, got, want)
		}
	}
	if got := byName[gitaOrphan].NestedVolumeEntries; len(got) != 0 {
		t.Fatalf("orphan volume entries = %#v, want none", got)
	}
}

func TestArchiveEntriesCoalescesNestedSevenZipVolumes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	writeZip(t, archive, map[string]string{
		"book1965.part1.7z.001": "volume root",
		"book1965.part1.7z.002": "volume continuation",
		"book1965.part1.7z.003": "volume continuation",
		"orphan.7z.003":         "orphan continuation",
		"other.pdf":             "book bytes",
	})
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()

	entries, _, err := archiveEntries(context.Background(), archive, zr.File, archiveScopeUSR, nil, false)
	if err != nil {
		t.Fatalf("archiveEntries() error = %v", err)
	}
	byName := make(map[string]archiveEntry, len(entries))
	for _, entry := range entries {
		byName[entry.File.Name] = entry
	}
	if len(byName) != 3 ||
		byName["book1965.part1.7z.002"].File != nil ||
		byName["book1965.part1.7z.003"].File != nil ||
		byName["orphan.7z.003"].File == nil ||
		byName["other.pdf"].File == nil {
		t.Fatalf("entries = %#v, want one 7z root plus orphan and other.pdf", byName)
	}
	root := byName["book1965.part1.7z.001"]
	if root.ContainerExt != "7z" || root.NestedVolumeFormat != "7z" {
		t.Fatalf("root format = %q/%q, want 7z", root.ContainerExt, root.NestedVolumeFormat)
	}
	want := []string{"book1965.part1.7z.002", "book1965.part1.7z.003"}
	if got := root.NestedVolumeEntries; !slices.Equal(got, want) {
		t.Fatalf("7z volume entries = %#v, want %#v", got, want)
	}
	orphan := byName["orphan.7z.003"]
	if orphan.ContainerExt != "7z" || len(orphan.NestedVolumeEntries) != 0 {
		t.Fatalf("orphan = %#v, want ungrouped 7z entry", orphan)
	}
}

func TestArchiveEntriesCoalescesNestedRARVolumes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "usr.zip")
	writeZip(t, archive, map[string]string{
		"book.rar":   "volume root",
		"book1.rar":  "volume continuation",
		"part01.rar": "numbered volume root",
		"part02.rar": "numbered volume continuation",
		"other.pdf":  "book bytes",
	})
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()

	entries, _, err := archiveEntries(context.Background(), archive, zr.File, archiveScopeUSR, nil, false)
	if err != nil {
		t.Fatalf("archiveEntries() error = %v", err)
	}
	byName := make(map[string]archiveEntry, len(entries))
	for _, entry := range entries {
		byName[entry.File.Name] = entry
	}
	if len(byName) != 3 ||
		byName["book1.rar"].File != nil ||
		byName["part02.rar"].File != nil ||
		byName["other.pdf"].File == nil {
		t.Fatalf("entries = %#v, want one record per RAR volume set plus other.pdf", byName)
	}
	if got := byName["book.rar"].NestedVolumeEntries; len(got) != 1 || got[0] != "book1.rar" {
		t.Fatalf("book.rar volume entries = %#v", got)
	}
	if got := byName["part01.rar"].NestedVolumeEntries; len(got) != 1 || got[0] != "part02.rar" {
		t.Fatalf("part01.rar volume entries = %#v", got)
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
	records, _, err := processArchiveBatch(
		context.Background(),
		repo,
		&config.Config{Database: config.DatabaseConfig{Name: "lib"}},
		archive,
		[]archiveEntry{{Index: 0, File: zr.File[0], Ext: "fb2"}},
	)
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

func writeTarGZBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, contents := range files {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents))}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			t.Fatalf("write tar entry: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func writeZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, contents := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := w.Write([]byte(contents)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

func writeZipRawNameEntry(t *testing.T, path string, rawName []byte, utf8Name string, data []byte) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:    string(rawName),
		Method:  zip.Deflate,
		NonUTF8: true,
		Extra:   unicodePathExtraField(rawName, utf8Name),
	})
	if err != nil {
		t.Fatalf("create raw-name zip entry: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write raw-name zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}

func unicodePathExtraField(rawName []byte, utf8Name string) []byte {
	name := []byte(utf8Name)
	out := make([]byte, 4+1+4+len(name))
	binary.LittleEndian.PutUint16(out[0:2], 0x7075)
	binary.LittleEndian.PutUint16(out[2:4], uint16(1+4+len(name)))
	out[4] = 1
	binary.LittleEndian.PutUint32(out[5:9], crc32.ChecksumIEEE(rawName))
	copy(out[9:], name)
	return out
}
