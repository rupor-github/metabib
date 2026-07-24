package flibinpx

import (
	"archive/zip"
	"context"
	"io"
	"os"
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

func TestGenerateFLibraryINPX(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "fb2-0000000001-0000000002.zip")
	prefix := filepath.Join(dir, "all")
	writeFLibDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      3,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives: []model.DatasetArchive{{
			ID:       "archive-0001",
			Name:     filepath.Base(archivePath),
			PathHint: archivePath,
			Entries:  2,
			Ignored:  []model.IndexRange{{Start: 1, End: 1}},
		}},
	},
		flibRecord("archive-0001", 0, "1.fb2"),
		flibRecord("archive-0001", 1, "2.fb2"),
		flibOnlineRecord(2),
	)
	fixedTmp := filepath.Join(dir, "flibusta_20260603.inpx.tmp")
	if err := os.WriteFile(fixedTmp, []byte("stale fixed tmp"), 0o644); err != nil {
		t.Fatalf("write fixed temp file: %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "flibusta"),
		SequenceMode:     SequenceAll,
		FB2Preference:    PreferMerge,
		FlattenMode:      FlattenPathLeaf,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
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
	if stats.Archives != 1 || stats.Files != 1 || stats.Records != 3 ||
		stats.DBRecords != 3 || stats.FB2Records != 0 || stats.Dummy != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	entries := readZipEntries(t, stats.OutputPath)
	if entries["structure.info"] != structureInfo {
		t.Fatalf("structure.info = %q", entries["structure.info"])
	}
	if _, ok := entries["online.inp"]; ok {
		t.Fatalf("unexpected online.inp for mixed archive input: %#v", entries)
	}
	if strings.Contains(entries["fb2-0000000001-0000000002.inp"], "dummy record") {
		t.Fatalf("inp contains dummy row: %q", entries["fb2-0000000001-0000000002.inp"])
	}
	lines := strings.Split(strings.TrimSuffix(entries["fb2-0000000001-0000000002.inp"], "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d inp=%q", len(lines), entries["fb2-0000000001-0000000002.inp"])
	}
	first := strings.Split(lines[0], inpxutil.FieldSep)
	second := strings.Split(lines[1], inpxutil.FieldSep)
	third := strings.Split(lines[2], inpxutil.FieldSep)
	if len(first) != 17 || len(second) != 17 || len(third) != 17 {
		t.Fatalf("field counts = %d, %d, %d first=%#v second=%#v third=%#v", len(first), len(second), len(third), first, second, third)
	}
	if first[3] != "Cycle" || first[4] != "1" || first[11] != "ru" {
		t.Fatalf("first fields = %#v", first)
	}
	if second[3] != "Publisher Series" || second[4] != "10" {
		t.Fatalf("second fields = %#v", second)
	}
	if third[3] != "Universe / Cycle" || third[4] != "7" {
		t.Fatalf("third fields = %#v", third)
	}
	if first[13] != "one:two:three:" || first[14] != "2025" || first[15] != "flibusta" {
		t.Fatalf("extended fields = %#v", first[13:16])
	}
}

func TestGenerateLogsEntryDiagnosticsSummary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	rec := flibRecord("archive-0001", 41, "42.fb2")
	rec.Claims.Bibliographic.Language = []model.Claim{{Observation: "db", Value: "RU"}}
	rec.Claims.Bibliographic.Authors = []model.Claim{{Observation: "db", Value: []model.PersonValue{{
		Identities: []model.IdentityTarget{{Scheme: "flibusta.person", Value: "19026"}},
		FirstName:  "Сергей",
		MiddleName: "Александрович",
		LastName:   "Васильев",
		NickName:   "археолог",
	}}}}
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	language, err := inpxutil.NewLanguageResolver(inpxutil.LanguageResolverOptions{Enabled: true, Log: logger})
	if err != nil {
		t.Fatalf("NewLanguageResolver() error = %v", err)
	}
	writeFLibDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      1,
		Database: &model.DatasetDatabase{
			DumpDate: "20260603",
			INPX:     ambiguousAuthorMetadata(),
		},
		Archives: []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip", PathHint: filepath.Join(dir, "books.zip"), Entries: 42}},
	}, rec)

	_, err = Generate(context.Background(), Options{
		InputPrefix:         prefix,
		OutputPrefix:        filepath.Join(dir, "flibusta"),
		SequenceMode:        SequenceAll,
		FB2Preference:       PreferComplement,
		FlattenMode:         FlattenAll,
		DedupMode:           DedupCaseInsensitive,
		DisambiguateAuthors: true,
		Language:            language,
		Log:                 logger,
		CommentTemplate:     "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:     "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if logs.FilterMessage("Disambiguated INPX DB author").Len() != 0 || logs.FilterMessage("Canonicalized INPX language").Len() != 0 {
		t.Fatalf("per-book logs without verbose = %#v", logs.All())
	}
	entries := logs.FilterMessage("FLibrary INPX entry created").All()
	if len(entries) != 1 {
		t.Fatalf("entry logs = %#v", logs.All())
	}
	fields := entries[0].ContextMap()
	if fields["disambiguated_author_books"] != int64(1) || fields["disambiguated_authors"] != int64(1) ||
		fields["canonicalized_language_books"] != int64(1) {
		t.Fatalf("entry log fields = %#v", fields)
	}
}

func TestGenerateFLibraryDatabaseOnlyWritesOnlineINP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "online")
	writeFLibDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "librusec",
		Records:      1,
		Database:     &model.DatasetDatabase{DumpDate: "20260713"},
	}, flibOnlineRecord(1))

	stats, err := Generate(context.Background(), Options{
		InputPrefix:     prefix,
		OutputPrefix:    filepath.Join(dir, "librusec"),
		SequenceMode:    SequenceAll,
		FB2Preference:   PreferComplement,
		FlattenMode:     FlattenAll,
		DedupMode:       DedupCaseInsensitive,
		CommentTemplate: "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate: "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.Archives != 1 || stats.Files != 1 || stats.Records != 1 || stats.DBRecords != 1 || stats.FB2Records != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	entries := readZipEntries(t, stats.OutputPath)
	if !strings.Contains(entries["online.inp"], "Last,First,") || !strings.Contains(entries["online.inp"], "Online Title") {
		t.Fatalf("online.inp = %q", entries["online.inp"])
	}
}

func TestFlattenFB2Sequences(t *testing.T) {
	t.Parallel()

	value := 2.5
	sequences := []model.SequenceValue{{
		Name: "Universe",
		Sequences: []model.SequenceValue{{
			Name:   "Cycle",
			Number: &model.NumberValue{Text: "2.5", Value: &value},
		}},
	}}
	tests := []struct {
		name string
		mode FlattenMode
		want []sequence
	}{
		{
			name: "all",
			mode: FlattenAll,
			want: []sequence{{Name: "Universe", Source: "fb2"}, {Name: "Cycle", Number: "2", Source: "fb2"}},
		},
		{name: "leaf", mode: FlattenLeaf, want: []sequence{{Name: "Cycle", Number: "2", Source: "fb2"}}},
		{name: "path", mode: FlattenPath, want: []sequence{{Name: "Universe > Cycle", Number: "2", Source: "fb2"}}},
		{
			name: "path-leaf",
			mode: FlattenPathLeaf,
			want: []sequence{{Name: "Universe > Cycle", Number: "2", Source: "fb2"}, {Name: "Cycle", Number: "2", Source: "fb2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := flattenFB2Sequences(sequences, tt.mode, " > ")
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d got=%#v", len(got), got)
			}
			for idx := range got {
				if got[idx] != tt.want[idx] {
					t.Fatalf("sequence[%d] = %#v, want %#v", idx, got[idx], tt.want[idx])
				}
			}
		})
	}
}

func TestRecordSequencesFB2PreferenceModes(t *testing.T) {
	t.Parallel()

	rec := flibRecord("archive-0001", 0, "1.fb2")
	view, err := inpxutil.DatasetRecordClaims(rec)
	if err != nil {
		t.Fatalf("DatasetRecordClaims() error = %v", err)
	}
	tests := []struct {
		preference FB2Preference
		want       []sequence
	}{
		{
			preference: PreferIgnore,
			want: []sequence{
				{Name: "Cycle", Number: "1", Source: "db"},
				{Name: "Publisher Series", Number: "10", Source: "db"},
			},
		},
		{
			preference: PreferComplement,
			want: []sequence{
				{Name: "Cycle", Number: "1", Source: "db"},
				{Name: "Publisher Series", Number: "10", Source: "db"},
			},
		},
		{
			preference: PreferMerge,
			want: []sequence{
				{Name: "Cycle", Number: "1", Source: "db"},
				{Name: "Publisher Series", Number: "10", Source: "db"},
				{Name: "Universe > Cycle", Number: "7", Source: "fb2"},
			},
		},
		{preference: PreferReplace, want: []sequence{{Name: "Universe > Cycle", Number: "7", Source: "fb2"}}},
	}
	for _, tt := range tests {
		t.Run(string(tt.preference), func(t *testing.T) {
			t.Parallel()

			got := recordSequences(rec, view, Options{
				SequenceMode:     SequenceAll,
				FB2Preference:    tt.preference,
				FlattenMode:      FlattenPath,
				DedupMode:        DedupCaseInsensitive,
				FB2PathSeparator: " > ",
			})
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d got=%#v want=%#v", len(got), got, tt.want)
			}
			for idx := range got {
				if got[idx] != tt.want[idx] {
					t.Fatalf("sequence[%d] = %#v, want %#v", idx, got[idx], tt.want[idx])
				}
			}
		})
	}
}

func TestDedupSequencesCaseInsensitive(t *testing.T) {
	t.Parallel()

	rec := flibOnlineRecord(1)
	got := dedupSequences(rec, []sequence{
		{Name: "Cycle", Number: "1", Source: "db"},
		{Name: "cycle", Number: "2", Source: "fb2"},
	}, Options{DedupMode: DedupCaseInsensitive})
	if len(got) != 1 || got[0].Name != "Cycle" || got[0].Number != "1" || got[0].Source != "db" {
		t.Fatalf("dedupSequences() = %#v", got)
	}
}

func TestPeopleStringSanitizesAuthorSeparators(t *testing.T) {
	t.Parallel()

	got := peopleString([]model.PersonValue{{
		LastName:   "Last, Jr:",
		FirstName:  " First\u00a0A: B ",
		MiddleName: "Middle, : C",
	}})
	if got != "Last， Jr,First A： B,Middle， ： C:" {
		t.Fatalf("peopleString() = %q", got)
	}
}

func TestPeopleStringSkipsCorruptEmptyAuthors(t *testing.T) {
	t.Parallel()

	got := peopleString([]model.PersonValue{{LastName: "����"}, {LastName: ":"}})
	if got != "неизвестный,автор,:" {
		t.Fatalf("peopleString() = %q", got)
	}
}

func TestBuildRecordFieldsLogsDisambiguatedDBAuthor(t *testing.T) {
	t.Parallel()

	position := int64(1)
	rec := flibRecord("archive-0001", 41, "42.fb2")
	rec.Claims.Bibliographic.Authors = []model.Claim{{Observation: "db", Value: []model.PersonValue{{
		Identities: []model.IdentityTarget{{Scheme: "flibusta.person", Value: "19026"}},
		FirstName:  "Сергей",
		MiddleName: "Александрович",
		LastName:   "Васильев",
		NickName:   "археолог",
		Position:   &position,
	}}}}
	core, logs := observer.New(zap.DebugLevel)
	_, _, _, ok, err := buildRecordFields(rec, Options{
		FB2Preference:       PreferComplement,
		AuthorDisambiguator: inpxutil.NewAuthorDisambiguator(ambiguousAuthorMetadata(), nil, false),
		Log:                 zap.New(core),
		DisambiguateAuthors: true,
		Verbose:             true,
	})
	if err != nil || !ok {
		t.Fatalf("buildRecordFields() ok=%t error=%v", ok, err)
	}
	entries := logs.FilterMessage("Disambiguated INPX DB author").All()
	if len(entries) != 1 {
		t.Fatalf("debug logs = %#v, want one disambiguation message", logs.All())
	}
	fields := entries[0].ContextMap()
	if fields["book_id"] != "42" || fields["flibusta_person_id"] != "19026" || fields["suffix"] != "[археолог]" {
		t.Fatalf("log fields = %#v", fields)
	}
}

func TestGenerateFLibraryAdditionalAnnotations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	writeFLibDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      1,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
		Archives: []model.DatasetArchive{{
			ID:      "archive-0001",
			Name:    "books.zip",
			Entries: 1,
		}},
	}, flibRecord("archive-0001", 0, "1.fb2"))

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "flibusta"),
		Additional:       true,
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
	if filepath.Base(stats.AdditionalOutputPath) != "flibusta_20260603-annotations.zip" {
		t.Fatalf("additional output = %q", stats.AdditionalOutputPath)
	}
	entries := readZipEntries(t, stats.AdditionalOutputPath)
	want := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<folder name=\"books.zip\">\n" +
		"\t<file name=\"1.fb2\">\n\t\t<p>Annotation &amp; details &lt;test&gt;</p>\n\t</file>\n</folder>\n"
	if entries["books.zip"] != want {
		t.Fatalf("annotation entry = %q", entries["books.zip"])
	}
}

func TestGenerateFLibraryAdditionalIgnoredForDatabaseOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	writeFLibDataset(t, prefix, model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Records:      1,
		Database:     &model.DatasetDatabase{DumpDate: "20260603"},
	}, flibOnlineRecord(1))
	core, logs := observer.New(zap.WarnLevel)

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "flibusta"),
		Additional:       true,
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
		SequenceMode:     SequenceAuthor,
		FB2Preference:    PreferComplement,
		FlattenMode:      FlattenAll,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
		Log:              zap.New(core),
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.AdditionalOutputPath != "" {
		t.Fatalf("additional output = %q, want empty", stats.AdditionalOutputPath)
	}
	if logs.FilterMessage("Skipping FLibrary additional artifacts for database-only input").Len() != 1 {
		t.Fatalf("logs = %#v, want database-only warning", logs.All())
	}
}

func TestGenresStringSanitizesGenreSeparators(t *testing.T) {
	t.Parallel()

	got := genresString([]model.GenreValue{{Code: "sf:history"}, {Code: ":"}, {Code: "sf,comma"}}, nil)
	if got != "sf：history:sf,comma:" {
		t.Fatalf("genresString() = %q", got)
	}

	got = genresString([]model.GenreValue{{Code: "�"}}, []model.GenreValue{{Code: "fb2:genre"}})
	if got != "fb2：genre:" {
		t.Fatalf("genresString() fallback = %q", got)
	}

	got = genresString([]model.GenreValue{{Code: "�"}}, nil)
	if got != "other:" {
		t.Fatalf("genresString() empty = %q", got)
	}
}

func ambiguousAuthorMetadata() *model.INPXMetadata {
	return &model.INPXMetadata{AmbiguousDBAuthors: []model.INPXAmbiguousDBAuthorGroup{{
		Key: "Васильев,Сергей,Александрович",
		Authors: []model.INPXAmbiguousDBAuthor{
			{ID: "19026", FirstName: "Сергей", MiddleName: "Александрович", LastName: "Васильев", NickName: "археолог"},
			{ID: "77926", FirstName: "Сергей", MiddleName: "Александрович", LastName: "Васильев", NickName: "поэт"},
		},
	}}}
}

func flibRecord(source string, index int, entry string) model.DatasetRecord {
	bookID := int64(index + 1)
	sequenceTypeAuthor := int64(0)
	sequenceTypePublisher := int64(1)
	sequenceLevelAuthor := int64(1)
	sequenceLevelPublisher := int64(101)
	dbSequenceNumber := 1.0
	dbPublisherSequenceNumber := 10.0
	fb2SequenceNumber := 7.0
	year := int64(2025)
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index},
		},
		Identities: &model.Identities{Catalog: []model.Identity{{
			Scheme:      "flibusta.book",
			Value:       bookIDString(bookID),
			Observation: "db",
		}}},
		Artifacts: []model.Artifact{{
			Name:      entry,
			MediaType: "application/fb2+xml",
			Size:      []model.ArtifactSize{{Observation: "db", Value: 123, Kind: "reported"}},
			Occurrences: []model.Occurrence{{
				Archive:          source,
				Entry:            entry,
				Index:            index,
				UncompressedSize: 123,
				Modified:         "2026-06-03T00:00:00Z",
			}},
		}},
		Observations: []model.Observation{
			{ID: "db", Source: "database", Kind: "database_book", Status: "present"},
			{ID: "fb2", Source: source, Kind: "fb2_description", Status: "present"},
		},
		Claims: model.Claims{
			Bibliographic: &model.BibliographicClaims{
				Title:      []model.Claim{{Observation: "db", Value: "Title"}, {Observation: "fb2", Value: "FB2 Title"}},
				Authors:    []model.Claim{{Observation: "db", Value: []model.PersonValue{{FirstName: "First", LastName: "Last"}}}},
				Genres:     []model.Claim{{Observation: "db", Value: []model.GenreValue{{Code: "sf"}}}},
				Annotation: []model.Claim{{Observation: "fb2", Value: "Annotation & details <test>"}},
				Language:   []model.Claim{{Observation: "db", Value: "ru"}, {Observation: "fb2", Value: "ru"}},
				Keywords:   []model.Claim{{Observation: "db", Value: "one,two;three"}},
				Sequences: []model.Claim{
					{Observation: "db", Value: []model.SequenceValue{
						{
							Name:   "Cycle",
							Number: &model.NumberValue{Value: &dbSequenceNumber},
							Level:  &sequenceLevelAuthor,
							Type:   &sequenceTypeAuthor,
						},
						{
							Name:   "Publisher Series",
							Number: &model.NumberValue{Value: &dbPublisherSequenceNumber},
							Level:  &sequenceLevelPublisher,
							Type:   &sequenceTypePublisher,
						},
					}},
					{Observation: "fb2", Value: []model.SequenceValue{{
						Name: "Universe",
						Sequences: []model.SequenceValue{{
							Name:   "Cycle",
							Number: &model.NumberValue{Text: "7", Value: &fb2SequenceNumber},
						}},
					}}},
				},
			},
			Publication: &model.PublicationClaims{Year: []model.Claim{{Observation: "db", Value: model.YearValue{Value: &year}}}},
			Catalog: &model.CatalogClaims{
				Time:   []model.Claim{{Observation: "db", Value: "2026-06-03T00:00:00Z"}},
				Status: []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "fb2"}}},
			},
		},
	}
}

func flibOnlineRecord(bookID int64) model.DatasetRecord {
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "librusec",
			Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: &bookID},
		},
		Identities: &model.Identities{Catalog: []model.Identity{{
			Scheme:      "flibusta.book",
			Value:       bookIDString(bookID),
			Observation: "db",
		}}},
		Artifacts: []model.Artifact{{
			Name: bookIDString(bookID) + ".fb2",
			Size: []model.ArtifactSize{{Observation: "db", Value: 123, Kind: "reported"}},
		}},
		Observations: []model.Observation{{ID: "db", Source: "database", Kind: "database_book", Status: "present"}},
		Claims: model.Claims{
			Bibliographic: &model.BibliographicClaims{
				Title:    []model.Claim{{Observation: "db", Value: "Online Title"}},
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

func writeFLibDataset(t *testing.T, prefix string, dataset model.Dataset, records ...model.DatasetRecord) {
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

func bookIDString(bookID int64) string { return strconv.FormatInt(bookID, 10) }

func readZipEntries(t *testing.T, path string) map[string]string {
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
