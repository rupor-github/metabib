package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/config"
	"metabib/state"
)

func TestParseChunkSize(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value string
		want  int64
	}{
		{value: "1", want: 1 << 20},
		{value: "10", want: 10 << 20},
		{value: "1MiB", want: 1 << 20},
		{value: "512KiB", want: 512 << 10},
		{value: "256kib", want: 256 << 10},
		{value: "2GIB", want: 2 << 30},
		{value: "1B", want: 1},
		{value: " 512 KiB ", want: 512 << 10},
		{value: "9223372036854775807B", want: 1<<63 - 1},
		{value: "8796093022207MiB", want: 8796093022207 << 20},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseChunkSize(tt.value)
			if err != nil || got != tt.want {
				t.Fatalf("parseChunkSize(%q) = %d, %v; want %d", tt.value, got, err, tt.want)
			}
		})
	}
}

func TestFetchRejectsInvalidChunkSizeBeforeDownloading(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"", "0", "0B", "-1", "-512KiB", "1.5MiB", "1e3B", "KiB", "1MB", "1MiB/s", "garbage",
		"9223372036854775808B", "8796093022208", "8796093022208MiB", "9007199254740992KiB", "8589934592GiB",
	} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			// No application environment or network setup: invalid sizes must fail
			// before runFetch accesses either.
			err := fetchCommand().Run(t.Context(), []string{"fetch", "--chunksize", value})
			if err == nil || !strings.Contains(err.Error(), "chunk size") {
				t.Fatalf("--chunksize %q error = %v, want invalid chunk size", value, err)
			}
		})
	}
}

func TestFetchChunkSizeCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		value string
		want  int64
	}{
		{name: "default", want: 10 << 20},
		{name: "legacy MiB", value: "1", want: 1 << 20},
		{name: "explicit MiB", value: "1MiB", want: 1 << 20},
		{name: "KiB", value: "512KiB", want: 512 << 10},
		{name: "byte interval", value: "8B", want: 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const sql = "CREATE TABLE libbook (BookId int);\n"
			var compressed bytes.Buffer
			gz := gzip.NewWriter(&compressed)
			if _, err := gz.Write([]byte(sql)); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/lib.sql.gz" {
					_, _ = w.Write(compressed.Bytes())
					return
				}
				_, _ = w.Write([]byte(`<a href="lib.sql.gz">SQL dump</a>`))
			}))
			defer server.Close()
			ctx := state.ContextWithEnv(t.Context())
			env := state.EnvFromContext(ctx)
			env.Cfg = &config.Config{Fetch: config.FetchConfig{Libraries: []config.FetchLibraryConfig{
				{Name: "flibusta", SQLURL: server.URL, SQLPattern: `href="([^"]+\.sql\.gz)"`},
			}}}
			core, logs := observer.New(zap.InfoLevel)
			env.Log = zap.New(core)
			env.Verbose = true
			dir := t.TempDir()
			args := []string{"fetch", "--noarchives", "--tosql", dir}
			if tt.value != "" {
				args = append(args, "--chunksize", tt.value)
			}
			if err := fetchCommand().Run(ctx, args); err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(filepath.Join(dir, "lib.sql"))
			if err != nil || string(out) != sql {
				t.Fatalf("downloaded SQL = %q, %v", out, err)
			}
			chunks := logs.FilterMessage("Downloaded chunk").All()
			wantChunks := int64(compressed.Len()) / tt.want
			if int64(len(chunks)) != wantChunks {
				t.Fatalf("progress entries = %d, want %d for %d-byte chunks", len(chunks), wantChunks, tt.want)
			}
			if len(chunks) > 0 && chunks[0].ContextMap()["bytes"] != tt.want {
				t.Fatalf("first progress offset = %v, want %d", chunks[0].ContextMap()["bytes"], tt.want)
			}
		})
	}
}
