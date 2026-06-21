package jsonl

import (
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/model"
)

func TestWriterRangeRenameAndSplit(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	w, err := Create(base, 1)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, id := range []int64{10, 20} {
		if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: id}}); err != nil {
			t.Fatalf("Write(%d) error = %v", id, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(base), "out.*.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want 2 files", matches)
	}
	if !strings.Contains(filepath.Base(matches[0]), "0000000010-0000000010") {
		t.Fatalf("first file name = %q", matches[0])
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"book_id":10`) {
		t.Fatalf("first file content = %s", data)
	}
}

func TestRangedPathDefaultExtension(t *testing.T) {
	t.Parallel()

	got := rangedPath("out", 1, 2)
	if got != "out.0000000001-0000000002.jsonl" {
		t.Fatalf("rangedPath() = %q", got)
	}
}

func TestWriterCompression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		compression Compression
		pattern     string
		read        func(*testing.T, string) string
	}{
		{name: "none", compression: CompressionNone, pattern: "out.*.jsonl", read: readPlainFile},
		{name: "gzip", compression: CompressionGzip, pattern: "out.*.jsonl.gz", read: readGzipFile},
		{name: "zstd", compression: CompressionZstd, pattern: "out.*.jsonl.zst", read: readZstdFile},
		{name: "zip", compression: CompressionZip, pattern: "out.*.jsonl.zip", read: readZipFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := filepath.Join(t.TempDir(), "out")
			w, err := CreateCompressed(base, 0, tt.compression)
			if err != nil {
				t.Fatalf("CreateCompressed() error = %v", err)
			}
			if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 10}}); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			matches, err := filepath.Glob(filepath.Join(filepath.Dir(base), tt.pattern))
			if err != nil {
				t.Fatalf("Glob() error = %v", err)
			}
			if len(matches) != 1 {
				t.Fatalf("matches = %#v, want 1 file", matches)
			}
			if got := tt.read(t, matches[0]); !strings.Contains(got, `"book_id":10`) {
				t.Fatalf("decompressed output = %q", got)
			}
		})
	}
}

func TestWriterZipEntryUsesRangedName(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "all")
	w, err := CreateCompressed(base, 0, CompressionZip)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 24}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(base), "all.*.jsonl.zip"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want 1 file", matches)
	}
	zr, err := zip.OpenReader(matches[0])
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	want := strings.TrimSuffix(filepath.Base(matches[0]), ".zip")
	if len(zr.File) != 1 || zr.File[0].Name != want {
		t.Fatalf("zip entries = %#v, want %q", zr.File, want)
	}
}

func TestParseCompression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  Compression
	}{
		{"", CompressionZstd},
		{"zstd", CompressionZstd},
		{"zst", CompressionZstd},
		{"gz", CompressionGzip},
		{"gzip", CompressionGzip},
		{"zip", CompressionZip},
		{"none", CompressionNone},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseCompression(tt.value)
			if err != nil {
				t.Fatalf("ParseCompression() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseCompression() = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := ParseCompression("bad"); err == nil {
		t.Fatal("ParseCompression(bad) error = nil")
	}
}

func TestNormalizeBasePathRemovesCompressionSuffix(t *testing.T) {
	t.Parallel()

	w, err := CreateCompressed(filepath.Join(t.TempDir(), "out.zst"), 0, CompressionZstd)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 1}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(w.basePath), "out.*.jsonl.zst"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want 1 file", matches)
	}
}

func TestWriterWarnsOnOverwrite(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	path := filepath.Join(filepath.Dir(base), "out.0000000010-0000000010.jsonl")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	core, logs := observer.New(zap.WarnLevel)
	w, err := CreateCompressed(base, 0, CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	w.WithLogger(zap.New(core))
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 10}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := readPlainFile(t, path); !strings.Contains(got, `"book_id":10`) {
		t.Fatalf("replacement output = %q", got)
	}
	if logs.FilterMessage("Overwriting existing JSONL output").Len() != 1 {
		t.Fatalf("logs = %#v, want overwrite warning", logs.All())
	}
}

func readPlainFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func readGzipFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
}

func readZstdFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
	r, err := zstd.NewReader(f)
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
}

func readZipFile(t *testing.T, path string) string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 1 {
		t.Fatalf("zip entries = %d, want 1", len(zr.File))
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("Open zip entry error = %v", err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
}
