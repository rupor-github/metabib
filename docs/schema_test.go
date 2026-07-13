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
	data, err := os.ReadFile("metabib.schema.json")
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
