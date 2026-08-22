package sliceinpx

import (
	"archive/zip"
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
)

func TestGenerateFiltersAndSplitsByLanguage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	ru := sliceRecord("archive-0001", 0, 1, "ru")
	en := sliceRecord("archive-0001", 1, 2, "en")
	ignored := sliceRecord("archive-0001", 2, 3, "ru")
	nonArchive := sliceOnlineRecord(4, "ru")
	writeSliceDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      4,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives: []model.DatasetArchive{{
			ID:      "archive-0001",
			Name:    "books.zip",
			Entries: 3,
			Ignored: []model.IndexRange{{Start: 2, End: 2}},
		}},
	}, ru, en, ignored, nonArchive)
	core, logs := observer.New(zap.InfoLevel)
	language, err := inpxutil.NewLanguageResolver(inpxutil.LanguageResolverOptions{Enabled: true})
	if err != nil {
		t.Fatalf("NewLanguageResolver() error = %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		Where:            `{{eq .Lang "ru"}}`,
		SplitBy:          `{{.Lang}}`,
		Language:         language,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		Log:              zap.New(core),
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.Records != 1 || stats.Files != 1 || stats.FilteredRecords != 1 || stats.SkippedIgnoredRecords != 1 || stats.SkippedNonArchiveRecords != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if len(stats.Splits) != 1 || stats.Splits[0].Entry != "ru.inp" || stats.Splits[0].Records != 1 || stats.Splits[0].Books != 1 {
		t.Fatalf("splits = %#v", stats.Splits)
	}
	entries := readSliceZipEntries(t, stats.OutputPath)
	if entries["structure.info"] != structureInfo {
		t.Fatalf("structure.info = %q", entries["structure.info"])
	}
	if _, ok := entries["books.inp"]; ok {
		t.Fatalf("unexpected books.inp: %#v", entries)
	}
	fields := strings.Split(strings.TrimSuffix(entries["ru.inp"], inpxutil.FieldSep+"\r\n"), inpxutil.FieldSep)
	if len(fields) != 17 || fields[11] != "1" || fields[12] != "books.zip" || fields[13] != "ru" {
		t.Fatalf("fields = %#v", fields)
	}
	if logs.FilterMessage("INPX slice records streamed").Len() != 1 || logs.FilterMessage("INPX slice split written").Len() != 1 {
		t.Fatalf("logs = %#v", logs.All())
	}
}

func TestGenerateRejectsDatabaseOnlyDataset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	writeSliceDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      1,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
	}, sliceOnlineRecord(1, "ru"))

	_, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err == nil || !strings.Contains(err.Error(), "archive-backed") {
		t.Fatalf("Generate() error = %v, want archive-backed error", err)
	}
}

func TestGenerateKeepsMultiSequenceBookInOneSplit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	rec := sliceRecord("archive-0001", 0, 1, "ru")
	seqType := int64(0)
	one := 1.0
	two := 2.0
	rec.Claims.Bibliographic.Sequences = []model.Claim{{Observation: "db", Value: []model.SequenceValue{
		{Name: "First", Number: &model.NumberValue{Value: &one}, Type: &seqType},
		{Name: "Second", Number: &model.NumberValue{Value: &two}, Type: &seqType},
	}}}
	writeSliceDataset(t, prefix, sliceDataset(), rec)

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		SplitBy:          `{{.Series}}`,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	entries := readSliceZipEntries(t, stats.OutputPath)
	if _, ok := entries["Second.inp"]; ok {
		t.Fatalf("book split across entries: %#v", entries)
	}
	lines := strings.Split(strings.TrimSuffix(entries["First.inp"], "\r\n"), "\r\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "First") || !strings.Contains(lines[1], "Second") {
		t.Fatalf("First.inp = %q", entries["First.inp"])
	}
}

func TestGenerateSplitsByDeleted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	active := sliceRecord("archive-0001", 0, 1, "en")
	active.Claims.Catalog.Deleted = []model.Claim{{Observation: "db", Value: model.DeletionValue{Raw: "0", State: "active"}}}
	deleted := sliceRecord("archive-0001", 1, 2, "en")
	deleted.Claims.Catalog.Deleted = []model.Claim{{Observation: "db", Value: model.DeletionValue{Raw: "1", State: "deleted"}}}
	writeSliceDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      2,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives:     []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip", Entries: 2}},
	}, active, deleted)

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		SplitBy:          `{{if .Deleted}}deleted-{{.Lang}}{{else}}{{.Lang}}{{end}}`,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	entries := readSliceZipEntries(t, stats.OutputPath)
	if !strings.Contains(entries["en.inp"], "Title 1") || !strings.Contains(entries["deleted-en.inp"], "Title 2") {
		t.Fatalf("entries = %#v", entries)
	}
	if strings.Contains(entries["deleted-en.inp"], "Title 1") || strings.Contains(entries["en.inp"], "Title 2") {
		t.Fatalf("deleted split mismatch: %#v", entries)
	}
}

func TestGenerateSplitsByAcceptedBookRange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	writeSliceDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      3,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives:     []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip", Entries: 3}},
	},
		sliceRecord("archive-0001", 0, 1, "ru"),
		sliceRecord("archive-0001", 1, 2, "ru"),
		sliceRecord("archive-0001", 2, 3, "ru"),
	)

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		SplitBy:          `{{rangeName .AcceptedBook 2 "other"}}`,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	entries := readSliceZipEntries(t, stats.OutputPath)
	if got := strings.Count(entries["0000000001-0000000002.inp"], "\r\n"); got != 2 {
		t.Fatalf("first range rows = %d inp=%q", got, entries["0000000001-0000000002.inp"])
	}
	if got := strings.Count(entries["0000000003-0000000004.inp"], "\r\n"); got != 1 {
		t.Fatalf("second range rows = %d inp=%q", got, entries["0000000003-0000000004.inp"])
	}
}

func TestGenerateWarnsWhenTemplateFiltersEverything(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	writeSliceDataset(t, prefix, sliceDataset(), sliceRecord("archive-0001", 0, 1, "ru"))
	core, logs := observer.New(zap.InfoLevel)

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		Where:            `false`,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		Log:              zap.New(core),
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.Records != 0 || stats.FilteredRecords != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if logs.FilterMessage("INPX slice wrote no records").Len() != 1 {
		t.Fatalf("logs = %#v", logs.All())
	}
}

func TestGenerateRejectsNonBooleanFilterOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	writeSliceDataset(t, prefix, sliceDataset(), sliceRecord("archive-0001", 0, 1, "ru"))

	_, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		Where:            `maybe`,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err == nil || !strings.Contains(err.Error(), "expected boolean") {
		t.Fatalf("Generate() error = %v, want boolean error", err)
	}
}

func TestGenerateCleansAnnotationTempsOnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	first := sliceRecord("archive-0001", 0, 1, "ru")
	first.Claims.Bibliographic.Annotation = []model.Claim{{Observation: "fb2", Value: "Annotation"}}
	broken := sliceRecord("missing", 1, 2, "ru")
	writeSliceDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      2,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives:     []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip", Entries: 1}},
	}, first, broken)

	_, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "slice"),
		Additional:       true,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
	})
	if err == nil || !strings.Contains(err.Error(), "undeclared archive source") {
		t.Fatalf("Generate() error = %v, want archive source error", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".inpx-annotation-*.tmp"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("annotation temp files leaked: %v", matches)
	}
}

func sliceDataset() model.Dataset {
	return model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      1,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives:     []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip", Entries: 1}},
	}
}

func writeSliceDataset(t *testing.T, prefix string, dataset model.Dataset, records ...model.DatasetRecord) {
	t.Helper()
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.WriteValue(dataset); err != nil {
		t.Fatalf("WriteValue(dataset) error = %v", err)
	}
	for _, rec := range records {
		if err := w.WriteValue(rec); err != nil {
			t.Fatalf("WriteValue(record) error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func sliceRecord(source string, index int, bookID int64, lang string) model.DatasetRecord {
	rec := sliceOnlineRecord(bookID, lang)
	rec.Record.Locator = model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index, BookID: &bookID}
	rec.Artifacts[0].Name = strconv.FormatInt(bookID, 10) + ".fb2"
	rec.Artifacts[0].Occurrences = []model.Occurrence{{Archive: source, Entry: rec.Artifacts[0].Name, Index: index, UncompressedSize: 123}}
	return rec
}

func sliceOnlineRecord(bookID int64, lang string) model.DatasetRecord {
	bookIDText := strconv.FormatInt(bookID, 10)
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: &bookID},
		},
		Identities: &model.Identities{Catalog: []model.Identity{{Scheme: "flibusta.book", Value: bookIDText, Observation: "db"}}},
		Artifacts:  []model.Artifact{{Name: bookIDText + ".fb2", Size: []model.ArtifactSize{{Observation: "db", Value: 123, Kind: "reported"}}}},
		Observations: []model.Observation{
			{ID: "db", Source: "database", Kind: "database_book", Status: "present"},
			{ID: "fb2", Source: "archive-0001", Kind: "fb2_description", Status: "present"},
		},
		Claims: model.Claims{
			Bibliographic: &model.BibliographicClaims{
				Title:    []model.Claim{{Observation: "db", Value: "Title " + bookIDText}},
				Authors:  []model.Claim{{Observation: "db", Value: []model.PersonValue{{FirstName: "First", LastName: "Last"}}}},
				Genres:   []model.Claim{{Observation: "db", Value: []model.GenreValue{{Code: "sf_history"}}}},
				Language: []model.Claim{{Observation: "db", Value: lang}},
			},
			Catalog: &model.CatalogClaims{
				Time:   []model.Claim{{Observation: "db", Value: "2026-07-13T00:00:00Z"}},
				Status: []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "fb2"}}},
			},
		},
	}
}

func readSliceZipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	entries := map[string]string{}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatalf("Open(%q) error = %v", file.Name, err)
		}
		buf := new(strings.Builder)
		if _, err := io.Copy(buf, r); err != nil {
			r.Close()
			t.Fatalf("ReadFrom(%q) error = %v", file.Name, err)
		}
		r.Close()
		entries[file.Name] = buf.String()
	}
	return entries
}
