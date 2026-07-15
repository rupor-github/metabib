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
		recordValidator := newDatasetRecordValidator(path, dataset)

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
			if err := recordValidator.validate(rec, records+1); err != nil {
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

type datasetRecordValidator struct {
	path             string
	databaseDeclared bool
	archives         map[string]model.DatasetArchive
}

func newDatasetRecordValidator(path string, dataset model.Dataset) datasetRecordValidator {
	archives := make(map[string]model.DatasetArchive, len(dataset.Archives))
	for _, archive := range dataset.Archives {
		archives[archive.ID] = archive
	}
	return datasetRecordValidator{path: path, databaseDeclared: dataset.Database != nil, archives: archives}
}

func (v datasetRecordValidator) validate(rec model.DatasetRecord, recordNumber int64) error {
	observations := make(map[string]struct{}, len(rec.Observations))
	for idx, observation := range rec.Observations {
		if observation.ID == "" {
			return fmt.Errorf("dataset JSONL %q record %d observation %d has empty ID", v.path, recordNumber, idx)
		}
		if _, exists := observations[observation.ID]; exists {
			return fmt.Errorf("dataset JSONL %q record %d declares duplicate observation ID %q", v.path, recordNumber, observation.ID)
		}
		observations[observation.ID] = struct{}{}
		if err := v.validateSource(recordNumber, fmt.Sprintf("observation %q", observation.ID), observation.Source); err != nil {
			return err
		}
		if observation.Locator != nil && observation.Source != "database" {
			if err := v.validateArchiveIndex(recordNumber, fmt.Sprintf("observation %q", observation.ID), observation.Source, observation.Locator.Index); err != nil {
				return err
			}
		}
	}
	for _, claim := range recordClaims(rec.Claims) {
		if err := v.validateObservationReference(recordNumber, "claim", observations, claim.Observation); err != nil {
			return err
		}
	}
	if err := v.validateIdentityObservations(recordNumber, observations, rec.Identities); err != nil {
		return err
	}
	for artifactIdx, artifact := range rec.Artifacts {
		for sizeIdx, size := range artifact.Size {
			if err := v.validateObservationReference(
				recordNumber,
				fmt.Sprintf("artifact %d size %d", artifactIdx, sizeIdx),
				observations,
				size.Observation,
			); err != nil {
				return err
			}
		}
		for checksumIdx, checksum := range artifact.Checksums {
			if err := v.validateObservationReference(
				recordNumber,
				fmt.Sprintf("artifact %d checksum %d", artifactIdx, checksumIdx),
				observations,
				checksum.Observation,
			); err != nil {
				return err
			}
		}
		for occurrenceIdx, occurrence := range artifact.Occurrences {
			if err := v.validateArchiveIndex(
				recordNumber,
				fmt.Sprintf("artifact %d occurrence %d", artifactIdx, occurrenceIdx),
				occurrence.Archive,
				&occurrence.Index,
			); err != nil {
				return err
			}
		}
	}
	for relationIdx, relation := range rec.Relations {
		if err := v.validateObservationReference(recordNumber, fmt.Sprintf("relation %d", relationIdx), observations, relation.Observation); err != nil {
			return err
		}
	}
	return nil
}

func (v datasetRecordValidator) validateSource(recordNumber int64, path string, source string) error {
	switch source {
	case "database":
		if v.databaseDeclared {
			return nil
		}
		return fmt.Errorf("dataset JSONL %q record %d %s references undeclared database source", v.path, recordNumber, path)
	case "":
		return fmt.Errorf("dataset JSONL %q record %d %s has empty source", v.path, recordNumber, path)
	default:
		if _, ok := v.archives[source]; ok {
			return nil
		}
		return fmt.Errorf("dataset JSONL %q record %d %s references undeclared archive source %q", v.path, recordNumber, path, source)
	}
}

func (v datasetRecordValidator) validateArchiveIndex(recordNumber int64, path string, archiveID string, index *int) error {
	archive, ok := v.archives[archiveID]
	if !ok {
		return fmt.Errorf("dataset JSONL %q record %d %s references undeclared archive source %q", v.path, recordNumber, path, archiveID)
	}
	if index == nil {
		return fmt.Errorf("dataset JSONL %q record %d %s has archive locator without index", v.path, recordNumber, path)
	}
	if *index < 0 || *index >= archive.Entries {
		return fmt.Errorf(
			"dataset JSONL %q record %d %s references invalid archive entry index %d for %q with %d entries",
			v.path,
			recordNumber,
			path,
			*index,
			archiveID,
			archive.Entries,
		)
	}
	return nil
}

func (v datasetRecordValidator) validateObservationReference(
	recordNumber int64,
	path string,
	observations map[string]struct{},
	observation string,
) error {
	if observation == "" {
		return fmt.Errorf("dataset JSONL %q record %d %s has empty observation reference", v.path, recordNumber, path)
	}
	if _, ok := observations[observation]; !ok {
		return fmt.Errorf(
			"dataset JSONL %q record %d %s references missing observation %q",
			v.path,
			recordNumber,
			path,
			observation,
		)
	}
	return nil
}

func (v datasetRecordValidator) validateIdentityObservations(recordNumber int64, observations map[string]struct{}, identities *model.Identities) error {
	if identities == nil {
		return nil
	}
	for idx, identity := range identities.Catalog {
		if err := v.validateObservationReference(recordNumber, fmt.Sprintf("catalog identity %d", idx), observations, identity.Observation); err != nil {
			return err
		}
	}
	for idx, identity := range identities.Document {
		if err := v.validateObservationReference(recordNumber, fmt.Sprintf("document identity %d", idx), observations, identity.Observation); err != nil {
			return err
		}
	}
	for idx, identity := range identities.Publication {
		if err := v.validateObservationReference(recordNumber, fmt.Sprintf("publication identity %d", idx), observations, identity.Observation); err != nil {
			return err
		}
	}
	return nil
}

func recordClaims(claims model.Claims) []model.Claim {
	out := make([]model.Claim, 0)
	appendBibliographicClaims := func(claims *model.BibliographicClaims) {
		if claims == nil {
			return
		}
		out = append(out, claims.Title...)
		out = append(out, claims.Authors...)
		out = append(out, claims.Translators...)
		out = append(out, claims.Illustrators...)
		out = append(out, claims.Genres...)
		out = append(out, claims.Annotation...)
		out = append(out, claims.Keywords...)
		out = append(out, claims.Language...)
		out = append(out, claims.SourceLanguage...)
		out = append(out, claims.BibliographicDate...)
		out = append(out, claims.Sequences...)
	}
	appendBibliographicClaims(claims.Bibliographic)
	appendBibliographicClaims(claims.Original)
	if claims.Publication != nil {
		out = append(out, claims.Publication.BookName...)
		out = append(out, claims.Publication.Publisher...)
		out = append(out, claims.Publication.City...)
		out = append(out, claims.Publication.Year...)
		out = append(out, claims.Publication.ISBN...)
		out = append(out, claims.Publication.Sequences...)
	}
	if claims.Document != nil {
		out = append(out, claims.Document.Authors...)
		out = append(out, claims.Document.ProgramUsed...)
		out = append(out, claims.Document.Date...)
		out = append(out, claims.Document.SourceURLs...)
		out = append(out, claims.Document.SourceOCR...)
		out = append(out, claims.Document.Version...)
		out = append(out, claims.Document.History...)
		out = append(out, claims.Document.Publishers...)
		out = append(out, claims.Document.CustomInfo...)
		out = append(out, claims.Document.Output...)
	}
	if claims.Catalog != nil {
		out = append(out, claims.Catalog.Time...)
		out = append(out, claims.Catalog.Modified...)
		out = append(out, claims.Catalog.Rating...)
		out = append(out, claims.Catalog.Deleted...)
		out = append(out, claims.Catalog.Aliases...)
		out = append(out, claims.Catalog.FileAuthor...)
		out = append(out, claims.Catalog.Status...)
	}
	return out
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
	if dataset.RecordSchema != model.DatasetRecordSchemaV1 {
		return fmt.Errorf(
			"dataset JSONL %q declares record schema %q, want %q",
			path,
			dataset.RecordSchema,
			model.DatasetRecordSchemaV1,
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
	return validateDatasetOrdering(path, dataset)
}

func validateDatasetOrdering(path string, dataset model.Dataset) error {
	ordering := dataset.Ordering
	if ordering.Mode == "" {
		return nil
	}
	if ordering.Direction != "ascending" {
		return fmt.Errorf("dataset JSONL %q declares ordering direction %q, want ascending", path, ordering.Direction)
	}
	switch ordering.Mode {
	case "archive_entry":
		if len(dataset.Archives) == 0 {
			return fmt.Errorf("dataset JSONL %q declares archive_entry ordering without archives", path)
		}
		if ordering.ArchiveKey != "ordinal" {
			return fmt.Errorf("dataset JSONL %q declares archive_entry archive key %q, want ordinal", path, ordering.ArchiveKey)
		}
		if ordering.EntryKey != "index" {
			return fmt.Errorf("dataset JSONL %q declares archive_entry entry key %q, want index", path, ordering.EntryKey)
		}
	case "database_book_id":
		if ordering.Source != "database" {
			return fmt.Errorf("dataset JSONL %q declares database_book_id source %q, want database", path, ordering.Source)
		}
		if ordering.ArchiveKey != "" || ordering.EntryKey != "" {
			return fmt.Errorf("dataset JSONL %q declares archive keys for database_book_id ordering", path)
		}
	default:
		return fmt.Errorf("dataset JSONL %q declares unsupported ordering mode %q", path, ordering.Mode)
	}
	return nil
}
