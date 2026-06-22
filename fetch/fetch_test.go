package fetch

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestGetLastBookID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"fb2-000001-000100.zip", "fb2-000101-000200.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := getLastBookID(dir)
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 200 {
		t.Fatalf("getLastBookID() = %d, want 200", got)
	}
}

func TestGetLastBookIDEmptyDirectory(t *testing.T) {
	t.Parallel()

	got, err := getLastBookID(t.TempDir())
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("getLastBookID() = %d, want 0", got)
	}
}

func TestGetLastBookIDWithMergingArchive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"fb2-000001-000100.zip", "fb2-000101-000150.merging"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := getLastBookID(dir)
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 150 {
		t.Fatalf("getLastBookID() = %d, want 150", got)
	}
}

func TestProcessFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tmp := filepath.Join(dir, "source.zip")
	out := filepath.Join(dir, "out.zip")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("1.fb2")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("book")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	if err := processFile(tmp, out); err != nil {
		t.Fatalf("processFile() error = %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("stat output: %v", err)
	}
}
