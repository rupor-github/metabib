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
		orderValidator := newDatasetOrderValidator(path, dataset)

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
			if err := orderValidator.validate(rec, records+1); err != nil {
				yield(DatasetValue{}, err)
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

type datasetOrderValidator struct {
	path            string
	dataset         model.Dataset
	archiveOrdinals map[string]int
	prevArchive     datasetArchiveOrder
	prevBookID      int64
	hasArchive      bool
	hasBookID       bool
}

type datasetArchiveOrder struct {
	ordinal int
	index   int
}

func newDatasetOrderValidator(path string, dataset model.Dataset) datasetOrderValidator {
	archiveOrdinals := make(map[string]int, len(dataset.Archives))
	for _, archive := range dataset.Archives {
		archiveOrdinals[archive.ID] = archive.Ordinal
	}
	return datasetOrderValidator{path: path, dataset: dataset, archiveOrdinals: archiveOrdinals}
}

func (v *datasetOrderValidator) validate(rec model.DatasetRecord, recordNumber int64) error {
	switch v.dataset.Ordering.Mode {
	case "":
		return nil
	case "archive_entry":
		return v.validateArchiveEntry(rec, recordNumber)
	case "database_book_id":
		return v.validateDatabaseBookID(rec, recordNumber)
	default:
		return fmt.Errorf("dataset JSONL %q declares unsupported ordering mode %q", v.path, v.dataset.Ordering.Mode)
	}
}

func (v *datasetOrderValidator) validateArchiveEntry(rec model.DatasetRecord, recordNumber int64) error {
	locator := rec.Record.Locator
	if locator.Kind != "archive_entry" {
		return fmt.Errorf("dataset JSONL %q record %d has locator kind %q, want archive_entry", v.path, recordNumber, locator.Kind)
	}
	if locator.Index == nil {
		return fmt.Errorf("dataset JSONL %q record %d has archive_entry locator without index", v.path, recordNumber)
	}
	ordinal, ok := v.archiveOrdinals[locator.Source]
	if !ok {
		return fmt.Errorf("dataset JSONL %q record %d references undeclared archive source %q", v.path, recordNumber, locator.Source)
	}
	current := datasetArchiveOrder{ordinal: ordinal, index: *locator.Index}
	if v.hasArchive && archiveOrderLess(current, v.prevArchive) {
		return fmt.Errorf(
			"dataset JSONL %q record %d is out of archive order: archive ordinal %d index %d after ordinal %d index %d",
			v.path,
			recordNumber,
			current.ordinal,
			current.index,
			v.prevArchive.ordinal,
			v.prevArchive.index,
		)
	}
	v.prevArchive = current
	v.hasArchive = true
	return nil
}

func archiveOrderLess(a datasetArchiveOrder, b datasetArchiveOrder) bool {
	return a.ordinal < b.ordinal || a.ordinal == b.ordinal && a.index < b.index
}

func (v *datasetOrderValidator) validateDatabaseBookID(rec model.DatasetRecord, recordNumber int64) error {
	locator := rec.Record.Locator
	if locator.Kind != "database_book" {
		return fmt.Errorf("dataset JSONL %q record %d has locator kind %q, want database_book", v.path, recordNumber, locator.Kind)
	}
	wantSource := v.dataset.Ordering.Source
	if wantSource == "" {
		wantSource = "database"
	}
	if locator.Source != wantSource {
		return fmt.Errorf("dataset JSONL %q record %d has locator source %q, want %q", v.path, recordNumber, locator.Source, wantSource)
	}
	if locator.BookID == nil {
		return fmt.Errorf("dataset JSONL %q record %d has database_book locator without book ID", v.path, recordNumber)
	}
	if v.hasBookID && *locator.BookID < v.prevBookID {
		return fmt.Errorf(
			"dataset JSONL %q record %d is out of database book ID order: %d after %d",
			v.path,
			recordNumber,
			*locator.BookID,
			v.prevBookID,
		)
	}
	v.prevBookID = *locator.BookID
	v.hasBookID = true
	return nil
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
	seenArchives := make(map[string]struct{}, len(dataset.Archives))
	for idx, archive := range dataset.Archives {
		if archive.ID == "" {
			return fmt.Errorf("dataset JSONL %q archive %d has empty ID", path, idx)
		}
		if _, ok := seenArchives[archive.ID]; ok {
			return fmt.Errorf("dataset JSONL %q declares duplicate archive ID %q", path, archive.ID)
		}
		seenArchives[archive.ID] = struct{}{}
		if archive.Ordinal != idx {
			return fmt.Errorf("dataset JSONL %q archive %q has ordinal %d, want %d", path, archive.ID, archive.Ordinal, idx)
		}
	}
	return nil
}
