package fetch

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetLastBookID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"fb2-000001-000100.zip", "fb2-000101-000200.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := getLastBookID(dir)
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 200 {
		t.Fatalf("getLastBookID() = %d, want 200", got)
	}
}

func TestGetLastBookIDEmptyDirectory(t *testing.T) {
	t.Parallel()

	got, err := getLastBookID(t.TempDir())
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("getLastBookID() = %d, want 0", got)
	}
}

func TestGetLastBookIDWithMergingArchive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"fb2-000001-000100.zip", "fb2-000101-000150.merging"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := getLastBookID(dir)
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 150 {
		t.Fatalf("getLastBookID() = %d, want 150", got)
	}
}

func TestGetLastBookIDWithRetainedDailyUpdates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{
		"fb2-000001-000100.zip",
		"f.fb2.000101-000150.zip",
		"000151-000200.zip",
		"f.fb2.000201-000250.zip.tmp",
		"2026-07-12.818211-818248.503.fb2.zip",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := getLastBookID(dir)
	if err != nil {
		t.Fatalf("getLastBookID() error = %v", err)
	}
	if got != 818248 {
		t.Fatalf("getLastBookID() = %d, want 818248", got)
	}
}

func TestDissectRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		ok     bool
		first  int
		second int
	}{
		{name: "flibusta", input: "f.fb2.000101-000150.zip", ok: true, first: 101, second: 150},
		{name: "plain", input: "000151-000200.zip", ok: true, first: 151, second: 200},
		{
			name:   "librusec fb2",
			input:  "2026-07-12.818211-818248.503.fb2.zip",
			ok:     true,
			first:  818211,
			second: 818248,
		},
		{name: "librusec pdf", input: "2026-07-12.818211-818248.503.pdf.zip", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok, first, second, err := dissectRange(tt.input)
			if err != nil {
				t.Fatalf("dissectRange() error = %v", err)
			}
			if ok != tt.ok || first != tt.first || second != tt.second {
				t.Fatalf(
					"dissectRange() = (%v, %d, %d), want (%v, %d, %d)",
					ok,
					first,
					second,
					tt.ok,
					tt.first,
					tt.second,
				)
			}
		})
	}
}

func TestLinksIncludesPartiallyOverlappingUpdates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			`<a href="f.fb2.000001-000100.zip">old</a>`,
			`<a href="f.fb2.000050-000150.zip">overlap</a>`,
			`<a href="f.fb2.000151-000200.zip">new</a>`,
		}, "\n")))
	}))
	defer server.Close()

	f := testFetcher(server)
	got, err := f.links(context.Background(), server.URL, `href="([^"]+)"`, 100, true)
	if err != nil {
		t.Fatalf("links() error = %v", err)
	}
	want := []string{"f.fb2.000050-000150.zip", "f.fb2.000151-000200.zip"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("links() = %v, want %v", got, want)
	}
}

func TestProcessFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tmp := filepath.Join(dir, "source.zip")
	out := filepath.Join(dir, "out.zip")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("1.fb2")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("book")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	if err := processFile(tmp, out); err != nil {
		t.Fatalf("processFile() error = %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("stat output: %v", err)
	}
}

func TestFetchFileResumeValidatesContentRange(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=3-" {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", "bytes 3-5/6")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("def"))
	}))
	defer server.Close()

	tmp := filepath.Join(t.TempDir(), "download.tmp")
	if err := os.WriteFile(tmp, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	f := testFetcher(server)
	_, size, err := f.fetchFile(context.Background(), server.URL, tmp, 3)
	if err != nil {
		t.Fatalf("fetchFile() error = %v", err)
	}
	if size != 6 {
		t.Fatalf("size = %d, want 6", size)
	}
	if got := readString(t, tmp); got != "abcdef" {
		t.Fatalf("temp content = %q, want abcdef", got)
	}
}

func TestFetchFileRestartsWhenResumeRangeIgnored(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("abcdef"))
	}))
	defer server.Close()

	tmp := filepath.Join(t.TempDir(), "download.tmp")
	if err := os.WriteFile(tmp, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	f := testFetcher(server)
	_, size, err := f.fetchFile(context.Background(), server.URL, tmp, 3)
	if err != nil {
		t.Fatalf("fetchFile() error = %v", err)
	}
	if size != 6 {
		t.Fatalf("size = %d, want 6", size)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want resume plus restart", requests)
	}
	if got := readString(t, tmp); got != "abcdef" {
		t.Fatalf("temp content = %q, want restarted file", got)
	}
}

func TestFetchFileRejectsMismatchedContentRange(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-2/6")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("abc"))
	}))
	defer server.Close()

	tmp := filepath.Join(t.TempDir(), "download.tmp")
	if err := os.WriteFile(tmp, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	f := testFetcher(server)
	_, _, err := f.fetchFile(context.Background(), server.URL, tmp, 3)
	if err == nil || !strings.Contains(err.Error(), "Content-Range starting at byte 0") {
		t.Fatalf("fetchFile() error = %v, want Content-Range mismatch", err)
	}
	if got := readString(t, tmp); got != "abc" {
		t.Fatalf("temp content after failed resume = %q, want unchanged partial", got)
	}
}

func testFetcher(server *httptest.Server) fetcher {
	return fetcher{
		opts: Options{
			Continue:  true,
			Timeout:   time.Second,
			ChunkSize: 1024,
			Retry:     1,
		},
		client:    server.Client(),
		userAgent: "metabib-test",
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}
