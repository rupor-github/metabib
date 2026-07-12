package inpxutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/model"
)

func TestDiscoverInputPartsUsesMetadataPartsAndWarnsUnlisted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	listed := filepath.Join(dir, "all.0000000001-0000000001.jsonl")
	extra := filepath.Join(dir, "all.0000000002-0000000002.jsonl")
	for _, path := range []string{listed, extra} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	core, logs := observer.New(zap.WarnLevel)
	parts, err := DiscoverInputParts(
		prefix,
		filepath.Join(dir, "all.meta.json.zst"),
		model.MergeMetadata{Parts: []string{filepath.Base(listed)}},
		zap.New(core),
	)
	if err != nil {
		t.Fatalf("DiscoverInputParts() error = %v", err)
	}
	if len(parts) != 1 || parts[0] != listed {
		t.Fatalf("parts = %#v, want %q", parts, listed)
	}
	if logs.FilterMessage("Ignoring JSONL input part not listed in merge metadata").Len() != 1 {
		t.Fatalf("logs = %#v, want one unlisted-part warning", logs.All())
	}
}

func TestDiscoverInputPartsRequiresMetadataParts(t *testing.T) {
	t.Parallel()

	_, err := DiscoverInputParts("all", "all.meta.json.zst", model.MergeMetadata{}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not list JSONL parts") {
		t.Fatalf("DiscoverInputParts() error = %v, want missing parts error", err)
	}
}
