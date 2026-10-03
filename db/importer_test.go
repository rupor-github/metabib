package db

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestImportFixupReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		dump  string
		input string
	}{
		{name: "empty", dump: "libbook.sql"},
		{name: "no final newline", dump: "libbook.sql", input: "CREATE TABLE x (id int);\nINSERT INTO x VALUES (1);"},
		{name: "skipped line", dump: "libbook.sql", input: "ALTER DATABASE `l` CHARACTER SET utf8;\nSELECT 1;\n"},
		{name: "all skipped", dump: "libbook.sql", input: "ALTER DATABASE `l` CHARACTER SET utf8;"},
		{name: "long line", dump: "libbook.sql", input: "INSERT INTO x VALUES ('" + strings.Repeat("данные", 10000) + "');\n"},
		{
			name: "author aliases", dump: "lib.libavtoraliase.sql",
			input: "  `AliaseId` int(11) NOT NULL auto_increment,\nINSERT INTO `libavtoraliase` VALUES (0,10,20);",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var want, got bytes.Buffer
			if err := writeImportFixup(&want, strings.NewReader(tt.input), isAuthorAliasDump(tt.dump)); err != nil {
				t.Fatal(err)
			}
			reader := importFixupReader(strings.NewReader(tt.input), tt.dump)
			if n, err := reader.Read(nil); n != 0 || err != nil {
				t.Fatalf("empty Read() = %d, %v", n, err)
			}
			buf := make([]byte, 3)
			for {
				n, err := reader.Read(buf)
				got.Write(buf[:n])
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatal("streaming fixups changed SQL contents")
			}
		})
	}
}

func TestImportDumpClientExitsWithoutReading(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test client is a Unix shell executable")
	}
	dir := t.TempDir()
	client := filepath.Join(dir, "client")
	if err := os.WriteFile(client, []byte("#!/bin/sh\nexit 17\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "libbook.sql")
	if err := os.WriteFile(path, []byte(strings.Repeat("INSERT INTO x VALUES (1);\n", 100000)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	importer := NewImporter(config.DatabaseConfig{Protocol: "unix", Name: "test"}, client, nil, nil, false, true)
	err := importer.importDump(ctx, client, DumpFile{Path: path, Name: "libbook.sql"})
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("import error=%v, want client exit 17", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("import did not return before its deadline: %v", ctx.Err())
	}
}

func TestImportDumpClientCancellation(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test client is a Unix shell executable")
	}
	dir := t.TempDir()
	client := filepath.Join(dir, "client")
	if err := os.WriteFile(client, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "libbook.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	importer := NewImporter(config.DatabaseConfig{Protocol: "unix", Name: "test"}, client, nil, nil, false, true)
	if err := importer.importDump(ctx, client, DumpFile{Path: path, Name: "libbook.sql"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled client error = %v", err)
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
