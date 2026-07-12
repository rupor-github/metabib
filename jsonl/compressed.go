package jsonl

import (
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func CompressedPath(path string, compression Compression) string {
	return compressedPath(normalizeBasePath(path, compression), compression)
}

func CreateCompressedFile(path string, compression Compression) (*os.File, io.Writer, io.Closer, error) {
	path = normalizeBasePath(path, compression)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create output directory: %w", err)
	}
	pattern := filepath.Base(path) + "-*.tmp"
	f, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create output %q: %w", filepath.Join(filepath.Dir(path), pattern), err)
	}
	switch compression {
	case CompressionNone:
		return f, f, nil, nil
	case CompressionGzip:
		gz := gzip.NewWriter(f)
		return f, gz, gz, nil
	case CompressionZstd:
		enc, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
		if err != nil {
			f.Close()
			return nil, nil, nil, fmt.Errorf("create zstd compressor %q: %w", f.Name(), err)
		}
		return f, enc, enc, nil
	case CompressionZip:
		zw := zip.NewWriter(f)
		name := filepath.Base(path)
		entry, err := zw.Create(name)
		if err != nil {
			zw.Close()
			f.Close()
			return nil, nil, nil, fmt.Errorf("create zip entry %q: %w", name, err)
		}
		return f, entry, zw, nil
	default:
		f.Close()
		return nil, nil, nil, fmt.Errorf("unsupported compression %q", compression)
	}
}

func OpenCompressedFile(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open compressed input %q: %w", path, err)
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".gzip"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("open gzip input %q: %w", path, err)
		}
		return compressedReadCloser{Reader: gz, closers: []io.Closer{gz, f}}, nil
	case strings.HasSuffix(lower, ".zst") || strings.HasSuffix(lower, ".zstd"):
		zr, err := zstd.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("open zstd input %q: %w", path, err)
		}
		return compressedReadCloser{Reader: zr, closers: []io.Closer{zstdCloser{zr}, f}}, nil
	case strings.HasSuffix(lower, ".zip"):
		f.Close()
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("open zip input %q: %w", path, err)
		}
		if len(zr.File) != 1 {
			zr.Close()
			return nil, fmt.Errorf("zip input %q has %d entries, want 1", path, len(zr.File))
		}
		r, err := zr.File[0].Open()
		if err != nil {
			zr.Close()
			return nil, fmt.Errorf("open zip input entry %q: %w", path, err)
		}
		return compressedReadCloser{Reader: r, closers: []io.Closer{r, zr}}, nil
	default:
		return f, nil
	}
}

type compressedReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (r compressedReadCloser) Close() error {
	var err error
	for _, closer := range r.closers {
		if closeErr := closer.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

type zstdCloser struct {
	*zstd.Decoder
}

func (c zstdCloser) Close() error {
	c.Decoder.Close()
	return nil
}
