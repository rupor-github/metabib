package main

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/config"
	"metabib/jsonl"
	"metabib/library"
	"metabib/model"
)

func consistencyTestPolicy() config.MergeConsistencyConfig {
	return config.MergeConsistencyConfig{
		Enabled: true,
		// Deliberately unordered: output must follow evidence strength instead.
		Checks: []string{"language", "authors", "title", "content_size", "document_type", "content_md5"},
	}
}

func consistencyTestRecord() model.Record {
	return model.Record{
		Schema: "metabib.record/2",
		ID: model.RecordID{
			Library: "flibusta", BookID: 893518, FileName: "893518", Extension: "fb2",
			Archive: &model.ArchiveInfo{
				Path: "books.zip", Entry: "893518.fb2", Index: 809,
				UncompressedSize: 2886972, ContentMD5: "e3f1abc3a350982eec256bb8be7baed7",
			},
		},
		Source: model.RecordSources{
			Database: model.DatabaseSource{
				Present: true,
				Book: &model.DBBook{
					BookID: 893518, FileType: "fb2", FileSize: 2886972,
					MD5: "e3f1abc3a350982eec256bb8be7baed7", Title: "Принцесса по приказу", Lang: "ru",
				},
				Authors: []model.Contributor{{FirstName: "Екатерина", LastName: "Каблукова"}},
			},
			FB2: model.FB2Source{
				Present: true,
				Description: &model.FB2Description{TitleInfo: &model.FB2TitleInfo{
					Title: "Принцесса по приказу", Language: "ru",
					Authors: []model.FB2Person{{FirstName: "Екатерина", LastName: "Каблукова"}},
				}},
			},
		},
	}
}

func consistencyTestDisagreement() model.Record {
	rec := consistencyTestRecord()
	rec.Source.Database.Book = &model.DBBook{
		BookID: 893518, FileType: "docx", FileSize: 1255082,
		MD5: "1a0171b03e33d48bae0eb430f1593db7", Title: "Свідок надії. Іван Павло ІІ. Книга 1", Lang: "uk",
	}
	rec.Source.Database.Authors = []model.Contributor{{FirstName: "Джордж", LastName: "Вейгел"}}
	return rec
}

func TestMergeConsistencyChecks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*model.Record)
		want   []string
	}{
		{name: "identical evidence", change: func(*model.Record) {}},
		{name: "md5", change: func(r *model.Record) {
			r.Source.Database.Book.MD5 = "1a0171b03e33d48bae0eb430f1593db7"
		}, want: []string{"content_md5_mismatch"}},
		{name: "document type", change: func(r *model.Record) { r.Source.Database.Book.FileType = "docx" },
			want: []string{"document_type_mismatch"}},
		{name: "size", change: func(r *model.Record) { r.Source.Database.Book.FileSize++ }, want: []string{"content_size_mismatch"}},
		{name: "title despite equal md5", change: func(r *model.Record) { r.Source.Database.Book.Title = "Another title" },
			want: []string{"title_mismatch"}},
		{name: "authors", change: func(r *model.Record) {
			r.Source.Database.Authors = []model.Contributor{{FirstName: "Джордж", LastName: "Вейгел"}}
		}, want: []string{"authors_mismatch"}},
		{name: "language", change: func(r *model.Record) { r.Source.Database.Book.Lang = "uk" }, want: []string{"language_mismatch"}},
		{name: "missing md5", change: func(r *model.Record) { r.Source.Database.Book.MD5 = "" }},
		{name: "invalid md5", change: func(r *model.Record) { r.Source.Database.Book.MD5 = strings.Repeat("g", 32) }},
		{name: "placeholder md5", change: func(r *model.Record) { r.Source.Database.Book.MD5 = strings.Repeat("0", 32) }},
		{name: "missing archive md5", change: func(r *model.Record) { r.ID.Archive.ContentMD5 = "" }},
		{name: "md5 normalization", change: func(r *model.Record) {
			r.Source.Database.Book.MD5 = " " + strings.ToUpper(r.ID.Archive.ContentMD5) + " "
		}},
		{name: "format normalization", change: func(r *model.Record) { r.Source.Database.Book.FileType = " .FB2 " }},
		{name: "format alias", change: func(r *model.Record) {
			r.ID.Extension, r.ID.Archive.Entry, r.Source.Database.Book.FileType = "htm", "893518.htm", "html"
		}},
		{name: "missing format", change: func(r *model.Record) { r.Source.Database.Book.FileType = "" }},
		{name: "unknown physical format", change: func(r *model.Record) { r.ID.Extension, r.ID.Archive.Entry = "", "893518" }},
		{name: "unknown size", change: func(r *model.Record) { r.Source.Database.Book.FileSize = 0 }},
		{name: "negative size", change: func(r *model.Record) { r.Source.Database.Book.FileSize = -1 }},
		{name: "missing title", change: func(r *model.Record) { r.Source.Database.Book.Title = "" }},
		{name: "quality filtered title", change: func(r *model.Record) { r.Source.Database.Book.Title = "���" }},
		{name: "quality filtered fb2 title", change: func(r *model.Record) { r.Source.FB2.Description.TitleInfo.Title = "���" }},
		{name: "missing authors", change: func(r *model.Record) { r.Source.Database.Authors = nil }},
		{name: "incomplete author", change: func(r *model.Record) {
			r.Source.Database.Authors = []model.Contributor{{LastName: "Вейгел"}}
		}},
		{name: "initials", change: func(r *model.Record) {
			r.Source.Database.Authors = []model.Contributor{{FirstName: "Д.", LastName: "Вейгел"}}
		}},
		{name: "partial author overlap", change: func(r *model.Record) {
			r.Source.Database.Authors = append(r.Source.Database.Authors, model.Contributor{FirstName: "Джордж", LastName: "Вейгел"})
		}},
		{name: "author identifiers ignored", change: func(r *model.Record) {
			r.Source.Database.Authors[0].ID = 42
			r.Source.FB2.Description.TitleInfo.Authors[0].ID = "unrelated-fb2-person-id"
		}},
		{name: "author order ignored", change: func(r *model.Record) {
			r.Source.Database.Authors = append(r.Source.Database.Authors, model.Contributor{FirstName: "Джордж", LastName: "Вейгел"})
			r.Source.FB2.Description.TitleInfo.Authors = append([]model.FB2Person{{FirstName: "Джордж", LastName: "Вейгел"}},
				r.Source.FB2.Description.TitleInfo.Authors...)
		}},
		{name: "language base tag", change: func(r *model.Record) { r.Source.Database.Book.Lang = "RU-ru" }},
		{name: "language display name", change: func(r *model.Record) { r.Source.Database.Book.Lang = "Russian" }},
		{name: "language alias", change: func(r *model.Record) { r.Source.Database.Book.Lang = "rus_alias" }},
		{name: "unknown language", change: func(r *model.Record) { r.Source.Database.Book.Lang = "und" }},
		{name: "multilingual", change: func(r *model.Record) { r.Source.Database.Book.Lang = "ru,en" }},
		{name: "unresolved language", change: func(r *model.Record) { r.Source.Database.Book.Lang = "not a real language" }},
		{name: "no database", change: func(r *model.Record) { r.Source.Database = model.DatabaseSource{} }},
		{name: "no parsed metadata", change: func(r *model.Record) { r.Source.FB2 = model.FB2Source{} }},
		{name: "nested scope", change: func(r *model.Record) {
			r.ID.ContainerExtension, r.ID.Archive.Entry = "zip", "893518.fb2.zip"
			r.Source.Database.Book.MD5 = "1a0171b03e33d48bae0eb430f1593db7"
			r.Source.Database.Book.FileSize++
		}},
		{name: "opaque nested entry", change: func(r *model.Record) {
			r.ID.Extension, r.ID.Archive.Entry = "rar", "893518.rar"
			r.Source.Database.Book.FileType = "pdf"
			r.Source.Database.Book.FileSize++
			r.Source.Database.Book.MD5 = "1a0171b03e33d48bae0eb430f1593db7"
		}},
		{name: "inner document type", change: func(r *model.Record) {
			r.ID.ContainerExtension, r.ID.Archive.Entry = "zip", "893518.fb2.zip"
			r.Source.Database.Book.FileType = "docx"
		}, want: []string{"document_type_mismatch"}},
		{name: "src and document info ignored", change: func(r *model.Record) {
			r.Source.FB2.Description.SrcTitleInfo = &model.FB2TitleInfo{Title: "Another title", Language: "en"}
			r.Source.FB2.Description.DocumentInfo = &model.FB2DocumentInfo{
				Authors: []model.FB2Person{{FirstName: "Джордж", LastName: "Вейгел"}},
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := consistencyTestRecord()
			tt.change(&rec)
			converted, err := datasetRecordFromRecord(rec, map[string]string{"books.zip": "archive-0001"})
			if err != nil {
				t.Fatal(err)
			}
			checker, err := newMergeConsistencyChecker(consistencyTestPolicy(), config.INPXLanguageConfig{
				Aliases: map[string]string{"rus_alias": "ru"},
			})
			if err != nil {
				t.Fatal(err)
			}
			checker.Apply(rec.ID, &converted)
			var got []string
			for _, issue := range converted.Issues {
				if issue.Stage == "consistency" {
					got = append(got, issue.Code)
					if checker.counts[issue.Code] != 1 || issue.Retryable || issue.Observation != "db" {
						t.Fatalf("issue/count = %#v / %#v", issue, checker.counts)
					}
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("consistency issues = %v, want %v; all issues = %#v", got, tt.want, converted.Issues)
			}
		})
	}
}

func TestMergeConsistencyTextNormalization(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ a, b string }{
		{"  CAFÉ\tＳｔｏｒｙ  ", "cafe\u0301 story"},
		{"«Book»—it’s   here", `"book"-it's here`},
		{"Straße", "STRASSE"},
	} {
		t.Run(tt.a, func(t *testing.T) {
			if a, b := consistencyText(tt.a), consistencyText(tt.b); a != b {
				t.Fatalf("normalized %q != %q", a, b)
			}
		})
	}
	if consistencyText("Book 1") == consistencyText("Book 2") {
		t.Fatal("meaningful numbers were discarded")
	}
}

func TestMergeConsistencySidecar(t *testing.T) {
	t.Parallel()
	rec := consistencyTestDisagreement()
	rec.Source.Sidecars = []model.SidecarSource{{
		Present: true, Kind: "fbd", Format: "fbd", Entry: "893518.fbd", Description: rec.Source.FB2.Description,
	}}
	rec.Source.FB2 = model.FB2Source{}
	converted, err := datasetRecordFromRecord(rec, map[string]string{"books.zip": "archive-0001"})
	if err != nil {
		t.Fatal(err)
	}
	checker, err := newMergeConsistencyChecker(consistencyTestPolicy(), config.INPXLanguageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	checker.Apply(rec.ID, &converted)
	if len(converted.Issues) != 6 {
		t.Fatalf("issues = %#v, want six", converted.Issues)
	}
	for _, issue := range converted.Issues[3:] {
		if issue.Details["archive_observation"] != "fbd" {
			t.Fatalf("sidecar issue lost provenance: %#v", issue)
		}
	}
}

func TestMergeConsistencyPreservesExistingIssues(t *testing.T) {
	t.Parallel()
	rec := consistencyTestDisagreement()
	existing := model.Issue{Observation: "db", Stage: "match", Code: "catalog_id_conflict", Message: "existing match evidence"}
	rec.Issues = []model.Issue{existing}
	converted, err := datasetRecordFromRecord(rec, map[string]string{"books.zip": "archive-0001"})
	if err != nil {
		t.Fatal(err)
	}
	checker, err := newMergeConsistencyChecker(consistencyTestPolicy(), config.INPXLanguageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	checker.Apply(rec.ID, &converted)
	if len(converted.Issues) != 7 || !reflect.DeepEqual(converted.Issues[0], existing) {
		t.Fatalf("existing issue changed or consistency issues missing: %#v", converted.Issues)
	}
	// Database-only exports have nothing to compare.
	rec.ID.Archive = nil
	converted, err = datasetRecordFromRecord(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	checker.Apply(rec.ID, &converted)
	if len(converted.Issues) != 1 || !reflect.DeepEqual(converted.Issues[0], existing) {
		t.Fatalf("database-only issues = %#v", converted.Issues)
	}
}

func TestMergeConsistencyConfiguration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		policy config.MergeConsistencyConfig
		want   []string
	}{
		{name: "disabled", policy: config.MergeConsistencyConfig{Checks: consistencyTestPolicy().Checks}},
		{name: "empty checks", policy: config.MergeConsistencyConfig{Enabled: true}},
		{name: "subset ordered", policy: config.MergeConsistencyConfig{Enabled: true, Checks: []string{"title", "content_md5"}},
			want: []string{"content_md5_mismatch", "title_mismatch"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := consistencyTestDisagreement()
			converted, err := datasetRecordFromRecord(rec, map[string]string{"books.zip": "archive-0001"})
			if err != nil {
				t.Fatal(err)
			}
			checker, err := newMergeConsistencyChecker(tt.policy, config.INPXLanguageConfig{})
			if err != nil {
				t.Fatal(err)
			}
			checker.Apply(rec.ID, &converted)
			var got []string
			for _, issue := range converted.Issues {
				got = append(got, issue.Code)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeConsistencyRegression893518(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	archivePath := filepath.Join(dir, "books.zip") // Does not exist: the check must use cached evidence only.
	rec := consistencyTestDisagreement()
	rec.ID.Archive.Path = archivePath
	database := rec.Source.Database
	rec.Source.Database = model.DatabaseSource{}
	writeTestManifest(t, manifestPath, rec)
	checker, err := newMergeConsistencyChecker(consistencyTestPolicy(), config.INPXLanguageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.InfoLevel)
	output := filepath.Join(dir, "merged")
	err = writeOutput(ctx, output, "none", nil, func(out *jsonl.Writer) error {
		header := model.Dataset{
			Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 1,
			Database: &model.DatasetDatabase{ID: "database"},
			Archives: []model.DatasetArchive{{ID: "archive-0001", Entries: 810, FB2Entries: 810}},
		}
		if err := out.WriteValue(header); err != nil {
			return err
		}
		count, err := mergeArchiveManifests(ctx,
			[]library.ArchiveManifestDecision{{ArchivePath: archivePath, ManifestPath: manifestPath}},
			databaseIndex{byID: map[int64]model.DatabaseSource{893518: database}},
			map[string]string{archivePath: "archive-0001"},
			model.DatasetProcessing{ParseFB2: true, FB2ReplacementQualityCheck: true, DatabaseReplacementQualityCheck: true},
			checker, out, zap.New(core),
		)
		if err == nil && count != 1 {
			t.Errorf("merge wrote %d records, want 1", count)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var merged model.DatasetRecord
	if err := jsonv2.Unmarshal(bytes.Split(bytes.TrimSpace(data), []byte{'\n'})[1], &merged); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"content_md5_mismatch", "document_type_mismatch", "content_size_mismatch", "title_mismatch", "authors_mismatch", "language_mismatch",
	}
	var got []string
	for _, issue := range merged.Issues {
		got = append(got, issue.Code)
		if issue.Details["match_method"] != "numeric_entry_stem" || issue.Details["book_id"] != float64(893518) {
			t.Fatalf("missing match evidence: %#v", issue)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
	md5 := merged.Issues[0].Details
	if md5["archive_value"] != rec.ID.Archive.ContentMD5 || md5["database_value"] != database.Book.MD5 {
		t.Fatalf("md5 evidence = %#v", md5)
	}
	if merged.Record.Locator.Source != "archive-0001" || *merged.Record.Locator.Index != 809 || len(merged.Artifacts) != 2 {
		t.Fatalf("physical evidence changed: %#v", merged)
	}
	if !reflect.DeepEqual(merged.Claims.Bibliographic.Title[0].Value, database.Book.Title) {
		t.Fatalf("database title changed: %#v", merged.Claims.Bibliographic.Title)
	}
	if logs.FilterMessage("Archive manifests merged").Len() != 1 || len(checker.counts) != 6 {
		t.Fatalf("missing aggregate log/counts: %#v", checker.counts)
	}
	for _, message := range []string{"Archive manifest merged", "Archive manifests merged"} {
		entries := logs.FilterMessage(message).All()
		if len(entries) != 1 {
			t.Fatalf("%s log entries = %d, want 1", message, len(entries))
		}
		fields := entries[0].ContextMap()
		if fields["db_id_filename_conflict_issues"] != int64(0) || fields["consistency_issues"] != int64(6) ||
			fields["consistency_issue_records"] != int64(1) {
			t.Fatalf("%s counters = %#v", message, fields)
		}
		counts, ok := fields["consistency_issues_by_code"].(map[string]int64)
		if !ok || !reflect.DeepEqual(counts, checker.counts) {
			t.Fatalf("%s issue codes = %#v, want %#v", message, fields["consistency_issues_by_code"], checker.counts)
		}
		if _, exists := fields["conflicting_match_evidence"]; exists {
			t.Fatalf("%s retains confusing old log field", message)
		}
	}
	var inspected bytes.Buffer
	if err := inspectDataset(ctx, inspectOptions{Input: output, Index: -1, Issues: true, JSON: true}, &inspected); err != nil {
		t.Fatal(err)
	}
	for _, code := range want {
		if !strings.Contains(inspected.String(), code) {
			t.Fatalf("inspect --issues omitted %s: %s", code, &inspected)
		}
	}
}

func TestMergeConsistencyArchiveLogCounts(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"enabled", "disabled", "empty checks", "nil checker"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			policy := consistencyTestPolicy()
			if mode == "disabled" {
				policy.Enabled = false
			} else if mode == "empty checks" {
				policy.Checks = nil
			}
			checker, err := newMergeConsistencyChecker(policy, config.INPXLanguageConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "nil checker" {
				checker = nil
			}
			languageOnly := consistencyTestRecord()
			languageOnly.Source.Database.Book.Lang = "uk"
			clean := consistencyTestRecord()
			clean.Issues = []model.Issue{{Stage: "match", Code: "existing_match_issue"}}
			groups := [][]model.Record{
				{consistencyTestDisagreement(), consistencyTestRecord()},
				{languageOnly},
				{clean},
			}
			var plan []library.ArchiveManifestDecision
			index := databaseIndex{byID: make(map[int64]model.DatabaseSource)}
			sources := make(map[string]string)
			var nextID int64 = 1
			for archiveIndex, records := range groups {
				archive := filepath.Join(dir, fmt.Sprintf("books-%d.zip", archiveIndex))
				manifest := archive + ".manifest.zst"
				for i := range records {
					rec := &records[i]
					rec.ID.BookID = nextID
					rec.ID.FileName = fmt.Sprint(nextID)
					rec.ID.Archive.Entry = fmt.Sprintf("%d.fb2", nextID)
					rec.ID.Archive.Path = archive
					rec.ID.Archive.Index = i
					rec.Source.Database.Book.BookID = nextID
					index.byID[nextID] = rec.Source.Database
					rec.Source.Database = model.DatabaseSource{}
					nextID++
				}
				writeTestManifest(t, manifest, records...)
				plan = append(plan, library.ArchiveManifestDecision{ArchivePath: archive, ManifestPath: manifest})
				sources[archive] = fmt.Sprintf("archive-%04d", archiveIndex+1)
			}
			out, err := jsonl.CreateCompressed(filepath.Join(dir, "output"), jsonl.CompressionNone)
			if err != nil {
				t.Fatal(err)
			}
			core, logs := observer.New(zap.InfoLevel)
			count, mergeErr := mergeArchiveManifests(context.Background(), plan, index, sources,
				model.DatasetProcessing{ParseFB2: true}, checker, out, zap.New(core))
			closeErr := out.Close()
			if mergeErr != nil || closeErr != nil || count != 4 {
				t.Fatalf("merge = %d, %v; close = %v", count, mergeErr, closeErr)
			}
			entries := logs.FilterMessage("Archive manifest merged").All()
			totals := logs.FilterMessage("Archive manifests merged").All()
			if len(entries) != 3 || len(totals) != 1 {
				t.Fatalf("archive logs = %d, total logs = %d", len(entries), len(totals))
			}
			entries = append(entries, totals[0])
			wantIssues := []int64{6, 1, 0, 7}
			wantRecords := []int64{1, 1, 0, 2}
			for i, entry := range entries {
				fields := entry.ContextMap()
				if mode != "enabled" {
					for _, key := range []string{"consistency_issues", "consistency_issue_records", "consistency_issues_by_code"} {
						if _, exists := fields[key]; exists {
							t.Fatalf("disabled consistency checks reported %s: %#v", key, fields)
						}
					}
					continue
				}
				if fields["consistency_issues"] != wantIssues[i] || fields["consistency_issue_records"] != wantRecords[i] {
					t.Fatalf("log %d issue counters = %#v", i, fields)
				}
				counts, ok := fields["consistency_issues_by_code"].(map[string]int64)
				if !ok {
					t.Fatalf("log %d has no issue code map: %#v", i, fields)
				}
				var issues int64
				for _, n := range counts {
					issues += n
				}
				if issues != wantIssues[i] {
					t.Fatalf("log %d issue code total = %d, want %d", i, issues, wantIssues[i])
				}
				if i == 1 && !reflect.DeepEqual(counts, map[string]int64{"language_mismatch": 1}) {
					t.Fatalf("second archive counts are cumulative: %#v", counts)
				}
			}
		})
	}
}
