package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"metabib/internal/fileutil"
)

// Offsets come from the partial file itself, so interrupted metadata writes cannot
// claim bytes that were never written. The sidecar identifies their representation.
type downloadState struct {
	URL       string `json:"url"`
	Validator string `json:"validator,omitempty"`
	Total     int64  `json:"total"`
	Complete  bool   `json:"complete,omitempty"`
	NoRanges  bool   `json:"no_ranges,omitempty"`
}

func responseValidator(resp *http.Response) string {
	if etag := resp.Header.Get("ETag"); strings.HasPrefix(etag, `"`) && strings.HasSuffix(etag, `"`) {
		return etag
	}
	if modified := resp.Header.Get("Last-Modified"); modified != "" {
		if _, err := http.ParseTime(modified); err == nil {
			return modified
		}
	}
	return ""
}

func validatorMatches(resp *http.Response, validator string) bool {
	if strings.HasPrefix(validator, `"`) {
		return resp.Header.Get("ETag") == validator
	}
	return validator != "" && resp.Header.Get("Last-Modified") == validator
}

func validatorChanged(resp *http.Response, validator string) bool {
	if strings.HasPrefix(validator, `"`) {
		return resp.Header.Get("ETag") != "" && !validatorMatches(resp, validator)
	}
	return resp.Header.Get("Last-Modified") != "" && !validatorMatches(resp, validator)
}

func restartDownload(part string, state *downloadState, reason error) error {
	state.Complete = false
	state.Validator = ""
	if err := saveDownloadState(part, *state); err != nil {
		return err
	}
	return temporaryError{err: reason}
}

func saveDownloadState(part string, state downloadState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode download metadata: %w", err)
	}
	out, err := fileutil.CreateHiddenTemp(filepath.Dir(part), filepath.Base(part)+".meta")
	if err != nil {
		return fmt.Errorf("create download metadata: %w", err)
	}
	defer func() { _ = os.Remove(out.Name()) }()
	_, writeErr := out.Write(body)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write download metadata: %w", err)
	}
	if err := fileutil.ReplaceOutputFile(out.Name(), part+".meta.json"); err != nil {
		return fmt.Errorf("publish download metadata: %w", err)
	}
	return nil
}

func (f fetcher) partialFile(sourceURL, file, dest string) (string, int64, downloadState, error) {
	state := downloadState{URL: sourceURL, Total: -1}
	if !f.opts.Continue {
		out, err := fileutil.CreateHiddenTemp(dest, "metabib-fetch")
		if err != nil {
			return "", 0, state, err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(out.Name())
			return "", 0, state, err
		}
		return out.Name(), 0, state, nil
	}
	part := filepath.Join(dest, "."+file+".part")
	info, err := os.Stat(part)
	if errors.Is(err, os.ErrNotExist) {
		return part, 0, state, nil
	}
	if err != nil {
		return part, 0, state, fmt.Errorf("stat partial download: %w", err)
	}
	body, err := os.ReadFile(part + ".meta.json")
	if errors.Is(err, os.ErrNotExist) {
		return part, 0, state, nil
	}
	if err != nil {
		return part, 0, state, fmt.Errorf("read download metadata: %w", err)
	}
	var saved downloadState
	if err := json.Unmarshal(body, &saved); err != nil || saved.URL != sourceURL ||
		(saved.Total >= 0 && info.Size() > saved.Total) || (saved.Complete && info.Size() != saved.Total) {
		if f.opts.Log != nil {
			f.opts.Log.Warn("Restarting partial download with invalid or outdated metadata", zap.String("file", file))
		}
		return part, 0, state, nil
	}
	if saved.Validator == "" && !saved.Complete {
		return part, 0, state, nil
	}
	return part, info.Size(), saved, nil
}

func removePartial(part string) error {
	var errs []error
	for _, name := range []string{part, part + ".meta.json"} {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f fetcher) file(ctx context.Context, file, baseURL, dest string) error {
	if filepath.Base(file) != file || file == "." || file == ".." {
		return fmt.Errorf("invalid download file name %q", file)
	}
	sourceURL := joinURL(baseURL, file)
	part, size, state, err := f.partialFile(sourceURL, file, dest)
	if err != nil {
		return err
	}
	if !f.opts.Continue {
		defer func() { _ = removePartial(part) }()
	}
	var lastErr error
	for attempt := 1; !state.Complete && attempt <= f.opts.Retry; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := size
		if !f.opts.Continue || state.Validator == "" || state.NoRanges {
			start = 0
		}
		began := time.Now()
		if f.opts.Log != nil {
			f.opts.Log.Info("Downloading file", zap.String("file", file), zap.Int("attempt", attempt),
				zap.Int("attempts", f.opts.Retry), zap.Int64("offset", start))
		}
		size, err = f.fetchFile(ctx, sourceURL, part, start, &state)
		if err == nil {
			break
		}
		lastErr = err
		if f.opts.Log != nil {
			f.opts.Log.Warn("Downloading file failed", zap.String("file", file), zap.Int("attempt", attempt),
				zap.Int("attempts", f.opts.Retry), zap.Int64("bytes", size), zap.Int64("total_bytes", state.Total),
				zap.Duration("elapsed", time.Since(began)), zap.String("partial_file", part), zap.Error(err))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) {
			break
		}
		if attempt < f.opts.Retry {
			if err := f.waitRetry(ctx, attempt, lastErr); err != nil {
				return err
			}
		}
	}
	if !state.Complete {
		return fmt.Errorf("download file %q: %w", file, lastErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := processFile(part, filepath.Join(dest, file)); err != nil {
		// A corrupt compressed artifact must be fetched afresh on the next run.
		// Keep the bytes for diagnostics, but never append to or reuse this artifact.
		var invalid invalidDownloadError
		if errors.As(err, &invalid) {
			state.Complete = false
			state.Validator = ""
			return errors.Join(err, saveDownloadState(part, state))
		}
		return err
	}
	if err := removePartial(part); err != nil {
		return fmt.Errorf("remove completed partial download: %w", err)
	}
	if f.opts.Log != nil {
		f.opts.Log.Info("File download completed", zap.String("file", file), zap.Int64("bytes", size))
	}
	return nil
}

func (f fetcher) fetchFile(ctx context.Context, sourceURL, part string, start int64, state *downloadState) (int64, error) {
	resp, err := f.requestResume(ctx, http.MethodGet, sourceURL, start, state.Validator)
	var statusErr httpStatusError
	if start > 0 && errors.As(err, &statusErr) && statusErr.code == http.StatusBadRequest {
		if f.opts.Log != nil {
			f.opts.Log.Info("Server rejected range; falling back to plain GET", zap.String("url", sourceURL),
				zap.Int("status", statusErr.code))
		}
		state.NoRanges = true
		if err := saveDownloadState(part, *state); err != nil {
			return start, err
		}
		// Keep the old bytes until a successful full response is ready to replace them.
		resp, err = f.requestResume(ctx, http.MethodGet, sourceURL, 0, "")
		if err == nil {
			start = 0
		}
	}
	if err != nil {
		return start, err
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		return start, errors.New("server returned encoded content despite Accept-Encoding: identity")
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		total, err := parseUnsatisfiedRange(resp.Header.Get("Content-Range"))
		if err == nil && total == start && state.Total == total && validatorMatches(resp, state.Validator) {
			state.Complete = true
			if err := saveDownloadState(part, *state); err != nil {
				state.Complete = false
				return start, err
			}
			return start, nil
		}
		return start, restartDownload(part, state, errors.New("resume range rejected; restarting download"))
	}
	total := resp.ContentLength
	responseBytes := resp.ContentLength
	if resp.StatusCode == http.StatusPartialContent {
		rangeStart, end, rangeTotal, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return start, restartDownload(part, state, err)
		}
		if rangeStart != start {
			err := fmt.Errorf("resume request from byte %d returned Content-Range starting at byte %d", start, rangeStart)
			return start, restartDownload(part, state, err)
		}
		if start > 0 && ((state.Total >= 0 && state.Total != rangeTotal) ||
			validatorChanged(resp, state.Validator)) {
			return start, restartDownload(part, state, errors.New("resume response changed representation; restarting download"))
		}
		total = rangeTotal
		responseBytes = end - rangeStart + 1
		if resp.ContentLength >= 0 && resp.ContentLength != responseBytes {
			return start, restartDownload(part, state, errors.New("Content-Length does not match Content-Range"))
		}
	} else if start > 0 {
		if f.opts.Log != nil {
			f.opts.Log.Info("Server ignored range or file changed; restarting download", zap.String("url", sourceURL))
		}
		// Consume this full response directly, rather than issuing another request.
		start = 0
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if start > 0 {
		flags = os.O_WRONLY | os.O_APPEND
	}
	out, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return start, fmt.Errorf("prepare partial file: %w", err)
	}
	defer func() { _ = out.Close() }()
	validator := responseValidator(resp)
	if start > 0 {
		validator = state.Validator
	}
	*state = downloadState{URL: sourceURL, Validator: validator, Total: total, NoRanges: state.NoRanges}
	if err := saveDownloadState(part, *state); err != nil {
		return start, err
	}
	size, err := f.copyResponse(out, resp.Body, sourceURL, start)
	if err != nil {
		return size, err
	}
	if (responseBytes >= 0 && size-start != responseBytes) || (total >= 0 && size != total) {
		return size, temporaryError{err: fmt.Errorf("incomplete response: downloaded %d of %d bytes", size, total)}
	}
	if err := out.Sync(); err != nil {
		return size, fmt.Errorf("sync partial file: %w", err)
	}
	if err := out.Close(); err != nil {
		return size, fmt.Errorf("close partial file: %w", err)
	}
	state.Complete = true
	state.Total = size
	if err := saveDownloadState(part, *state); err != nil {
		state.Complete = false
		return size, err
	}
	return size, nil
}

func (f fetcher) copyResponse(out io.Writer, body io.Reader, sourceURL string, size int64) (int64, error) {
	for {
		n, err := io.CopyN(out, body, f.opts.ChunkSize)
		size += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return size, nil
			}
			return size, fmt.Errorf("copy response body: %w", err)
		}
		if f.opts.Verbose && f.opts.Log != nil {
			f.opts.Log.Info("Downloaded chunk", zap.String("url", sourceURL), zap.Int64("bytes", size))
		}
	}
}

func parseContentRange(value string) (int64, int64, int64, error) {
	interval, totalString, ok := strings.Cut(strings.TrimPrefix(value, "bytes "), "/")
	startString, endString, hasEnd := strings.Cut(interval, "-")
	start, startErr := strconv.ParseInt(startString, 10, 64)
	end, endErr := strconv.ParseInt(endString, 10, 64)
	total, totalErr := strconv.ParseInt(totalString, 10, 64)
	if !strings.HasPrefix(value, "bytes ") || !ok || !hasEnd || startErr != nil || endErr != nil || totalErr != nil ||
		start < 0 || end < start || total <= end {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	return start, end, total, nil
}

func parseUnsatisfiedRange(value string) (int64, error) {
	total, err := strconv.ParseInt(strings.TrimPrefix(value, "bytes */"), 10, 64)
	if !strings.HasPrefix(value, "bytes */") || err != nil || total < 0 {
		return 0, fmt.Errorf("invalid unsatisfied Content-Range %q", value)
	}
	return total, nil
}
