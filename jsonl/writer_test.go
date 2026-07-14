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

func TestWriterPublishesSingleFile(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	w, err := CreateCompressed(base, CompressionNone)
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
	path := filepath.Join(filepath.Dir(base), "out.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"book_id":10`) || !strings.Contains(string(data), `"book_id":20`) {
		t.Fatalf("output content = %s", data)
	}
	if matches := globOutput(t, filepath.Dir(base), "out.*.jsonl"); len(matches) != 0 {
		t.Fatalf("range-named matches = %#v, want none", matches)
	}
}

func TestWriterStageDoesNotPublishUntilCommit(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	w, err := CreateCompressed(base, CompressionNone)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 10}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if matches := globOutput(t, filepath.Dir(base), "out.jsonl"); len(matches) != 0 {
		t.Fatalf("published matches before stage = %#v, want none", matches)
	}
	if err := w.Stage(); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if matches := globOutput(t, filepath.Dir(base), "out.jsonl"); len(matches) != 0 {
		t.Fatalf("published matches before commit = %#v, want none", matches)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if matches := globOutput(t, filepath.Dir(base), "out.jsonl"); len(matches) != 1 {
		t.Fatalf("published matches after commit = %#v, want 1", matches)
	}
}

func TestWriterAbortRemovesStagedOutput(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	w, err := CreateCompressed(base, CompressionNone)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 10}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Stage(); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if matches := globOutput(t, filepath.Dir(base), "out*"); len(matches) != 0 {
		t.Fatalf("matches after abort = %#v, want none", matches)
	}
}

func TestWritersUseUniqueStagingPaths(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	first, err := CreateCompressed(base, CompressionNone)
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	second, err := CreateCompressed(base, CompressionNone)
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	for idx, w := range []*Writer{first, second} {
		if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: int64(idx + 1)}}); err != nil {
			t.Fatalf("Write(%d) error = %v", idx, err)
		}
		if err := w.Stage(); err != nil {
			t.Fatalf("Stage(%d) error = %v", idx, err)
		}
	}
	if first.stagedParts[0].stagePath == second.stagedParts[0].stagePath {
		t.Fatalf("staging paths collided: %q", first.stagedParts[0].stagePath)
	}
	if matches := globOutput(t, filepath.Dir(base), "out.jsonl-*.tmp"); len(matches) != 2 {
		t.Fatalf("staging matches = %#v, want 2", matches)
	}
	if err := first.Abort(); err != nil {
		t.Fatalf("Abort(first) error = %v", err)
	}
	if err := second.Abort(); err != nil {
		t.Fatalf("Abort(second) error = %v", err)
	}
}

func TestWriterWriteValueSupportsHeaderOnlyDataset(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "all")
	w, err := CreateCompressed(base, CompressionNone)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := w.WriteValue(model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.RecordSchemaV2, Records: 0}); err != nil {
		t.Fatalf("WriteValue() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	data := readPlainFile(t, filepath.Join(filepath.Dir(base), "all.jsonl"))
	if !strings.Contains(data, `"schema":"metabib.dataset/1"`) || strings.Contains(data, "metabib.record/2\n{") {
		t.Fatalf("header-only output = %q", data)
	}
}

func TestWriterCompression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		compression Compression
		path        string
		read        func(*testing.T, string) string
	}{
		{name: "none", compression: CompressionNone, path: "out.jsonl", read: readPlainFile},
		{name: "gzip", compression: CompressionGzip, path: "out.jsonl.gz", read: readGzipFile},
		{name: "zstd", compression: CompressionZstd, path: "out.jsonl.zst", read: readZstdFile},
		{name: "zip", compression: CompressionZip, path: "out.jsonl.zip", read: readZipFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := filepath.Join(t.TempDir(), "out")
			w, err := CreateCompressed(base, tt.compression)
			if err != nil {
				t.Fatalf("CreateCompressed() error = %v", err)
			}
			if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 10}}); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			path := filepath.Join(filepath.Dir(base), tt.path)
			if got := tt.read(t, path); !strings.Contains(got, `"book_id":10`) {
				t.Fatalf("decompressed output = %q", got)
			}
		})
	}
}

func TestWriterZipEntryUsesJSONLName(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "all")
	w, err := CreateCompressed(base, CompressionZip)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 24}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	zr, err := zip.OpenReader(filepath.Join(filepath.Dir(base), "all.jsonl.zip"))
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "all.jsonl" {
		t.Fatalf("zip entries = %#v, want all.jsonl", zr.File)
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

	w, err := CreateCompressed(filepath.Join(t.TempDir(), "out.jsonl.zst"), CompressionZstd)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 1}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if matches := globOutput(t, filepath.Dir(w.basePath), "out.jsonl.zst"); len(matches) != 1 {
		t.Fatalf("matches = %#v, want 1 file", matches)
	}
}

func TestWriterWarnsOnOverwrite(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out")
	path := filepath.Join(filepath.Dir(base), "out.jsonl")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	core, logs := observer.New(zap.WarnLevel)
	w, err := CreateCompressed(base, CompressionNone)
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

func globOutput(t *testing.T, dir string, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	return matches
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
		t.Fatalf("Open entry error = %v", err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
}
