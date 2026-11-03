package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rupor-github/gencfg"
)

func TestMergeConsistencyConfiguration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		yaml    string
		want    []string
		wantErr bool
	}{
		{name: "omitted uses defaults", want: []string{"content_md5", "document_type", "content_size", "title", "authors", "language"}},
		{name: "disabled", yaml: "    enabled: false\n"},
		{name: "empty", yaml: "    checks: []\n"},
		{name: "ordered subset", yaml: "    checks: [title, content_md5]\n", want: []string{"content_md5", "title"}},
		{name: "unknown check", yaml: "    checks: [typo]\n", wantErr: true},
		{name: "duplicate", yaml: "    checks: [title, title]\n", wantErr: true},
		{name: "empty name", yaml: "    checks: ['']\n", wantErr: true},
		{name: "unknown field", yaml: "    strict: true\n", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "metabib.yaml")
			data := "version: 1\n"
			if tt.yaml != "" {
				data += "processing:\n  merge_consistency:\n" + tt.yaml
			}
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfiguration(path, gencfg.WithRootDir(root))
			if tt.wantErr {
				if err == nil {
					t.Fatal("invalid merge consistency configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Processing.MergeConsistency.ActiveChecks(); !slices.Equal(got, tt.want) {
				t.Fatalf("checks = %v, want %v", got, tt.want)
			}
			dumped, err := Dump(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, dumped, 0o600); err != nil {
				t.Fatal(err)
			}
			roundtrip, err := LoadConfiguration(path, gencfg.WithRootDir(root))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(roundtrip.Processing.MergeConsistency.ActiveChecks(), tt.want) {
				t.Fatal("dump/load changed checks")
			}
		})
	}
}
