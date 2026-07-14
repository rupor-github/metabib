package jsonl

import (
	"context"
	"fmt"
	"io"
	"iter"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"metabib/model"
)

type DatasetValue struct {
	Header  bool
	Dataset model.Dataset
	Record  model.DatasetRecord
}

func DatasetValues(ctx context.Context, path string) iter.Seq2[DatasetValue, error] {
	return func(yield func(DatasetValue, error) bool) {
		r, err := OpenCompressedFile(path)
		if err != nil {
			yield(DatasetValue{}, err)
			return
		}
		closed := false
		closeReader := func() error {
			if closed {
				return nil
			}
			closed = true
			return r.Close()
		}
		defer closeReader()

		dec := jsontext.NewDecoder(r)
		var dataset model.Dataset
		if err := jsonv2.UnmarshalDecode(dec, &dataset); err != nil {
			if err == io.EOF {
				yield(DatasetValue{}, fmt.Errorf("dataset JSONL %q is empty", path))
				return
			}
			yield(DatasetValue{}, fmt.Errorf("decode dataset header %q: %w", path, err))
			return
		}
		if err := validateDatasetHeader(path, dataset); err != nil {
			yield(DatasetValue{}, err)
			return
		}
		if !yield(DatasetValue{Header: true, Dataset: dataset}, nil) {
			return
		}

		var records int64
		for records < dataset.Records {
			if err := ctx.Err(); err != nil {
				yield(DatasetValue{}, err)
				return
			}
			var rec model.DatasetRecord
			if err := jsonv2.UnmarshalDecode(dec, &rec); err != nil {
				if err == io.EOF {
					yield(DatasetValue{}, fmt.Errorf(
						"dataset JSONL %q record count mismatch: declared %d, read %d",
						path,
						dataset.Records,
						records,
					))
					return
				}
				yield(DatasetValue{}, fmt.Errorf("decode dataset record %q: %w", path, err))
				return
			}
			if rec.Schema != dataset.RecordSchema {
				yield(DatasetValue{}, fmt.Errorf(
					"dataset JSONL %q record %d has schema %q, want %q",
					path,
					records+1,
					rec.Schema,
					dataset.RecordSchema,
				))
				return
			}
			if !yield(DatasetValue{Record: rec}, nil) {
				return
			}
			records++
		}

		var extra any
		if err := jsonv2.UnmarshalDecode(dec, &extra); err == nil {
			yield(DatasetValue{}, fmt.Errorf("dataset JSONL %q contains data after declared %d records", path, dataset.Records))
			return
		} else if err != io.EOF {
			yield(DatasetValue{}, fmt.Errorf("decode trailing dataset JSONL %q: %w", path, err))
			return
		}
		if err := closeReader(); err != nil {
			yield(DatasetValue{}, err)
		}
	}
}

func validateDatasetHeader(path string, dataset model.Dataset) error {
	if dataset.Schema != model.DatasetSchemaV1 {
		return fmt.Errorf("dataset JSONL %q has schema %q, want %q", path, dataset.Schema, model.DatasetSchemaV1)
	}
	if dataset.RecordSchema != model.RecordSchemaV2 {
		return fmt.Errorf(
			"dataset JSONL %q declares record schema %q, want %q",
			path,
			dataset.RecordSchema,
			model.RecordSchemaV2,
		)
	}
	if dataset.Records < 0 {
		return fmt.Errorf("dataset JSONL %q declares negative record count %d", path, dataset.Records)
	}
	return nil
}
