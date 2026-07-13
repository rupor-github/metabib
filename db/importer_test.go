package db

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"metabib/config"
)

func TestDiscoverDumps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDump(t, dir, "b.sql", "-- Dump completed on 2026-06-20  2:19:33\n")
	writeDump(t, dir, "a.sql", "-- Dump completed on 2026-06-20  12:00:01\n")
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	dumps, dumpDate, err := DiscoverDumps(dir, false)
	if err != nil {
		t.Fatalf("DiscoverDumps() error = %v", err)
	}
	if dumpDate != "2026-06-20" {
		t.Fatalf("dumpDate = %q", dumpDate)
	}
	if len(dumps) != 2 || dumps[0].Name != "a.sql" || dumps[1].Name != "b.sql" {
		t.Fatalf("dumps not sorted or wrong length: %#v", dumps)
	}
	if dumps[1].DumpCompleted != "2026-06-20T02:19:33" {
		t.Fatalf("DumpCompleted = %q", dumps[1].DumpCompleted)
	}
}

func TestDiscoverDumpsDateMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDump(t, dir, "a.sql", "-- Dump completed on 2026-06-20  2:19:33\n")
	writeDump(t, dir, "b.sql", "-- Dump completed on 2026-06-21  2:19:33\n")

	if _, _, err := DiscoverDumps(dir, false); err == nil {
		t.Fatal("DiscoverDumps() error = nil, want date mismatch")
	}
	dumps, dumpDate, err := DiscoverDumps(dir, true)
	if err != nil {
		t.Fatalf("DiscoverDumps(allow mismatch) error = %v", err)
	}
	if dumpDate != "" {
		t.Fatalf("dumpDate = %q, want empty", dumpDate)
	}
	if dumps[0].DumpDate == "" || dumps[1].DumpDate == "" {
		t.Fatalf("per-file dates were not preserved: %#v", dumps)
	}
}

func TestClientArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.DatabaseConfig
		want []string
	}{
		{
			name: "tcp passwordless disables ssl",
			cfg:  config.DatabaseConfig{Protocol: "tcp", Host: "127.0.0.1", Port: 3306, User: "root", Name: "lib"},
			want: []string{"--protocol", "tcp", "--host", "127.0.0.1", "--port", "3306", "--skip-ssl", "lib"},
		},
		{
			name: "unix socket",
			cfg:  config.DatabaseConfig{Protocol: "unix", Host: "/tmp/metabib.sock", User: "root", Password: "pw", Name: "lib"},
			want: []string{"--socket", "/tmp/metabib.sock", "--password=pw", "lib"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := NewImporter(tt.cfg, "", nil, nil, false, true).clientArgs()
			if err != nil {
				t.Fatalf("clientArgs() error = %v", err)
			}
			for _, want := range tt.want {
				if !contains(args, want) {
					t.Fatalf("clientArgs() = %#v, missing %q", args, want)
				}
			}
		})
	}
}

func TestAuthorAliasFixup(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		"CREATE TABLE `libavtoraliase` (",
		"  `AliaseId` int(11) NOT NULL auto_increment,",
		"  `BadId` int(11) NOT NULL default '0',",
		"  `GoodId` int(11) NOT NULL default '0'",
		");",
		"INSERT INTO `libavtoraliase` VALUES (0,10,20);",
	}, "\n")
	var out bytes.Buffer
	if err := writeAuthorAliasFixup(&out, strings.NewReader(input)); err != nil {
		t.Fatalf("writeAuthorAliasFixup() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "`dummyId` int(11) NOT NULL default '0',") {
		t.Fatalf("fixup output missing dummyId: %s", got)
	}
	if !strings.Contains(got, "INSERT INTO `libavtoraliase` (dummyId, BadId, GoodId) VALUES (0,10,20);") {
		t.Fatalf("fixup output missing explicit INSERT columns: %s", got)
	}
	if !isAuthorAliasDump("lib.libavtoraliase.sql") {
		t.Fatal("isAuthorAliasDump() = false")
	}
}

func TestImportFixupSkipsAlterDatabase(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		"CREATE TABLE `libpolka` (`id` int);",
		"ALTER DATABASE `l` CHARACTER SET utf8mb4 COLLATE utf8mb4_uca1400_ai_ci ;",
		"INSERT INTO `libpolka` VALUES (1);",
	}, "\n")
	var out bytes.Buffer
	if err := writeImportFixup(&out, strings.NewReader(input), false); err != nil {
		t.Fatalf("writeImportFixup() error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, "ALTER DATABASE") {
		t.Fatalf("fixup output still contains ALTER DATABASE: %s", got)
	}
	if !strings.Contains(got, "CREATE TABLE") || !strings.Contains(got, "INSERT INTO") {
		t.Fatalf("fixup output removed ordinary SQL: %s", got)
	}
}

func TestFilterImportDumps(t *testing.T) {
	t.Parallel()

	dumps := []DumpFile{
		{Name: "libbook.sql"},
		{Name: "libavtor.sql"},
		{Name: "libavtors.sql"},
		{Name: "libgenre.sql"},
		{Name: "libgenres.sql"},
		{Name: "libseq.sql"},
		{Name: "libseqs.sql"},
		{Name: "librate.sql"},
		{Name: "libpolka.sql"},
		{Name: "libmag.sql"},
		{Name: "libmags.sql"},
		{Name: "libquality.sql"},
	}
	filtered := filterImportDumps(dumps, FormatLibrusecCurrent)
	if len(filtered) != 8 {
		t.Fatalf("librusec filtered dumps = %v, want 8 required dumps", filtered)
	}
	for _, dump := range filtered {
		if !librusecImportDump(dump.Name) {
			t.Fatalf("unexpected dump selected for Librusec import: %s", dump.Name)
		}
	}
	unfiltered := filterImportDumps(dumps, FormatFlibustaCurrent)
	if len(unfiltered) != len(dumps) {
		t.Fatalf("flibusta filtered dumps = %d, want %d", len(unfiltered), len(dumps))
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()

	if host, port := splitHostPortDefault(":3307", "127.0.0.1", "3306"); host != "127.0.0.1" || port != "3307" {
		t.Fatalf("splitHostPortDefault() = %q, %q", host, port)
	}
	if got := quoteIdentifier("a`b"); got != "`a``b`" {
		t.Fatalf("quoteIdentifier() = %q", got)
	}
	if got := zeroPadHour("2:19:33"); got != "02:19:33" {
		t.Fatalf("zeroPadHour() = %q", got)
	}
}

func writeDump(t *testing.T, dir string, name string, footer string) {
	t.Helper()
	data := []byte("CREATE TABLE x (id int);\n" + footer)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write dump %q: %v", name, err)
	}
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
