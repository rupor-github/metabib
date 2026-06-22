package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

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

func TestWriteOutput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "out")
	err := writeOutput(context.Background(), path, "", "", nil, func(out *jsonl.Writer) error {
		return out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "out.*.jsonl.zst"))
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
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "out.*.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v", matches)
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
		finalPath := filepath.Join(dir, "out.0000000042-0000000042.jsonl")
		if err := os.Mkdir(finalPath, 0o755); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		t.Fatal("writeOutput() error = nil, want close rename error")
	}
}

func TestWriteOutputJoinsWriteAndCloseErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeErr := assertErr("write failed")
	err := writeOutput(context.Background(), filepath.Join(dir, "out"), "", "none", nil, func(out *jsonl.Writer) error {
		if err := out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}}); err != nil {
			return err
		}
		finalPath := filepath.Join(dir, "out.0000000042-0000000042.jsonl")
		if err := os.Mkdir(finalPath, 0o755); err != nil {
			return err
		}
		return writeErr
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeOutput() error = %v, want write error", err)
	}
	if err == writeErr {
		t.Fatalf("writeOutput() error = %v, want joined close error", err)
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
