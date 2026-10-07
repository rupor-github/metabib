package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	rand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

var (
	errHeaderTimeout     = errors.New("connection/response header timeout")
	errInactivityTimeout = errors.New("download inactivity timeout")
)

type temporaryError struct {
	err        error
	retryAfter time.Duration
}

func (e temporaryError) Error() string { return e.err.Error() }

func (e temporaryError) Unwrap() error { return e.err }

type httpStatusError struct {
	code   int
	status string
	url    string
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("status %s from %s", e.status, e.url)
}

func retryable(err error) bool {
	var temp temporaryError
	return errors.As(err, &temp)
}

func (f fetcher) request(ctx context.Context, method string, sourceURL string, start int64) (*http.Response, error) {
	return f.requestResume(ctx, method, sourceURL, start, "")
}

func (f fetcher) requestResume(ctx context.Context, method, sourceURL string, start int64, validator string) (*http.Response, error) {
	reqCtx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(reqCtx, method, sourceURL, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	req.Header.Set("Referer", sourceURL)
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Expires", "0")
	// Byte offsets must refer to the downloaded representation, without transparent decoding.
	req.Header.Set("Accept-Encoding", "identity")
	if start > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(start, 10)+"-")
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	timer := time.AfterFunc(f.opts.Timeout, func() { cancel(errHeaderTimeout) })
	resp, err := f.client.Do(req)
	timer.Stop()
	if err != nil {
		cause := context.Cause(reqCtx)
		cancel(nil)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cause != nil {
			err = cause
		} else {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				err = fmt.Errorf("%w: %w", errHeaderTimeout, err)
			}
		}
		return nil, temporaryError{err: err}
	}
	if f.opts.Log != nil {
		f.opts.Log.Info("Download response", zap.String("url", sourceURL), zap.String("final_url", resp.Request.URL.String()),
			zap.Int("status", resp.StatusCode), zap.Int64("content_length", resp.ContentLength),
			zap.String("content_range", resp.Header.Get("Content-Range")), zap.String("etag", resp.Header.Get("ETag")),
			zap.String("last_modified", resp.Header.Get("Last-Modified")), zap.String("accept_ranges", resp.Header.Get("Accept-Ranges")),
			zap.Strings("transfer_encoding", resp.TransferEncoding))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent &&
		!(start > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable) {
		_ = resp.Body.Close()
		cancel(nil)
		err := httpStatusError{code: resp.StatusCode, status: resp.Status, url: resp.Request.URL.String()}
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, temporaryError{err: err, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
		}
		return nil, err
	}
	resp.Body = &idleReadCloser{ReadCloser: resp.Body, timeout: f.opts.Timeout, cancel: cancel, cause: func() error {
		return context.Cause(reqCtx)
	}}
	return resp, nil
}

// The timer runs only while waiting for a network read. Slow progress and disk writes
// do not consume a fixed chunk deadline. Close/cancel releases any blocked read.
type idleReadCloser struct {
	io.ReadCloser
	timeout time.Duration
	cancel  context.CancelCauseFunc
	cause   func() error
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	timer := time.AfterFunc(r.timeout, func() { r.cancel(errInactivityTimeout) })
	n, err := r.ReadCloser.Read(p)
	timer.Stop()
	if cause := r.cause(); cause != nil {
		return n, temporaryError{err: cause}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, temporaryError{err: err}
	}
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.cancel(nil)
	return r.ReadCloser.Close()
}

const maxRetryAfter = 5 * time.Minute

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		// Clamp before multiplication to avoid overflowing time.Duration.
		return time.Duration(min(max(seconds, 0), int64(maxRetryAfter/time.Second))) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return min(max(date.Sub(now), 0), maxRetryAfter)
	}
	return 0
}

func retryDelay(attempt int, err error) time.Duration {
	ceiling := min(500*time.Millisecond*time.Duration(1<<min(max(attempt-1, 0), 6)), 30*time.Second)
	delay := ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
	var temp temporaryError
	if errors.As(err, &temp) {
		delay = max(delay, temp.retryAfter)
	}
	return delay
}

func (f fetcher) waitRetry(ctx context.Context, attempt int, err error) error {
	delay := retryDelay(attempt, err)
	if f.opts.Log != nil {
		f.opts.Log.Info("Waiting before retry", zap.Int("attempt", attempt+1), zap.Duration("delay", delay))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newHTTPClient(opts Options) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Fetch proxies are configured per profile, rather than through environment variables.
	transport.Proxy = nil
	transport.DisableCompression = true
	direct := &net.Dialer{Timeout: opts.Timeout, KeepAlive: 30 * time.Second}
	transport.DialContext = direct.DialContext
	transport.TLSHandshakeTimeout = opts.Timeout
	transport.ResponseHeaderTimeout = opts.Timeout
	if opts.Library.Proxy != "" {
		proxyURL, err := url.Parse(opts.Library.Proxy)
		if err != nil {
			return nil, fmt.Errorf("parse proxy URL: %w", err)
		}
		switch proxyURL.Scheme {
		case "socks5":
			var auth *proxy.Auth
			if proxyURL.User != nil && proxyURL.User.Username() != "" {
				password, _ := proxyURL.User.Password()
				auth = &proxy.Auth{User: proxyURL.User.Username(), Password: password}
			}
			dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, direct)
			if err != nil {
				return nil, fmt.Errorf("create SOCKS5 proxy dialer: %w", err)
			}
			contextDialer, ok := dialer.(proxy.ContextDialer)
			if !ok {
				return nil, errors.New("SOCKS5 dialer does not support context cancellation")
			}
			transport.DialContext = contextDialer.DialContext
		case "http", "https":
			transport.Proxy = http.ProxyURL(proxyURL)
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
		}
	}
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after 5 redirects")
		}
		if opts.Sticky {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return client, nil
}
