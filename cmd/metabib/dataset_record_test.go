package main

import (
	"testing"

	"metabib/model"
)

func TestDatasetRecordFromDatabaseRecordPopulatesClaims(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID:     model.RecordID{Library: "flibusta", BookID: 42, FileName: "42", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book: &model.DBBook{
				BookID:     42,
				FileSize:   123,
				Time:       "2026-07-14T04:05:06Z",
				Title:      "Database title",
				Lang:       "ru",
				SrcLang:    "en",
				FileType:   "fb2",
				Year:       1972,
				Deleted:    "1",
				FileAuthor: "file author",
				Keywords:   "one,two",
				MD5:        "0123456789abcdef0123456789abcdef",
				Modified:   "2026-07-14T05:00:00Z",
				ReplacedBy: 43,
			},
			Authors: []model.Contributor{{
				ID:         7,
				FirstName:  "First",
				MiddleName: "Middle",
				LastName:   "Last",
				NickName:   "Nick",
				Position:   0,
			}},
			Genres: []model.DBGenre{{
				ID:             9,
				Code:           "sf",
				TranslatedCode: "sci-fi",
				Description:    "Science fiction",
				Meta:           "meta",
			}},
			Sequences: []model.DBSequence{{
				ID:     10,
				Name:   "Cycle",
				Number: 0,
				Type:   0,
			}},
			Rating:      &model.DBRating{Average: 4.5, Count: 5, Min: 1, Max: 5},
			Filenames:   []string{"42.fb2"},
			JoinedBooks: []model.DBJoinedBook{{ID: 11, Time: "2026-07-14T06:00:00Z", BadID: 42, GoodID: 43, RealID: 44}},
		}},
	}

	converted, err := datasetRecordFromRecord(rec, nil)
	if err != nil {
		t.Fatalf("datasetRecordFromRecord() error = %v", err)
	}
	if converted.Schema != model.RecordSchemaV2 || converted.Record.Locator.Kind != "database_book" {
		t.Fatalf("converted record = %#v", converted.Record)
	}
	if converted.Identities == nil || len(converted.Identities.Catalog) != 1 || converted.Identities.Catalog[0].Value != "42" {
		t.Fatalf("identities = %#v", converted.Identities)
	}
	if got := converted.Claims.Bibliographic.Title[0].Value; got != "Database title" {
		t.Fatalf("title claim = %#v", got)
	}
	authors, ok := converted.Claims.Bibliographic.Authors[0].Value.([]model.PersonValue)
	if !ok || len(authors) != 1 || authors[0].FirstName != "First" || authors[0].LastName != "Last" {
		t.Fatalf("authors claim = %#v", converted.Claims.Bibliographic.Authors[0].Value)
	}
	sequences, ok := converted.Claims.Bibliographic.Sequences[0].Value.([]model.SequenceValue)
	if !ok || len(sequences) != 1 || sequences[0].Number == nil || sequences[0].Number.Value == nil || *sequences[0].Number.Value != 0 {
		t.Fatalf("sequences claim = %#v", converted.Claims.Bibliographic.Sequences[0].Value)
	}
	if sequences[0].Type == nil || *sequences[0].Type != 0 {
		t.Fatalf("sequence type = %#v, want explicit zero", sequences[0].Type)
	}
	if len(converted.Artifacts) != 1 || len(converted.Artifacts[0].Checksums) != 1 || converted.Artifacts[0].Name != "42.fb2" {
		t.Fatalf("artifacts = %#v", converted.Artifacts)
	}
	if len(converted.Relations) != 2 || converted.Relations[0].Type != "replaced_by" || converted.Relations[1].Type != "joined_books" {
		t.Fatalf("relations = %#v", converted.Relations)
	}
}
