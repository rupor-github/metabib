package rollup

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		{info: fakeInfo{name: "f.fb2.000151-000200.zip"}},
		{info: fakeInfo{name: "fb2-000001-000100.zip"}},
	}
	updates, err := getUpdates(files, 150)
	if err != nil {
		t.Fatalf("getUpdates() error = %v", err)
	}
	if len(updates) != 1 || updates[0].begin != 151 || updates[0].end != 200 {
		t.Fatalf("updates = %#v, want 151-200", updates)
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

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000, KeepUpdates: true})
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

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1, KeepUpdates: true})
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

	res, err := Run(context.Background(), Options{ArchiveDir: archives, UpdateDirs: []string{updates}, SizeBytes: 1_000_000, KeepUpdates: true})
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

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
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
