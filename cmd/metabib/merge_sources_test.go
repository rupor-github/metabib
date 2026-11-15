package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"metabib/flibinpx"
	"metabib/jsonl"
	"metabib/library"
	"metabib/mhlinpx"
	"metabib/model"
	"metabib/sliceinpx"
)

func TestMergeArchiveManifestsDatabaseSources(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name             string
		databaseDeclared bool
		databaseMatched  bool
		wantStatus       string
	}{
		{name: "archive only"},
		{name: "empty database", databaseDeclared: true, wantStatus: "absent"},
		{name: "matching database", databaseDeclared: true, databaseMatched: true, wantStatus: "present"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			dir := t.TempDir()
			archivePath := filepath.Join(dir, "books.zip")
			manifestPath := filepath.Join(dir, "books.manifest.zst")
			writeTestManifest(t, manifestPath, model.Record{
				ID: model.RecordID{
					Library: "flibusta", BookID: 42, FileName: "42", Extension: "fb2",
					Archive: &model.ArchiveInfo{Path: archivePath, Entry: "42.fb2", UncompressedSize: 123},
				},
				Source: model.RecordSources{FB2: model.FB2Source{
					Present: true,
					Description: &model.FB2Description{TitleInfo: &model.FB2TitleInfo{
						Title: "Archive title", Language: "ru",
						Authors: []model.FB2Person{{FirstName: "Example", LastName: "Author"}},
					}},
				}},
			})
			dataset := model.Dataset{
				Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1,
				Library: "flibusta", Created: "2026-11-15T00:00:00Z", Records: 1,
				Archives: []model.DatasetArchive{{
					ID: "archive-0001", Name: "books.zip", PathHint: archivePath, Entries: 1, FB2Entries: 1,
				}},
				Processing: model.DatasetProcessing{
					ParseFB2: true, FB2ReplacementQualityCheck: true, DatabaseReplacementQualityCheck: true,
				},
				Ordering: model.DatasetOrdering{
					Mode: "archive_entry", Direction: "ascending", ArchiveKey: "ordinal", EntryKey: "index",
				},
			}
			index := databaseIndex{}
			if tt.databaseDeclared {
				dataset.Database = &model.DatasetDatabase{ID: "database", DumpDate: "20261115"}
				index.byID = make(map[int64]model.DatabaseSource)
				index.byFile = make(map[string]int64)
			}
			if tt.databaseMatched {
				index.byID[42] = model.DatabaseSource{
					Present: true, Book: &model.DBBook{BookID: 42, Title: "Database title", FileType: "fb2"},
				}
			}
			prefix := filepath.Join(dir, "merged")
			if err := writeOutput(ctx, prefix, "none", nil, func(out *jsonl.Writer) error {
				if err := out.WriteValue(dataset); err != nil {
					return err
				}
				count, err := mergeArchiveManifests(
					ctx, []library.ArchiveManifestDecision{{ArchivePath: archivePath, ManifestPath: manifestPath}},
					index, map[string]string{archivePath: "archive-0001"}, dataset.Database != nil, dataset.Processing, nil, out, nil,
				)
				if err == nil && count != 1 {
					t.Errorf("merged %d records, want 1", count)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}

			var records int
			for value, err := range jsonl.DatasetValues(ctx, prefix+".jsonl") {
				if err != nil {
					t.Fatal(err)
				}
				if value.Header {
					continue
				}
				records++
				var status string
				for _, observation := range value.Record.Observations {
					if observation.Source == "database" {
						status = observation.Status
					}
				}
				if status != tt.wantStatus {
					t.Fatalf("database observation status = %q, want %q", status, tt.wantStatus)
				}
				identities := value.Record.Identities
				if identities == nil || len(identities.Catalog) == 0 {
					t.Fatal("missing inferred archive catalog identity")
				}
				inferred := identities.Catalog[len(identities.Catalog)-1]
				if inferred.Observation != "archive" || inferred.Basis != "numeric_entry_stem" || inferred.Value != "42" {
					t.Fatalf("inferred identity = %#v", inferred)
				}
			}
			if records != 1 {
				t.Fatalf("read %d records, want 1", records)
			}
			if err := inspectDataset(ctx, inspectOptions{Input: prefix, Index: -1, Validate: true}, io.Discard); err != nil {
				t.Fatalf("inspect validation: %v", err)
			}

			t.Run("mhl-inpx", func(t *testing.T) {
				stats, err := mhlinpx.Generate(ctx, mhlinpx.Options{
					InputPrefix: prefix, OutputPrefix: filepath.Join(dir, "mhl"),
					Format: mhlinpx.Format2X, SequenceMode: mhlinpx.SequenceAuthor, FB2Preference: mhlinpx.PreferComplement,
					CommentTemplate: "{{.DatabaseName}}", VersionTemplate: "{{.DumpDate}}",
				})
				if err != nil {
					t.Fatal(err)
				}
				if stats.Records != 1 || stats.Dummy != 0 {
					t.Fatalf("INPX stats = %#v, want one non-dummy record", stats)
				}
			})
			t.Run("flib-inpx", func(t *testing.T) {
				stats, err := flibinpx.Generate(ctx, flibinpx.Options{
					InputPrefix: prefix, OutputPrefix: filepath.Join(dir, "flib"),
					CommentTemplate: "{{.DatabaseName}}", VersionTemplate: "{{.DumpDate}}",
				})
				if err != nil {
					t.Fatal(err)
				}
				if stats.Records != 1 {
					t.Fatalf("INPX records = %d, want 1", stats.Records)
				}
			})
			t.Run("inpx", func(t *testing.T) {
				stats, err := sliceinpx.Generate(ctx, sliceinpx.Options{
					InputPrefix: prefix, OutputPrefix: filepath.Join(dir, "slice"),
					Where: `{{eq .Lang "ru"}}`, SplitBy: `{{.Lang}}`,
					CommentTemplate: "{{.DatabaseName}}", VersionTemplate: "{{.DumpDate}}",
				})
				if err != nil {
					t.Fatal(err)
				}
				if stats.Records != 1 || len(stats.Splits) != 1 {
					t.Fatalf("INPX stats = %#v, want one record and split", stats)
				}
			})
		})
	}
}
