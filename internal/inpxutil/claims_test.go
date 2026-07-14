package inpxutil

import (
	"testing"

	jsonv2 "encoding/json/v2"

	"metabib/model"
)

func TestDatasetRecordClaimsDecodesJSONClaimValues(t *testing.T) {
	t.Parallel()

	year := int64(1972)
	rating := 4.5
	sequenceNumber := 7.5
	rec := model.DatasetRecord{
		Schema: model.RecordSchemaV2,
		Record: model.RecordDescriptor{
			Library: "flibusta",
			Locator: model.RecordLocator{Kind: "archive_entry", Source: "archive-0001"},
		},
		Artifacts: []model.Artifact{{
			Name: "42.fb2",
			Size: []model.ArtifactSize{{Observation: "db", Value: 123, Kind: "reported"}},
			Occurrences: []model.Occurrence{{
				Archive:          "archive-0001",
				Entry:            "42.fb2",
				Index:            5,
				UncompressedSize: 456,
				Modified:         "2026-07-14T04:05:06Z",
			}},
		}},
		Observations: []model.Observation{
			{ID: "db", Source: "database", Kind: "database_book", Status: "present"},
			{ID: "fb2", Source: "archive-0001", Kind: "fb2_description", Status: "present"},
		},
		Claims: model.Claims{
			Bibliographic: &model.BibliographicClaims{
				Title: []model.Claim{
					{Observation: "db", Value: "Database title"},
					{Observation: "fb2", Value: "FB2 title"},
				},
				Authors: []model.Claim{{Observation: "fb2", Value: []model.PersonValue{{
					FirstName: "First",
					LastName:  "Last",
				}}}},
				Genres: []model.Claim{{Observation: "db", Value: []model.GenreValue{{Code: "sf"}}}},
				Sequences: []model.Claim{{Observation: "fb2", Value: []model.SequenceValue{{
					Name:   "Cycle",
					Number: &model.NumberValue{Text: "7.5", Value: &sequenceNumber},
					Sequences: []model.SequenceValue{{
						Name:   "Subcycle",
						Number: &model.NumberValue{Text: "2"},
					}},
				}}}},
				Language: []model.Claim{{Observation: "fb2", Value: "ru"}},
				Keywords: []model.Claim{{Observation: "db", Value: "one,two"}},
			},
			Original: &model.BibliographicClaims{
				Title: []model.Claim{{Observation: "fb2", Value: "Original title"}},
			},
			Publication: &model.PublicationClaims{
				Year: []model.Claim{
					{Observation: "db", Value: model.YearValue{Value: &year}},
					{Observation: "fb2", Value: model.YearValue{Text: "1972", Value: &year}},
				},
				ISBN: []model.Claim{{Observation: "fb2", Value: "9780000000000"}},
			},
			Catalog: &model.CatalogClaims{
				Time:    []model.Claim{{Observation: "db", Value: "2026-07-14T04:05:06Z"}},
				Deleted: []model.Claim{{Observation: "db", Value: model.DeletionValue{Raw: "1", State: "deleted"}}},
				Rating:  []model.Claim{{Observation: "db", Value: model.RatingValue{Average: &rating, Count: 2}}},
				Status: []model.Claim{{
					Observation: "db",
					Value:       model.CatalogStatusValue{FileType: "fb2", MD5: "0123456789abcdef0123456789abcdef"},
				}},
			},
		},
	}
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded model.DatasetRecord
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	view, err := DatasetRecordClaims(decoded)
	if err != nil {
		t.Fatalf("DatasetRecordClaims() error = %v", err)
	}
	if !view.HasDatabase || !view.HasFB2 {
		t.Fatalf("source flags = db:%v fb2:%v", view.HasDatabase, view.HasFB2)
	}
	if view.Database.Title != "Database title" || view.FB2.Title != "FB2 title" || view.Original.Title != "Original title" {
		t.Fatalf("titles = db:%q fb2:%q original:%q", view.Database.Title, view.FB2.Title, view.Original.Title)
	}
	if len(view.FB2.Authors) != 1 || view.FB2.Authors[0].LastName != "Last" {
		t.Fatalf("FB2 authors = %#v", view.FB2.Authors)
	}
	if len(view.Database.Genres) != 1 || view.Database.Genres[0].Code != "sf" {
		t.Fatalf("database genres = %#v", view.Database.Genres)
	}
	if len(view.FB2.Sequences) != 1 || len(view.FB2.Sequences[0].Sequences) != 1 {
		t.Fatalf("FB2 sequences = %#v", view.FB2.Sequences)
	}
	if view.FB2.Sequences[0].Number == nil || view.FB2.Sequences[0].Number.Value == nil || *view.FB2.Sequences[0].Number.Value != 7.5 {
		t.Fatalf("FB2 sequence number = %#v", view.FB2.Sequences[0].Number)
	}
	if view.DatabasePublication.Year != "1972" {
		t.Fatalf("database publication = %#v", view.DatabasePublication)
	}
	if view.FB2Publication.Year != "1972" || view.FB2Publication.ISBN != "9780000000000" {
		t.Fatalf("FB2 publication = %#v", view.FB2Publication)
	}
	if view.Catalog.Deleted != "1" || view.Catalog.Rating != "4.5" || view.Catalog.FileType != "fb2" {
		t.Fatalf("catalog = %#v", view.Catalog)
	}
	if view.Artifact.Name != "42.fb2" || view.Artifact.Size != 123 || view.Artifact.Date != "2026-07-14" {
		t.Fatalf("artifact = %#v", view.Artifact)
	}
}
