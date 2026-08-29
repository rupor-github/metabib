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
	if !cfg.Database.Temporary {
		t.Fatal("Database.Temporary = false, want true")
	}
	if cfg.Database.Socket != "" || cfg.Database.PIDFile != "" || cfg.Database.LogFile != "" {
		t.Fatalf(
			"managed support paths = socket %q pid %q log %q, want empty defaults",
			cfg.Database.Socket,
			cfg.Database.PIDFile,
			cfg.Database.LogFile,
		)
	}
	if cfg.Processing.DatabaseWorkers < 1 || cfg.Processing.ArchiveWorkers < 1 {
		t.Fatalf("workers were not expanded: database=%d archive=%d", cfg.Processing.DatabaseWorkers, cfg.Processing.ArchiveWorkers)
	}
	if cfg.Processing.Manifests.ArchiveDir != "" {
		t.Fatalf("ArchiveDir = %q, want empty", cfg.Processing.Manifests.ArchiveDir)
	}
	if cfg.Processing.NestedArchiveInspection.MaxCompressedSizeMiB != 498 ||
		cfg.Processing.NestedArchiveInspection.MaxCompressedBytes() != 498*1024*1024 {
		t.Fatalf("NestedArchiveInspection = %#v", cfg.Processing.NestedArchiveInspection)
	}
	for _, name := range []string{"flibusta", "flibusta-usr", "flibusta-all", "librusec", "librusec-usr", "librusec-all"} {
		lib, ok := cfg.Fetch.FindLibrary(name)
		if !ok {
			t.Fatalf("default %s fetch profile is missing", name)
		}
		if lib.ArchiveContent == "" {
			t.Fatalf("default %s fetch profile archive_content is empty", name)
		}
	}
	if cfg.Rollup.ValidateCRC {
		t.Fatal("Rollup.ValidateCRC = true, want false")
	}
	if cfg.Rollup.Finalization.Policy != "size" {
		t.Fatalf("Rollup.Finalization.Policy = %q, want size", cfg.Rollup.Finalization.Policy)
	}
	if cfg.Rollup.Finalization.Size.TargetMiB.FB2 != 2048 || cfg.Rollup.Finalization.Size.TargetMiB.USR != 4096 {
		t.Fatalf("Rollup.Finalization.Size.TargetMiB = %#v, want fb2=2048 usr=4096", cfg.Rollup.Finalization.Size.TargetMiB)
	}
	if cfg.Rollup.Finalization.Rolling.Duration != "14d" || cfg.Rollup.Finalization.Calendar.Bucket != "month" {
		t.Fatalf("Rollup.Finalization = %#v", cfg.Rollup.Finalization)
	}
	if len(cfg.Rollup.UpdatePatterns) != 4 {
		t.Fatalf("Rollup.UpdatePatterns length = %d, want 4", len(cfg.Rollup.UpdatePatterns))
	}
	for _, pattern := range cfg.Rollup.UpdatePatterns {
		if pattern.Family != "fb2" && pattern.Family != "usr" {
			t.Fatalf("Rollup.UpdatePatterns contains invalid family: %#v", pattern)
		}
		if pattern.Pattern == "" {
			t.Fatalf("Rollup.UpdatePatterns contains empty pattern: %#v", pattern)
		}
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
	if !cfg.INPX.DisambiguateAuthors {
		t.Fatal("INPX.DisambiguateAuthors = false, want true")
	}
	if cfg.INPX.FLibrary.SequenceDedup != "case-insensitive" || cfg.INPX.FLibrary.FB2PathSeparator != " / " {
		t.Fatalf("FLibrary INPX defaults = %#v", cfg.INPX.FLibrary)
	}
	if !cfg.INPX.Language.Canonicalize {
		t.Fatal("INPX.Language.Canonicalize = false, want true")
	}
	if cfg.INPX.Language.Aliases["gr"] != "el" || cfg.INPX.Language.Aliases["un"] != "und" || cfg.INPX.Language.Aliases["Человеческое, слишком человеческое"] != "ru" {
		t.Fatalf("INPX language aliases = %#v", cfg.INPX.Language.Aliases)
	}
	if strings.Join(cfg.INPX.Language.FallbackLocales, ",") != "en,ru,bg" {
		t.Fatalf("INPX language fallback locales = %#v", cfg.INPX.Language.FallbackLocales)
	}
	if len(cfg.INPX.Language.ContextRules) != 2 || cfg.INPX.Language.ContextRules[0].From != "ba" || cfg.INPX.Language.ContextRules[1].From != "xa" {
		t.Fatalf("INPX language context rules = %#v", cfg.INPX.Language.ContextRules)
	}
}

func TestLoadConfigurationDefaultsMissingArchiveContentToFB2(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metabib.yaml")
	data := []byte(strings.Join([]string{
		"fetch:",
		"  libraries:",
		"    - name: custom",
		"      archive_pattern: 'href=\"([^\"]+)\"'",
		"      sql_pattern: 'href=\"([^\"]+)\"'",
		"      archive_url: http://example.com/daily/",
		"      sql_url: http://example.com/sql/",
	}, "\n"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfiguration(path, gencfg.WithRootDir(t.TempDir()))
	if err != nil {
		t.Fatalf("LoadConfiguration() error = %v", err)
	}
	lib, ok := cfg.Fetch.FindLibrary("custom")
	if !ok {
		t.Fatal("custom fetch profile is missing")
	}
	if lib.ArchiveContent != "fb2" {
		t.Fatalf("ArchiveContent = %q, want fb2", lib.ArchiveContent)
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
		"  fb2_body_fingerprints: false",
		"inpx:",
		"  language:",
		"    canonicalize: false",
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
	if cfg.INPX.Language.Canonicalize {
		t.Fatal("INPX.Language.Canonicalize = true, want false")
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

func TestLoadConfigurationRejectsFB2BodyFingerprintsWithoutParseFB2(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metabib.yaml")
	data := []byte(strings.Join([]string{
		"processing:",
		"  parse_fb2: false",
		"  fb2_body_fingerprints: true",
	}, "\n"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfiguration(path, gencfg.WithRootDir(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "processing.fb2_body_fingerprints requires processing.parse_fb2") {
		t.Fatalf("LoadConfiguration() error = %v, want fb2_body_fingerprints validation error", err)
	}
}

func TestLoadConfigurationRejectsOldRollupTargetSize(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metabib.yaml")
	data := []byte(strings.Join([]string{
		"rollup:",
		"  target_size_mib:",
		"    fb2: 2048",
		"    usr: 4096",
	}, "\n"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfiguration(path, gencfg.WithRootDir(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "target_size_mib") {
		t.Fatalf("LoadConfiguration() error = %v, want target_size_mib unknown field error", err)
	}
}

func TestLoadConfigurationAcceptsRollingFinalization(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metabib.yaml")
	data := []byte(strings.Join([]string{
		"rollup:",
		"  finalization:",
		"    policy: rolling",
		"    rolling:",
		"      duration: 2w",
	}, "\n"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfiguration(path, gencfg.WithRootDir(t.TempDir()))
	if err != nil {
		t.Fatalf("LoadConfiguration() error = %v", err)
	}
	if cfg.Rollup.Finalization.Policy != "rolling" || cfg.Rollup.Finalization.Rolling.Duration != "2w" {
		t.Fatalf("Rollup.Finalization = %#v, want rolling 2w", cfg.Rollup.Finalization)
	}
}

func TestLoadConfigurationRejectsSubDayRollingFinalization(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metabib.yaml")
	data := []byte(strings.Join([]string{
		"rollup:",
		"  finalization:",
		"    policy: rolling",
		"    rolling:",
		"      duration: 24h",
	}, "\n"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfiguration(path, gencfg.WithRootDir(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "whole-day or whole-week") {
		t.Fatalf("LoadConfiguration() error = %v, want rolling duration validation error", err)
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

func TestDumpKeepsINPXTemplatesReadable(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfiguration("", gencfg.WithRootDir(t.TempDir()))
	if err != nil {
		t.Fatalf("LoadConfiguration() error = %v", err)
	}
	data, err := Dump(cfg)
	if err != nil {
		t.Fatalf("Dump() error = %v", err)
	}
	dumped := string(data)
	for _, want := range []string{
		`comment_template: "\uFEFF{{ .DatabaseName }} FB2 - {{ .DisplayDate }}\r\n`,
		`version_template: "{{ .DumpDate }}\r\n"`,
		"{{ .DatabaseName }} FB2 - {{ .DisplayDate }}",
		"Локальные архивы библиотеки {{ .DatabaseName }}",
	} {
		if !strings.Contains(dumped, want) {
			t.Fatalf("Dump() missing %q:\n%s", want, dumped)
		}
	}
	for _, escaped := range []string{"\\x7B", "\\x20"} {
		if strings.Contains(dumped, escaped) {
			t.Fatalf("Dump() contains escaped template bytes %q:\n%s", escaped, dumped)
		}
	}

	roundTrip, err := unmarshalConfig(data, &Config{}, false)
	if err != nil {
		t.Fatalf("unmarshal dumped config: %v", err)
	}
	if roundTrip.INPX.CommentTemplate != cfg.INPX.CommentTemplate {
		t.Fatalf("CommentTemplate round trip mismatch:\n got %q\nwant %q", roundTrip.INPX.CommentTemplate, cfg.INPX.CommentTemplate)
	}
	if roundTrip.INPX.VersionTemplate != cfg.INPX.VersionTemplate {
		t.Fatalf("VersionTemplate round trip mismatch: got %q want %q", roundTrip.INPX.VersionTemplate, cfg.INPX.VersionTemplate)
	}
}
