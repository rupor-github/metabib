package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rupor-github/gencfg"
)

func TestLoadConfigurationDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg, err := LoadConfiguration("", gencfg.WithRootDir(root))
	if err != nil {
		t.Fatalf("LoadConfiguration() error = %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("Version = %d, want 1", cfg.Version)
	}
	if cfg.Database.DataDir != filepath.Join(root, "data", "mariadb") {
		t.Fatalf("DataDir = %q", cfg.Database.DataDir)
	}
	if cfg.Processing.DatabaseWorkers < 1 || cfg.Processing.ArchiveWorkers < 1 {
		t.Fatalf("workers were not expanded: database=%d archive=%d", cfg.Processing.DatabaseWorkers, cfg.Processing.ArchiveWorkers)
	}
	if cfg.Processing.Manifests.ArchiveDir != "" {
		t.Fatalf("ArchiveDir = %q, want empty", cfg.Processing.Manifests.ArchiveDir)
	}
	if _, ok := cfg.Fetch.FindLibrary("flibusta"); !ok {
		t.Fatal("default flibusta fetch profile is missing")
	}
	if cfg.Rollup.ValidateCRC {
		t.Fatal("Rollup.ValidateCRC = true, want false")
	}
	if cfg.Database.AdminPath != "" {
		t.Fatalf("Database.AdminPath = %q, want empty", cfg.Database.AdminPath)
	}
	if !strings.Contains(cfg.INPX.CommentTemplate, "{{ .DatabaseName }}") {
		t.Fatalf("CommentTemplate = %q, want unprocessed INPX template", cfg.INPX.CommentTemplate)
	}
	if !strings.Contains(cfg.INPX.VersionTemplate, "{{ .DumpDate }}") {
		t.Fatalf("VersionTemplate = %q, want unprocessed INPX template", cfg.INPX.VersionTemplate)
	}
	if cfg.INPX.FLibrary.SequenceDedup != "case-insensitive" || cfg.INPX.FLibrary.FB2PathSeparator != " / " {
		t.Fatalf("FLibrary INPX defaults = %#v", cfg.INPX.FLibrary)
	}
}

func TestLoadConfigurationFileOverridesDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "metabib.yaml")
	data := []byte(strings.Join([]string{
		"database:",
		"  name: custom",
		"  managed: false",
		"  admin_path: /custom/mariadb-admin",
		"rollup:",
		"  validate_crc: true",
		"processing:",
		"  parse_fb2: false",
		"logging:",
		"  console:",
		"    level: none",
	}, "\n"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfiguration(path, gencfg.WithRootDir(root))
	if err != nil {
		t.Fatalf("LoadConfiguration() error = %v", err)
	}
	if cfg.Database.Name != "custom" {
		t.Fatalf("Database.Name = %q", cfg.Database.Name)
	}
	if cfg.Database.Managed {
		t.Fatal("Database.Managed = true, want false")
	}
	if cfg.Database.AdminPath != "/custom/mariadb-admin" {
		t.Fatalf("Database.AdminPath = %q", cfg.Database.AdminPath)
	}
	if cfg.Processing.ParseFB2 {
		t.Fatal("Processing.ParseFB2 = true, want false")
	}
	if !cfg.Rollup.ValidateCRC {
		t.Fatal("Rollup.ValidateCRC = false, want true")
	}
	if cfg.Database.User != "root" {
		t.Fatalf("default Database.User was not preserved: %q", cfg.Database.User)
	}
}

func TestLoadConfigurationRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metabib.yaml")
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfiguration(path, gencfg.WithRootDir(t.TempDir())); err == nil {
		t.Fatal("LoadConfiguration() error = nil, want unknown field error")
	}
}

func TestDump(t *testing.T) {
	t.Parallel()

	cfg := &Config{Version: 1}
	data, err := Dump(cfg)
	if err != nil {
		t.Fatalf("Dump() error = %v", err)
	}
	if !strings.Contains(string(data), "version: 1") {
		t.Fatalf("Dump() = %q, want version", data)
	}
}
