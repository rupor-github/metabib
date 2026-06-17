package jsonl

import (
	"encoding/json"
	"fmt"
	"os"

	"metabib/model"
)

type Writer struct {
	file *os.File
	enc  *json.Encoder
}

func Create(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create JSONL output %q: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &Writer{file: f, enc: enc}, nil
}

func (w *Writer) Write(rec model.Record) error {
	if err := w.enc.Encode(rec); err != nil {
		return fmt.Errorf("write JSONL record: %w", err)
	}
	return nil
}

func (w *Writer) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}
