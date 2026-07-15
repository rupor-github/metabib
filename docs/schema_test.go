package docs_test

import (
	jsonstd "encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	"metabib/model"
)

func TestMetabibSchemaCoversRecordModelFields(t *testing.T) {
	t.Parallel()

	schema := readMetabibSchema(t)
	for _, want := range []string{
		`"record_id"`,
		`"archive_info"`,
		`"record_sources"`,
		`"database_source"`,
		`"database_book"`,
		`"contributor"`,
		`"database_genre"`,
		`"database_sequence"`,
		`"database_rating"`,
		`"database_joined_book"`,
		`"fb2_source"`,
		`"fb2_description"`,
		`"fb2_title_info"`,
		`"fb2_genre"`,
		`"fb2_person"`,
		`"fb2_date"`,
		`"fb2_sequence"`,
		`"fb2_document_info"`,
		`"fb2_publish_info"`,
		`"fb2_custom_info"`,
		`"fb2_output"`,
		`"fb2_output_part"`,
		`"fb2_output_document_class"`,
	} {
		assertSchemaContains(t, schema, want)
	}

	for _, want := range []string{
		`"schema"`,
		`"id"`,
		`"sources"`,
		`"errors"`,
		`"library"`,
		`"book_id"`,
		`"file_name"`,
		`"extension"`,
		`"archive"`,
		`"path"`,
		`"entry"`,
		`"index"`,
		`"compressed_size"`,
		`"uncompressed_size"`,
		`"content_md5"`,
		`"modified"`,
		`"database"`,
		`"fb2"`,
		`"present"`,
		`"book"`,
		`"authors"`,
		`"translators"`,
		`"illustrators"`,
		`"genres"`,
		`"sequences"`,
		`"rating"`,
		`"filenames"`,
		`"joined_books"`,
		`"file_size"`,
		`"time"`,
		`"title"`,
		`"lang"`,
		`"src_lang"`,
		`"file_type"`,
		`"year"`,
		`"deleted"`,
		`"file_author"`,
		`"keywords"`,
		`"md5"`,
		`"replaced_by"`,
		`"first_name"`,
		`"middle_name"`,
		`"last_name"`,
		`"nick_name"`,
		`"uid"`,
		`"email"`,
		`"homepage"`,
		`"gender"`,
		`"master_id"`,
		`"position"`,
		`"code"`,
		`"translated_code"`,
		`"description"`,
		`"meta"`,
		`"name"`,
		`"number"`,
		`"level"`,
		`"type"`,
		`"average"`,
		`"count"`,
		`"min"`,
		`"max"`,
		`"bad_id"`,
		`"good_id"`,
		`"real_id"`,
		`"title_info"`,
		`"src_title_info"`,
		`"document_info"`,
		`"publish_info"`,
		`"custom_info"`,
		`"output"`,
		`"annotation"`,
		`"date"`,
		`"language"`,
		`"source_language"`,
		`"match"`,
		`"home_pages"`,
		`"emails"`,
		`"text"`,
		`"value"`,
		`"program_used"`,
		`"src_urls"`,
		`"src_ocr"`,
		`"version"`,
		`"history"`,
		`"publishers"`,
		`"book_name"`,
		`"publisher"`,
		`"city"`,
		`"isbn"`,
		`"mode"`,
		`"include_all"`,
		`"price"`,
		`"currency"`,
		`"parts"`,
		`"output_document_classes"`,
		`"href"`,
		`"include"`,
		`"create"`,
	} {
		assertSchemaContains(t, schema, want)
	}
}

func TestMetabibSchemaExcludesNonTextualFB2DescriptionFields(t *testing.T) {
	t.Parallel()

	schema := readMetabibSchema(t)
	for _, unwanted := range []string{`"coverpage"`, `"fb2_image"`, `"alt"`} {
		if strings.Contains(schema, unwanted) {
			t.Fatalf("schema contains non-textual FB2 field %s", unwanted)
		}
	}
}

func TestMetabibSchemaUsesTypedReferences(t *testing.T) {
	t.Parallel()

	schema := readMetabibSchema(t)
	for _, want := range []string{
		`"id": {"$ref": "#/$defs/record_id"}`,
		`"archive": {"$ref": "#/$defs/archive_info"}`,
		`"sources": {"$ref": "#/$defs/record_sources"}`,
		`"book": {"$ref": "#/$defs/database_book"}`,
		`"authors": {"type": "array", "items": {"$ref": "#/$defs/contributor"}}`,
		`"rating": {"$ref": "#/$defs/database_rating"}`,
		`"description": {"$ref": "#/$defs/fb2_description"}`,
		`"title_info": {"$ref": "#/$defs/fb2_title_info"}`,
		`"document_info": {"$ref": "#/$defs/fb2_document_info"}`,
		`"publish_info": {"$ref": "#/$defs/fb2_publish_info"}`,
		`"output": {"type": "array", "items": {"$ref": "#/$defs/fb2_output"}}`,
	} {
		assertSchemaContains(t, schema, want)
	}
}

func TestDatasetSchemaCoversDatasetModelFields(t *testing.T) {
	t.Parallel()

	schema := readSchema(t, "metabib-dataset.schema.json")
	for _, want := range []string{
		`"schema"`,
		`"metabib.dataset/1"`,
		`"id"`,
		`"record_schema"`,
		`"metabib.dataset_record/1"`,
		`"library"`,
		`"created"`,
		`"records"`,
		`"generator"`,
		`"normalization"`,
		`"database"`,
		`"archives"`,
		`"ordering"`,
		`"processing"`,
		`"dump_dir_hint"`,
		`"dump_date"`,
		`"path_hint"`,
		`"checksum"`,
		`"ordinal"`,
		`"fb2_entries"`,
		`"ignored"`,
		`"dummy"`,
		`"archive_content_checksum"`,
	} {
		assertSchemaContains(t, schema, want)
	}
}

func TestRecordSchemaCoversDatasetRecordModelFields(t *testing.T) {
	t.Parallel()

	schema := readSchema(t, "metabib-dataset-record.schema.json")
	for _, want := range []string{
		`"schema"`,
		`"metabib.dataset_record/1"`,
		`"record"`,
		`"locator"`,
		`"identities"`,
		`"artifacts"`,
		`"observations"`,
		`"claims"`,
		`"relations"`,
		`"issues"`,
		`"archive_entry"`,
		`"database_book"`,
		`"catalog"`,
		`"document"`,
		`"publication"`,
		`"basis"`,
		`"status"`,
		`"coverage"`,
		`"match"`,
		`"bibliographic"`,
		`"original"`,
		`"source_language"`,
		`"bibliographic_date"`,
		`"book_name"`,
		`"source_urls"`,
		`"source_ocr"`,
		`"file_author"`,
		`"media_type"`,
		`"occurrences"`,
		`"compressed_size"`,
		`"uncompressed_size"`,
		`"participants"`,
		`"retryable"`,
		`"identities"`,
		`"first_name"`,
		`"middle_name"`,
		`"last_name"`,
		`"nick_name"`,
		`"homepages"`,
		`"gender"`,
		`"master_id"`,
		`"translated_code"`,
		`"level"`,
		`"text"`,
		`"average"`,
		`"state"`,
		`"file_type"`,
		`"md5"`,
	} {
		assertSchemaContains(t, schema, want)
	}
}

func TestFullDatasetValidatesAgainstDatasetSchema(t *testing.T) {
	t.Parallel()

	datasetData, err := jsonv2.Marshal(fullDataset())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var dataset any
	if err := jsonstd.Unmarshal(datasetData, &dataset); err != nil {
		t.Fatalf("Unmarshal(dataset) error = %v", err)
	}
	var schema map[string]any
	if err := jsonstd.Unmarshal([]byte(readSchema(t, "metabib-dataset.schema.json")), &schema); err != nil {
		t.Fatalf("Unmarshal(schema) error = %v", err)
	}
	if err := validateJSONSchema(schema, dataset); err != nil {
		t.Fatalf("validate dataset against schema: %v\ndataset=%s", err, datasetData)
	}
}

func TestFullDatasetRecordValidatesAgainstRecordSchema(t *testing.T) {
	t.Parallel()

	recordData, err := jsonv2.Marshal(fullDatasetRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var record any
	if err := jsonstd.Unmarshal(recordData, &record); err != nil {
		t.Fatalf("Unmarshal(record) error = %v", err)
	}
	var schema map[string]any
	if err := jsonstd.Unmarshal([]byte(readSchema(t, "metabib-dataset-record.schema.json")), &schema); err != nil {
		t.Fatalf("Unmarshal(schema) error = %v", err)
	}
	if err := validateJSONSchema(schema, record); err != nil {
		t.Fatalf("validate record against schema: %v\nrecord=%s", err, recordData)
	}
}

func TestFullRecordValidatesAgainstMetabibSchema(t *testing.T) {
	t.Parallel()

	recordData, err := jsonv2.Marshal(fullRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var record any
	if err := jsonstd.Unmarshal(recordData, &record); err != nil {
		t.Fatalf("Unmarshal(record) error = %v", err)
	}
	var schema map[string]any
	if err := jsonstd.Unmarshal([]byte(readMetabibSchema(t)), &schema); err != nil {
		t.Fatalf("Unmarshal(schema) error = %v", err)
	}
	if err := validateJSONSchema(schema, record); err != nil {
		t.Fatalf("validate record against schema: %v\nrecord=%s", err, recordData)
	}
}

func fullDataset() model.Dataset {
	return model.Dataset{
		Schema:       model.DatasetSchemaV1,
		ID:           "urn:uuid:7bbf64c3-8bb9-4c0f-b183-6ef31da26335",
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      "flibusta",
		Created:      "2026-07-14T12:00:00Z",
		Records:      1,
		Generator:    model.DatasetGenerator{Name: "metabib", Version: "2.0.0"},
		Normalization: model.DatasetNormalization{
			Model: "metabib.claims/1",
		},
		Database: &model.DatasetDatabase{
			ID:          "database",
			Format:      "flibusta-current",
			DumpDirHint: "/data/flibusta_20260714",
			DumpDate:    "2026-07-14",
			Dumps: []model.DatasetDump{{
				Name:          "libbook.sql",
				PathHint:      "/data/flibusta_20260714/libbook.sql",
				DumpCompleted: "2026-07-14T04:05:06Z",
				Modified:      "2026-07-14T04:06:00Z",
				Checksum:      &model.Checksum{Algorithm: "md5", Value: "0123456789abcdef0123456789abcdef"},
			}},
		},
		Archives: []model.DatasetArchive{{
			ID:         "archive-0001",
			Ordinal:    0,
			Name:       "fb2-0000000001-0000001000.zip",
			PathHint:   "/data/flibusta/fb2-0000000001-0000001000.zip",
			Modified:   "2026-07-14T05:00:00Z",
			Checksum:   &model.Checksum{Algorithm: "md5", Scope: "container", Value: "abcdef0123456789abcdef0123456789"},
			Entries:    1002,
			FB2Entries: 1000,
			Ignored:    []model.IndexRange{{Start: 0, End: 0}},
			Dummy:      []model.IndexRange{{Start: 1001, End: 1001}},
		}},
		Ordering: model.DatasetOrdering{
			Mode:       "archive_entry",
			ArchiveKey: "ordinal",
			EntryKey:   "index",
			Direction:  "ascending",
		},
		Processing: model.DatasetProcessing{
			ParseFB2:               true,
			FB2Coverage:            "description",
			ArchiveContentChecksum: model.DatasetChecksumOption{Enabled: true, Algorithm: "md5"},
		},
	}
}

func fullDatasetRecord() model.DatasetRecord {
	index := 17
	bookID := int64(42)
	candidate := int64(42)
	position := int64(1)
	sequenceID := int64(10)
	sequenceLevel := int64(1)
	sequenceNumber := 7.5
	year := int64(1972)
	ratingAverage := 4.5
	ratingMin := int64(1)
	ratingMax := int64(5)
	return model.DatasetRecord{
		Schema: model.DatasetRecordSchemaV1,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: "archive-0001", Index: &index},
		},
		Identities: &model.Identities{
			Catalog: []model.Identity{
				{Scheme: "flibusta.book", Value: "42", Observation: "archive", Basis: "numeric_entry_stem"},
				{Scheme: "flibusta.book", Value: "42", Observation: "db"},
			},
			Document:    []model.Identity{{Scheme: "fb2.document", Value: "urn:uuid:document", Observation: "fb2"}},
			Publication: []model.Identity{{Scheme: "isbn", Value: "9780000000000", Observation: "fb2"}},
		},
		Artifacts: []model.Artifact{{
			Name:      "42.fb2",
			MediaType: "application/fb2+xml",
			Size: []model.ArtifactSize{
				{Observation: "db", Value: 123456, Kind: "reported"},
				{Observation: "archive", Value: 123456, Kind: "uncompressed"},
			},
			Checksums: []model.ArtifactChecksum{{
				Observation: "archive",
				Algorithm:   "md5",
				Scope:       "content",
				Origin:      "calculated",
				Value:       "0123456789abcdef0123456789abcdef",
			}},
			Occurrences: []model.Occurrence{{
				Archive:          "archive-0001",
				Entry:            "42.fb2",
				Index:            17,
				CompressedSize:   45678,
				UncompressedSize: 123456,
				Modified:         "2026-07-13T00:00:00Z",
			}},
		}},
		Observations: []model.Observation{
			{
				ID:       "archive",
				Source:   "archive-0001",
				Kind:     "archive_entry",
				Status:   "present",
				Locator:  &model.ObservationLocator{Entry: "42.fb2", Index: &index},
				Coverage: "inventory",
			},
			{
				ID:       "db",
				Source:   "database",
				Kind:     "database_book",
				Status:   "present",
				Locator:  &model.ObservationLocator{BookID: &bookID},
				Coverage: "complete",
				Match: &model.Match{
					Method:    "numeric_entry_stem",
					Input:     "42.fb2",
					Candidate: &candidate,
				},
			},
			{
				ID:       "fb2",
				Source:   "archive-0001",
				Kind:     "fb2_description",
				Status:   "present",
				Parent:   "archive",
				Locator:  &model.ObservationLocator{Entry: "42.fb2", Index: &index},
				Coverage: "description",
			},
		},
		Claims: model.Claims{
			Bibliographic: &model.BibliographicClaims{
				Title: []model.Claim{
					{Observation: "db", Value: "Database title"},
					{Observation: "fb2", Value: "FB2 title"},
				},
				Authors: []model.Claim{{Observation: "fb2", Value: []model.PersonValue{
					{
						FirstName: "Arkady",
						LastName:  "Strugatsky",
						Position:  &position,
						Emails:    []string{"arkady@example.org"},
					},
					{FirstName: "Boris", LastName: "Strugatsky"},
				}}},
				Genres:            []model.Claim{{Observation: "db", Value: []model.GenreValue{{Code: "sf", TranslatedCode: "sci-fi"}}}},
				Language:          []model.Claim{{Observation: "fb2", Value: "ru"}},
				SourceLanguage:    []model.Claim{{Observation: "db", Value: "en"}},
				BibliographicDate: []model.Claim{{Observation: "fb2", Value: model.DateValue{Text: "1972", Value: "1972-01-01"}}},
				Sequences: []model.Claim{{Observation: "fb2", Value: []model.SequenceValue{{
					Identities: []model.IdentityTarget{{Scheme: "flibusta.sequence", Value: "10"}},
					Name:       "Cycle",
					Number:     &model.NumberValue{Text: "7.5", Value: &sequenceNumber},
					Level:      &sequenceLevel,
					Type:       &sequenceID,
				}}}},
			},
			Publication: &model.PublicationClaims{
				BookName:  []model.Claim{{Observation: "fb2", Value: "Paper book"}},
				Publisher: []model.Claim{{Observation: "fb2", Value: "Publisher"}},
				Year:      []model.Claim{{Observation: "db", Value: model.YearValue{Value: &year}}},
				ISBN:      []model.Claim{{Observation: "fb2", Value: "9780000000000"}},
			},
			Document: &model.DocumentClaims{
				ProgramUsed: []model.Claim{{Observation: "fb2", Value: "metabib"}},
				SourceURLs:  []model.Claim{{Observation: "fb2", Value: []any{"https://example.org/1"}}},
				Version:     []model.Claim{{Observation: "fb2", Value: "1.0"}},
			},
			Catalog: &model.CatalogClaims{
				Time:    []model.Claim{{Observation: "db", Value: "2026-07-14T04:05:06Z"}},
				Aliases: []model.Claim{{Observation: "db", Value: []model.AliasValue{{Name: "42.fb2"}}}},
				Rating: []model.Claim{{Observation: "db", Value: model.RatingValue{
					Average: &ratingAverage,
					Count:   5,
					Min:     &ratingMin,
					Max:     &ratingMax,
				}}},
				Deleted: []model.Claim{{Observation: "db", Value: model.DeletionValue{Raw: "1", State: "deleted"}}},
				Status:  []model.Claim{{Observation: "db", Value: model.CatalogStatusValue{FileType: "fb2", MD5: "0123456789abcdef0123456789abcdef"}}},
			},
		},
		Relations: []model.Relation{{
			Type:        "replaced_by",
			Observation: "db",
			Target:      &model.IdentityTarget{Scheme: "flibusta.book", Value: "43"},
		}},
		Issues: []model.Issue{{
			Observation: "fb2",
			Stage:       "parse",
			Code:        "invalid_xml",
			Path:        "/description/title-info",
			Message:     "unexpected XML token",
			Retryable:   false,
		}},
	}
}

func fullRecord() model.Record {
	return model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "lib",
			BookID:    1,
			FileName:  "1",
			Extension: "fb2",
			Archive: &model.ArchiveInfo{
				Path:             "archive.zip",
				Entry:            "1.fb2",
				Index:            2,
				CompressedSize:   3,
				UncompressedSize: 4,
				ContentMD5:       "content-md5",
				Modified:         "2026-06-22T00:00:00Z",
			},
		},
		Source: model.RecordSources{
			Database: model.DatabaseSource{
				Present: true,
				Book: &model.DBBook{
					BookID:     1,
					FileSize:   123,
					Time:       "2026-06-22T00:00:00Z",
					Title:      "DB title",
					Lang:       "ru",
					SrcLang:    "en",
					FileType:   "fb2",
					Year:       1972,
					Deleted:    "0",
					FileAuthor: "file author",
					Keywords:   "db keywords",
					MD5:        "db-md5",
					Modified:   "2026-06-22T00:00:00Z",
					ReplacedBy: 2,
				},
				Authors:      []model.Contributor{{ID: 1, FirstName: "First", MiddleName: "Middle", LastName: "Last", NickName: "Nick", UID: 2, Email: "a@example.org", Homepage: "https://example.org", Gender: "m", MasterID: 3, Position: 4}},
				Translators:  []model.Contributor{{ID: 5, FirstName: "Tr", MiddleName: "M", LastName: "Person", NickName: "TrNick", UID: 6, Email: "t@example.org", Homepage: "https://example.net", Gender: "f", MasterID: 7, Position: 8}},
				Illustrators: []model.Contributor{{ID: 6, FirstName: "Il", LastName: "Artist"}},
				Genres:       []model.DBGenre{{ID: 9, Code: "sf", TranslatedCode: "sci-fi", Description: "Science fiction", Meta: "meta"}},
				Sequences:    []model.DBSequence{{ID: 10, Name: "Cycle", Number: 1, Level: 2, Type: 3}},
				Rating:       &model.DBRating{Average: 4, Count: 5, Min: 1, Max: 5},
				Filenames:    []string{"1.fb2"},
				JoinedBooks:  []model.DBJoinedBook{{ID: 11, Time: "2026-06-22T00:00:00Z", BadID: 1, GoodID: 2, RealID: 3}},
			},
			FB2: model.FB2Source{Present: true, Description: &model.FB2Description{
				TitleInfo: &model.FB2TitleInfo{
					Genres:      []model.FB2Genre{{Code: "sf", Match: "80"}},
					Authors:     []model.FB2Person{{ID: "a1", FirstName: "Arkady", MiddleName: "N", LastName: "Strugatsky", NickName: "ABS", HomePages: []string{"https://example.org/a"}, Emails: []string{"a@example.org"}}},
					Title:       "FB2 title",
					Annotation:  "Annotation text",
					Keywords:    "fb2 keywords",
					Date:        &model.FB2Date{Text: "1972", Value: "1972-01-01"},
					Language:    "ru",
					SourceLang:  "en",
					Translators: []model.FB2Person{{ID: "t1", FirstName: "Tr", LastName: "Person"}},
					Sequences:   []model.FB2Sequence{{Name: "Cycle", Number: "1", Lang: "ru", Nested: []model.FB2Sequence{{Name: "Nested", Number: "2"}}}},
				},
				SrcTitleInfo: &model.FB2TitleInfo{Title: "Original", Language: "en"},
				DocumentInfo: &model.FB2DocumentInfo{
					Authors:     []model.FB2Person{{ID: "d1", NickName: "doc author"}},
					ProgramUsed: "metabib",
					Date:        &model.FB2Date{Text: "2020", Value: "2020-01-02"},
					SrcURLs:     []string{"https://example.org/1"},
					SrcOCR:      "ocr",
					ID:          "doc-id",
					Version:     "1.0",
					History:     "history",
					Publishers:  []model.FB2Person{{NickName: "publisher"}},
				},
				PublishInfo: &model.FB2PublishInfo{BookName: "Paper", Publisher: "Pub", City: "City", Year: "1973", ISBN: "isbn", Sequences: []model.FB2Sequence{{Name: "PaperSeq", Number: "3"}}},
				CustomInfo:  []model.FB2CustomInfo{{Type: "source", Text: "custom"}},
				Output:      []model.FB2Output{{Mode: "free", IncludeAll: "allow", Price: "1.25", Currency: "USD", Parts: []model.FB2OutputPart{{Type: "simple", Href: "#part", Include: "require"}}, OutputDocumentClasses: []model.FB2OutputDocumentClass{{Name: "reader", Create: "allow", Price: "0", Parts: []model.FB2OutputPart{{Href: "#part2", Include: "deny"}}}}}},
			}},
		},
		Errors: []string{"warning"},
	}
}

func validateJSONSchema(schema map[string]any, value any) error {
	defs, _ := schema["$defs"].(map[string]any)
	return validateSchemaNode(schema, value, defs, "$", schema)
}

func validateSchemaNode(schema map[string]any, value any, defs map[string]any, path string, root map[string]any) error {
	if ref, ok := schema["$ref"].(string); ok {
		resolved, err := resolveSchemaRef(ref, defs, root)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return validateSchemaNode(resolved, value, defs, path, root)
	}
	if want, ok := schema["const"]; ok && value != want {
		return fmt.Errorf("%s: got const %v, want %v", path, value, want)
	}
	kind, _ := schema["type"].(string)
	switch kind {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: got %T, want object", path, value)
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, name := range stringList(schema["required"]) {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("%s: missing required property %q", path, name)
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for name := range object {
				if _, ok := properties[name]; !ok {
					return fmt.Errorf("%s: unexpected property %q", path, name)
				}
			}
		}
		for name, childSchema := range properties {
			childValue, ok := object[name]
			if !ok {
				continue
			}
			if allowed, ok := childSchema.(bool); ok {
				if !allowed {
					return fmt.Errorf("%s.%s: boolean schema is false", path, name)
				}
				continue
			}
			child, ok := childSchema.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s: invalid schema node", path, name)
			}
			if err := validateSchemaNode(child, childValue, defs, path+"."+name, root); err != nil {
				return err
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: got %T, want array", path, value)
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return nil
		}
		for i, item := range values {
			if err := validateSchemaNode(items, item, defs, fmt.Sprintf("%s[%d]", path, i), root); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: got %T, want string", path, value)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s: got %v (%T), want integer", path, value, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: got %T, want number", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: got %T, want boolean", path, value)
		}
	}
	return nil
}

func resolveSchemaRef(ref string, defs map[string]any, root map[string]any) (map[string]any, error) {
	if ref == "#" {
		return root, nil
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	if name == ref {
		return nil, fmt.Errorf("unsupported ref %q", ref)
	}
	resolved, ok := defs[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing ref %q", ref)
	}
	return resolved, nil
}

func stringList(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func readMetabibSchema(t *testing.T) string {
	t.Helper()
	return readSchema(t, "metabib.schema.json")
}

func readSchema(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func assertSchemaContains(t *testing.T, schema string, want string) {
	t.Helper()
	if !strings.Contains(schema, want) {
		t.Fatalf("schema missing %s", want)
	}
}
