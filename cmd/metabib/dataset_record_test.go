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
				UID:        70,
				FirstName:  "First",
				MiddleName: "Middle",
				LastName:   "Last",
				NickName:   "Nick",
				Email:      "author@example.org",
				Homepage:   "https://example.org/author",
				Gender:     "m",
				MasterID:   700,
				Position:   1,
			}},
			Translators:  []model.Contributor{{ID: 8, FirstName: "Translator", LastName: "Person", Position: 2}},
			Illustrators: []model.Contributor{{ID: 9, FirstName: "Illustrator", LastName: "Artist", Position: 3}},
			Genres: []model.DBGenre{{
				ID:             19,
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
	if converted.Schema != model.DatasetRecordSchemaV1 || converted.Record.Locator.Kind != "database_book" {
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
	if len(authors[0].Identities) != 2 || authors[0].Email != "author@example.org" || authors[0].Homepage != "https://example.org/author" ||
		authors[0].Gender != "m" || authors[0].MasterID != "700" || authors[0].Position == nil || *authors[0].Position != 1 {
		t.Fatalf("author metadata = %#v", authors[0])
	}
	translators, ok := converted.Claims.Bibliographic.Translators[0].Value.([]model.PersonValue)
	if !ok || len(translators) != 1 || translators[0].FirstName != "Translator" {
		t.Fatalf("translators claim = %#v", converted.Claims.Bibliographic.Translators[0].Value)
	}
	illustrators, ok := converted.Claims.Bibliographic.Illustrators[0].Value.([]model.PersonValue)
	if !ok || len(illustrators) != 1 || illustrators[0].FirstName != "Illustrator" {
		t.Fatalf("illustrators claim = %#v", converted.Claims.Bibliographic.Illustrators[0].Value)
	}
	sequences, ok := converted.Claims.Bibliographic.Sequences[0].Value.([]model.SequenceValue)
	if !ok || len(sequences) != 1 || sequences[0].Number == nil || sequences[0].Number.Value == nil || *sequences[0].Number.Value != 0 {
		t.Fatalf("sequences claim = %#v", converted.Claims.Bibliographic.Sequences[0].Value)
	}
	if len(sequences[0].Identities) != 1 || sequences[0].Type == nil || *sequences[0].Type != 0 {
		t.Fatalf("sequence type = %#v, want explicit zero", sequences[0].Type)
	}
	if got := converted.Claims.Catalog.Modified[0].Value; got != "2026-07-14T05:00:00Z" {
		t.Fatalf("modified claim = %#v", got)
	}
	if got := converted.Claims.Catalog.FileAuthor[0].Value; got != "file author" {
		t.Fatalf("file author claim = %#v", got)
	}
	if len(converted.Artifacts) != 1 || len(converted.Artifacts[0].Checksums) != 1 || converted.Artifacts[0].Name != "42.fb2" {
		t.Fatalf("artifacts = %#v", converted.Artifacts)
	}
	if len(converted.Relations) != 2 || converted.Relations[0].Type != "replaced_by" || converted.Relations[1].Type != "joined_books" {
		t.Fatalf("relations = %#v", converted.Relations)
	}
}

func TestDatasetRecordFromArchiveRecordPopulatesFB2Claims(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			FileName:  "book",
			Extension: "fb2",
			Archive: &model.ArchiveInfo{
				Path:             "/archives/books.zip",
				Entry:            "book.fb2",
				Index:            5,
				CompressedSize:   123,
				UncompressedSize: 456,
				ContentMD5:       "0123456789abcdef0123456789abcdef",
				Modified:         "2026-07-14T05:00:00Z",
			},
		},
		Source: model.RecordSources{FB2: model.FB2Source{
			Present: true,
			Description: &model.FB2Description{
				TitleInfo: &model.FB2TitleInfo{
					Genres: []model.FB2Genre{{Code: "sf", Match: "exact"}},
					Authors: []model.FB2Person{{
						ID:         "person-1",
						FirstName:  "Arkady",
						MiddleName: "N",
						LastName:   "Strugatsky",
						NickName:   "ABS",
						Emails:     []string{"arkady@example.org"},
						HomePages:  []string{"https://example.org/arkady"},
					}},
					Title:      "FB2 title",
					Annotation: "Annotation",
					Keywords:   "one, two",
					Date:       &model.FB2Date{Text: "1972", Value: "1972-01-01"},
					Language:   "ru",
					SourceLang: "en",
					Translators: []model.FB2Person{{
						FirstName: "Translator",
						Emails:    []string{"translator@example.org"},
					}},
					Sequences: []model.FB2Sequence{{
						Name:   "Cycle",
						Number: "7.5",
						Lang:   "ru",
						Nested: []model.FB2Sequence{{
							Name:   "Subcycle",
							Number: "2",
							Nested: []model.FB2Sequence{{Name: "Sub-subcycle", Number: "1"}},
						}},
					}},
				},
				SrcTitleInfo: &model.FB2TitleInfo{Title: "Original title", Language: "en"},
				DocumentInfo: &model.FB2DocumentInfo{
					ID:          "urn:uuid:document",
					Authors:     []model.FB2Person{{FirstName: "Doc", LastName: "Author"}},
					ProgramUsed: "metabib",
					Date:        &model.FB2Date{Text: "2026-07-14", Value: "2026-07-14"},
					SrcURLs:     []string{"https://example.org/source"},
					SrcOCR:      "ocr",
					Version:     "1.0",
					History:     "history",
					Publishers:  []model.FB2Person{{FirstName: "Doc", LastName: "Publisher"}},
				},
				PublishInfo: &model.FB2PublishInfo{
					BookName:  "Paper book",
					Publisher: "Publisher",
					City:      "City",
					Year:      "1972",
					ISBN:      "9780000000000",
					Sequences: []model.FB2Sequence{{Name: "Publication cycle", Number: "1"}},
				},
				CustomInfo: []model.FB2CustomInfo{{Type: "note", Text: "custom"}},
				Output:     []model.FB2Output{{Mode: "paid"}},
			},
		}},
	}

	converted, err := datasetRecordFromRecord(rec, map[string]string{"/archives/books.zip": "archive-0001"})
	if err != nil {
		t.Fatalf("datasetRecordFromRecord() error = %v", err)
	}
	if converted.Record.Locator.Kind != "archive_entry" || converted.Record.Locator.Source != "archive-0001" {
		t.Fatalf("record locator = %#v", converted.Record.Locator)
	}
	if converted.Identities == nil || len(converted.Identities.Document) != 1 || len(converted.Identities.Publication) != 1 {
		t.Fatalf("identities = %#v", converted.Identities)
	}
	if got := converted.Claims.Bibliographic.Title[0].Value; got != "FB2 title" {
		t.Fatalf("FB2 title claim = %#v", got)
	}
	if got := converted.Claims.Original.Title[0].Value; got != "Original title" {
		t.Fatalf("original title claim = %#v", got)
	}
	if got := converted.Claims.Original.Language[0].Value; got != "en" {
		t.Fatalf("original language claim = %#v", got)
	}
	authors, ok := converted.Claims.Bibliographic.Authors[0].Value.([]model.PersonValue)
	if !ok || len(authors) != 1 || len(authors[0].Identities) != 1 || authors[0].Position == nil || *authors[0].Position != 1 {
		t.Fatalf("FB2 authors claim = %#v", converted.Claims.Bibliographic.Authors[0].Value)
	}
	if authors[0].MiddleName != "N" || authors[0].NickName != "ABS" || len(authors[0].Emails) != 1 || len(authors[0].Homepages) != 1 {
		t.Fatalf("FB2 author metadata = %#v", authors[0])
	}
	genres, ok := converted.Claims.Bibliographic.Genres[0].Value.([]model.GenreValue)
	if !ok || len(genres) != 1 || genres[0].Code != "sf" || genres[0].Match != "exact" {
		t.Fatalf("FB2 genres claim = %#v", converted.Claims.Bibliographic.Genres[0].Value)
	}
	sequences, ok := converted.Claims.Bibliographic.Sequences[0].Value.([]model.SequenceValue)
	if !ok || len(sequences) != 1 || sequences[0].Number == nil || sequences[0].Number.Text != "7.5" {
		t.Fatalf("FB2 sequences claim = %#v", converted.Claims.Bibliographic.Sequences[0].Value)
	}
	if sequences[0].Number.Value == nil || *sequences[0].Number.Value != 7.5 || len(sequences[0].Sequences) != 1 {
		t.Fatalf("FB2 sequence value = %#v", sequences[0])
	}
	if sequences[0].Sequences[0].Name != "Subcycle" || len(sequences[0].Sequences[0].Sequences) != 1 {
		t.Fatalf("FB2 nested sequence = %#v", sequences[0].Sequences[0])
	}
	if sequences[0].Sequences[0].Sequences[0].Name != "Sub-subcycle" {
		t.Fatalf("FB2 deep nested sequence = %#v", sequences[0].Sequences[0].Sequences[0])
	}
	if got := converted.Claims.Document.ProgramUsed[0].Value; got != "metabib" {
		t.Fatalf("program used claim = %#v", got)
	}
	if got := converted.Claims.Document.SourceOCR[0].Value; got != "ocr" {
		t.Fatalf("source OCR claim = %#v", got)
	}
	if got := converted.Claims.Document.History[0].Value; got != "history" {
		t.Fatalf("history claim = %#v", got)
	}
	publishers, ok := converted.Claims.Document.Publishers[0].Value.([]model.PersonValue)
	if !ok || len(publishers) != 1 || publishers[0].FirstName != "Doc" {
		t.Fatalf("document publishers claim = %#v", converted.Claims.Document.Publishers[0].Value)
	}
	if got := converted.Claims.Publication.City[0].Value; got != "City" {
		t.Fatalf("publication city claim = %#v", got)
	}
	year, ok := converted.Claims.Publication.Year[0].Value.(model.YearValue)
	if !ok || year.Text != "1972" || year.Value == nil || *year.Value != 1972 {
		t.Fatalf("publication year claim = %#v", converted.Claims.Publication.Year[0].Value)
	}
	if got := converted.Claims.Publication.ISBN[0].Value; got != "9780000000000" {
		t.Fatalf("ISBN claim = %#v", got)
	}
	if len(converted.Claims.Document.CustomInfo) != 1 || len(converted.Claims.Document.Output) != 1 {
		t.Fatalf("document claims = %#v", converted.Claims.Document)
	}
	publicationSequences, ok := converted.Claims.Publication.Sequences[0].Value.([]model.SequenceValue)
	if !ok || len(publicationSequences) != 1 || publicationSequences[0].Name != "Publication cycle" {
		t.Fatalf("publication sequences claim = %#v", converted.Claims.Publication.Sequences[0].Value)
	}
	if len(converted.Artifacts) != 1 || len(converted.Artifacts[0].Size) != 1 || len(converted.Artifacts[0].Checksums) != 1 {
		t.Fatalf("archive artifact = %#v", converted.Artifacts)
	}
	if converted.Artifacts[0].Size[0].Observation != "archive" || converted.Artifacts[0].Size[0].Value != 456 ||
		converted.Artifacts[0].Checksums[0].Value != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("archive artifact provenance = %#v", converted.Artifacts[0])
	}
}

func TestDatasetRecordFromArchiveRecordRecordsAbsentDatabase(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:  "flibusta",
			BookID:   42,
			FileName: "42",
			Archive:  &model.ArchiveInfo{Path: "/archives/books.zip", Entry: "42.fb2", Index: 5},
		},
	}

	converted, err := datasetRecordFromRecord(rec, map[string]string{"/archives/books.zip": "archive-0001"})
	if err != nil {
		t.Fatalf("datasetRecordFromRecord() error = %v", err)
	}
	if len(converted.Observations) < 2 {
		t.Fatalf("observations = %#v, want archive and absent database", converted.Observations)
	}
	db := converted.Observations[1]
	if db.ID != "db" || db.Source != "database" || db.Kind != "database_book" || db.Status != "absent" {
		t.Fatalf("database observation = %#v", db)
	}
	if db.Locator == nil || db.Locator.BookID == nil || *db.Locator.BookID != 42 {
		t.Fatalf("database locator = %#v, want book ID 42", db.Locator)
	}
	if converted.Claims.Bibliographic != nil {
		t.Fatalf("unexpected database claims: %#v", converted.Claims)
	}
	if converted.Identities == nil || len(converted.Identities.Catalog) != 1 {
		t.Fatalf("unexpected database claims or identities: claims=%#v identities=%#v", converted.Claims, converted.Identities)
	}
	identity := converted.Identities.Catalog[0]
	if identity.Observation != "archive" || identity.Basis != "numeric_entry_stem" || identity.Value != "42" {
		t.Fatalf("inferred identity = %#v", identity)
	}
}

func TestDatasetRecordFromArchiveRecordRecordsDatabaseMatch(t *testing.T) {
	t.Parallel()

	bookID := int64(42)
	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:  "flibusta",
			BookID:   bookID,
			FileName: "42",
			Archive:  &model.ArchiveInfo{Path: "/archives/books.zip", Entry: "42.fb2", Index: 5},
		},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: bookID},
		}},
	}
	match := &model.Match{Method: "numeric_entry_stem", Input: "42", Candidate: &bookID, BookID: &bookID}

	converted, err := datasetRecordFromRecordWithMatch(
		rec,
		map[string]string{"/archives/books.zip": "archive-0001"},
		match,
		bookID,
		false,
	)
	if err != nil {
		t.Fatalf("datasetRecordFromRecordWithMatch() error = %v", err)
	}
	if len(converted.Observations) < 2 || converted.Observations[1].Match == nil {
		t.Fatalf("observations = %#v, want database match", converted.Observations)
	}
	if converted.Observations[1].Match.Method != "numeric_entry_stem" {
		t.Fatalf("database match = %#v", converted.Observations[1].Match)
	}
	if converted.Identities == nil || len(converted.Identities.Catalog) != 2 {
		t.Fatalf("catalog identities = %#v, want database and inferred", converted.Identities)
	}
}

func TestDatasetRecordFromArchiveRecordRecordsFB2Error(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library: "flibusta",
			Archive: &model.ArchiveInfo{Path: "/archives/books.zip", Entry: "bad.fb2", Index: 5},
		},
		Errors: []string{"invalid FB2"},
	}

	converted, err := datasetRecordFromRecord(rec, map[string]string{"/archives/books.zip": "archive-0001"})
	if err != nil {
		t.Fatalf("datasetRecordFromRecord() error = %v", err)
	}
	if len(converted.Observations) != 3 || converted.Observations[2].ID != "fb2" ||
		converted.Observations[2].Status != "error" {
		t.Fatalf("observations = %#v, want FB2 error", converted.Observations)
	}
	if len(converted.Issues) != 1 || converted.Issues[0].Observation != "fb2" ||
		converted.Issues[0].Code != "source_error" {
		t.Fatalf("issues = %#v, want FB2 source_error", converted.Issues)
	}
}

func TestDatasetRecordFromArchiveRecordRecordsFB2NotCollected(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library: "flibusta",
			Archive: &model.ArchiveInfo{Path: "/archives/books.zip", Entry: "book.fb2", Index: 5},
		},
	}

	converted, err := datasetRecordFromRecordWithMatch(
		rec,
		map[string]string{"/archives/books.zip": "archive-0001"},
		nil,
		0,
		true,
	)
	if err != nil {
		t.Fatalf("datasetRecordFromRecordWithMatch() error = %v", err)
	}
	if len(converted.Observations) != 3 || converted.Observations[2].ID != "fb2" ||
		converted.Observations[2].Status != "not_collected" {
		t.Fatalf("observations = %#v, want FB2 not_collected", converted.Observations)
	}
}

func TestDatasetRecordFromArchiveRecordRecordsFB2Absent(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library: "flibusta",
			Archive: &model.ArchiveInfo{Path: "/archives/books.zip", Entry: "book.fb2", Index: 5},
		},
	}

	converted, err := datasetRecordFromRecord(rec, map[string]string{"/archives/books.zip": "archive-0001"})
	if err != nil {
		t.Fatalf("datasetRecordFromRecord() error = %v", err)
	}
	if len(converted.Observations) != 3 || converted.Observations[2].ID != "fb2" ||
		converted.Observations[2].Status != "absent" {
		t.Fatalf("observations = %#v, want absent FB2 description", converted.Observations)
	}
}

func TestDatasetRecordFromArchiveRecordRecordsTitleInfoCoverage(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library: "flibusta",
			Archive: &model.ArchiveInfo{Path: "/archives/books.zip", Entry: "book.fb2", Index: 5},
		},
		Source: model.RecordSources{FB2: model.FB2Source{
			Present:     true,
			Description: &model.FB2Description{TitleInfo: &model.FB2TitleInfo{Title: "FB2 title"}},
		}},
	}

	converted, err := datasetRecordFromRecord(rec, map[string]string{"/archives/books.zip": "archive-0001"})
	if err != nil {
		t.Fatalf("datasetRecordFromRecord() error = %v", err)
	}
	if len(converted.Observations) != 3 || converted.Observations[2].Coverage != "title_info" {
		t.Fatalf("observations = %#v, want title_info FB2 coverage", converted.Observations)
	}
}
