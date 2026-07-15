package mhlinpx

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "fb2-0000000001-0000000003.zip")
	prefix := filepath.Join(dir, "all")
	writeDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      2,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives: []model.DatasetArchive{{
			ID:       "archive-0001",
			Name:     filepath.Base(archivePath),
			PathHint: archivePath,
			Entries:  3,
			Ignored:  []model.IndexRange{{Start: 1, End: 1}},
		}},
	}, mhlDatasetRecord("archive-0001", 0, "1.fb2", 1), mhlOnlineDatasetRecord(1, "Online Only"))
	fixedTmp := filepath.Join(dir, "flibusta_20260603.inpx.tmp")
	if err := os.WriteFile(fixedTmp, []byte("stale fixed tmp"), 0o644); err != nil {
		t.Fatalf("write fixed temp file: %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:   prefix,
		OutputPrefix:  filepath.Join(dir, "flibusta"),
		Format:        Format2X,
		SequenceMode:  SequenceAuthor,
		FB2Preference: PreferComplement,
		QuickFix:      true,
		Limits:        DefaultLimits(),
		CommentTemplate: strings.Join([]string{
			"\ufeff{{ .DatabaseName }} FB2 - {{ .DisplayDate }}",
			"{{ .DatabaseName }}_{{ .DumpDate }}",
			"65536",
			"Локальные архивы библиотеки {{ .DatabaseName }} (FB2) {{ .DisplayDate }}",
		}, "\r\n"),
		VersionTemplate: "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if filepath.Base(stats.OutputPath) != "flibusta_20260603.inpx" {
		t.Fatalf("output = %q", stats.OutputPath)
	}
	if data, err := os.ReadFile(fixedTmp); err != nil || string(data) != "stale fixed tmp" {
		t.Fatalf("fixed temp file = %q, %v", data, err)
	}
	if stats.Archives != 1 || stats.Files != 2 || stats.Records != 1 || stats.DBRecords != 1 ||
		stats.FB2Records != 0 || stats.Dummy != 1 || stats.DumpDate != "20260603" {
		t.Fatalf("stats = %#v", stats)
	}
	entries, comment := readZipEntries(t, stats.OutputPath)
	if comment != "flibusta - 2026-06-03" {
		t.Fatalf("comment = %q", comment)
	}
	if !strings.Contains(entries["fb2-0000000001-0000000003.inp"], "Last,First,") {
		t.Fatalf("inp = %q", entries["fb2-0000000001-0000000003.inp"])
	}
	if !strings.Contains(entries["fb2-0000000001-0000000003.inp"], "dummy record") {
		t.Fatalf("inp missing dummy = %q", entries["fb2-0000000001-0000000003.inp"])
	}
	if strings.TrimSpace(entries["version.info"]) != "20260603" {
		t.Fatalf("version.info = %q", entries["version.info"])
	}
	if _, ok := entries["online.inp"]; ok {
		t.Fatalf("unexpected online.inp for mixed archive input: %#v", entries)
	}
	if !strings.HasPrefix(entries["collection.info"], "\ufeff") {
		t.Fatalf("collection.info missing UTF-8 BOM: %q", entries["collection.info"])
	}
	if !strings.Contains(entries["collection.info"], "flibusta_20260603") {
		t.Fatalf("collection.info = %q", entries["collection.info"])
	}
}

func TestGenerateDatabaseOnlyWritesOnlineINP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "online")
	writeDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "librusec",
		Records:      1,
		Database:     &model.DatasetDatabase{DumpDate: "20260713"},
	}, mhlOnlineDatasetRecord(1, "Online Title"))

	stats, err := Generate(context.Background(), Options{
		InputPrefix:     prefix,
		OutputPrefix:    filepath.Join(dir, "librusec"),
		Format:          Format2X,
		SequenceMode:    SequenceAuthor,
		FB2Preference:   PreferComplement,
		QuickFix:        true,
		Limits:          DefaultLimits(),
		CommentTemplate: "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate: "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.Archives != 1 || stats.Files != 1 || stats.Records != 1 || stats.DBRecords != 1 || stats.Dummy != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	entries, _ := readZipEntries(t, stats.OutputPath)
	if !strings.Contains(entries["online.inp"], "Last,First,") || !strings.Contains(entries["online.inp"], "Online Title") {
		t.Fatalf("online.inp = %q", entries["online.inp"])
	}
}

func TestInfoTemplates(t *testing.T) {
	t.Parallel()

	meta := inpxutil.Metadata{
		Library:     "flibusta",
		DumpDate:    "20260603",
		DumpDateISO: "2026-06-03",
	}
	got, err := collectionInfo(meta, Options{CommentTemplate: "\ufeff{{ .DatabaseName | upper }} {{ .DumpDate }} {{ .DisplayDate }}"})
	if err != nil {
		t.Fatalf("collectionInfo() error = %v", err)
	}
	if got != "\ufeffFLIBUSTA 20260603 2026-06-03" {
		t.Fatalf("collectionInfo() = %q", got)
	}
	got, err = versionInfo(meta, Options{VersionTemplate: "{{ .DumpDate }} {{ .DatabaseName | upper }}\r\n"})
	if err != nil {
		t.Fatalf("versionInfo() error = %v", err)
	}
	if got != "20260603 FLIBUSTA\r\n" {
		t.Fatalf("versionInfo() = %q", got)
	}
}

func TestDBSequenceEmitsZeroNumber(t *testing.T) {
	t.Parallel()

	sequenceType := int64(0)
	number := 0.0
	name, num := dbSequence([]model.SequenceValue{{
		Name:   "Series",
		Number: &model.NumberValue{Value: &number},
		Type:   &sequenceType,
	}}, SequenceAuthor)
	if name != "Series" || num != "0" {
		t.Fatalf("dbSequence() = %q, %q", name, num)
	}
}

func TestAuthorsStringDBPresentWithoutAuthors(t *testing.T) {
	t.Parallel()

	fb2Authors := []model.PersonValue{{FirstName: "неизвестен", LastName: "Автор"}}
	got := authorsString(true, nil, fb2Authors, Options{FB2Preference: PreferComplement, Limits: DefaultLimits()})
	if got != "неизвестный,автор,:" {
		t.Fatalf("authorsString() = %q", got)
	}
}

func TestRecordLineRUKSAppendsMD5AndReplacement(t *testing.T) {
	t.Parallel()

	rec := mhlOnlineDatasetRecord(1, "Title")
	rec.Claims.Catalog.Status = []model.Claim{{
		Observation: "db",
		Value:       model.CatalogStatusValue{FileType: "fb2", MD5: "0123456789abcdef0123456789abcdef"},
	}}
	rec.Relations = []model.Relation{{
		Type:        "replaced_by",
		Observation: "db",
		Target:      &model.IdentityTarget{Scheme: "flibusta.book", Value: "42"},
	}}
	line, _, err := recordLine(rec, Options{Format: FormatRUKS, QuickFix: true, Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("recordLine() error = %v", err)
	}
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), fieldSep)
	if len(fields) != 17 {
		t.Fatalf("field count = %d fields=%#v line=%q", len(fields), fields, line)
	}
	if fields[14] != "0123456789abcdef0123456789abcdef" || fields[15] != "42" || fields[16] != "" {
		t.Fatalf("RUKS tail fields = %#v", fields[14:])
	}
}

func TestRecordLineUsesArtifactStemForFB2OnlyLIBID(t *testing.T) {
	t.Parallel()

	index := 23387
	number := 2.0
	rec := model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: "archive-0001", Index: &index},
		},
		Artifacts: []model.Artifact{{
			Name: "31280.fb2",
			Occurrences: []model.Occurrence{{
				Archive:          "archive-0001",
				Entry:            "31280.fb2",
				Index:            index,
				UncompressedSize: 1385596,
				Modified:         "2011-03-24T14:40:10-04:00",
			}},
		}},
		Observations: []model.Observation{
			{ID: "archive", Source: "archive-0001", Kind: "archive_entry", Status: "present"},
			{ID: "db", Source: "database", Kind: "database_book", Status: "absent"},
			{ID: "fb2", Source: "archive-0001", Kind: "fb2_description", Status: "present"},
		},
		Claims: model.Claims{Bibliographic: &model.BibliographicClaims{
			Title: []model.Claim{{Observation: "fb2", Value: "Дорога в Омаху"}},
			Authors: []model.Claim{{
				Observation: "fb2",
				Value:       []model.PersonValue{{FirstName: "Роберт", LastName: "Ладлэм"}},
			}},
			Genres: []model.Claim{{Observation: "fb2", Value: []model.GenreValue{{Code: "humor_prose"}}}},
			Sequences: []model.Claim{{Observation: "fb2", Value: []model.SequenceValue{{
				Name:   "Маккензи Хаукинз",
				Number: &model.NumberValue{Text: "02", Value: &number},
			}}}},
			Language: []model.Claim{{Observation: "fb2", Value: "ru"}},
		}},
	}

	line, _, err := recordLine(rec, Options{
		Format:        Format2X,
		SequenceMode:  SequenceAuthor,
		FB2Preference: PreferComplement,
		QuickFix:      true,
		Limits:        DefaultLimits(),
	})
	if err != nil {
		t.Fatalf("recordLine() error = %v", err)
	}
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), fieldSep)
	if len(fields) != 15 {
		t.Fatalf("field count = %d fields=%#v line=%q", len(fields), fields, line)
	}
	if fields[3] != "Маккензи Хаукинз" || fields[4] != "2" {
		t.Fatalf("sequence fields = %#v", fields[3:5])
	}
	if fields[5] != "31280" || fields[6] != "1385596" ||
		fields[7] != "31280" || fields[8] != "" || fields[9] != "fb2" {
		t.Fatalf("file fields = %#v", fields[5:10])
	}
}

func TestCleanseRemovesINPLayoutCharacters(t *testing.T) {
	t.Parallel()

	got := inpxutil.Cleanse("a" + fieldSep + "b\rc\r\nd\ne\u00a0f")
	if strings.Contains(got, fieldSep) || strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("Cleanse() = %q, still contains layout characters", got)
	}
	if got != "a b c de f" {
		t.Fatalf("Cleanse() = %q", got)
	}
}

func TestRecordLineSanitizesFieldSeparators(t *testing.T) {
	t.Parallel()

	rec := mhlOnlineDatasetRecord(1, "bad"+fieldSep+"title")
	rec.Artifacts[0].Name = "file" + fieldSep + "name.fb2"
	rec.Claims.Bibliographic.Authors = []model.Claim{{Observation: "db", Value: []model.PersonValue{{
		FirstName: "First" + fieldSep + "Name",
		LastName:  "Last\rName",
	}}}}
	rec.Claims.Bibliographic.Genres = []model.Claim{{Observation: "db", Value: []model.GenreValue{{Code: "sf" + fieldSep + "bad"}}}}
	rec.Claims.Bibliographic.Language = []model.Claim{{Observation: "db", Value: "en" + fieldSep + "ru"}}
	rec.Claims.Bibliographic.Keywords = []model.Claim{{Observation: "db", Value: "bad\rkeywords"}}
	rec.Claims.Catalog.Time = []model.Claim{{Observation: "db", Value: "bad" + fieldSep + "date"}}
	rec.Claims.Catalog.Deleted = []model.Claim{{Observation: "db", Value: model.DeletionValue{Raw: "bad\rdelete"}}}
	rec.Claims.Catalog.Status = []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "fb2" + fieldSep + "bad"}}}
	line, _, err := recordLine(rec, Options{Format: FormatRUKS, QuickFix: true, Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("recordLine() error = %v", err)
	}
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), fieldSep)
	if len(fields) != 17 {
		t.Fatalf("field count = %d fields=%#v line=%q", len(fields), fields, line)
	}
	for idx, field := range fields[:len(fields)-1] {
		if strings.Contains(field, fieldSep) || strings.Contains(field, "\r") || strings.Contains(field, "\n") {
			t.Fatalf("field %d = %q contains unsanitized layout character", idx, field)
		}
	}
}

func writeDataset(t *testing.T, prefix string, dataset model.Dataset, records ...model.DatasetRecord) {
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

func mhlDatasetRecord(source string, index int, entry string, bookID int64) model.DatasetRecord {
	rec := mhlOnlineDatasetRecord(bookID, "Title")
	rec.Record.Locator = model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index}
	rec.Artifacts[0].Name = entry
	rec.Artifacts[0].Occurrences = []model.Occurrence{{
		Archive:          source,
		Entry:            entry,
		Index:            index,
		UncompressedSize: 123,
		Modified:         "2026-06-03T00:00:00Z",
	}}
	rec.Claims.Catalog.Deleted = []model.Claim{{Observation: "db", Value: model.DeletionValue{Raw: "1"}}}
	return rec
}

func mhlOnlineDatasetRecord(bookID int64, title string) model.DatasetRecord {
	bookIDText := strconv.FormatInt(bookID, 10)
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: &bookID},
		},
		Identities: &model.Identities{Catalog: []model.Identity{{
			Scheme:      "flibusta.book",
			Value:       bookIDText,
			Observation: "db",
		}}},
		Artifacts: []model.Artifact{{
			Name: bookIDText + ".fb2",
			Size: []model.ArtifactSize{{Observation: "db", Value: 123, Kind: "reported"}},
		}},
		Observations: []model.Observation{{ID: "db", Source: "database", Kind: "database_book", Status: "present"}},
		Claims: model.Claims{
			Bibliographic: &model.BibliographicClaims{
				Title:    []model.Claim{{Observation: "db", Value: title}},
				Authors:  []model.Claim{{Observation: "db", Value: []model.PersonValue{{FirstName: "First", LastName: "Last"}}}},
				Genres:   []model.Claim{{Observation: "db", Value: []model.GenreValue{{Code: "sf"}}}},
				Language: []model.Claim{{Observation: "db", Value: "ru"}},
			},
			Catalog: &model.CatalogClaims{
				Time:   []model.Claim{{Observation: "db", Value: "2026-07-13T00:00:00Z"}},
				Status: []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "fb2"}}},
			},
		},
	}
}

func readZipEntries(t *testing.T, path string) (map[string]string, string) {
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
	return entries, string(zr.Comment)
}
