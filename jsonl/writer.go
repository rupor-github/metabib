package jsonl

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	jsonv2 "encoding/json/v2"
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
	compression Compression
	log         *zap.Logger

	file        *os.File
	closer      interface{ Close() error }
	buf         *bufio.Writer
	stagePath   string
	stagedParts []stagedPart
	committed   bool
}

type stagedPart struct {
	stagePath string
	finalPath string
}

func CreateCompressed(path string, compression Compression) (*Writer, error) {
	path = normalizeBasePath(path, compression)
	return &Writer{basePath: path, compression: compression}, nil
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
	return w.WriteValue(rec)
}

func (w *Writer) WriteValue(value any) error {
	data, err := jsonv2.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL value: %w", err)
	}
	data = append(data, '\n')
	if w.file == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	if _, err := w.buf.Write(data); err != nil {
		return fmt.Errorf("write JSONL value: %w", err)
	}
	return nil
}

func (w *Writer) Close() error {
	return w.Commit()
}

func (w *Writer) Stage() error {
	if w == nil || w.file == nil {
		return nil
	}
	var errs []error
	if err := w.buf.Flush(); err != nil {
		errs = append(errs, fmt.Errorf("flush JSONL output %q: %w", w.stagePath, err))
	}
	if w.closer != nil {
		if err := w.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close JSONL compressor %q: %w", w.stagePath, err))
		}
	}
	if err := w.file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close JSONL output %q: %w", w.stagePath, err))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	w.stagedParts = []stagedPart{{stagePath: w.stagePath, finalPath: w.finalPath()}}
	w.file = nil
	w.closer = nil
	w.buf = nil
	w.stagePath = ""
	return nil
}

func (w *Writer) StagedFinalPaths() []string {
	if w == nil || len(w.stagedParts) == 0 {
		return nil
	}
	paths := make([]string, len(w.stagedParts))
	for idx, part := range w.stagedParts {
		paths[idx] = part.finalPath
	}
	return paths
}

func (w *Writer) Commit() error {
	if w == nil || w.committed {
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
	w.committed = true
	return nil
}

func (w *Writer) Abort() error {
	if w == nil || w.committed {
		return nil
	}
	var errs []error
	if w.closer != nil {
		if err := w.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close JSONL compressor %q: %w", w.stagePath, err))
		}
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close JSONL output %q: %w", w.stagePath, err))
		}
	}
	if w.stagePath != "" {
		if err := removeIfExists(w.stagePath); err != nil {
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
	w.stagePath = ""
	w.stagedParts = nil
	return errors.Join(errs...)
}

func (w *Writer) open() error {
	f, out, closer, err := CreateCompressedFile(w.jsonlPath(), w.compression)
	if err != nil {
		return err
	}
	w.file = f
	w.closer = closer
	w.buf = bufio.NewWriter(out)
	w.stagePath = f.Name()
	return nil
}

func (w *Writer) jsonlPath() string {
	if strings.HasSuffix(strings.ToLower(w.basePath), ".jsonl") {
		return w.basePath
	}
	return w.basePath + ".jsonl"
}

func (w *Writer) finalPath() string {
	return compressedPath(w.jsonlPath(), w.compression)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove JSONL output %q: %w", path, err)
	}
	return nil
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
			path = strings.TrimSuffix(path, path[len(path)-len(suffix):])
			break
		}
	}
	return filepath.Clean(path)
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
