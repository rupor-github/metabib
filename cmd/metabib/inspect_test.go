package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"metabib/jsonl"
	"metabib/model"
)

func TestInspectDatasetSummary(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, Index: -1}, &out); err != nil {
		t.Fatalf("inspectDataset() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Dataset",
		"records: 1",
		"ambiguous db author groups: 1",
		"ambiguous db authors: 2",
		"archives: 1",
		"parse fb2: true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect summary = %q, missing %q", text, want)
		}
	}
	if strings.Contains(text, "ambiguous db author map") {
		t.Fatalf("inspect summary includes verbose author map: %q", text)
	}
}

func TestInspectDatasetSummaryVerbose(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, Index: -1, Verbose: true}, &out); err != nil {
		t.Fatalf("inspectDataset(verbose) error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"ambiguous db author map",
		"Васильев,Сергей,Александрович",
		"19026: Васильев, Сергей, Александрович (археолог)",
		"77926: Васильев, Сергей, Александрович (поэт)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect verbose summary = %q, missing %q", text, want)
		}
	}
}

func TestInspectDatasetValidate(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, Index: -1, Validate: true}, &out); err != nil {
		t.Fatalf("inspectDataset(validate) error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "validation: ok") || !strings.Contains(text, "records read: 1") {
		t.Fatalf("inspect validation = %q", text)
	}
}

func TestInspectDatasetListsArchives(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, Index: -1, Archives: true}, &out); err != nil {
		t.Fatalf("inspectDataset(archives) error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"Archives", "archive-0001", "books.zip", "/data/books.zip"} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect archives = %q, missing %q", text, want)
		}
	}
}

func TestInspectDatasetListsArchivesAsJSON(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, Index: -1, Archives: true, JSON: true}, &out); err != nil {
		t.Fatalf("inspectDataset(archives json) error = %v", err)
	}
	text := out.String()
	for _, want := range []string{`"archives":[`, `"id":"archive-0001"`, `"path_hint":"/data/books.zip"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect archives JSON = %q, missing %q", text, want)
		}
	}
}

func TestInspectDatasetFindsBookIDAsJSON(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, BookID: 42, Index: -1, JSON: true}, &out); err != nil {
		t.Fatalf("inspectDataset(book-id) error = %v", err)
	}
	text := out.String()
	for _, want := range []string{`"record_number":1`, `"schema":"metabib.dataset_record/1"`, `"Title"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect record JSON = %q, missing %q", text, want)
		}
	}
}

func TestInspectDatasetFindsArchiveIndex(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, Archive: "archive-0001", Index: 1}, &out); err != nil {
		t.Fatalf("inspectDataset(archive index) error = %v", err)
	}
	if !strings.Contains(out.String(), "record number: 1") || !strings.Contains(out.String(), "42.fb2") {
		t.Fatalf("inspect archive output = %q", out.String())
	}
}

func TestInspectDatasetFindsFile(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	if err := inspectDataset(context.Background(), inspectOptions{Input: prefix, File: "42.fb2", Index: -1}, &out); err != nil {
		t.Fatalf("inspectDataset(file) error = %v", err)
	}
	if !strings.Contains(out.String(), "record number: 1") {
		t.Fatalf("inspect file output = %q", out.String())
	}
}

func TestInspectDatasetNoMatch(t *testing.T) {
	t.Parallel()

	prefix := writeInspectDataset(t)
	var out bytes.Buffer
	err := inspectDataset(context.Background(), inspectOptions{Input: prefix, BookID: 99, Index: -1}, &out)
	if !errors.Is(err, errInspectNoMatch) {
		t.Fatalf("inspectDataset(no match) error = %v, want errInspectNoMatch", err)
	}
}

func writeInspectDataset(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prefix := filepath.Join(dir, "combined")
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.WriteValue(inspectTestDataset()); err != nil {
		_ = w.Abort()
		t.Fatalf("WriteValue(dataset) error = %v", err)
	}
	if err := w.WriteValue(inspectTestRecord()); err != nil {
		_ = w.Abort()
		t.Fatalf("WriteValue(record) error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return prefix
}

func inspectTestDataset() model.Dataset {
	return model.Dataset{
		Schema:       model.DatasetSchemaV1,
		ID:           "dataset-id",
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Created:      "2026-07-15T00:00:00Z",
		Records:      1,
		Generator:    model.DatasetGenerator{Name: "metabib", Version: "0.0.1-test"},
		Database: &model.DatasetDatabase{
			ID:       "database",
			DumpDate: "20260715",
			INPX: &model.INPXMetadata{AmbiguousDBAuthors: []model.INPXAmbiguousDBAuthorGroup{{
				Key: "Васильев,Сергей,Александрович",
				Authors: []model.INPXAmbiguousDBAuthor{
					{ID: "19026", FirstName: "Сергей", MiddleName: "Александрович", LastName: "Васильев", NickName: "археолог"},
					{ID: "77926", FirstName: "Сергей", MiddleName: "Александрович", LastName: "Васильев", NickName: "поэт"},
				},
			}}},
		},
		Archives: []model.DatasetArchive{{
			ID:         "archive-0001",
			Ordinal:    0,
			Name:       "books.zip",
			PathHint:   "/data/books.zip",
			Entries:    2,
			FB2Entries: 1,
		}},
		Processing: model.DatasetProcessing{
			ParseFB2:               true,
			FB2Coverage:            "description",
			ArchiveContentChecksum: model.DatasetChecksumOption{Enabled: true, Algorithm: "md5"},
		},
	}
}

func inspectTestRecord() model.DatasetRecord {
	index := 1
	bookID := int64(42)
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: "archive-0001", Index: &index},
		},
		Identities: &model.Identities{Catalog: []model.Identity{{
			Scheme:      "flibusta.book",
			Value:       "42",
			Observation: "db",
		}}},
		Artifacts: []model.Artifact{{
			Name: "42.fb2",
			Size: []model.ArtifactSize{{Observation: "db", Value: 123, Kind: "reported"}},
			Occurrences: []model.Occurrence{{
				Archive:          "archive-0001",
				Entry:            "42.fb2",
				Index:            index,
				UncompressedSize: 123,
			}},
		}},
		Observations: []model.Observation{
			{
				ID:      "archive",
				Source:  "archive-0001",
				Kind:    "archive_entry",
				Status:  "present",
				Locator: &model.ObservationLocator{Entry: "42.fb2", Index: &index},
			},
			{
				ID:      "db",
				Source:  "database",
				Kind:    "database_book",
				Status:  "present",
				Locator: &model.ObservationLocator{BookID: &bookID},
			},
		},
		Claims: model.Claims{Bibliographic: &model.BibliographicClaims{
			Title: []model.Claim{{Observation: "db", Value: "Title"}},
		}},
	}
}
