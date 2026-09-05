package main

import (
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"metabib/model"
)

func datasetRecordFromRecord(rec model.Record, archiveSources map[string]string) (model.DatasetRecord, error) {
	return datasetRecordFromRecordWithMatch(rec, archiveSources, nil, rec.ID.BookID, false, nil)
}

func datasetRecordFromRecordWithMatch(
	rec model.Record,
	archiveSources map[string]string,
	databaseMatch *model.Match,
	inferredBookID int64,
	fb2NotCollected bool,
	log *zap.Logger,
) (model.DatasetRecord, error) {
	libraryName := rec.ID.Library
	out := model.DatasetRecord{
		Schema:       model.DatasetRecordSchemaV1,
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
		artifact := model.Artifact{
			Name:      archiveArtifactName(rec.ID),
			MediaType: mediaType(rec.ID.Extension),
			Occurrences: []model.Occurrence{{
				Archive:          source,
				Entry:            rec.ID.Archive.Entry,
				Index:            rec.ID.Archive.Index,
				CompressedSize:   rec.ID.Archive.CompressedSize,
				UncompressedSize: rec.ID.Archive.UncompressedSize,
				Modified:         rec.ID.Archive.Modified,
			}},
		}
		if rec.ID.Archive.UncompressedSize > 0 {
			artifact.Size = append(artifact.Size, model.ArtifactSize{
				Observation: "archive",
				Value:       rec.ID.Archive.UncompressedSize,
				Kind:        "uncompressed",
			})
		}
		if rec.ID.Archive.ContentMD5 != "" {
			artifact.Checksums = append(artifact.Checksums, model.ArtifactChecksum{
				Observation: "archive",
				Algorithm:   "md5",
				Scope:       "content",
				Origin:      "calculated",
				Value:       rec.ID.Archive.ContentMD5,
			})
		}
		if rec.Source.FB2.Fingerprints != nil {
			artifact.Fingerprints = rec.Source.FB2.Fingerprints
		}
		out.Artifacts = []model.Artifact{artifact}
		appendDatabaseObservation(&out, rec, databaseMatch, log)
		appendInferredCatalogIdentity(&out, inferredBookID)
		appendFB2Observation(&out, rec, source, &index, fb2NotCollected)
		appendSidecarObservations(&out, rec, source, &index)
		appendFB2Claims(&out, rec.Source.FB2)
		appendSidecarClaims(&out, rec.Source.Sidecars)
		appendRecordIssues(&out, rec)
		return out, nil
	}
	bookID := rec.ID.BookID
	out.Record = model.RecordDescriptor{
		Library: libraryName,
		Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: positiveBookID(bookID)},
	}
	appendDatabaseObservation(&out, rec, nil, log)
	appendRecordIssues(&out, rec)
	return out, nil
}

func archiveArtifactName(id model.RecordID) string {
	if id.FileName == "" {
		if id.Archive == nil {
			return ""
		}
		return pathBase(id.Archive.Entry)
	}
	if id.Extension == "" {
		if id.ContainerExtension != "" && id.Archive != nil {
			return pathBase(id.Archive.Entry)
		}
		return id.FileName
	}
	return id.FileName + "." + id.Extension
}

func appendDatabaseObservation(out *model.DatasetRecord, rec model.Record, databaseMatch *model.Match, log *zap.Logger) {
	if !rec.Source.Database.Present {
		if rec.ID.Archive != nil {
			out.Observations = append(out.Observations, model.Observation{
				ID:      "db",
				Source:  "database",
				Kind:    "database_book",
				Status:  "absent",
				Locator: &model.ObservationLocator{BookID: positiveBookID(rec.ID.BookID)},
			})
		}
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
		Match:    databaseMatch,
	})
	appendDatabaseIdentities(out, bookID)
	appendDatabaseClaims(out, rec.Source.Database, log)
	appendDatabaseArtifacts(out, rec.Source.Database)
	appendDatabaseRelations(out, rec.Source.Database)
}

func appendInferredCatalogIdentity(out *model.DatasetRecord, bookID int64) {
	if bookID <= 0 {
		return
	}
	if out.Identities == nil {
		out.Identities = &model.Identities{}
	}
	out.Identities.Catalog = append(out.Identities.Catalog, model.Identity{
		Scheme:      "flibusta.book",
		Value:       strconv.FormatInt(bookID, 10),
		Observation: "archive",
		Basis:       "numeric_entry_stem",
	})
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

func appendDatabaseClaims(out *model.DatasetRecord, db model.DatabaseSource, log *zap.Logger) {
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
	if annotation, ok := selectDatabaseAnnotation(db, log); ok {
		bib := bibliographicClaims(out)
		bib.Annotation = append(
			bib.Annotation,
			model.Claim{Observation: "db", Value: annotation.Body, Raw: annotation},
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

func selectDatabaseAnnotation(db model.DatabaseSource, log *zap.Logger) (model.DBAnnotation, bool) {
	annotations := db.Annotations
	if len(annotations) == 0 {
		return model.DBAnnotation{}, false
	}
	if len(annotations) == 1 {
		return annotations[0], annotations[0].Body != ""
	}
	title := ""
	bookID := int64(0)
	if db.Book != nil {
		title = strings.TrimSpace(db.Book.Title)
		bookID = db.Book.BookID
	}
	var selected model.DBAnnotation
	found := false
	for _, annotation := range annotations {
		if annotation.Body == "" || !strings.EqualFold(strings.TrimSpace(annotation.Title), title) {
			continue
		}
		if !found || annotation.NID > selected.NID {
			selected = annotation
			found = true
		}
	}
	if found {
		return selected, true
	}
	for _, annotation := range annotations {
		if annotation.Body == "" {
			continue
		}
		if !found || annotation.NID > selected.NID {
			selected = annotation
			found = true
		}
	}
	if !found {
		return model.DBAnnotation{}, false
	}
	if log != nil {
		log.Debug(
			"Selected highest-NID database annotation without title match",
			zap.Int64("book_id", bookID),
			zap.String("title", title),
			zap.Int("annotations", len(annotations)),
			zap.Int64("selected_nid", selected.NID),
		)
	}
	return selected, true
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

func documentClaims(out *model.DatasetRecord) *model.DocumentClaims {
	if out.Claims.Document == nil {
		out.Claims.Document = &model.DocumentClaims{}
	}
	return out.Claims.Document
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

func appendFB2Observation(out *model.DatasetRecord, rec model.Record, source string, index *int, fb2NotCollected bool) {
	if !rec.Source.FB2.Present {
		if recordIsFB2(rec) {
			status := ""
			if len(rec.Errors) > 0 {
				status = "error"
			} else if fb2NotCollected {
				status = "not_collected"
			} else {
				status = "absent"
			}
			out.Observations = append(out.Observations, model.Observation{
				ID:      "fb2",
				Source:  source,
				Kind:    "fb2_description",
				Status:  status,
				Parent:  "archive",
				Locator: &model.ObservationLocator{Entry: rec.ID.Archive.Entry, Index: index},
			})
		}
		return
	}
	out.Observations = append(out.Observations, model.Observation{
		ID:       "fb2",
		Source:   source,
		Kind:     "fb2_description",
		Status:   "present",
		Parent:   "archive",
		Locator:  &model.ObservationLocator{Entry: rec.ID.Archive.Entry, Index: index},
		Coverage: fb2ObservationCoverage(rec.Source.FB2.Description),
	})
}

func appendSidecarObservations(out *model.DatasetRecord, rec model.Record, source string, index *int) {
	for _, sidecar := range rec.Source.Sidecars {
		if !sidecar.Present {
			continue
		}
		out.Observations = append(out.Observations, model.Observation{
			ID:       sidecar.Kind,
			Source:   source,
			Kind:     sidecar.Format,
			Status:   "present",
			Parent:   "archive",
			Locator:  &model.ObservationLocator{Entry: sidecar.Entry, Index: index},
			Coverage: fb2ObservationCoverage(sidecar.Description),
		})
	}
}

func appendRecordIssues(out *model.DatasetRecord, rec model.Record) {
	out.Issues = append(out.Issues, rec.Issues...)
	for _, msg := range rec.Errors {
		out.Issues = append(out.Issues, model.Issue{
			Observation: recordIssueObservation(rec),
			Stage:       "parse",
			Code:        "source_error",
			Message:     msg,
			Retryable:   false,
		})
	}
}

func recordIssueObservation(rec model.Record) string {
	if rec.ID.Archive != nil && recordIsFB2(rec) {
		return "fb2"
	}
	if rec.ID.Archive != nil {
		return "archive"
	}
	return ""
}

func recordIsFB2(rec model.Record) bool {
	if strings.EqualFold(rec.ID.Extension, "fb2") {
		return true
	}
	return rec.ID.Archive != nil && strings.HasSuffix(strings.ToLower(rec.ID.Archive.Entry), ".fb2")
}

func fb2ObservationCoverage(desc *model.FB2Description) string {
	if desc == nil {
		return ""
	}
	if desc.SrcTitleInfo != nil || desc.DocumentInfo != nil || desc.PublishInfo != nil ||
		len(desc.CustomInfo) > 0 || len(desc.Output) > 0 {
		return "description"
	}
	return "title_info"
}

func appendFB2Claims(out *model.DatasetRecord, src model.FB2Source) {
	if !src.Present || src.Description == nil {
		return
	}
	appendDescriptionClaims(out, src.Description, "fb2")
}

func appendSidecarClaims(out *model.DatasetRecord, sidecars []model.SidecarSource) {
	for _, sidecar := range sidecars {
		if !sidecar.Present || sidecar.Description == nil {
			continue
		}
		appendDescriptionClaims(out, sidecar.Description, sidecar.Kind)
	}
}

func appendDescriptionClaims(out *model.DatasetRecord, desc *model.FB2Description, observation string) {
	appendFB2Identities(out, desc, observation)
	if hasFB2TitleInfoClaims(desc.TitleInfo) {
		appendFB2TitleInfoClaims(desc.TitleInfo, bibliographicClaims(out), observation)
	}
	if hasFB2TitleInfoClaims(desc.SrcTitleInfo) {
		appendFB2TitleInfoClaims(desc.SrcTitleInfo, originalClaims(out), observation)
	}
	appendFB2DocumentClaims(out, desc, observation)
	appendFB2PublicationClaims(out, desc.PublishInfo, observation)
}

func appendFB2Identities(out *model.DatasetRecord, desc *model.FB2Description, observation string) {
	if desc.DocumentInfo != nil && desc.DocumentInfo.ID != "" {
		if out.Identities == nil {
			out.Identities = &model.Identities{}
		}
		out.Identities.Document = append(out.Identities.Document, model.Identity{
			Scheme:      "fb2.document",
			Value:       desc.DocumentInfo.ID,
			Observation: observation,
		})
	}
	if desc.PublishInfo != nil && desc.PublishInfo.ISBN != "" {
		if out.Identities == nil {
			out.Identities = &model.Identities{}
		}
		out.Identities.Publication = append(out.Identities.Publication, model.Identity{
			Scheme:      "isbn",
			Value:       desc.PublishInfo.ISBN,
			Observation: observation,
		})
	}
}

func originalClaims(out *model.DatasetRecord) *model.BibliographicClaims {
	if out.Claims.Original == nil {
		out.Claims.Original = &model.BibliographicClaims{}
	}
	return out.Claims.Original
}

func hasFB2TitleInfoClaims(titleInfo *model.FB2TitleInfo) bool {
	return titleInfo != nil && (titleInfo.Title != "" || len(titleInfo.Authors) > 0 || len(titleInfo.Translators) > 0 ||
		len(titleInfo.Genres) > 0 || titleInfo.Annotation != "" || titleInfo.Keywords != "" || titleInfo.Date != nil ||
		titleInfo.Language != "" || titleInfo.SourceLang != "" || len(titleInfo.Sequences) > 0)
}

func appendFB2TitleInfoClaims(titleInfo *model.FB2TitleInfo, claims *model.BibliographicClaims, observation string) {
	if titleInfo.Title != "" {
		claims.Title = append(claims.Title, model.Claim{Observation: observation, Value: titleInfo.Title})
	}
	if len(titleInfo.Authors) > 0 {
		claims.Authors = append(claims.Authors, model.Claim{Observation: observation, Value: fb2PersonValues(titleInfo.Authors)})
	}
	if len(titleInfo.Translators) > 0 {
		claims.Translators = append(claims.Translators, model.Claim{Observation: observation, Value: fb2PersonValues(titleInfo.Translators)})
	}
	if len(titleInfo.Genres) > 0 {
		claims.Genres = append(claims.Genres, model.Claim{Observation: observation, Value: fb2GenreValues(titleInfo.Genres)})
	}
	if titleInfo.Annotation != "" {
		claims.Annotation = append(claims.Annotation, model.Claim{Observation: observation, Value: titleInfo.Annotation})
	}
	if titleInfo.Keywords != "" {
		claims.Keywords = append(claims.Keywords, model.Claim{Observation: observation, Value: titleInfo.Keywords})
	}
	if titleInfo.Date != nil {
		claims.BibliographicDate = append(claims.BibliographicDate, model.Claim{Observation: observation, Value: fb2DateValue(*titleInfo.Date)})
	}
	if titleInfo.Language != "" {
		claims.Language = append(claims.Language, model.Claim{Observation: observation, Value: titleInfo.Language})
	}
	if titleInfo.SourceLang != "" {
		claims.SourceLanguage = append(claims.SourceLanguage, model.Claim{Observation: observation, Value: titleInfo.SourceLang})
	}
	if len(titleInfo.Sequences) > 0 {
		claims.Sequences = append(claims.Sequences, model.Claim{Observation: observation, Value: fb2SequenceValues(titleInfo.Sequences)})
	}
}

func appendFB2DocumentClaims(out *model.DatasetRecord, desc *model.FB2Description, observation string) {
	if desc.DocumentInfo != nil {
		docInfo := desc.DocumentInfo
		if len(docInfo.Authors) > 0 {
			claims := documentClaims(out)
			claims.Authors = append(claims.Authors, model.Claim{Observation: observation, Value: fb2PersonValues(docInfo.Authors)})
		}
		if docInfo.ProgramUsed != "" {
			claims := documentClaims(out)
			claims.ProgramUsed = append(claims.ProgramUsed, model.Claim{Observation: observation, Value: docInfo.ProgramUsed})
		}
		if docInfo.Date != nil {
			claims := documentClaims(out)
			claims.Date = append(claims.Date, model.Claim{Observation: observation, Value: fb2DateValue(*docInfo.Date)})
		}
		if len(docInfo.SrcURLs) > 0 {
			claims := documentClaims(out)
			claims.SourceURLs = append(claims.SourceURLs, model.Claim{Observation: observation, Value: docInfo.SrcURLs})
		}
		if docInfo.SrcOCR != "" {
			claims := documentClaims(out)
			claims.SourceOCR = append(claims.SourceOCR, model.Claim{Observation: observation, Value: docInfo.SrcOCR})
		}
		if docInfo.Version != "" {
			claims := documentClaims(out)
			claims.Version = append(claims.Version, model.Claim{Observation: observation, Value: docInfo.Version})
		}
		if docInfo.History != "" {
			claims := documentClaims(out)
			claims.History = append(claims.History, model.Claim{Observation: observation, Value: docInfo.History})
		}
		if len(docInfo.Publishers) > 0 {
			claims := documentClaims(out)
			claims.Publishers = append(claims.Publishers, model.Claim{Observation: observation, Value: fb2PersonValues(docInfo.Publishers)})
		}
	}
	if len(desc.CustomInfo) > 0 {
		claims := documentClaims(out)
		claims.CustomInfo = append(claims.CustomInfo, model.Claim{Observation: observation, Value: desc.CustomInfo})
	}
	if len(desc.Output) > 0 {
		claims := documentClaims(out)
		claims.Output = append(claims.Output, model.Claim{Observation: observation, Value: desc.Output})
	}
}

func appendFB2PublicationClaims(out *model.DatasetRecord, publishInfo *model.FB2PublishInfo, observation string) {
	if publishInfo == nil {
		return
	}
	if publishInfo.BookName != "" {
		claims := publicationClaims(out)
		claims.BookName = append(claims.BookName, model.Claim{Observation: observation, Value: publishInfo.BookName})
	}
	if publishInfo.Publisher != "" {
		claims := publicationClaims(out)
		claims.Publisher = append(claims.Publisher, model.Claim{Observation: observation, Value: publishInfo.Publisher})
	}
	if publishInfo.City != "" {
		claims := publicationClaims(out)
		claims.City = append(claims.City, model.Claim{Observation: observation, Value: publishInfo.City})
	}
	if publishInfo.Year != "" {
		claims := publicationClaims(out)
		claims.Year = append(claims.Year, model.Claim{Observation: observation, Value: yearValue(publishInfo.Year)})
	}
	if publishInfo.ISBN != "" {
		claims := publicationClaims(out)
		claims.ISBN = append(claims.ISBN, model.Claim{Observation: observation, Value: publishInfo.ISBN})
	}
	if len(publishInfo.Sequences) > 0 {
		claims := publicationClaims(out)
		claims.Sequences = append(claims.Sequences, model.Claim{Observation: observation, Value: fb2SequenceValues(publishInfo.Sequences)})
	}
}

func fb2PersonValues(people []model.FB2Person) []model.PersonValue {
	values := make([]model.PersonValue, 0, len(people))
	for i, person := range people {
		position := int64(i + 1)
		value := model.PersonValue{
			FirstName:  person.FirstName,
			MiddleName: person.MiddleName,
			LastName:   person.LastName,
			NickName:   person.NickName,
			Emails:     person.Emails,
			Homepages:  person.HomePages,
			Position:   &position,
		}
		if person.ID != "" {
			value.Identities = append(value.Identities, model.IdentityTarget{Scheme: "fb2.person", Value: person.ID})
		}
		values = append(values, value)
	}
	return values
}

func fb2GenreValues(genres []model.FB2Genre) []model.GenreValue {
	values := make([]model.GenreValue, 0, len(genres))
	for _, genre := range genres {
		values = append(values, model.GenreValue{Code: genre.Code, Match: genre.Match})
	}
	return values
}

func fb2SequenceValues(sequences []model.FB2Sequence) []model.SequenceValue {
	values := make([]model.SequenceValue, 0, len(sequences))
	for _, sequence := range sequences {
		values = append(values, fb2SequenceValue(sequence))
	}
	return values
}

func fb2SequenceValue(sequence model.FB2Sequence) model.SequenceValue {
	return model.SequenceValue{
		Name:      sequence.Name,
		Number:    numberValue(sequence.Number),
		Language:  sequence.Lang,
		Sequences: fb2SequenceValues(sequence.Nested),
	}
}

func fb2DateValue(date model.FB2Date) model.DateValue {
	return model.DateValue(date)
}

func yearValue(raw string) model.YearValue {
	value := model.YearValue{Text: raw}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err == nil {
		value.Value = &parsed
	}
	return value
}

func numberValue(raw string) *model.NumberValue {
	if raw == "" {
		return nil
	}
	value := model.NumberValue{Text: raw}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err == nil {
		value.Value = &parsed
	}
	return &value
}

func mediaType(extension string) string {
	if strings.EqualFold(extension, "fb2") {
		return "application/fb2+xml"
	}
	return ""
}

func recordFileKeys(rec model.Record) []string {
	candidates := recordFileCandidates(rec)
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if skipDatabaseFileKey(rec, candidate) {
			continue
		}
		keys = append(keys, candidate.key)
	}
	return keys
}

func skipDatabaseFileKey(rec model.Record, candidate recordFileCandidate) bool {
	if rec.ID.Extension == "" || candidate.input != rec.ID.FileName {
		return false
	}
	id, ok := numericFileKey(candidate.key)
	return !ok || id != rec.ID.BookID
}

func numericFileKey(key string) (int64, bool) {
	id, err := strconv.ParseInt(key, 10, 64)
	return id, err == nil && id > 0
}

type recordFileCandidate struct {
	input string
	key   string
}

func recordFileCandidates(rec model.Record) []recordFileCandidate {
	candidates := make([]recordFileCandidate, 0, len(rec.Source.Database.Filenames)+3)
	appendCandidate := func(input string) {
		if input != "" {
			candidates = append(candidates, recordFileCandidate{input: input, key: fileKey(input)})
		}
	}
	appendCandidate(rec.ID.FileName)
	if rec.ID.FileName != "" && rec.ID.Extension != "" {
		appendCandidate(rec.ID.FileName + "." + rec.ID.Extension)
	}
	if rec.ID.Archive != nil {
		appendCandidate(pathBase(rec.ID.Archive.Entry))
		appendCandidate(rec.ID.Archive.Entry)
	}
	for _, name := range rec.Source.Database.Filenames {
		appendCandidate(name)
	}
	return candidates
}

func pathBase(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func fileKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
