package library

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metabib/config"
)

func TestDatasetForDatabaseOnly(t *testing.T) {
	t.Parallel()

	dataset, err := DatasetFor(
		context.Background(),
		"flibusta",
		DatabaseManifestDecision{
			DumpDir:  "/dumps",
			DumpDate: "2026-07-14",
			Format:   "flibusta-current",
			Records:  42,
		},
		nil,
		config.ProcessingConfig{},
		"1.2.3",
	)
	if err != nil {
		t.Fatalf("DatasetFor() error = %v", err)
	}
	if dataset.Schema != "metabib.dataset/1" ||
		dataset.RecordSchema != "metabib.record/2" ||
		dataset.Library != "flibusta" {
		t.Fatalf("dataset identity = %#v", dataset)
	}
	if !strings.HasPrefix(dataset.ID, "urn:uuid:") {
		t.Fatalf("dataset ID = %q", dataset.ID)
	}
	if dataset.Records != 42 {
		t.Fatalf("Records = %d, want 42", dataset.Records)
	}
	if dataset.Database == nil || dataset.Database.ID != "database" || dataset.Database.DumpDate != "2026-07-14" {
		t.Fatalf("Database = %#v", dataset.Database)
	}
	if dataset.Ordering.Mode != "database_book_id" || dataset.Ordering.Source != "database" {
		t.Fatalf("Ordering = %#v", dataset.Ordering)
	}
	if dataset.Generator.Version != "1.2.3" {
		t.Fatalf("Generator = %#v", dataset.Generator)
	}
}

func TestDatasetForArchiveOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "books.zip")
	writeMetadataTestZip(t, archivePath)
	dataset, err := DatasetFor(
		context.Background(),
		"flibusta",
		DatabaseManifestDecision{},
		[]ArchiveManifestDecision{{
			ArchivePath:  archivePath,
			ManifestPath: filepath.Join(dir, "books.manifest.zst"),
			ArchiveMD5:   "abc123",
			Records:      2,
		}},
		config.ProcessingConfig{ParseFB2: true, FB2DescriptionTree: true, ArchiveContentMD5: true},
		"1.2.3",
	)
	if err != nil {
		t.Fatalf("DatasetFor() error = %v", err)
	}
	if dataset.Records != 2 {
		t.Fatalf("Records = %d, want 2", dataset.Records)
	}
	if dataset.Ordering.Mode != "archive_entry" ||
		dataset.Ordering.ArchiveKey != "ordinal" ||
		dataset.Ordering.EntryKey != "index" {
		t.Fatalf("Ordering = %#v", dataset.Ordering)
	}
	if len(dataset.Archives) != 1 {
		t.Fatalf("Archives = %#v, want one", dataset.Archives)
	}
	archive := dataset.Archives[0]
	if archive.ID != "archive-0001" ||
		archive.Ordinal != 0 ||
		archive.Name != "books.zip" ||
		archive.PathHint != archivePath {
		t.Fatalf("Archive = %#v", archive)
	}
	if archive.Entries != 4 || archive.FB2Entries != 2 || len(archive.Ignored) != 1 || len(archive.Dummy) != 1 {
		t.Fatalf("Archive layout = %#v", archive)
	}
	if archive.Checksum == nil || archive.Checksum.Scope != "container" || archive.Checksum.Value != "abc123" {
		t.Fatalf("Archive checksum = %#v", archive.Checksum)
	}
	if !dataset.Processing.ParseFB2 || dataset.Processing.FB2Coverage != "description" {
		t.Fatalf("Processing = %#v", dataset.Processing)
	}
	if !dataset.Processing.ArchiveContentChecksum.Enabled ||
		dataset.Processing.ArchiveContentChecksum.Algorithm != "md5" {
		t.Fatalf("ArchiveContentChecksum = %#v", dataset.Processing.ArchiveContentChecksum)
	}
}

func writeMetadataTestZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"1.fb2", "ignored/", "cover.jpg", "2.fb2"} {
		w, err := zw.Create(name)
		if err != nil {
			zw.Close()
			f.Close()
			t.Fatalf("Create(%q) error = %v", name, err)
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := w.Write([]byte("data")); err != nil {
				zw.Close()
				f.Close()
				t.Fatalf("Write(%q) error = %v", name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		t.Fatalf("Close zip error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close file error = %v", err)
	}
}
