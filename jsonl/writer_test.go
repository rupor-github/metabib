package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metabib/model"
)

func TestWriterRangeRenameAndSplit(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "out.jsonl")
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
