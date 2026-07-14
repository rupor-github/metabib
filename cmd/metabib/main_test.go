package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"

	"metabib/db"
	"metabib/jsonl"
	"metabib/library"
	"metabib/model"
	"metabib/state"
)

func TestParseSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  int64
	}{
		{"", 0},
		{"1k", 1024},
		{"1.5m", 1572864},
		{"2gb", 2 * 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseSize(tt.value)
			if err != nil {
				t.Fatalf("parseSize() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseSize() = %d, want %d", got, tt.want)
			}
		})
	}
	if _, err := parseSize("bad"); err == nil {
		t.Fatal("parseSize(bad) error = nil")
	}
}

func TestRollupCommandHasNoKeepUpdatesFlag(t *testing.T) {
	t.Parallel()

	for _, flag := range rollupCommand().Flags {
		if slices.Contains(flag.Names(), "keep-updates") {
			t.Fatal("rollup command still exposes --keep-updates")
		}
	}
}

func TestRecordFileKeys(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		ID:     model.RecordID{FileName: "Book", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{Filenames: []string{"Other.FB2"}}},
	}
	keys := recordFileKeys(rec)
	for _, want := range []string{"book", "book.fb2", "other.fb2"} {
		if !containsString(keys, want) {
			t.Fatalf("recordFileKeys() = %#v, missing %q", keys, want)
		}
	}
}

func TestDumpDirDatesDiffer(t *testing.T) {
	t.Parallel()

	if dumpDirDatesDiffer([]db.DumpFile{{DumpDate: "2026-06-20"}, {DumpDate: "2026-06-20"}}) {
		t.Fatal("dumpDirDatesDiffer() = true for same dates")
	}
	if !dumpDirDatesDiffer([]db.DumpFile{{DumpDate: "2026-06-20"}, {DumpDate: "2026-06-21"}}) {
		t.Fatal("dumpDirDatesDiffer() = false for different dates")
	}
}

func TestImportProvenanceFromDatabaseManifest(t *testing.T) {
	t.Parallel()

	manifest := library.DatabaseManifestDecision{
		DumpDir:  "/dumps",
		DumpDate: "2026-06-20",
		Dumps: []library.DumpManifestSource{{
			Path:          "/dumps/libbook.sql",
			Name:          "libbook.sql",
			DumpDate:      "2026-06-20",
			DumpCompleted: "2026-06-20T02:19:33",
			Modified:      "2026-06-20T02:19:34Z",
			MD5:           "abc123",
		}},
	}
	got := importProvenanceFromDatabaseManifest(manifest)
	if got.DumpDir != manifest.DumpDir || got.DumpDate != manifest.DumpDate || len(got.Dumps) != 1 || got.Dumps[0].MD5 != manifest.Dumps[0].MD5 {
		t.Fatalf("importProvenanceFromDatabaseManifest() = %#v", got)
	}
}

func TestWriteOutput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "out")
	err := writeOutput(context.Background(), path, "", "", nil, func(out *jsonl.Writer) error {
		return out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "out.jsonl.zst"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestWriteOutputNoCompression(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "out")
	err := writeOutput(context.Background(), path, "", "none", nil, func(out *jsonl.Writer) error {
		return out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "out.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestWriteOutputWritesDatasetHeaderFirst(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "all")
	err := writeOutput(context.Background(), path, "", "none", nil, func(out *jsonl.Writer) error {
		if err := out.WriteValue(model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.RecordSchemaV2, Records: 1}); err != nil {
			return err
		}
		return out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	data, err := os.ReadFile(path + ".jsonl")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %#v, want header and one record", lines)
	}
	if !strings.Contains(lines[0], `"schema":"metabib.dataset/1"`) || !strings.Contains(lines[1], `"schema":"metabib.record/1"`) {
		t.Fatalf("output lines = %#v", lines)
	}
}

func TestWriteOutputReturnsCloseError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	err := writeOutput(context.Background(), path, "", "none", nil, func(out *jsonl.Writer) error {
		if err := out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}}); err != nil {
			return err
		}
		finalPath := filepath.Join(dir, "out.jsonl")
		if err := os.Mkdir(finalPath, 0o755); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		t.Fatal("writeOutput() error = nil, want close rename error")
	}
}

func TestWriteOutputAbortsOnWriteError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeErr := assertErr("write failed")
	err := writeOutput(context.Background(), filepath.Join(dir, "out"), "1b", "none", nil, func(out *jsonl.Writer) error {
		for _, id := range []int64{41, 42} {
			if err := out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: id}}); err != nil {
				return err
			}
		}
		return writeErr
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeOutput() error = %v, want write error", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "out*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches after failed writeOutput = %#v, want none", matches)
	}
}

func TestMergeArchiveManifestsRewritesArchivePath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	oldPath := filepath.Join(dir, "old", "books.zip")
	writeTestManifest(t, manifestPath, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			BookID:  1,
			Archive: &model.ArchiveInfo{Path: oldPath, Entry: "1.fb2"},
		},
	})

	currentPath := filepath.Join(dir, "new", "books.zip")
	out, err := jsonl.CreateCompressed(filepath.Join(dir, "out"), 0, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if _, err := mergeArchiveManifests(
		ctx,
		[]library.ArchiveManifestDecision{{ArchivePath: currentPath, ManifestPath: manifestPath}},
		databaseIndex{},
		map[string]string{currentPath: "archive-0001"},
		out,
		nil,
	); err != nil {
		t.Fatalf("mergeArchiveManifests() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close output error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "out.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want one output", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"schema":"metabib.record/2"`) || !strings.Contains(string(data), `"source":"archive-0001"`) {
		t.Fatalf("merged output = %s, want v2 archive source", data)
	}
	if strings.Contains(string(data), oldPath) || strings.Contains(string(data), currentPath) {
		t.Fatalf("merged output = %s, should not contain source archive paths", data)
	}
}

func writeTestManifest(t *testing.T, path string, records ...model.Record) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", path, err)
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		f.Close()
		t.Fatalf("NewWriter() error = %v", err)
	}
	if err := jsonv2.MarshalWrite(enc, map[string]any{"schema": "metabib.archive_manifest/1", "records": len(records)}); err != nil {
		enc.Close()
		f.Close()
		t.Fatalf("MarshalWrite(header) error = %v", err)
	}
	if _, err := enc.Write([]byte{'\n'}); err != nil {
		enc.Close()
		f.Close()
		t.Fatalf("Write(header newline) error = %v", err)
	}
	for _, rec := range records {
		if err := jsonv2.MarshalWrite(enc, rec); err != nil {
			enc.Close()
			f.Close()
			t.Fatalf("MarshalWrite(record) error = %v", err)
		}
		if _, err := enc.Write([]byte{'\n'}); err != nil {
			enc.Close()
			f.Close()
			t.Fatalf("Write(record newline) error = %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		f.Close()
		t.Fatalf("Close encoder error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close file error = %v", err)
	}
}

func TestFailIfReportsNotReady(t *testing.T) {
	t.Parallel()

	if err := failIfReportsNotReady(nil, false); err != nil {
		t.Fatalf("failIfReportsNotReady(nil) error = %v", err)
	}
	if err := failIfReportsNotReady([]library.ManifestReport{{Valid: true, Fresh: true}}, false); err != nil {
		t.Fatalf("failIfReportsNotReady(ready) error = %v", err)
	}
	if err := failIfReportsNotReady([]library.ManifestReport{{Valid: true, Fresh: false}}, false); err == nil {
		t.Fatal("failIfReportsNotReady(stale) error = nil")
	}
	if err := failIfReportsNotReady([]library.ManifestReport{{Valid: true, Fresh: false}}, true); err != nil {
		t.Fatalf("failIfReportsNotReady(stale allowed) error = %v", err)
	}
}

func TestExitErrHandlerNoLogger(t *testing.T) {
	t.Parallel()

	errWasHandled = false
	ctx := state.ContextWithEnv(context.Background())
	exitErrHandler(ctx, nil, assertErr("boom"))
	if errWasHandled {
		t.Fatal("errWasHandled = true without logger")
	}

	errWasHandled = false
	ctx = state.ContextWithEnv(context.Background())
	state.EnvFromContext(ctx).Log = zap.NewNop()
	exitErrHandler(ctx, nil, assertErr("boom"))
	if !errWasHandled {
		t.Fatal("errWasHandled = false with logger")
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
