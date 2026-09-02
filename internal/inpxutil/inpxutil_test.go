package inpxutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/jsonl"
	"metabib/model"
)

func TestDiscoverDatasetInputUsesExactPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "all.jsonl.zst")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := DiscoverDatasetInput(path)
	if err != nil {
		t.Fatalf("DiscoverDatasetInput() error = %v", err)
	}
	if got != path {
		t.Fatalf("DiscoverDatasetInput() = %q, want %q", got, path)
	}
}

func TestDiscoverDatasetInputUsesPrefixCandidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	path := prefix + ".jsonl.gz"
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := DiscoverDatasetInput(prefix)
	if err != nil {
		t.Fatalf("DiscoverDatasetInput() error = %v", err)
	}
	if got != path {
		t.Fatalf("DiscoverDatasetInput() = %q, want %q", got, path)
	}
}

func TestDiscoverDatasetInputRejectsMissingAndAmbiguousInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	if _, err := DiscoverDatasetInput(prefix); err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("DiscoverDatasetInput(missing) error = %v, want found 0", err)
	}
	for _, path := range []string{prefix + ".jsonl", prefix + ".jsonl.zst"} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	if _, err := DiscoverDatasetInput(prefix); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("DiscoverDatasetInput(ambiguous) error = %v, want found 2", err)
	}
}

func TestDiscoverDatasetInputRejectsMissingExactPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "all.jsonl.zip")
	if _, err := DiscoverDatasetInput(path); err == nil || !strings.Contains(err.Error(), "stat dataset input") {
		t.Fatalf("DiscoverDatasetInput() error = %v, want stat error", err)
	}
}

func TestLoadDatasetInputInitializesArchiveRowsFromHeader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      3,
		Archives: []model.DatasetArchive{{
			ID:       "archive-0001",
			Name:     "books.zip",
			PathHint: "/archives/books.zip",
			Entries:  10,
		}},
	}
	if err := writeDatasetInput(prefix, dataset,
		testDatasetArchiveRecord("archive-0001", 2, "2.fb2"),
		testDatasetArchiveRecord("archive-0001", 2, "duplicate.fb2"),
		testDatasetArchiveRecord("archive-0001", 4, "4.fb2"),
	); err != nil {
		t.Fatalf("writeDatasetInput() error = %v", err)
	}
	core, logs := observer.New(zap.WarnLevel)

	loadedDataset, archives, loaded, err := LoadDatasetInput(context.Background(), prefix, zap.New(core))
	if err != nil {
		t.Fatalf("LoadDatasetInput() error = %v", err)
	}
	if loadedDataset.RecordSchema != model.DatasetRecordSchemaV1 || loaded != 3 {
		t.Fatalf("loaded dataset = %#v, records = %d", loadedDataset, loaded)
	}
	archive := archives["archive-0001"]
	if archive == nil || archive.Meta.PathHint != "/archives/books.zip" || archive.Meta.Entries != 10 {
		t.Fatalf("archive rows = %#v", archives)
	}
	if len(archive.Records) != 2 || archive.Records[2].Artifacts[0].Name != "2.fb2" || archive.Records[4].Artifacts[0].Name != "4.fb2" {
		t.Fatalf("records = %#v", archive.Records)
	}
	if logs.FilterMessage("Duplicate archive index in INPX dataset input; keeping first record").Len() != 1 {
		t.Fatalf("logs = %#v, want one duplicate warning", logs.All())
	}
}

func TestDatasetArchiveListUsesHeaderOrdinalOrder(t *testing.T) {
	t.Parallel()

	archives := map[string]*DatasetArchiveRows{
		"archive-0001": {Meta: model.DatasetArchive{ID: "archive-0001", Ordinal: 0, Name: "z.zip"}},
		"archive-0002": {Meta: model.DatasetArchive{ID: "archive-0002", Ordinal: 1, Name: "a.zip"}},
		"archive-0003": {Meta: model.DatasetArchive{ID: "archive-0003", Ordinal: 1, Name: "b.zip"}},
	}

	got := DatasetArchiveList(archives)
	want := []string{"archive-0001", "archive-0002", "archive-0003"}
	if len(got) != len(want) {
		t.Fatalf("DatasetArchiveList() length = %d, want %d", len(got), len(want))
	}
	for idx, archive := range got {
		if archive.Meta.ID != want[idx] {
			t.Fatalf("DatasetArchiveList()[%d] = %q, want %q", idx, archive.Meta.ID, want[idx])
		}
	}
}

func TestLoadDatasetInputBucketsDatabaseOnlyRecords(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      1,
		Database:     &model.DatasetDatabase{ID: "database"},
	}
	rec := model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "database_book", Source: "database"},
		},
		Observations: []model.Observation{{ID: "db", Source: "database", Kind: "database_book", Status: "present"}},
	}
	if err := writeDatasetInput(prefix, dataset, rec); err != nil {
		t.Fatalf("writeDatasetInput() error = %v", err)
	}

	_, archives, loaded, err := LoadDatasetInput(context.Background(), prefix, nil)
	if err != nil {
		t.Fatalf("LoadDatasetInput() error = %v", err)
	}
	if loaded != 1 {
		t.Fatalf("loaded = %d, want 1", loaded)
	}
	online := archives[OnlineArchivePath]
	if online == nil || online.Meta.Name != OnlineArchiveName || online.Meta.Entries != 1 || online.Records[0].Record.Locator.Kind != "database_book" {
		t.Fatalf("online archive = %#v", online)
	}
}

func TestLoadDatasetInputRejectsUnknownArchiveSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "all")
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		RecordSchema: model.DatasetRecordSchemaV1,
		Records:      1,
		Archives:     []model.DatasetArchive{{ID: "archive-0001", Name: "books.zip"}},
	}
	if err := writeDatasetInput(prefix, dataset, testDatasetArchiveRecord("missing", 1, "1.fb2")); err != nil {
		t.Fatalf("writeDatasetInput() error = %v", err)
	}

	_, _, _, err := LoadDatasetInput(context.Background(), prefix, nil)
	if err == nil || !strings.Contains(err.Error(), "undeclared archive source") {
		t.Fatalf("LoadDatasetInput() error = %v, want undeclared archive source", err)
	}
}

func TestRecordMatchesContentUsesNestedArchiveExtension(t *testing.T) {
	t.Parallel()

	index := 0
	tests := []struct {
		name    string
		entry   string
		mode    ContentMode
		want    bool
		wantExt string
	}{
		{name: "nested pdf excluded from fb2", entry: "1968.pdf.zip", mode: ContentFB2, wantExt: "pdf"},
		{name: "nested pdf included in usr", entry: "1968.pdf.zip", mode: ContentUSR, want: true, wantExt: "pdf"},
		{name: "nested fb2 included in fb2", entry: "1968.fb2.zip", mode: ContentFB2, want: true, wantExt: "fb2"},
		{name: "nested fb2 excluded from usr", entry: "1968.fb2.zip", mode: ContentUSR, wantExt: "fb2"},
		{name: "tar gz nested pdf", entry: "1968.pdf.tar.gz", mode: ContentUSR, want: true, wantExt: "pdf"},
		{name: "logical artifact over physical container", entry: "logical.pdf", mode: ContentUSR, want: true, wantExt: "pdf"},
		{name: "extensionless artifact uses occurrence container", entry: "opaque", mode: ContentUSR, want: true, wantExt: "zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := testDatasetArchiveRecord("archive-0001", index, tt.entry)
			rec.Artifacts[0].Occurrences = []model.Occurrence{{Archive: "archive-0001", Entry: tt.entry, Index: index}}
			if tt.name == "logical artifact over physical container" {
				rec.Artifacts[0].Occurrences[0].Entry = "logical.zip"
			}
			if tt.name == "extensionless artifact uses occurrence container" {
				rec.Artifacts[0].Occurrences[0].Entry = "opaque.zip"
			}
			rec.Claims.Catalog = &model.CatalogClaims{
				Status: []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "fb2"}}},
			}
			got, err := RecordMatchesContent(tt.mode, rec)
			if err != nil {
				t.Fatalf("RecordMatchesContent() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("RecordMatchesContent() = %v, want %v", got, tt.want)
			}
			if ext := ArchiveRecordExtension(rec); ext != tt.wantExt {
				t.Fatalf("ArchiveRecordExtension() = %q, want %q", ext, tt.wantExt)
			}
		})
	}
}

func TestRecordFileNameAndExtensionUsesArchiveEntryFinalExtension(t *testing.T) {
	t.Parallel()

	index := 0
	rec := testDatasetArchiveRecord("archive-0001", index, "dir/logical\u00a0name.txt")
	rec.Artifacts[0].Occurrences = []model.Occurrence{{Archive: "archive-0001", Entry: "dir\\logical\u00a0name.txt.zip", Index: index}}
	rec.Claims.Catalog = &model.CatalogClaims{
		Status: []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "txt"}}},
	}
	view, err := DatasetRecordClaims(rec)
	if err != nil {
		t.Fatalf("DatasetRecordClaims() error = %v", err)
	}
	fileName, ext := RecordFileNameAndExtension(rec, view)
	if fileName != "dir\\logical\u00a0name.txt" || ext != "zip" {
		t.Fatalf("RecordFileNameAndExtension() = %q, %q, want dir\\logical\\u00a0name.txt, zip", fileName, ext)
	}
}

func TestCleanse(t *testing.T) {
	t.Parallel()

	if got := Cleanse("plain text"); got != "plain text" {
		t.Fatalf("Cleanse(no-op) = %q", got)
	}
	got := Cleanse("a" + FieldSep + "b\rc\r\nd\ne\u00a0f")
	if strings.Contains(got, FieldSep) || strings.Contains(got, "\r") || strings.Contains(got, "\n") || strings.Contains(got, "\u00a0") {
		t.Fatalf("Cleanse() = %q, still contains layout characters", got)
	}
	if got != "a b c de f" {
		t.Fatalf("Cleanse() = %q, want %q", got, "a b c de f")
	}
}

func TestCleanseFileNamePercentEncodesINPXSeparators(t *testing.T) {
	t.Parallel()

	got := CleanseFileName("a" + FieldSep + "b\rc\r\nd\ne\u00a0f")
	if strings.Contains(got, FieldSep) || strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("CleanseFileName() = %q, still contains layout characters", got)
	}
	if got != "a~04b~0Dc~0D~0Ad~0Ae\u00a0f" {
		t.Fatalf("CleanseFileName() = %q, want %q", got, "a~04b~0Dc~0D~0Ad~0Ae\u00a0f")
	}
	if !FileNameEscapeAmbiguous("literal~0a-name") || FileNameEscapeAmbiguous("literal%0A-name") {
		t.Fatalf("FileNameEscapeAmbiguous() did not detect only tilde escape ambiguity")
	}
}

func TestCleanseAuthorComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "trailing colon", value: "Ливадный:", want: "Ливадный"},
		{name: "url and email", value: "(http://marsexxx.com, marsexxx@ya.ru)", want: "(http：//marsexxx.com， marsexxx@ya.ru)"},
		{name: "date time", value: "Ср, 13 окт 2010 11:41", want: "Ср， 13 окт 2010 11：41"},
		{name: "minimal", value: ":", want: ""},
		{name: "corrupt", value: "�����:������", want: ""},
		{name: "layout", value: " Last,\u00a0Jr: \r\n First" + FieldSep + "  Middle ", want: "Last， Jr： First Middle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CleanseAuthorComponent(tt.value); got != tt.want {
				t.Fatalf("CleanseAuthorComponent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCleanseGenreCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: "sf_history", want: "sf_history"},
		{name: "internal colon", value: "sf:history", want: "sf：history"},
		{name: "preserves comma", value: "sf,history", want: "sf,history"},
		{name: "boundary colon", value: ":sf:", want: "sf"},
		{name: "minimal", value: ":", want: ""},
		{name: "corrupt", value: "sf�history", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CleanseGenreCode(tt.value); got != tt.want {
				t.Fatalf("CleanseGenreCode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOutputPathValidatesCompactDumpDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prefix  string
		date    string
		want    string
		wantErr bool
	}{
		{
			name:   "empty date",
			prefix: "out/books",
			want:   "out/books.inpx",
		},
		{
			name:   "valid compact date",
			prefix: "out/books",
			date:   "20260603",
			want:   "out/books_20260603.inpx",
		},
		{
			name:   "already suffixed",
			prefix: "out/books_20260603",
			date:   "20260603",
			want:   "out/books_20260603.inpx",
		},
		{
			name:    "iso date",
			prefix:  "out/books",
			date:    "2026-06-03",
			wantErr: true,
		},
		{
			name:    "short date",
			prefix:  "out/books",
			date:    "2026063",
			wantErr: true,
		},
		{
			name:    "long date",
			prefix:  "out/books",
			date:    "202606030",
			wantErr: true,
		},
		{
			name:    "non-digit",
			prefix:  "out/books",
			date:    "2026060x",
			wantErr: true,
		},
		{
			name:    "path separator",
			prefix:  "out/books",
			date:    "202606/3",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := OutputPath(tt.prefix, Metadata{DumpDate: tt.date})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("OutputPath() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("OutputPath() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("OutputPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDatasetMetadataNormalizesISODumpDate(t *testing.T) {
	t.Parallel()

	meta := DatasetMetadata(model.Dataset{
		Library:  "flibusta",
		Database: &model.DatasetDatabase{DumpDate: "2026-07-13"},
	})
	if meta.Library != "flibusta" || meta.DumpDate != "20260713" || meta.DumpDateISO != "2026-07-13" {
		t.Fatalf("DatasetMetadata() = %#v", meta)
	}
	path, err := OutputPath("all_mhl", meta)
	if err != nil {
		t.Fatalf("OutputPath() error = %v", err)
	}
	if path != "all_mhl_20260713.inpx" {
		t.Fatalf("OutputPath() = %q", path)
	}
}

func TestEnsureDumpDateUsesCurrentDateAndWarns(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	meta := Metadata{}
	before := time.Now().UTC().Format("20060102")
	EnsureDumpDate(&meta, zap.New(core))
	after := time.Now().UTC().Format("20060102")

	if meta.DumpDate != before && meta.DumpDate != after {
		t.Fatalf("DumpDate = %q, want current date %q or %q", meta.DumpDate, before, after)
	}
	if meta.DumpDateISO == "" {
		t.Fatal("DumpDateISO is empty")
	}
	if logs.FilterMessage("INPX input metadata has empty dump date; using current date").Len() != 1 {
		t.Fatalf("logs = %#v, want one empty-date warning", logs.All())
	}
}

func TestEnsureDumpDateKeepsExistingDate(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	meta := Metadata{DumpDate: "20260603", DumpDateISO: "2026-06-03"}
	EnsureDumpDate(&meta, zap.New(core))

	if meta.DumpDate != "20260603" || meta.DumpDateISO != "2026-06-03" {
		t.Fatalf("metadata changed: %#v", meta)
	}
	if logs.Len() != 0 {
		t.Fatalf("logs = %#v, want no warnings", logs.All())
	}
}

func TestDBAuthorAmbiguityCollectorUsesDBAuthorIdentity(t *testing.T) {
	t.Parallel()

	collector := NewDBAuthorAmbiguityCollector()
	collector.AddContributor(model.Contributor{
		ID: 19026, FirstName: "Сергей", MiddleName: "Александрович", LastName: "Васильев", NickName: "археолог",
	})
	collector.AddContributor(model.Contributor{
		ID: 77926, FirstName: "Сергей", MiddleName: "Александрович", LastName: "Васильев", NickName: "поэт",
	})
	collector.AddContributor(model.Contributor{ID: 1, FirstName: "Other", LastName: "Author"})
	metadata := collector.Metadata()
	if metadata == nil || len(metadata.AmbiguousDBAuthors) != 1 {
		t.Fatalf("Metadata() = %#v, want one ambiguous group", metadata)
	}
	if metadata.AmbiguousDBAuthors[0].Key != "Васильев,Сергей,Александрович" {
		t.Fatalf("ambiguous key = %q", metadata.AmbiguousDBAuthors[0].Key)
	}
	core, logs := observer.New(zap.DebugLevel)
	disambiguator := NewAuthorDisambiguator(metadata, AuthorDisambiguationLast, zap.New(core), true)
	if disambiguator == nil {
		t.Fatal("NewAuthorDisambiguator() = nil, want disambiguator")
	}

	author := model.PersonValue{
		Identities: []model.IdentityTarget{{Scheme: "flibusta.person", Value: "19026"}},
		LastName:   "Васильев",
	}
	if got := disambiguator.LastName(author); got != "Васильев [археолог]" {
		t.Fatalf("LastName() = %q, want nickname suffix", got)
	}
	fb2Author := model.PersonValue{LastName: "Васильев"}
	if got := disambiguator.LastName(fb2Author); got != "Васильев" {
		t.Fatalf("FB2 LastName() = %q, want unchanged", got)
	}
	if logs.FilterMessage("INPX DB author disambiguated").Len() != 2 {
		t.Fatalf("debug logs = %#v, want two disambiguation messages", logs.All())
	}
}

func TestDBAuthorAmbiguityCollectorFallsBackToID(t *testing.T) {
	t.Parallel()

	collector := NewDBAuthorAmbiguityCollector()
	collector.AddContributor(model.Contributor{ID: 1, FirstName: "First", MiddleName: "Middle", LastName: "Last", NickName: "same"})
	collector.AddContributor(model.Contributor{ID: 2, FirstName: "First", MiddleName: "Middle", LastName: "Last", NickName: "same"})
	disambiguator := NewAuthorDisambiguator(collector.Metadata(), AuthorDisambiguationLast, nil, false)
	if got := disambiguator.LastName(model.PersonValue{
		Identities: []model.IdentityTarget{{Scheme: "flibusta.person", Value: "2"}},
		LastName:   "Last",
	}); got != "Last [#2]" {
		t.Fatalf("LastName() = %q, want ID suffix", got)
	}
}

func TestAuthorKeyUsesConfiguredDisambiguationField(t *testing.T) {
	t.Parallel()

	person := model.PersonValue{FirstName: "First", MiddleName: "Middle", LastName: "Last"}
	tests := []struct {
		name  string
		field AuthorDisambiguationField
		want  string
	}{
		{
			name:  "last",
			field: AuthorDisambiguationLast,
			want:  "Last [x],First,Middle",
		},
		{
			name:  "first",
			field: AuthorDisambiguationFirst,
			want:  "Last,First [x],Middle",
		},
		{
			name:  "middle",
			field: AuthorDisambiguationMiddle,
			want:  "Last,First,Middle [x]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := authorKey(person, "[x]", tt.field); got != tt.want {
				t.Fatalf("authorKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetadataForContentSelectsScopedGroups(t *testing.T) {
	t.Parallel()

	metadata := &model.INPXMetadata{
		ScopedDBAuthorAmbiguity: true,
		AmbiguousDBAuthors: []model.INPXAmbiguousDBAuthorGroup{{
			Key:     "all",
			Authors: []model.INPXAmbiguousDBAuthor{{ID: "1"}, {ID: "2"}},
		}},
		AmbiguousDBAuthorsFB2: []model.INPXAmbiguousDBAuthorGroup{{
			Key:     "fb2",
			Authors: []model.INPXAmbiguousDBAuthor{{ID: "3"}, {ID: "4"}},
		}},
		AmbiguousDBAuthorsUSR: []model.INPXAmbiguousDBAuthorGroup{{
			Key:     "usr",
			Authors: []model.INPXAmbiguousDBAuthor{{ID: "5"}, {ID: "6"}},
		}},
	}

	if got := MetadataForContent(metadata, ContentFB2).AmbiguousDBAuthors[0].Key; got != "fb2" {
		t.Fatalf("FB2 metadata key = %q", got)
	}
	if got := MetadataForContent(metadata, ContentUSR).AmbiguousDBAuthors[0].Key; got != "usr" {
		t.Fatalf("USR metadata key = %q", got)
	}
	if got := MetadataForContent(metadata, ContentAll).AmbiguousDBAuthors[0].Key; got != "all" {
		t.Fatalf("all metadata key = %q", got)
	}
}

func TestMetadataForContentKeepsEmptyScopedGroupsEmpty(t *testing.T) {
	t.Parallel()

	metadata := &model.INPXMetadata{
		ScopedDBAuthorAmbiguity: true,
		AmbiguousDBAuthors: []model.INPXAmbiguousDBAuthorGroup{{
			Key:     "all",
			Authors: []model.INPXAmbiguousDBAuthor{{ID: "1"}, {ID: "2"}},
		}},
	}
	if got := MetadataForContent(metadata, ContentFB2); got != nil {
		t.Fatalf("FB2 metadata = %#v, want nil", got)
	}
}

func TestMetadataForContentFallsBackToLegacyGroups(t *testing.T) {
	t.Parallel()

	metadata := &model.INPXMetadata{AmbiguousDBAuthors: []model.INPXAmbiguousDBAuthorGroup{{
		Key:     "legacy",
		Authors: []model.INPXAmbiguousDBAuthor{{ID: "1"}, {ID: "2"}},
	}}}
	if got := MetadataForContent(metadata, ContentFB2).AmbiguousDBAuthors[0].Key; got != "legacy" {
		t.Fatalf("legacy metadata key = %q", got)
	}
}

func writeDatasetInput(prefix string, dataset model.Dataset, records ...model.DatasetRecord) error {
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		return err
	}
	if err := w.WriteValue(dataset); err != nil {
		w.Abort()
		return err
	}
	for _, rec := range records {
		if err := w.WriteValue(rec); err != nil {
			w.Abort()
			return err
		}
	}
	return w.Close()
}

func testDatasetArchiveRecord(source string, index int, entry string) model.DatasetRecord {
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index},
		},
		Artifacts: []model.Artifact{{Name: entry}},
		Observations: []model.Observation{{
			ID:     "archive",
			Source: source,
			Kind:   "archive_entry",
			Status: "present",
		}},
	}
}
