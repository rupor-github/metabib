package main

import (
	"fmt"
	"strconv"
	"strings"

	"metabib/model"
)

func datasetRecordFromRecord(rec model.Record, archiveSources map[string]string) (model.DatasetRecord, error) {
	libraryName := rec.ID.Library
	out := model.DatasetRecord{
		Schema:       model.RecordSchemaV2,
		Observations: make([]model.Observation, 0, 3),
		Claims:       model.Claims{},
	}
	if rec.ID.Archive != nil {
		source := archiveSources[rec.ID.Archive.Path]
		if source == "" {
			return model.DatasetRecord{}, fmt.Errorf("archive %q is not declared in dataset header", rec.ID.Archive.Path)
		}
		index := rec.ID.Archive.Index
		out.Record = model.RecordDescriptor{
			Library: libraryName,
			Locator: model.RecordLocator{Kind: "archive_entry", Source: source, Index: &index},
		}
		out.Observations = append(out.Observations, model.Observation{
			ID:       "archive",
			Source:   source,
			Kind:     "archive_entry",
			Status:   "present",
			Locator:  &model.ObservationLocator{Entry: rec.ID.Archive.Entry, Index: &index},
			Coverage: "inventory",
		})
		out.Artifacts = []model.Artifact{{
			Name:      rec.ID.Archive.Entry,
			MediaType: mediaType(rec.ID.Extension),
			Occurrences: []model.Occurrence{{
				Archive:          source,
				Entry:            rec.ID.Archive.Entry,
				Index:            rec.ID.Archive.Index,
				CompressedSize:   rec.ID.Archive.CompressedSize,
				UncompressedSize: rec.ID.Archive.UncompressedSize,
				Modified:         rec.ID.Archive.Modified,
			}},
		}}
		appendDatabaseObservation(&out, rec)
		appendFB2Observation(&out, rec, source, &index)
		return out, nil
	}
	bookID := rec.ID.BookID
	out.Record = model.RecordDescriptor{
		Library: libraryName,
		Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: positiveBookID(bookID)},
	}
	appendDatabaseObservation(&out, rec)
	return out, nil
}

func appendDatabaseObservation(out *model.DatasetRecord, rec model.Record) {
	if !rec.Source.Database.Present {
		return
	}
	bookID := rec.ID.BookID
	if rec.Source.Database.Book != nil && rec.Source.Database.Book.BookID > 0 {
		bookID = rec.Source.Database.Book.BookID
	}
	out.Observations = append(out.Observations, model.Observation{
		ID:       "db",
		Source:   "database",
		Kind:     "database_book",
		Status:   "present",
		Locator:  &model.ObservationLocator{BookID: positiveBookID(bookID)},
		Coverage: "complete",
	})
	appendDatabaseIdentities(out, bookID)
	appendDatabaseClaims(out, rec.Source.Database)
	appendDatabaseArtifacts(out, rec.Source.Database)
	appendDatabaseRelations(out, rec.Source.Database)
}

func appendDatabaseIdentities(out *model.DatasetRecord, bookID int64) {
	if bookID <= 0 {
		return
	}
	if out.Identities == nil {
		out.Identities = &model.Identities{}
	}
	out.Identities.Catalog = append(out.Identities.Catalog, model.Identity{
		Scheme:      "flibusta.book",
		Value:       strconv.FormatInt(bookID, 10),
		Observation: "db",
	})
}

func appendDatabaseClaims(out *model.DatasetRecord, db model.DatabaseSource) {
	if db.Book != nil {
		appendDatabaseBookClaims(out, *db.Book)
	}
	if len(db.Authors) > 0 {
		bib := bibliographicClaims(out)
		bib.Authors = append(
			bib.Authors,
			model.Claim{Observation: "db", Value: contributorValues(db.Authors)},
		)
	}
	if len(db.Translators) > 0 {
		bib := bibliographicClaims(out)
		bib.Translators = append(
			bib.Translators,
			model.Claim{Observation: "db", Value: contributorValues(db.Translators)},
		)
	}
	if len(db.Illustrators) > 0 {
		bib := bibliographicClaims(out)
		bib.Illustrators = append(
			bib.Illustrators,
			model.Claim{Observation: "db", Value: contributorValues(db.Illustrators)},
		)
	}
	if len(db.Genres) > 0 {
		bib := bibliographicClaims(out)
		bib.Genres = append(
			bib.Genres,
			model.Claim{Observation: "db", Value: genreValues(db.Genres)},
		)
	}
	if len(db.Sequences) > 0 {
		bib := bibliographicClaims(out)
		bib.Sequences = append(
			bib.Sequences,
			model.Claim{Observation: "db", Value: sequenceValues(db.Sequences)},
		)
	}
	if db.Rating != nil {
		catalog := catalogClaims(out)
		catalog.Rating = append(
			catalog.Rating,
			model.Claim{Observation: "db", Value: ratingValue(*db.Rating)},
		)
	}
	if len(db.Filenames) > 0 {
		catalog := catalogClaims(out)
		catalog.Aliases = append(
			catalog.Aliases,
			model.Claim{Observation: "db", Value: aliasValues(db.Filenames)},
		)
	}
}

func appendDatabaseBookClaims(out *model.DatasetRecord, book model.DBBook) {
	if book.Title != "" {
		bib := bibliographicClaims(out)
		bib.Title = append(
			bib.Title,
			model.Claim{Observation: "db", Value: book.Title},
		)
	}
	if book.Lang != "" {
		bib := bibliographicClaims(out)
		bib.Language = append(
			bib.Language,
			model.Claim{Observation: "db", Value: book.Lang},
		)
	}
	if book.SrcLang != "" {
		bib := bibliographicClaims(out)
		bib.SourceLanguage = append(
			bib.SourceLanguage,
			model.Claim{Observation: "db", Value: book.SrcLang},
		)
	}
	if book.Year != 0 {
		publication := publicationClaims(out)
		year := book.Year
		publication.Year = append(
			publication.Year,
			model.Claim{Observation: "db", Value: model.YearValue{Value: &year}},
		)
	}
	if book.Time != "" {
		catalog := catalogClaims(out)
		catalog.Time = append(catalog.Time, model.Claim{Observation: "db", Value: book.Time})
	}
	if book.Modified != "" {
		catalog := catalogClaims(out)
		catalog.Modified = append(
			catalog.Modified,
			model.Claim{Observation: "db", Value: book.Modified},
		)
	}
	if book.Deleted != "" {
		catalog := catalogClaims(out)
		catalog.Deleted = append(
			catalog.Deleted,
			model.Claim{Observation: "db", Value: deletionValue(book.Deleted)},
		)
	}
	if book.FileAuthor != "" {
		catalog := catalogClaims(out)
		catalog.FileAuthor = append(
			catalog.FileAuthor,
			model.Claim{Observation: "db", Value: book.FileAuthor},
		)
	}
	if book.FileType != "" || book.MD5 != "" {
		catalog := catalogClaims(out)
		catalog.Status = append(catalog.Status, model.Claim{
			Observation: "db",
			Value:       model.CatalogStatusValue{FileType: book.FileType, MD5: book.MD5},
		})
	}
	if book.Keywords != "" {
		bib := bibliographicClaims(out)
		bib.Keywords = append(
			bib.Keywords,
			model.Claim{Observation: "db", Value: book.Keywords},
		)
	}
}

func appendDatabaseArtifacts(out *model.DatasetRecord, db model.DatabaseSource) {
	if db.Book == nil {
		return
	}
	book := *db.Book
	name := ""
	if len(db.Filenames) > 0 {
		name = db.Filenames[0]
	}
	artifact := model.Artifact{Name: name, MediaType: mediaType(book.FileType)}
	if book.FileSize > 0 {
		artifact.Size = append(artifact.Size, model.ArtifactSize{
			Observation: "db",
			Value:       uint64(book.FileSize),
			Kind:        "reported",
		})
	}
	if book.MD5 != "" {
		artifact.Checksums = append(artifact.Checksums, model.ArtifactChecksum{
			Observation: "db",
			Algorithm:   "md5",
			Scope:       "content",
			Origin:      "reported",
			Value:       book.MD5,
		})
	}
	if artifact.Name != "" || len(artifact.Size) > 0 || len(artifact.Checksums) > 0 {
		out.Artifacts = append(out.Artifacts, artifact)
	}
}

func appendDatabaseRelations(out *model.DatasetRecord, db model.DatabaseSource) {
	if db.Book != nil && db.Book.ReplacedBy > 0 {
		out.Relations = append(out.Relations, model.Relation{
			Type:        "replaced_by",
			Observation: "db",
			Target: &model.IdentityTarget{
				Scheme: "flibusta.book",
				Value:  strconv.FormatInt(db.Book.ReplacedBy, 10),
			},
		})
	}
	for _, joined := range db.JoinedBooks {
		out.Relations = append(out.Relations, model.Relation{
			Type:        "joined_books",
			Observation: "db",
			EventID:     strconv.FormatInt(joined.ID, 10),
			Time:        joined.Time,
			Participants: map[string]string{
				"bad":  formatPositiveID(joined.BadID),
				"good": formatPositiveID(joined.GoodID),
				"real": formatPositiveID(joined.RealID),
			},
		})
	}
}

func bibliographicClaims(out *model.DatasetRecord) *model.BibliographicClaims {
	if out.Claims.Bibliographic == nil {
		out.Claims.Bibliographic = &model.BibliographicClaims{}
	}
	return out.Claims.Bibliographic
}

func publicationClaims(out *model.DatasetRecord) *model.PublicationClaims {
	if out.Claims.Publication == nil {
		out.Claims.Publication = &model.PublicationClaims{}
	}
	return out.Claims.Publication
}

func catalogClaims(out *model.DatasetRecord) *model.CatalogClaims {
	if out.Claims.Catalog == nil {
		out.Claims.Catalog = &model.CatalogClaims{}
	}
	return out.Claims.Catalog
}

func contributorValues(contributors []model.Contributor) []model.PersonValue {
	values := make([]model.PersonValue, 0, len(contributors))
	for _, contributor := range contributors {
		position := contributor.Position
		value := model.PersonValue{
			FirstName:  contributor.FirstName,
			MiddleName: contributor.MiddleName,
			LastName:   contributor.LastName,
			NickName:   contributor.NickName,
			Email:      contributor.Email,
			Homepage:   contributor.Homepage,
			Gender:     contributor.Gender,
			Position:   positiveInt64(position),
		}
		if contributor.ID > 0 {
			value.Identities = append(value.Identities, model.IdentityTarget{
				Scheme: "flibusta.person",
				Value:  strconv.FormatInt(contributor.ID, 10),
			})
		}
		if contributor.UID > 0 {
			value.Identities = append(value.Identities, model.IdentityTarget{
				Scheme: "flibusta.uid",
				Value:  strconv.FormatInt(contributor.UID, 10),
			})
		}
		if contributor.MasterID > 0 {
			value.MasterID = strconv.FormatInt(contributor.MasterID, 10)
		}
		values = append(values, value)
	}
	return values
}

func genreValues(genres []model.DBGenre) []model.GenreValue {
	values := make([]model.GenreValue, 0, len(genres))
	for _, genre := range genres {
		values = append(values, model.GenreValue{
			Code:           genre.Code,
			TranslatedCode: genre.TranslatedCode,
			Description:    genre.Description,
			Meta:           genre.Meta,
		})
	}
	return values
}

func sequenceValues(sequences []model.DBSequence) []model.SequenceValue {
	values := make([]model.SequenceValue, 0, len(sequences))
	for _, sequence := range sequences {
		number := float64(sequence.Number)
		sequenceType := sequence.Type
		value := model.SequenceValue{
			Name:   sequence.Name,
			Number: &model.NumberValue{Value: &number},
			Level:  positiveInt64(sequence.Level),
			Type:   &sequenceType,
		}
		if sequence.ID > 0 {
			value.Identities = append(value.Identities, model.IdentityTarget{
				Scheme: "flibusta.sequence",
				Value:  strconv.FormatInt(sequence.ID, 10),
			})
		}
		values = append(values, value)
	}
	return values
}

func ratingValue(rating model.DBRating) model.RatingValue {
	average := rating.Average
	return model.RatingValue{
		Average: positiveFloat64(average),
		Count:   rating.Count,
		Min:     positiveInt64(rating.Min),
		Max:     positiveInt64(rating.Max),
	}
}

func aliasValues(names []string) []model.AliasValue {
	values := make([]model.AliasValue, 0, len(names))
	for _, name := range names {
		values = append(values, model.AliasValue{Name: name})
	}
	return values
}

func deletionValue(raw string) model.DeletionValue {
	value := model.DeletionValue{Raw: raw}
	switch raw {
	case "0", "false", "no":
		value.State = "active"
	case "1", "true", "yes":
		value.State = "deleted"
	}
	return value
}

func positiveInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func positiveFloat64(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func formatPositiveID(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func positiveBookID(bookID int64) *int64 {
	if bookID <= 0 {
		return nil
	}
	return &bookID
}

func appendFB2Observation(out *model.DatasetRecord, rec model.Record, source string, index *int) {
	if !rec.Source.FB2.Present {
		return
	}
	out.Observations = append(out.Observations, model.Observation{
		ID:       "fb2",
		Source:   source,
		Kind:     "fb2_description",
		Status:   "present",
		Parent:   "archive",
		Locator:  &model.ObservationLocator{Entry: rec.ID.Archive.Entry, Index: index},
		Coverage: "description",
	})
}

func mediaType(extension string) string {
	if strings.EqualFold(extension, "fb2") {
		return "application/fb2+xml"
	}
	return ""
}

func recordFileKeys(rec model.Record) []string {
	keys := make([]string, 0, len(rec.Source.Database.Filenames)+2)
	if rec.ID.FileName != "" {
		keys = append(keys, fileKey(rec.ID.FileName))
		if rec.ID.Extension != "" {
			keys = append(keys, fileKey(rec.ID.FileName+"."+rec.ID.Extension))
		}
	}
	for _, name := range rec.Source.Database.Filenames {
		keys = append(keys, fileKey(name))
	}
	return keys
}

func fileKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
