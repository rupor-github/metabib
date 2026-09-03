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

	regexp2 "github.com/dlclark/regexp2/v2"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"

	"metabib/config"
	"metabib/internal/fileutil"
	"metabib/misc"
)

const NewArchivesExitCode = 2

const profilePatternTimeout = time.Second

const (
	archiveFamilyFB2 = "fb2"
	archiveFamilyUSR = "usr"
)

type Options struct {
	Library       config.FetchLibraryConfig
	ArchiveDir    string
	SQLDir        string
	NoArchives    bool
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

type archiveHighWater struct {
	Begin int
	End   int
	Daily bool
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
	if opts.NoArchives && !opts.DownloadSQL {
		return Result{}, errors.New("nothing to fetch: --noarchives and --nosql cannot be used together")
	}
	if !opts.NoArchives && opts.ArchiveDir == "" {
		return Result{}, errors.New("archive output directory is required")
	}

	var err error
	archiveDir := ""
	if !opts.NoArchives {
		archiveDir, err = filepath.Abs(opts.ArchiveDir)
		if err != nil {
			return Result{}, fmt.Errorf("resolve archive output directory: %w", err)
		}
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

	highWater := map[string]archiveHighWater{archiveFamilyFB2: {}, archiveFamilyUSR: {}}
	if !opts.NoArchives {
		if err := os.MkdirAll(archiveDir, 0o777); err != nil {
			return Result{}, fmt.Errorf("create archive output directory %q: %w", archiveDir, err)
		}
		var err error
		highWater, err = getArchiveHighWater(archiveDir)
		if err != nil {
			return Result{}, err
		}
	}
	if opts.Log != nil {
		opts.Log.Info(
			"Processing fetch profile",
			zap.String("library", libraryName),
			zap.String("profile", opts.Library.Name),
			zap.String("archive_content", archiveContent(opts.Library)),
			zap.Int("fb2_last_book_id", highWater[archiveFamilyFB2].End),
			zap.Int("usr_last_book_id", highWater[archiveFamilyUSR].End),
		)
	}

	archiveLinks := []string(nil)
	if !opts.NoArchives {
		var err error
		archiveLinks, err = f.archiveLinks(ctx, opts.Library.ArchiveURL, opts.Library.ArchivePattern, archiveDir, highWater)
		if err != nil {
			return Result{}, err
		}
		if err := f.files(ctx, archiveLinks, opts.Library.ArchiveURL, archiveDir); err != nil {
			return Result{}, err
		}
	}

	res := Result{LibraryName: libraryName, LastBookID: max(highWater[archiveFamilyFB2].End, highWater[archiveFamilyUSR].End), Archives: len(archiveLinks), SQLDir: sqlDir}
	if opts.Log != nil && !opts.NoArchives {
		opts.Log.Info("Archive fetch completed", zap.Int("archives", res.Archives), zap.String("directory", archiveDir))
	}
	if !opts.DownloadSQL {
		return res, nil
	}

	sqlLinks, err := f.links(ctx, opts.Library.SQLURL, opts.Library.SQLPattern)
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

func (f fetcher) archiveLinks(ctx context.Context, baseURL string, pattern string, dest string, highWater map[string]archiveHighWater) ([]string, error) {
	links, err := f.links(ctx, baseURL, pattern)
	if err != nil {
		return nil, err
	}
	content := archiveContent(f.opts.Library)
	selected := make([]string, 0, len(links))
	for _, link := range links {
		family, _, second, ok, err := classifyUpdateName(link)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("archive pattern selected unsupported update name %q", link)
		}
		if !archiveContentAccepts(content, family) {
			return nil, fmt.Errorf("archive pattern selected %s update %q for %s profile", family, link, content)
		}
		last := highWater[family]
		if last.End < second || (last.End == second && last.Daily && !fileExists(filepath.Join(dest, link))) {
			selected = append(selected, link)
		}
	}
	return selected, nil
}

func (f fetcher) links(ctx context.Context, baseURL string, pattern string) ([]string, error) {
	body, err := f.fetchString(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return nil, fmt.Errorf("compile link pattern: %w", err)
	}
	re.MatchTimeout = profilePatternTimeout
	match, err := re.FindStringMatch(body)
	if err != nil {
		return nil, fmt.Errorf("match links at %s: %w", baseURL, err)
	}
	if match == nil {
		return nil, fmt.Errorf("no suitable links found at %s", baseURL)
	}
	links := make([]string, 0)
	for match != nil {
		if match.GroupCount() < 2 {
			return nil, fmt.Errorf("link pattern must capture file name as first group")
		}
		links = append(links, match.GroupByNumber(1).String())
		match, err = re.FindNextMatch(match)
		if err != nil {
			return nil, fmt.Errorf("match links at %s: %w", baseURL, err)
		}
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
		out, err = fileutil.CreateHiddenTemp("", "metabib-fetch")
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
	highWater, err := getArchiveHighWater(path)
	if err != nil {
		return 0, err
	}
	return highWater[archiveFamilyFB2].End, nil
}

func getArchiveHighWater(path string) (map[string]archiveHighWater, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read archive directory %q: %w", path, err)
	}
	highWater := map[string]archiveHighWater{archiveFamilyFB2: {}, archiveFamilyUSR: {}}
	mergeBegin := map[string]int{archiveFamilyFB2: 0, archiveFamilyUSR: 0}
	mergeEnd := map[string]int{archiveFamilyFB2: 0, archiveFamilyUSR: 0}
	mergeCount := map[string]int{archiveFamilyFB2: 0, archiveFamilyUSR: 0}
	for _, entry := range entries {
		name := entry.Name()
		family, first, second, daily, ok, err := classifyArchiveRangeName(name)
		if err != nil {
			return nil, err
		}
		if ok && highWater[family].End < second {
			highWater[family] = archiveHighWater{Begin: first, End: second, Daily: daily}
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if family, first, second, ok, err := classifyMergeName(name); err != nil {
			return nil, err
		} else if ok {
			mergeBegin[family] = first
			mergeEnd[family] = second
			mergeCount[family]++
		}
	}
	for _, family := range []string{archiveFamilyFB2, archiveFamilyUSR} {
		if mergeCount[family] > 1 {
			return nil, fmt.Errorf("there could only be single %s merge archive", family)
		}
		if mergeCount[family] == 0 {
			continue
		}
		last := highWater[family]
		if mergeBegin[family] < last.Begin ||
			(mergeBegin[family] > last.Begin && mergeBegin[family] <= last.End) ||
			mergeEnd[family] < last.End {
			return nil, fmt.Errorf(
				"%s merge (%d:%d) and last (%d:%d) archive do not match",
				family,
				mergeBegin[family],
				mergeEnd[family],
				last.Begin,
				last.End,
			)
		}
		highWater[family] = archiveHighWater{Begin: mergeBegin[family], End: mergeEnd[family]}
	}
	return highWater, nil
}

var (
	mergeNameRE         = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.merging$`)
	localRangeRE        = regexp.MustCompile(`(?i)^([a-z0-9]+)-([0-9]+)-([0-9]+)\.zip$`)
	plainFB2UpdateRE    = regexp.MustCompile(`(?i)^([0-9]+)-([0-9]+)\.zip$`)
	flibustaFB2UpdateRE = regexp.MustCompile(`(?i)^f(?:\.fb2)?\.([0-9]+)-([0-9]+)\.zip$`)
	flibustaAnyUpdateRE = regexp.MustCompile(`(?i)^f\.([^.]+)\.([0-9]+)-([0-9]+)\.zip$`)
	librusecAnyUpdateRE = regexp.MustCompile(`(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.([^.]+)\.zip$`)
)

func classifyMergeName(name string) (string, int, int, bool, error) {
	match := mergeNameRE.FindStringSubmatch(name)
	if match == nil || !knownArchiveFamily(match[1]) {
		return "", 0, 0, false, nil
	}
	first, second, err := parseRange(match[2], match[3], name)
	return strings.ToLower(match[1]), first, second, true, err
}

func classifyArchiveRangeName(name string) (string, int, int, bool, bool, error) {
	if family, first, second, ok, err := classifyLocalRangeName(name); ok || err != nil {
		return family, first, second, false, ok, err
	}
	family, first, second, ok, err := classifyUpdateName(name)
	return family, first, second, true, ok, err
}

func classifyRangeName(name string) (string, int, int, bool, error) {
	family, first, second, _, ok, err := classifyArchiveRangeName(name)
	return family, first, second, ok, err
}

func classifyLocalRangeName(name string) (string, int, int, bool, error) {
	match := localRangeRE.FindStringSubmatch(name)
	if match == nil || !knownArchiveFamily(match[1]) {
		return "", 0, 0, false, nil
	}
	first, second, err := parseRange(match[2], match[3], name)
	return strings.ToLower(match[1]), first, second, true, err
}

func classifyUpdateName(name string) (string, int, int, bool, error) {
	if match := plainFB2UpdateRE.FindStringSubmatch(name); match != nil {
		first, second, err := parseRange(match[1], match[2], name)
		return archiveFamilyFB2, first, second, true, err
	}
	if match := flibustaFB2UpdateRE.FindStringSubmatch(name); match != nil {
		first, second, err := parseRange(match[1], match[2], name)
		return archiveFamilyFB2, first, second, true, err
	}
	if match := flibustaAnyUpdateRE.FindStringSubmatch(name); match != nil {
		if strings.EqualFold(match[1], archiveFamilyFB2) {
			first, second, err := parseRange(match[2], match[3], name)
			return archiveFamilyFB2, first, second, true, err
		}
		first, second, err := parseRange(match[2], match[3], name)
		return archiveFamilyUSR, first, second, true, err
	}
	match := librusecAnyUpdateRE.FindStringSubmatch(name)
	if match == nil {
		return "", 0, 0, false, nil
	}
	family := archiveFamilyUSR
	if strings.EqualFold(match[3], archiveFamilyFB2) {
		family = archiveFamilyFB2
	}
	first, second, err := parseRange(match[1], match[2], name)
	return family, first, second, true, err
}

func dissectRange(name string) (bool, int, int, error) {
	_, first, second, ok, err := classifyRangeName(name)
	return ok, first, second, err
}

func parseRange(firstText string, secondText string, name string) (int, int, error) {
	first, err := strconv.Atoi(firstText)
	if err != nil {
		return 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	second, err := strconv.Atoi(secondText)
	if err != nil {
		return 0, 0, fmt.Errorf("dissect %q: %w", name, err)
	}
	return first, second, nil
}

func knownArchiveFamily(family string) bool {
	family = strings.ToLower(family)
	return family == archiveFamilyFB2 || family == archiveFamilyUSR
}

func archiveContent(lib config.FetchLibraryConfig) string {
	if lib.ArchiveContent == "" {
		return archiveFamilyFB2
	}
	return strings.ToLower(lib.ArchiveContent)
}

func archiveContentAccepts(content string, family string) bool {
	return content == "all" || content == family
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
