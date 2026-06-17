package jsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"metabib/model"
)

type Writer struct {
	basePath string
	maxBytes int64

	file        *os.File
	buf         *bufio.Writer
	partPath    string
	partBytes   int64
	partStartID int64
	partEndID   int64
	partIndex   int
}

func Create(path string, maxBytes int64) (*Writer, error) {
	w := &Writer{basePath: path, maxBytes: maxBytes}
	return w, nil
}

func (w *Writer) Write(rec model.Record) error {
	data, err := json.Marshal(rec)
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
	if w == nil || w.file == nil {
		return nil
	}
	return w.closePart()
}

func (w *Writer) openPart(bookID int64) error {
	if err := os.MkdirAll(filepath.Dir(w.basePath), 0o755); err != nil {
		return fmt.Errorf("create JSONL output directory: %w", err)
	}
	w.partIndex++
	w.partStartID = bookID
	w.partEndID = bookID
	w.partBytes = 0
	w.partPath = fmt.Sprintf("%s.part-%06d.tmp", w.basePath, w.partIndex)
	f, err := os.Create(w.partPath)
	if err != nil {
		return fmt.Errorf("create JSONL output %q: %w", w.partPath, err)
	}
	w.file = f
	w.buf = bufio.NewWriter(f)
	return nil
}

func (w *Writer) closePart() error {
	if w.file == nil {
		return nil
	}
	if err := w.buf.Flush(); err != nil {
		return fmt.Errorf("flush JSONL output %q: %w", w.partPath, err)
	}
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close JSONL output %q: %w", w.partPath, err)
	}
	finalPath := rangedPath(w.basePath, w.partStartID, w.partEndID)
	if err := os.Rename(w.partPath, finalPath); err != nil {
		return fmt.Errorf("rename JSONL output %q to %q: %w", w.partPath, finalPath, err)
	}
	w.file = nil
	w.buf = nil
	w.partPath = ""
	return nil
}

func rangedPath(path string, startID int64, endID int64) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	if ext == "" {
		ext = ".jsonl"
	}
	return fmt.Sprintf("%s.%010d-%010d%s", base, startID, endID, ext)
}
