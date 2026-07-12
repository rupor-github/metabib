package jsonl

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	jsonv2 "encoding/json/v2"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/model"
)

type Compression string

const (
	CompressionNone Compression = "none"
	CompressionGzip Compression = "gz"
	CompressionZstd Compression = "zstd"
	CompressionZip  Compression = "zip"
)

type Writer struct {
	basePath    string
	maxBytes    int64
	compression Compression
	log         *zap.Logger
	finalPaths  map[string]struct{}
	stagedParts []stagedPart

	file        *os.File
	closer      io.Closer
	buf         *bufio.Writer
	partPath    string
	partBytes   int64
	partStartID int64
	partEndID   int64
	partIndex   int
}

type stagedPart struct {
	stagePath string
	finalPath string
}

func Create(path string, maxBytes int64) (*Writer, error) {
	return CreateCompressed(path, maxBytes, CompressionNone)
}

func CreateCompressed(path string, maxBytes int64, compression Compression) (*Writer, error) {
	path = normalizeBasePath(path, compression)
	w := &Writer{basePath: path, maxBytes: maxBytes, compression: compression, finalPaths: make(map[string]struct{})}
	return w, nil
}

func (w *Writer) WithLogger(log *zap.Logger) *Writer {
	w.log = log
	return w
}

func ParseCompression(value string) (Compression, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "zstd", "zst":
		return CompressionZstd, nil
	case "gz", "gzip":
		return CompressionGzip, nil
	case "zip":
		return CompressionZip, nil
	case "none", "no", "off", "false":
		return CompressionNone, nil
	default:
		return "", fmt.Errorf("invalid JSONL output compression %q", value)
	}
}

func (w *Writer) Write(rec model.Record) error {
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	data = append(data, '\n')
	if w.file != nil && w.maxBytes > 0 && w.partBytes > 0 && w.partBytes+int64(len(data)) > w.maxBytes {
		if err := w.closePart(); err != nil {
			return err
		}
	}
	if w.file == nil {
		if err := w.openPart(rec.ID.BookID); err != nil {
			return err
		}
	}
	if _, err := w.buf.Write(data); err != nil {
		return fmt.Errorf("write JSONL record: %w", err)
	}
	w.partBytes += int64(len(data))
	w.partEndID = rec.ID.BookID
	return nil
}

func (w *Writer) Close() error {
	return w.Commit()
}

func (w *Writer) Stage() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.closePart()
}

func (w *Writer) Commit() error {
	if w == nil {
		return nil
	}
	if err := w.Stage(); err != nil {
		return err
	}
	for _, part := range w.stagedParts {
		if _, err := os.Stat(part.finalPath); err == nil {
			if w.log != nil {
				w.log.Warn("Overwriting existing JSONL output", zap.String("file", part.finalPath))
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat JSONL output %q: %w", part.finalPath, err)
		}
	}
	for _, part := range w.stagedParts {
		if err := fileutil.ReplaceOutputFile(part.stagePath, part.finalPath); err != nil {
			return fmt.Errorf("rename JSONL output %q to %q: %w", part.stagePath, part.finalPath, err)
		}
	}
	w.stagedParts = nil
	return nil
}

func (w *Writer) Abort() error {
	if w == nil {
		return nil
	}
	var errs []error
	if w.closer != nil {
		if err := w.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close JSONL compressor %q: %w", w.partPath, err))
		}
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close JSONL output %q: %w", w.partPath, err))
		}
	}
	if w.partPath != "" {
		if err := removeIfExists(w.partPath); err != nil {
			errs = append(errs, err)
		}
	}
	for _, part := range w.stagedParts {
		if err := removeIfExists(part.stagePath); err != nil {
			errs = append(errs, err)
		}
	}
	w.file = nil
	w.closer = nil
	w.buf = nil
	w.partPath = ""
	w.stagedParts = nil
	return errors.Join(errs...)
}

func (w *Writer) openPart(bookID int64) error {
	if err := os.MkdirAll(filepath.Dir(w.basePath), 0o755); err != nil {
		return fmt.Errorf("create JSONL output directory: %w", err)
	}
	w.partIndex++
	w.partStartID = bookID
	w.partEndID = bookID
	w.partBytes = 0
	pattern := fmt.Sprintf("%s.part-%06d-*.tmp", filepath.Base(w.basePath), w.partIndex)
	f, err := os.CreateTemp(filepath.Dir(w.basePath), pattern)
	if err != nil {
		return fmt.Errorf("create JSONL output %q: %w", filepath.Join(filepath.Dir(w.basePath), pattern), err)
	}
	w.partPath = f.Name()
	out, closer, err := w.compressedWriter(f)
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.closer = closer
	w.buf = bufio.NewWriter(out)
	return nil
}

func (w *Writer) closePart() error {
	if w.file == nil {
		return nil
	}
	var errs []error
	if err := w.buf.Flush(); err != nil {
		errs = append(errs, fmt.Errorf("flush JSONL output %q: %w", w.partPath, err))
	}
	if w.closer != nil {
		if err := w.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close JSONL compressor %q: %w", w.partPath, err))
		}
	}
	if err := w.file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close JSONL output %q: %w", w.partPath, err))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	finalJSONLPath := w.finalJSONLPath()
	finalPath := compressedPath(finalJSONLPath, w.compression)
	partPath := w.partPath
	if w.compression == CompressionZip {
		var err error
		partPath, err = w.zipPart(finalJSONLPath)
		if err != nil {
			return err
		}
	}
	w.stagedParts = append(w.stagedParts, stagedPart{stagePath: partPath, finalPath: finalPath})
	w.finalPaths[finalPath] = struct{}{}
	w.file = nil
	w.closer = nil
	w.buf = nil
	w.partPath = ""
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove JSONL output %q: %w", path, err)
	}
	return nil
}

func (w *Writer) finalJSONLPath() string {
	path := rangedPath(w.basePath, w.partStartID, w.partEndID)
	compressed := compressedPath(path, w.compression)
	reason := ""
	if w.partStartID == 0 || w.partEndID == 0 {
		reason = "zero_book_id"
	} else if _, exists := w.finalPaths[compressed]; exists {
		reason = "repeated_range"
	}
	if reason == "" {
		return path
	}
	partPath := rangedPartPath(w.basePath, w.partStartID, w.partEndID, w.partIndex)
	if w.log != nil {
		w.log.Warn(
			"Changing JSONL output path to avoid overwriting a non-unique range",
			zap.String("reason", reason),
			zap.String("range_file", compressed),
			zap.String("file", compressedPath(partPath, w.compression)),
			zap.Int("part", w.partIndex),
		)
	}
	return partPath
}

func (w *Writer) compressedWriter(f *os.File) (io.Writer, io.Closer, error) {
	switch w.compression {
	case CompressionNone:
		return f, nil, nil
	case CompressionGzip:
		gz := gzip.NewWriter(f)
		return gz, gz, nil
	case CompressionZstd:
		enc, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
		if err != nil {
			return nil, nil, fmt.Errorf("create JSONL zstd compressor %q: %w", w.partPath, err)
		}
		return enc, enc, nil
	case CompressionZip:
		return f, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported JSONL output compression %q", w.compression)
	}
}

func (w *Writer) zipPart(finalJSONLPath string) (string, error) {
	zipPath := w.partPath + ".zip"
	out, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("create JSONL zip output %q: %w", zipPath, err)
	}
	zw := zip.NewWriter(out)
	entry, err := zw.Create(filepath.Base(finalJSONLPath))
	if err != nil {
		zw.Close()
		out.Close()
		return "", fmt.Errorf("create JSONL zip entry %q: %w", finalJSONLPath, err)
	}
	in, err := os.Open(w.partPath)
	if err != nil {
		zw.Close()
		out.Close()
		return "", fmt.Errorf("open JSONL output %q: %w", w.partPath, err)
	}
	if _, err := io.Copy(entry, in); err != nil {
		in.Close()
		zw.Close()
		out.Close()
		return "", fmt.Errorf("write JSONL zip entry %q: %w", finalJSONLPath, err)
	}
	if err := in.Close(); err != nil {
		zw.Close()
		out.Close()
		return "", fmt.Errorf("close JSONL output %q: %w", w.partPath, err)
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return "", fmt.Errorf("close JSONL zip writer %q: %w", zipPath, err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close JSONL zip output %q: %w", zipPath, err)
	}
	if err := os.Remove(w.partPath); err != nil {
		return "", fmt.Errorf("remove JSONL output %q: %w", w.partPath, err)
	}
	return zipPath, nil
}

func rangedPath(path string, startID int64, endID int64) string {
	return fmt.Sprintf("%s.%010d-%010d.jsonl", path, startID, endID)
}

func rangedPartPath(path string, startID int64, endID int64, partIndex int) string {
	return fmt.Sprintf("%s.%010d-%010d.part-%06d.jsonl", path, startID, endID, partIndex)
}

func compressedPath(path string, compression Compression) string {
	switch compression {
	case CompressionGzip:
		return path + ".gz"
	case CompressionZstd:
		return path + ".zst"
	case CompressionZip:
		return path + ".zip"
	default:
		return path
	}
}

func normalizeBasePath(path string, compression Compression) string {
	for _, suffix := range compressionSuffixes(compression) {
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			return strings.TrimSuffix(path, path[len(path)-len(suffix):])
		}
	}
	return path
}

func compressionSuffixes(compression Compression) []string {
	switch compression {
	case CompressionGzip:
		return []string{".gz", ".gzip"}
	case CompressionZstd:
		return []string{".zst", ".zstd"}
	case CompressionZip:
		return []string{".zip"}
	default:
		return nil
	}
}
