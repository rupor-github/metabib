package rollup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestNameToID(t *testing.T) {
	t.Parallel()

	if got := nameToID("123.fb2"); got != 123 {
		t.Fatalf("nameToID() = %d, want 123", got)
	}
	if got := nameToID("bad.fb2"); got != -1 {
		t.Fatalf("nameToID(bad) = %d, want -1", got)
	}
}

func TestGetUpdates(t *testing.T) {
	t.Parallel()

	files := []archive{
		{info: fakeInfo{name: "f.fb2.000101-000150.zip"}},
		{info: fakeInfo{name: "f.fb2.000140-000160.zip"}},
		{info: fakeInfo{name: "f.fb2.000151-000200.zip"}},
		{info: fakeInfo{name: "f.fb2.000201-000250.zip.tmp"}},
		{info: fakeInfo{name: "backup-f.fb2.000251-000300.zip"}},
		{info: fakeInfo{name: "fb2-000001-000100.zip"}},
	}
	updates, err := getUpdates(files, 150)
	if err != nil {
		t.Fatalf("getUpdates() error = %v", err)
	}
	if len(updates) != 2 || updates[0].begin != 140 || updates[0].end != 160 || updates[1].begin != 151 || updates[1].end != 200 {
		t.Fatalf("updates = %#v, want 140-160 and 151-200", updates)
	}
}

func TestLocalArchiveNamesRequireExactMatch(t *testing.T) {
	t.Parallel()

	files := []archive{
		{info: fakeInfo{name: "fb2-000001-000100.zip.tmp"}},
		{info: fakeInfo{name: "backup-fb2-000001-000200.zip"}},
		{info: fakeInfo{name: "fb2-000001-000300.merging.tmp"}},
		{info: fakeInfo{name: "fb2-000001-000400.zip"}},
	}
	last, err := getLastArchive(files)
	if err != nil {
		t.Fatalf("getLastArchive() error = %v", err)
	}
	if last.end != 400 {
		t.Fatalf("last.end = %d, want 400", last.end)
	}
	merge, err := getMergeArchive(files)
	if err != nil {
		t.Fatalf("getMergeArchive() error = %v", err)
	}
	if merge.info != nil {
		t.Fatalf("merge = %#v, want none", merge)
	}
}

func TestArchiveNameWidth(t *testing.T) {
	t.Parallel()

	if got := archiveNameWidth(archive{}, archive{}); got != 10 {
		t.Fatalf("archiveNameWidth(empty) = %d, want 10", got)
	}
	last := archive{info: fakeInfo{name: "fb2-000001-000100.zip"}}
	if got := archiveNameWidth(last, archive{}); got != 6 {
		t.Fatalf("archiveNameWidth(last) = %d, want 6", got)
	}
	merge := archive{info: fakeInfo{name: "fb2-0000000101-0000000200.merging"}}
	if got := archiveNameWidth(last, merge); got != 10 {
		t.Fatalf("archiveNameWidth(merge) = %d, want 10", got)
	}
}

func TestRunCreatesMergeArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000002.zip"), map[string]string{
		"1.fb2": "one",
		"2.fb2": "two",
	})

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized != 0 {
		t.Fatalf("Finalized = %d, want 0", res.Finalized)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	entries, err := countZipEntries(res.ActiveMerge)
	if err != nil {
		t.Fatalf("countZipEntries() error = %v", err)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2", entries)
	}
}

func TestRunFinalizesArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000002.zip"), map[string]string{
		"1.fb2": "one",
		"2.fb2": "two",
	})

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized == 0 {
		t.Fatal("Finalized = 0, want at least one finalized archive")
	}
	if filepath.Base(res.FinalizedArchives[0]) != "fb2-0000000001-0000000001.zip" {
		t.Fatalf("first finalized archive = %q", res.FinalizedArchives[0])
	}
}

func TestRunRemovesSupersededLastArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	oldArchive := filepath.Join(archives, "fb2-0000000001-0000000001.zip")
	writeZip(t, oldArchive, map[string]string{"1.fb2": "one"})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000002-0000000002.zip"), map[string]string{"2.fb2": "two"})

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(oldArchive); !os.IsNotExist(err) {
		t.Fatalf("old archive stat error = %v, want not exist", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	entries, err := countZipEntries(res.ActiveMerge)
	if err != nil {
		t.Fatalf("countZipEntries() error = %v", err)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2", entries)
	}
}

func TestRunPreservesUpdateArchives(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	bogusUpdate := filepath.Join(updates, "f.fb2.0000000001-0000000001.zip.tmp")
	writeZip(t, bogusUpdate, map[string]string{"1.fb2": "one"})
	validUpdate := filepath.Join(updates, "f.fb2.0000000002-0000000002.zip")
	writeZip(t, validUpdate, map[string]string{"2.fb2": "two"})

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(bogusUpdate); err != nil {
		t.Fatalf("bogus update stat error = %v, want preserved", err)
	}
	if _, err := os.Stat(validUpdate); err != nil {
		t.Fatalf("valid update stat error = %v, want preserved", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000002-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
}

func TestRunKeepsNewEntriesFromOverlappingUpdates(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	oldArchive := filepath.Join(archives, "fb2-0000000001-0000000100.zip")
	writeZip(t, oldArchive, map[string]string{"1.fb2": "one", "100.fb2": "hundred"})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000050-0000000102.zip"), map[string]string{
		"50.fb2":  "duplicate",
		"101.fb2": "new",
		"102.fb2": "new",
	})
	core, logs := observer.New(zap.WarnLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir: archives,
		UpdateDirs: []string{updates},
		SizeBytes:  1_000_000,
		Log:        zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000102.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	if entries, err := countZipEntries(res.ActiveMerge); err != nil || entries != 4 {
		t.Fatalf("entries=%d err=%v, want 4 entries", entries, err)
	}
	if logs.FilterMessage("Overlapping archive update selected").Len() != 1 {
		t.Fatalf("logs = %#v, want overlapping update warning", logs.All())
	}
	if logs.FilterMessage("Skipping already finalized archive entry from overlapping update").Len() != 1 {
		t.Fatalf("logs = %#v, want duplicate entry warning", logs.All())
	}
}

func TestRunUsesMinMaxRangeForOutOfOrderEntries(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZipOrdered(t, filepath.Join(updates, "f.fb2.0000000001-0000000002.zip"), []zipEntry{
		{Name: "2.fb2", Content: "two"},
		{Name: "1.fb2", Content: "one"},
	})

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
}

func TestRunFailsWhenUpdateCannotBeOpened(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	updatePath := filepath.Join(updates, "f.fb2.0000000001-0000000001.zip")
	if err := os.WriteFile(updatePath, []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write update: %v", err)
	}

	_, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000})
	if err == nil || !strings.Contains(err.Error(), "open update archive") {
		t.Fatalf("Run() error = %v, want open update archive error", err)
	}
	if _, err := os.Stat(updatePath); err != nil {
		t.Fatalf("update stat error = %v, want preserved", err)
	}
}

func TestRunFailsWhenEntryCopyFailsAndKeepsCommittedOutput(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	firstUpdate := filepath.Join(updates, "f.fb2.0000000001-0000000001.zip")
	secondUpdate := filepath.Join(updates, "f.fb2.0000000002-0000000002.zip")
	writeZip(t, firstUpdate, map[string]string{"1.fb2": "one"})
	writeZip(t, secondUpdate, map[string]string{"2.fb2": "two"})
	copyErr := errors.New("copy failed")
	copies := 0

	_, err := run(
		context.Background(),
		Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1},
		func(writer *zip.Writer, file *zip.File) error {
			copies++
			if copies == 2 {
				return copyErr
			}
			return writer.Copy(file)
		},
	)
	if !errors.Is(err, copyErr) {
		t.Fatalf("run() error = %v, want copy failure", err)
	}
	for _, path := range []string{firstUpdate, secondUpdate} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("update %q stat error = %v, want preserved", path, err)
		}
	}
	outputs, err := filepath.Glob(filepath.Join(archives, "fb2-*.zip"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(outputs) != 1 {
		t.Fatalf("committed outputs = %#v, want one", outputs)
	}
	if entries, err := countZipEntries(outputs[0]); err != nil || entries != 1 {
		t.Fatalf("committed output entries=%d err=%v, want one", entries, err)
	}
}

func TestRunWarnsAndIgnoresEmptyAndNonNumericEntries(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	updatePath := filepath.Join(updates, "f.fb2.0000000001-0000000003.zip")
	writeZipOrdered(t, updatePath, []zipEntry{
		{Name: "1.fb2", Content: "one"},
		{Name: "2.fb2"},
		{Name: "notes.txt", Content: "ignored"},
	})
	core, logs := observer.New(zap.WarnLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:  archives,
		UpdateDirs:  []string{updates},
		SizeBytes:   1_000_000,
		ValidateCRC: true,
		Log:         zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if logs.FilterMessage("Skipping empty archive entry").Len() != 1 {
		t.Fatalf("empty entry warnings = %d, want one", logs.FilterMessage("Skipping empty archive entry").Len())
	}
	if logs.FilterMessage("Skipping entry with non-numeric name").Len() != 1 {
		t.Fatalf("non-numeric entry warnings = %d, want one", logs.FilterMessage("Skipping entry with non-numeric name").Len())
	}
	if entries, err := countZipEntries(res.ActiveMerge); err != nil || entries != 1 {
		t.Fatalf("active merge entries=%d err=%v, want one", entries, err)
	}
	if _, err := os.Stat(updatePath); err != nil {
		t.Fatalf("update stat error = %v, want preserved", err)
	}
}

func TestRunOptionalCRCValidation(t *testing.T) {
	t.Parallel()

	fastArchives := t.TempDir()
	fastUpdates := t.TempDir()
	fastUpdate := filepath.Join(fastUpdates, "f.fb2.0000000001-0000000001.zip")
	writeCorruptStoredZip(t, fastUpdate, "1.fb2", "one")
	sourceMethod, sourceRaw := readRawZipEntry(t, fastUpdate, "1.fb2")

	res, err := Run(context.Background(), Options{ArchiveDir: fastArchives, UpdateDirs: []string{fastUpdates}, SizeBytes: 1_000_000})
	if err != nil {
		t.Fatalf("Run(validate_crc=false) error = %v", err)
	}
	outputMethod, outputRaw := readRawZipEntry(t, res.ActiveMerge, "1.fb2")
	if outputMethod != sourceMethod || !bytes.Equal(outputRaw, sourceRaw) {
		t.Fatalf("direct copy changed compressed entry: method=%d/%d bytes_equal=%v", outputMethod, sourceMethod, bytes.Equal(outputRaw, sourceRaw))
	}

	checkedArchives := t.TempDir()
	checkedUpdates := t.TempDir()
	checkedUpdate := filepath.Join(checkedUpdates, "f.fb2.0000000001-0000000001.zip")
	writeCorruptStoredZip(t, checkedUpdate, "1.fb2", "one")
	_, err = Run(context.Background(), Options{
		ArchiveDir:  checkedArchives,
		UpdateDirs:  []string{checkedUpdates},
		SizeBytes:   1_000_000,
		ValidateCRC: true,
	})
	if !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("Run(validate_crc=true) error = %v, want zip.ErrChecksum", err)
	}
	if _, err := os.Stat(checkedUpdate); err != nil {
		t.Fatalf("checked update stat error = %v, want preserved", err)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	entries := make([]zipEntry, 0, len(files))
	for name, content := range files {
		entries = append(entries, zipEntry{Name: name, Content: content})
	}
	writeZipOrdered(t, path, entries)
}

func writeCorruptStoredZip(t *testing.T, path string, name string, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		t.Fatalf("create stored entry %s: %v", name, err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatalf("write stored entry %s: %v", name, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip for corruption: %v", err)
	}
	offset, err := zr.File[0].DataOffset()
	if err != nil {
		zr.Close()
		t.Fatalf("entry data offset: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close zip reader: %v", err)
	}
	f, err = os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open zip for corruption: %v", err)
	}
	b := []byte{0}
	if _, err := f.ReadAt(b, offset); err != nil {
		f.Close()
		t.Fatalf("read entry byte: %v", err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b, offset); err != nil {
		f.Close()
		t.Fatalf("corrupt entry byte: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupted zip: %v", err)
	}
}

func readRawZipEntry(t *testing.T, path string, name string) (uint16, []byte) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer zr.Close()
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		r, err := file.OpenRaw()
		if err != nil {
			t.Fatalf("open raw entry %s: %v", name, err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read raw entry %s: %v", name, err)
		}
		return file.Method, data
	}
	t.Fatalf("entry %s not found in %s", name, path)
	return 0, nil
}

type zipEntry struct {
	Name    string
	Content string
}

func writeZipOrdered(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		w, err := zw.Create(entry.Name)
		if err != nil {
			t.Fatalf("create entry %s: %v", entry.Name, err)
		}
		if _, err := w.Write([]byte(entry.Content)); err != nil {
			t.Fatalf("write entry %s: %v", entry.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}

type fakeInfo struct {
	name string
}

func (f fakeInfo) Name() string { return f.name }

func (f fakeInfo) Size() int64 { return 0 }

func (f fakeInfo) Mode() os.FileMode { return 0 }

func (f fakeInfo) ModTime() time.Time { return time.Time{} }

func (f fakeInfo) IsDir() bool { return false }

func (f fakeInfo) Sys() any { return nil }
