package fetch

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func gzipPayload(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFileRecovery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		acrossRuns bool
		etag       string
		modified   string
		changed    bool
	}{
		{name: "resume within run", etag: `"v1"`},
		{name: "resume across runs", etag: `"v1"`, acrossRuns: true},
		{name: "Last-Modified resume", etag: `W/"v1"`, modified: "Wed, 07 Oct 2026 12:00:00 GMT"},
		{name: "missing validator restarts"},
		{name: "weak ETag restarts", etag: `W/"v1"`},
		{name: "changed dump restarts", etag: `"v1"`, changed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := "CREATE TABLE original (id int);\n"
			wanted := original
			payload := gzipPayload(t, original)
			changedPayload := gzipPayload(t, "CREATE TABLE changed (id int);\n")
			split := len(payload) / 2
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET without HEAD probes", r.Method)
					http.Error(w, "no HEAD", http.StatusBadRequest)
					return
				}
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("request did not disable transparent compression")
				}
				w.Header().Set("ETag", tt.etag)
				w.Header().Set("Last-Modified", tt.modified)
				if requests.Add(1) == 1 {
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					_, _ = w.Write(payload[:split])
					return // Declared length exceeds body, causing unexpected EOF.
				}
				validator := tt.modified
				if tt.etag == `"v1"` {
					validator = tt.etag
				}
				if validator != "" {
					if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", split) || r.Header.Get("If-Range") != validator {
						t.Errorf("resume headers = %v", r.Header)
					}
					if !tt.changed {
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", split, len(payload)-1, len(payload)))
						w.WriteHeader(http.StatusPartialContent)
						_, _ = w.Write(payload[split:])
						return
					}
				} else if r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "" {
					t.Errorf("unsafe resume without validator: %v", r.Header)
				}
				if tt.changed {
					w.Header().Set("ETag", `"v2"`)
					_, _ = w.Write(changedPayload)
				} else {
					_, _ = w.Write(payload)
				}
			}))
			defer server.Close()
			f := testFetcher(server)
			f.opts.Retry = 2
			dir := t.TempDir()
			part := filepath.Join(dir, ".lib.sql.gz.part")
			if tt.acrossRuns {
				f.opts.Retry = 1
				err := f.file(t.Context(), "lib.sql.gz", server.URL, dir)
				if !retryable(err) || !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("first run error = %v, want retryable unexpected EOF", err)
				}
				if got := readString(t, part); got != string(payload[:split]) {
					t.Fatal("partial bytes not preserved")
				}
				if _, err := os.Stat(filepath.Join(dir, "lib.sql")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unfinished download published: %v", err)
				}
				// Reconstruct the fetcher to exercise sidecar loading in a fresh run.
				f = testFetcher(server)
			}
			if err := f.file(t.Context(), "lib.sql.gz", server.URL, dir); err != nil {
				t.Fatal(err)
			}
			if tt.changed {
				wanted = "CREATE TABLE changed (id int);\n"
			}
			if got := readString(t, filepath.Join(dir, "lib.sql")); got != wanted {
				t.Fatalf("output = %q, want %q", got, wanted)
			}
			if requests.Load() != 2 {
				t.Fatalf("requests = %d, want 2", requests.Load())
			}
			for _, name := range []string{part, part + ".meta.json"} {
				if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("completed partial not removed: %s: %v", name, err)
				}
			}
		})
	}
}

func TestTimeoutAllowsSlowProgress(t *testing.T) {
	t.Parallel()
	for _, index := range []bool{false, true} {
		t.Run(fmt.Sprintf("index=%t", index), func(t *testing.T) {
			t.Parallel()
			const text = "slow download"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(text)))
				for _, b := range []byte(text) {
					_, _ = w.Write([]byte{b})
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						return
					case <-time.After(40 * time.Millisecond):
					}
				}
			}))
			defer server.Close()
			f := testFetcher(server)
			f.opts.Timeout = 200 * time.Millisecond
			if index {
				got, err := f.fetchString(t.Context(), server.URL)
				if err != nil || got != text {
					t.Fatalf("fetchString = %q, %v", got, err)
				}
			} else {
				part := filepath.Join(t.TempDir(), "download.part")
				state := downloadState{URL: server.URL, Total: -1}
				_, err := f.fetchFile(t.Context(), server.URL, part, 0, &state)
				if err != nil || readString(t, part) != text {
					t.Fatalf("slow chunk download error = %v", err)
				}
			}
		})
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		headers bool
		cancel  bool
		want    error
	}{
		{name: "header timeout", want: errHeaderTimeout},
		{name: "body inactivity timeout", headers: true, want: errInactivityTimeout},
		{name: "cancellation", headers: true, cancel: true, want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			started := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tt.headers {
					w.Header().Set("Content-Length", "6")
					w.Header().Set("ETag", `"v1"`)
					_, _ = w.Write([]byte("abc"))
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			f := testFetcher(server)
			f.opts.Timeout = 100 * time.Millisecond
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				f.opts.Retry = 10
				f.opts.Timeout = time.Second
				var wg sync.WaitGroup
				wg.Go(func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				})
				defer func() {
					cancel()
					wg.Wait()
				}()
			}
			err := f.file(ctx, "lib.sql.gz", server.URL, t.TempDir())
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want no retry after cancellation", requests.Load())
			}
		})
	}
}

func TestRangeNotSatisfiableRecovery(t *testing.T) {
	t.Parallel()
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			t.Parallel()
			payload := gzipPayload(t, "SQL dump\n")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				if requests.Add(1) == 1 {
					if r.Header.Get("Range") == "" {
						t.Error("expected resume range")
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				if r.Header.Get("Range") != "" {
					t.Error("expected restart without range")
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			dir := t.TempDir()
			part := filepath.Join(dir, ".lib.sql.gz.part")
			partial := payload
			if !complete {
				partial = payload[:len(payload)/2]
			}
			if err := os.WriteFile(part, partial, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := saveDownloadState(part, downloadState{URL: server.URL + "/lib.sql.gz", Validator: `"v1"`, Total: int64(len(payload))}); err != nil {
				t.Fatal(err)
			}
			f := testFetcher(server)
			f.opts.Retry = 2
			if err := f.file(t.Context(), "lib.sql.gz", server.URL, dir); err != nil {
				t.Fatal(err)
			}
			wantRequests := int32(2)
			if complete {
				wantRequests = 1
			}
			if requests.Load() != wantRequests || readString(t, filepath.Join(dir, "lib.sql")) != "SQL dump\n" {
				t.Fatal("range recovery failed")
			}
		})
	}
}

func TestHTTPRetryPolicy(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusNotFound, http.StatusBadRequest,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				_, _ = w.Write([]byte("index"))
			}))
			defer server.Close()
			f := testFetcher(server)
			f.opts.Retry = 2
			got, err := f.fetchString(t.Context(), server.URL)
			if status == http.StatusNotFound || status == http.StatusBadRequest {
				if err == nil || retryable(err) || requests.Load() != 1 {
					t.Fatalf("permanent status retried: %v", err)
				}
			} else if err != nil || got != "index" || requests.Load() != 2 {
				t.Fatalf("transient status not retried: %q, %v", got, err)
			}
		})
	}
}

func TestPlainGETFallback(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name           string
		rangeStatus    int
		fallbackStatus int
		interrupt      bool
		acrossRuns     bool
		wantRequests   int32
	}{
		{name: "success with one attempt", rangeStatus: http.StatusBadRequest, wantRequests: 2},
		{name: "interrupted fallback retries without ranges", rangeStatus: http.StatusBadRequest, interrupt: true, wantRequests: 3},
		{name: "interrupted fallback persists across runs", rangeStatus: http.StatusBadRequest,
			interrupt: true, acrossRuns: true, wantRequests: 3},
		{name: "plain GET also rejected", rangeStatus: http.StatusBadRequest,
			fallbackStatus: http.StatusBadRequest, wantRequests: 2},
		{name: "other permanent errors do not trigger fallback", rangeStatus: http.StatusForbidden, wantRequests: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := gzipPayload(t, "original dump\n")
			const wanted = "replacement dump\n"
			payload := gzipPayload(t, wanted)
			split := len(original) / 2
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if n == 1 {
					if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", split) || r.Header.Get("If-Range") != `"v1"` {
						t.Errorf("missing resume headers: %v", r.Header)
					}
					http.Error(w, "range rejected", tt.rangeStatus)
					return
				}
				if r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "" {
					t.Errorf("fallback/retry still sent range headers: %v", r.Header)
				}
				if tt.fallbackStatus != 0 {
					http.Error(w, "plain GET rejected", tt.fallbackStatus)
					return
				}
				w.Header().Set("ETag", `"v2"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				if tt.interrupt && n == 2 {
					_, _ = w.Write(payload[:len(payload)/2])
					return
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			dir := t.TempDir()
			part := filepath.Join(dir, ".lib.sql.gz.part")
			if err := os.WriteFile(part, original[:split], 0o600); err != nil {
				t.Fatal(err)
			}
			state := downloadState{URL: server.URL + "/lib.sql.gz", Validator: `"v1"`, Total: int64(len(original))}
			if err := saveDownloadState(part, state); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "lib.sql")
			if err := os.WriteFile(output, []byte("previous output"), 0o600); err != nil {
				t.Fatal(err)
			}
			f := testFetcher(server)
			if tt.interrupt && !tt.acrossRuns {
				f.opts.Retry = 2
			}
			if tt.fallbackStatus != 0 || tt.rangeStatus != http.StatusBadRequest {
				f.opts.Retry = 3
			}
			err := f.file(t.Context(), "lib.sql.gz", server.URL, dir)
			if tt.acrossRuns {
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("first run error = %v, want interrupted fallback", err)
				}
				_, _, saved, savedErr := f.partialFile(state.URL, "lib.sql.gz", dir)
				if savedErr != nil || !saved.NoRanges || saved.Validator != `"v2"` ||
					readString(t, part) != string(payload[:len(payload)/2]) || readString(t, output) != "previous output" {
					t.Fatalf("fallback progress/state not preserved: %+v, %v", saved, savedErr)
				}
				f = testFetcher(server)
				err = f.file(t.Context(), "lib.sql.gz", server.URL, dir)
			}
			if tt.fallbackStatus != 0 || tt.rangeStatus != http.StatusBadRequest {
				var statusErr httpStatusError
				if !errors.As(err, &statusErr) || retryable(err) {
					t.Fatalf("error = %v, want permanent HTTP error", err)
				}
				if readString(t, part) != string(original[:split]) || readString(t, output) != "previous output" {
					t.Fatal("failed fallback changed existing partial or published output")
				}
			} else if err != nil || readString(t, output) != wanted {
				t.Fatalf("fallback failed: %v", err)
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Fatalf("requests = %d, want %d", got, tt.wantRequests)
			}
		})
	}
}

func TestRetryDelays(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		value string
		want  time.Duration
	}{
		{value: "2", want: 2 * time.Second},
		{value: "9223372036854775807", want: maxRetryAfter},
		{value: "-2"},
		{value: "nonsense"},
		{value: now.Add(5 * time.Second).Format(http.TimeFormat), want: 5 * time.Second},
		{value: now.Add(-time.Second).Format(http.TimeFormat)},
		{value: now.Add(time.Hour).Format(http.TimeFormat), want: maxRetryAfter},
	} {
		t.Run(tt.value, func(t *testing.T) {
			if got := parseRetryAfter(tt.value, now); got != tt.want {
				t.Fatalf("delay = %v, want %v", got, tt.want)
			}
		})
	}
	for _, attempt := range []int{1, 2, 10, 100} {
		ceiling := min(500*time.Millisecond*time.Duration(1<<min(attempt-1, 6)), 30*time.Second)
		if delay := retryDelay(attempt, nil); delay < ceiling/2 || delay > ceiling {
			t.Fatalf("attempt %d delay = %v, ceiling %v", attempt, delay, ceiling)
		}
	}
	if delay := retryDelay(1, temporaryError{err: errors.New("busy"), retryAfter: time.Minute}); delay != time.Minute {
		t.Fatalf("Retry-After ignored: %v", delay)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (fetcher{}).waitRetry(ctx, 1, temporaryError{err: errors.New("busy"), retryAfter: maxRetryAfter}); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry cancellation = %v", err)
	}
}

func TestPartialMetadataRecovery(t *testing.T) {
	t.Parallel()
	for _, metadata := range []string{"", "invalid JSON", `{"url":"other","validator":"v1","total":6}`} {
		t.Run(fmt.Sprintf("metadata=%q", metadata), func(t *testing.T) {
			t.Parallel()
			payload := gzipPayload(t, "SQL\n")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" {
					t.Error("resumed with invalid metadata")
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			dir := t.TempDir()
			part := filepath.Join(dir, ".lib.sql.gz.part")
			if err := os.WriteFile(part, []byte("old data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if metadata != "" {
				if err := os.WriteFile(part+".meta.json", []byte(metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := testFetcher(server).file(t.Context(), "lib.sql.gz", server.URL, dir); err != nil {
				t.Fatal(err)
			}
			if got := readString(t, filepath.Join(dir, "lib.sql")); got != "SQL\n" {
				t.Fatalf("output = %q", got)
			}
		})
	}
}

func TestInvalidDownloadPreservesOutput(t *testing.T) {
	t.Parallel()
	for _, ext := range []string{"gz", "zip"} {
		t.Run(ext, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			src := filepath.Join(dir, "download.part")
			dst := filepath.Join(dir, "out."+ext)
			published := dst
			if ext == "gz" {
				published = strings.TrimSuffix(dst, ".gz")
			}
			if err := os.WriteFile(published, []byte("existing output"), 0o600); err != nil {
				t.Fatal(err)
			}
			var zipped bytes.Buffer
			zw := zip.NewWriter(&zipped)
			entry, err := zw.CreateHeader(&zip.FileHeader{Name: "book.fb2", Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			const book = "FB2 book content"
			if _, err := entry.Write([]byte(book)); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			bad := zipped.Bytes()
			bad[bytes.Index(bad, []byte(book))] ^= 0xff
			if ext == "gz" {
				bad = gzipPayload(t, "new SQL")
				bad[len(bad)-8] ^= 0xff // Corrupt CRC, after valid data has already decompressed.
			}
			if err := os.WriteFile(src, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			err = processFile(src, dst)
			var invalid invalidDownloadError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want invalid download", err)
			}
			if got := readString(t, published); got != "existing output" {
				t.Fatalf("previous output overwritten: %q", got)
			}
		})
	}
}

func TestBadResumeResponseRestartsSafely(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		contentRange string
		length       string
		etag         string
	}{
		{name: "wrong start", contentRange: "bytes 0-2/6"},
		{name: "changed total", contentRange: "bytes 3-5/7"},
		{name: "missing Content-Range"},
		{name: "invalid end", contentRange: "bytes 3-6/6"},
		{name: "wrong length", contentRange: "bytes 3-5/6", length: "4"},
		{name: "changed ETag", contentRange: "bytes 3-5/6", etag: `"v2"`},
		{name: "weak ETag", contentRange: "bytes 3-5/6", etag: `W/"v1"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.Header().Set("Content-Range", tt.contentRange)
					w.Header().Set("ETag", tt.etag)
					if tt.length != "" {
						w.Header().Set("Content-Length", tt.length)
					}
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write([]byte("def"))
					return
				}
				if r.Header.Get("Range") != "" {
					t.Error("restart still sent a range")
				}
				_, _ = w.Write([]byte("abcdef"))
			}))
			defer server.Close()
			part := filepath.Join(t.TempDir(), "download.part")
			if err := os.WriteFile(part, []byte("abc"), 0o600); err != nil {
				t.Fatal(err)
			}
			state := downloadState{URL: server.URL, Validator: `"v1"`, Total: 6}
			f := testFetcher(server)
			_, err := f.fetchFile(t.Context(), server.URL, part, 3, &state)
			if !retryable(err) || state.Validator != "" || readString(t, part) != "abc" {
				t.Fatalf("unsafe resume: state = %+v, error = %v", state, err)
			}
			_, err = f.fetchFile(t.Context(), server.URL, part, 0, &state)
			if err != nil || !state.Complete || readString(t, part) != "abcdef" {
				t.Fatalf("restart failed: %v", err)
			}
		})
	}
}

func TestNoContinueCleansFailedDownload(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "6")
		_, _ = w.Write([]byte("abc"))
	}))
	defer server.Close()
	f := testFetcher(server)
	f.opts.Continue = false
	dir := t.TempDir()
	if err := f.file(t.Context(), "lib.sql.gz", server.URL, dir); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("download error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary downloads not cleaned: %v, %v", entries, err)
	}
}

func TestCompletedPartialSurvivesPublicationFailure(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "download should not be needed", http.StatusInternalServerError)
	}))
	defer server.Close()
	dir := t.TempDir()
	part := filepath.Join(dir, ".lib.sql.gz.part")
	payload := gzipPayload(t, "SQL dump\n")
	if err := os.WriteFile(part, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	state := downloadState{URL: server.URL + "/lib.sql.gz", Total: int64(len(payload)), Complete: true}
	if err := saveDownloadState(part, state); err != nil {
		t.Fatal(err)
	}
	// A directory at the destination forces publication to fail after decompression.
	blocked := filepath.Join(dir, "lib.sql")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	f := testFetcher(server)
	if err := f.file(t.Context(), "lib.sql.gz", server.URL, dir); err == nil {
		t.Fatal("publication unexpectedly succeeded")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := f.file(t.Context(), "lib.sql.gz", server.URL, dir); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || readString(t, blocked) != "SQL dump\n" {
		t.Fatal("completed partial was not reused after publication failure")
	}
}

func TestRetryAfterFromServer(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	var began atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			began.Store(time.Now().UnixNano())
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if time.Since(time.Unix(0, began.Load())) < time.Second {
			t.Error("retry occurred before Retry-After elapsed")
		}
		_, _ = w.Write([]byte("index"))
	}))
	defer server.Close()
	f := testFetcher(server)
	f.opts.Retry = 2
	if got, err := f.fetchString(t.Context(), server.URL); err != nil || got != "index" {
		t.Fatalf("fetchString = %q, %v", got, err)
	}
}
