package fetch

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/proxy"

	"metabib/config"
	"metabib/misc"
)

const NewArchivesExitCode = 2

type Options struct {
	Library       config.FetchLibraryConfig
	ArchiveDir    string
	SQLDir        string
	DownloadSQL   bool
	Retry         int
	Timeout       time.Duration
	ChunkSize     int64
	Continue      bool
	Sticky        bool
	Verbose       bool
	Log           *zap.Logger
	UserAgentName string
}

type Result struct {
	LibraryName string
	LastBookID  int
	Archives    int
	SQLTables   int
	SQLDir      string
}

type temporaryError struct {
	err error
}

func (e temporaryError) Error() string { return e.err.Error() }

func (e temporaryError) Unwrap() error { return e.err }

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Retry < 1 {
		return Result{}, fmt.Errorf("retry must be at least 1")
	}
	if opts.Timeout <= 0 {
		return Result{}, fmt.Errorf("timeout must be positive")
	}
	if opts.ChunkSize <= 0 {
		return Result{}, fmt.Errorf("chunk size must be positive")
	}
	if opts.ArchiveDir == "" {
		return Result{}, errors.New("archive output directory is required")
	}

	archiveDir, err := filepath.Abs(opts.ArchiveDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve archive output directory: %w", err)
	}
	libraryName := opts.Library.LibraryName
	if libraryName == "" {
		libraryName = opts.Library.Name
	}
	sqlDir := opts.SQLDir
	if sqlDir == "" {
		sqlDir = libraryName + "_" + time.Now().UTC().Format("20060102_150405")
	}
	sqlDir, err = filepath.Abs(sqlDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve SQL output directory: %w", err)
	}

	client, err := newHTTPClient(opts)
	if err != nil {
		return Result{}, err
	}
	f := fetcher{opts: opts, client: client, userAgent: userAgent(opts)}

	if err := os.MkdirAll(archiveDir, 0o777); err != nil {
		return Result{}, fmt.Errorf("create archive output directory %q: %w", archiveDir, err)
	}
	lastBook, err := getLastBookID(archiveDir)
	if err != nil {
		return Result{}, err
	}
	if opts.Log != nil {
		opts.Log.Info("Processing fetch profile", zap.String("library", libraryName), zap.String("profile", opts.Library.Name), zap.Int("last_book_id", lastBook))
	}

	archiveLinks, err := f.links(ctx, opts.Library.ArchiveURL, opts.Library.ArchivePattern, lastBook, true)
	if err != nil {
		return Result{}, err
	}
	if err := f.files(ctx, archiveLinks, opts.Library.ArchiveURL, archiveDir); err != nil {
		return Result{}, err
	}

	res := Result{LibraryName: libraryName, LastBookID: lastBook, Archives: len(archiveLinks), SQLDir: sqlDir}
	if opts.Log != nil {
		opts.Log.Info("Archive fetch completed", zap.Int("archives", res.Archives), zap.String("directory", archiveDir))
	}
	if !opts.DownloadSQL {
		return res, nil
	}

	sqlLinks, err := f.links(ctx, opts.Library.SQLURL, opts.Library.SQLPattern, 0, false)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(sqlDir, 0o777); err != nil {
		return Result{}, fmt.Errorf("create SQL output directory %q: %w", sqlDir, err)
	}
	if err := f.files(ctx, sqlLinks, opts.Library.SQLURL, sqlDir); err != nil {
		return Result{}, err
	}
	res.SQLTables = len(sqlLinks)
	if opts.Log != nil {
		opts.Log.Info("SQL fetch completed", zap.Int("tables", res.SQLTables), zap.String("directory", sqlDir))
	}
	return res, nil
}

type fetcher struct {
	opts      Options
	client    *http.Client
	userAgent string
}

func (f fetcher) links(ctx context.Context, baseURL string, pattern string, lastBook int, onlyNew bool) ([]string, error) {
	body, err := f.fetchString(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile link pattern: %w", err)
	}
	matches := re.FindAllStringSubmatch(body, -1)
	if matches == nil {
		return nil, fmt.Errorf("no suitable links found at %s", baseURL)
	}
	links := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		if onlyNew {
			ok, _, second, err := dissectRange(match[1])
			if err != nil {
				return nil, err
			}
			if !ok || lastBook >= second {
				continue
			}
		}
		links = append(links, match[1])
	}
	return links, nil
}

func (f fetcher) fetchString(ctx context.Context, sourceURL string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= f.opts.Retry; attempt++ {
		if f.opts.Log != nil {
			f.opts.Log.Info("Downloading index", zap.String("url", sourceURL), zap.Int("attempt", attempt), zap.Int("attempts", f.opts.Retry))
		}
		resp, err := f.request(ctx, http.MethodGet, sourceURL, 0)
		if err == nil {
			timer := time.AfterFunc(f.opts.Timeout, func() { _ = resp.Body.Close() })
			body, readErr := io.ReadAll(resp.Body)
			timer.Stop()
			closeErr := resp.Body.Close()
			if readErr == nil && closeErr == nil {
				return string(body), nil
			}
			if readErr != nil {
				err = readErr
			} else {
				err = closeErr
			}
		}
		lastErr = err
		if !retryable(err) {
			break
		}
		if f.opts.Log != nil {
			f.opts.Log.Warn("Downloading index failed", zap.String("url", sourceURL), zap.Int("attempt", attempt), zap.Int("attempts", f.opts.Retry), zap.Error(err))
		}
	}
	return "", fmt.Errorf("download index %q: %w", sourceURL, lastErr)
}

func (f fetcher) files(ctx context.Context, files []string, baseURL string, dest string) error {
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.file(ctx, file, baseURL, dest); err != nil {
			return err
		}
	}
	return nil
}

func (f fetcher) file(ctx context.Context, file string, baseURL string, dest string) error {
	var start int64
	var tmp string
	var lastErr error
	success := false
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	for attempt := 1; attempt <= f.opts.Retry; attempt++ {
		withRanges := false
		if f.opts.Continue && start > 0 {
			var err error
			withRanges, err = f.acceptsRanges(ctx, joinURL(baseURL, file))
			if err != nil && f.opts.Log != nil {
				f.opts.Log.Warn("Range support check failed", zap.String("file", file), zap.Error(err))
			}
		}
		if !withRanges {
			start = 0
		}
		if f.opts.Log != nil {
			f.opts.Log.Info("Downloading file", zap.String("file", file), zap.Int("attempt", attempt), zap.Int("attempts", f.opts.Retry), zap.Int64("offset", start))
		}
		var err error
		tmp, start, err = f.fetchFile(ctx, joinURL(baseURL, file), tmp, start)
		if err == nil {
			success = true
			break
		}
		lastErr = err
		if !retryable(err) {
			break
		}
		if f.opts.Log != nil {
			f.opts.Log.Warn("Downloading file failed", zap.String("file", file), zap.Int("attempt", attempt), zap.Int("attempts", f.opts.Retry), zap.Error(err))
		}
	}
	if !success {
		return fmt.Errorf("download file %q: %w", file, lastErr)
	}
	if err := processFile(tmp, filepath.Join(dest, file)); err != nil {
		return err
	}
	return nil
}

func (f fetcher) acceptsRanges(ctx context.Context, sourceURL string) (bool, error) {
	resp, err := f.request(ctx, http.MethodHead, sourceURL, 1)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	timer := time.AfterFunc(f.opts.Timeout, func() { _ = resp.Body.Close() })
	defer timer.Stop()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusPartialContent, err
}

func (f fetcher) fetchFile(ctx context.Context, sourceURL string, tmpIn string, start int64) (string, int64, error) {
	tmpOut := tmpIn
	size := start
	var out *os.File
	var err error
	if tmpIn != "" {
		if start > 0 {
			out, err = os.OpenFile(tmpIn, os.O_RDWR|os.O_APPEND, 0o666)
		} else {
			out, err = os.Create(tmpIn)
		}
	} else {
		out, err = os.CreateTemp("", "metabib-fetch-")
		if err == nil {
			tmpOut = out.Name()
		}
	}
	if err != nil {
		return tmpOut, size, fmt.Errorf("prepare temporary file: %w", err)
	}
	defer func() { _ = out.Close() }()

	resp, err := f.request(ctx, http.MethodGet, sourceURL, start)
	if err != nil {
		return tmpOut, size, err
	}
	if start > 0 {
		restart, err := validateResumeResponse(resp, start)
		if err != nil {
			_ = resp.Body.Close()
			return tmpOut, size, err
		}
		if restart {
			_ = resp.Body.Close()
			if err := out.Close(); err != nil {
				return tmpOut, size, fmt.Errorf("close temporary file before restart: %w", err)
			}
			out, err = os.Create(tmpOut)
			if err != nil {
				return tmpOut, 0, fmt.Errorf("restart temporary file: %w", err)
			}
			size = 0
			start = 0
			resp, err = f.request(ctx, http.MethodGet, sourceURL, 0)
			if err != nil {
				return tmpOut, size, err
			}
		}
	}
	defer resp.Body.Close()
	timer := time.AfterFunc(f.opts.Timeout, func() { _ = resp.Body.Close() })
	defer timer.Stop()
	for {
		timer.Reset(f.opts.Timeout)
		read, copyErr := io.CopyN(out, resp.Body, f.opts.ChunkSize)
		size += read
		if copyErr == nil {
			if f.opts.Verbose && f.opts.Log != nil {
				f.opts.Log.Info("Downloaded chunk", zap.String("url", sourceURL), zap.Int64("bytes", size))
			}
			continue
		}
		if errors.Is(copyErr, io.EOF) {
			break
		}
		return tmpOut, size, temporaryError{err: fmt.Errorf("read response body: %w", copyErr)}
	}
	return tmpOut, size, nil
}

func validateResumeResponse(resp *http.Response, start int64) (bool, error) {
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode != http.StatusPartialContent {
		return false, fmt.Errorf("resume request from byte %d returned status %s", start, resp.Status)
	}
	rangeStart, err := parseContentRangeStart(resp.Header.Get("Content-Range"))
	if err != nil {
		return false, err
	}
	if rangeStart != start {
		return false, fmt.Errorf("resume request from byte %d returned Content-Range starting at byte %d", start, rangeStart)
	}
	return false, nil
}

func parseContentRangeStart(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("resume response missing Content-Range")
	}
	if !strings.HasPrefix(strings.ToLower(value), "bytes ") {
		return 0, fmt.Errorf("unsupported Content-Range %q", value)
	}
	rangePart := strings.TrimSpace(value[len("bytes "):])
	dash := strings.IndexByte(rangePart, '-')
	if dash <= 0 {
		return 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	start, err := strconv.ParseInt(rangePart[:dash], 10, 64)
	if err != nil || start < 0 {
		return 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	return start, nil
}

func (f fetcher) request(ctx context.Context, method string, sourceURL string, start int64) (*http.Response, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, method, sourceURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Referer", sourceURL)
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Expires", "0")
	if f.opts.Continue && start > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(start, 10)+"-")
	}
	timer := time.AfterFunc(f.opts.Timeout, cancel)
	resp, err := f.client.Do(req)
	timer.Stop()
	if err != nil {
		cancel()
		return nil, temporaryError{err: err}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		cancel()
		// return nil, fmt.Errorf("status %s", resp.Status)
		// Make all errors re-tryable - here "502 bad gateway" is often
		// transient
		return nil, temporaryError{err: fmt.Errorf("status %s", resp.Status)}
	}
	resp.Body = cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func newHTTPClient(opts Options) (*http.Client, error) {
	transport := &http.Transport{DisableKeepAlives: true}
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
			dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, proxy.Direct)
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
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
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

func retryable(err error) bool {
	var temp temporaryError
	return errors.As(err, &temp)
}

func getLastBookID(path string) (int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, fmt.Errorf("read archive directory %q: %w", path, err)
	}
	lastBegin, lastEnd := 0, 0
	mergeBegin, mergeEnd, mergeCount := 0, 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if ok, first, second, err := dissectRange(name); err != nil {
			return 0, err
		} else if ok && lastEnd < second {
			lastBegin = first
			lastEnd = second
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if ok, first, second, err := dissectMergeName(name); err != nil {
			return 0, err
		} else if ok {
			mergeBegin = first
			mergeEnd = second
			mergeCount++
		}
	}
	if mergeCount > 1 {
		return 0, errors.New("there could only be single merge archive")
	}
	if mergeCount == 0 {
		return lastEnd, nil
	}
	if mergeBegin < lastBegin || (mergeBegin > lastBegin && mergeBegin <= lastEnd) || mergeEnd < lastEnd {
		return 0, fmt.Errorf("merge (%d:%d) and last (%d:%d) archive do not match", mergeBegin, mergeEnd, lastBegin, lastEnd)
	}
	return mergeEnd, nil
}

var (
	mergeNameRE = regexp.MustCompile(`(?i)\s*fb2-([0-9]+)-([0-9]+)\.merging`)
	rangeRE     = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]+)-([0-9]+)(?:\.zip|\.[0-9]+\.fb2\.zip)`)
)

func dissectMergeName(name string) (bool, int, int, error) {
	return dissect(mergeNameRE, name)
}

func dissectRange(name string) (bool, int, int, error) {
	return dissect(rangeRE, name)
}

func dissect(re *regexp.Regexp, name string) (bool, int, int, error) {
	match := re.FindStringSubmatch(name)
	if match == nil {
		return false, 0, 0, nil
	}
	first, err := strconv.Atoi(match[1])
	if err != nil {
		return true, 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	second, err := strconv.Atoi(match[2])
	if err != nil {
		return true, 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	return true, first, second, nil
}

func joinURL(baseURL string, file string) string {
	if strings.HasSuffix(baseURL, "/") {
		return baseURL + file
	}
	return baseURL + "/" + file
}

func processFile(tmp string, file string) error {
	switch ext := strings.ToLower(filepath.Ext(file)); ext {
	case ".zip":
		if err := checkZip(tmp); err != nil {
			return err
		}
		return copyFileContents(tmp, file)
	case ".gz":
		return ungzipFile(tmp, strings.TrimSuffix(file, ext))
	default:
		return fmt.Errorf("unknown downloaded file extension %q", ext)
	}
}

func checkZip(file string) error {
	r, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("check zip %q: %w", file, err)
	}
	defer r.Close()
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("check zip entry %q: %w", f.Name, err)
		}
		if err := rc.Close(); err != nil {
			return fmt.Errorf("close zip entry %q: %w", f.Name, err)
		}
	}
	return nil
}

func copyFileContents(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	defer out.Close()
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %q to %q: %w", src, dst, err)
	}
	return out.Sync()
}

func ungzipFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open gzip %q: %w", src, err)
	}
	defer in.Close()
	r, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("read gzip %q: %w", src, err)
	}
	defer r.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("decompress %q to %q: %w", src, dst, err)
	}
	return nil
}

func userAgent(opts Options) string {
	name := opts.UserAgentName
	if name == "" {
		name = misc.GetAppName()
	}
	version := misc.GetVersion()
	if opts.Library.UserAgentSuffix != "" {
		return fmt.Sprintf("%s/%s %s", name, version, opts.Library.UserAgentSuffix)
	}
	return fmt.Sprintf("%s/%s", name, version)
}
