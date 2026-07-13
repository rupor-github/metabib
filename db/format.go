package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Format string

const (
	FormatFlibustaCurrent Format = "flibusta-current"
	FormatLibrusecCurrent Format = "librusec-current"
)

var ErrUnknownFormat = errors.New("unknown database format")

func DetectDumpFormat(dumps []DumpFile) (Format, error) {
	for _, dump := range dumps {
		if !strings.Contains(strings.ToLower(filepath.Base(dump.Name)), "libbook") {
			continue
		}
		data, err := readDumpPrefix(dump.Path)
		if err != nil {
			return "", err
		}
		flibusta := bytes.Contains(data, []byte("`BookId`"))
		librusec := bytes.Contains(data, []byte("`bid`"))
		switch {
		case flibusta && !librusec:
			return FormatFlibustaCurrent, nil
		case librusec && !flibusta:
			return FormatLibrusecCurrent, nil
		default:
			return "", fmt.Errorf("%w: ambiguous libbook schema in %q", ErrUnknownFormat, dump.Path)
		}
	}
	return "", fmt.Errorf("%w: libbook SQL dump not found", ErrUnknownFormat)
}

func readDumpPrefix(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open SQL dump %q for format detection: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read SQL dump %q for format detection: %w", path, err)
	}
	return buf[:n], nil
}

func (r *Repository) DetectFormat(ctx context.Context) (Format, error) {
	if r.format != "" {
		return r.format, nil
	}
	flibusta, err := r.columnExists(ctx, "libbook", "BookId")
	if err != nil {
		return "", err
	}
	librusecID, err := r.columnExists(ctx, "libbook", "bid")
	if err != nil {
		return "", err
	}
	librusecAuthors, err := r.tableExists(ctx, "libavtors")
	if err != nil {
		return "", err
	}
	librusecGenres, err := r.tableExists(ctx, "libgenres")
	if err != nil {
		return "", err
	}
	librusecSeqs, err := r.tableExists(ctx, "libseqs")
	if err != nil {
		return "", err
	}
	switch {
	case flibusta && !librusecID:
		r.format = FormatFlibustaCurrent
	case librusecID && librusecAuthors && librusecGenres && librusecSeqs && !flibusta:
		r.format = FormatLibrusecCurrent
	default:
		return "", fmt.Errorf("%w: unsupported libbook schema", ErrUnknownFormat)
	}
	return r.format, nil
}
