package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"

	"metabib/model"
)

func datasetRecordFromRecord(rec model.Record, archiveSources map[string]string) (model.DatasetRecord, error) {
	return datasetRecordFromRecordWithMatch(rec, archiveSources, nil, rec.ID.BookID, false, true, true, nil)
}

func datasetRecordFromRecordWithMatch(
	rec model.Record,
	archiveSources map[string]string,
	databaseMatch *model.Match,
	inferredBookID int64,
	fb2NotCollected bool,
	fb2ReplacementQualityCheck bool,
	databaseReplacementQualityCheck bool,
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
		appendDatabaseObservation(&out, rec, databaseMatch, databaseReplacementQualityCheck, log)
		appendInferredCatalogIdentity(&out, inferredBookID)
		appendFB2Observation(&out, rec, source, &index, fb2NotCollected)
		appendSidecarObservations(&out, rec, source, &index)
		appendFB2Claims(&out, rec.Source.FB2, fb2ReplacementQualityCheck)
		appendSidecarClaims(&out, rec.Source.Sidecars, fb2ReplacementQualityCheck)
		appendRecordIssues(&out, rec)
		return out, nil
	}
	bookID := rec.ID.BookID
	out.Record = model.RecordDescriptor{
		Library: libraryName,
		Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: positiveBookID(bookID)},
	}
	appendDatabaseObservation(&out, rec, nil, databaseReplacementQualityCheck, log)
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

func appendDatabaseObservation(
	out *model.DatasetRecord,
	rec model.Record,
	databaseMatch *model.Match,
	databaseReplacementQualityCheck bool,
	log *zap.Logger,
) {
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
	quality := metadataQuality{
		enabled:     databaseReplacementQualityCheck,
		observation: "db",
		source:      "Database",
		out:         out,
		seen:        make(map[string]struct{}),
	}
	appendDatabaseIdentities(out, bookID)
	appendDatabaseClaims(out, rec.Source.Database, quality, log)
	appendDatabaseArtifacts(out, rec.Source.Database, quality)
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

func appendDatabaseClaims(out *model.DatasetRecord, db model.DatabaseSource, quality metadataQuality, log *zap.Logger) {
	if db.Book != nil {
		appendDatabaseBookClaims(out, *db.Book, quality)
	}
	if authors := contributorValues(db.Authors, quality, "authors"); len(authors) > 0 {
		bib := bibliographicClaims(out)
		bib.Authors = append(
			bib.Authors,
			model.Claim{Observation: "db", Value: authors},
		)
	}
	if translators := contributorValues(db.Translators, quality, "translators"); len(translators) > 0 {
		bib := bibliographicClaims(out)
		bib.Translators = append(
			bib.Translators,
			model.Claim{Observation: "db", Value: translators},
		)
	}
	if illustrators := contributorValues(db.Illustrators, quality, "illustrators"); len(illustrators) > 0 {
		bib := bibliographicClaims(out)
		bib.Illustrators = append(
			bib.Illustrators,
			model.Claim{Observation: "db", Value: illustrators},
		)
	}
	if genres := genreValues(db.Genres, quality); len(genres) > 0 {
		bib := bibliographicClaims(out)
		bib.Genres = append(
			bib.Genres,
			model.Claim{Observation: "db", Value: genres},
		)
	}
	if sequences := sequenceValues(db.Sequences, quality); len(sequences) > 0 {
		bib := bibliographicClaims(out)
		bib.Sequences = append(
			bib.Sequences,
			model.Claim{Observation: "db", Value: sequences},
		)
	}
	if db.Rating != nil {
		catalog := catalogClaims(out)
		catalog.Rating = append(
			catalog.Rating,
			model.Claim{Observation: "db", Value: ratingValue(*db.Rating)},
		)
	}
	if annotation, annotationIndex, ok := selectDatabaseAnnotation(db, log); ok {
		annotationPath := fmt.Sprintf("annotations[%d]", annotationIndex)
		annotation.Title = quality.text(annotationPath+".title", annotation.Title)
		if body := quality.text(annotationPath+".body", annotation.Body); body != "" {
			annotation.Body = body
			bib := bibliographicClaims(out)
			bib.Annotation = append(
				bib.Annotation,
				model.Claim{Observation: "db", Value: body, Raw: annotation},
			)
		}
	}
	if aliases := aliasValues(db.Filenames, quality); len(aliases) > 0 {
		catalog := catalogClaims(out)
		catalog.Aliases = append(
			catalog.Aliases,
			model.Claim{Observation: "db", Value: aliases},
		)
	}
}

func selectDatabaseAnnotation(db model.DatabaseSource, log *zap.Logger) (model.DBAnnotation, int, bool) {
	annotations := db.Annotations
	if len(annotations) == 0 {
		return model.DBAnnotation{}, 0, false
	}
	if len(annotations) == 1 {
		return annotations[0], 0, annotations[0].Body != ""
	}
	title := ""
	bookID := int64(0)
	if db.Book != nil {
		title = strings.TrimSpace(db.Book.Title)
		bookID = db.Book.BookID
	}
	var selected model.DBAnnotation
	selectedIndex := 0
	found := false
	for i, annotation := range annotations {
		if annotation.Body == "" || !strings.EqualFold(strings.TrimSpace(annotation.Title), title) {
			continue
		}
		if !found || annotation.NID > selected.NID {
			selected = annotation
			selectedIndex = i
			found = true
		}
	}
	if found {
		return selected, selectedIndex, true
	}
	for i, annotation := range annotations {
		if annotation.Body == "" {
			continue
		}
		if !found || annotation.NID > selected.NID {
			selected = annotation
			selectedIndex = i
			found = true
		}
	}
	if !found {
		return model.DBAnnotation{}, 0, false
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
	return selected, selectedIndex, true
}

func appendDatabaseBookClaims(out *model.DatasetRecord, book model.DBBook, quality metadataQuality) {
	if title := quality.text("book.title", book.Title); title != "" {
		bib := bibliographicClaims(out)
		bib.Title = append(
			bib.Title,
			model.Claim{Observation: "db", Value: title},
		)
	}
	if lang := quality.text("book.lang", book.Lang); lang != "" {
		bib := bibliographicClaims(out)
		bib.Language = append(
			bib.Language,
			model.Claim{Observation: "db", Value: lang},
		)
	}
	if srcLang := quality.text("book.src_lang", book.SrcLang); srcLang != "" {
		bib := bibliographicClaims(out)
		bib.SourceLanguage = append(
			bib.SourceLanguage,
			model.Claim{Observation: "db", Value: srcLang},
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
	if bookTime := quality.text("book.time", book.Time); bookTime != "" {
		catalog := catalogClaims(out)
		catalog.Time = append(catalog.Time, model.Claim{Observation: "db", Value: bookTime})
	}
	if modified := quality.text("book.modified", book.Modified); modified != "" {
		catalog := catalogClaims(out)
		catalog.Modified = append(
			catalog.Modified,
			model.Claim{Observation: "db", Value: modified},
		)
	}
	if deleted := quality.text("book.deleted", book.Deleted); deleted != "" {
		catalog := catalogClaims(out)
		catalog.Deleted = append(
			catalog.Deleted,
			model.Claim{Observation: "db", Value: deletionValue(deleted)},
		)
	}
	if fileAuthor := quality.text("book.file_author", book.FileAuthor); fileAuthor != "" {
		catalog := catalogClaims(out)
		catalog.FileAuthor = append(
			catalog.FileAuthor,
			model.Claim{Observation: "db", Value: fileAuthor},
		)
	}
	fileType := quality.text("book.file_type", book.FileType)
	md5 := quality.text("book.md5", book.MD5)
	if fileType != "" || md5 != "" {
		catalog := catalogClaims(out)
		catalog.Status = append(catalog.Status, model.Claim{
			Observation: "db",
			Value:       model.CatalogStatusValue{FileType: fileType, MD5: md5},
		})
	}
	if keywords := quality.text("book.keywords", book.Keywords); keywords != "" {
		bib := bibliographicClaims(out)
		bib.Keywords = append(
			bib.Keywords,
			model.Claim{Observation: "db", Value: keywords},
		)
	}
}

func appendDatabaseArtifacts(out *model.DatasetRecord, db model.DatabaseSource, quality metadataQuality) {
	if db.Book == nil {
		return
	}
	book := *db.Book
	name := ""
	if len(db.Filenames) > 0 {
		name = quality.text("filenames[0]", db.Filenames[0])
	}
	fileType := quality.text("book.file_type", book.FileType)
	artifact := model.Artifact{Name: name, MediaType: mediaType(fileType)}
	if book.FileSize > 0 {
		artifact.Size = append(artifact.Size, model.ArtifactSize{
			Observation: "db",
			Value:       uint64(book.FileSize),
			Kind:        "reported",
		})
	}
	if md5 := quality.text("book.md5", book.MD5); md5 != "" {
		artifact.Checksums = append(artifact.Checksums, model.ArtifactChecksum{
			Observation: "db",
			Algorithm:   "md5",
			Scope:       "content",
			Origin:      "reported",
			Value:       md5,
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

func contributorValues(contributors []model.Contributor, quality metadataQuality, path string) []model.PersonValue {
	values := make([]model.PersonValue, 0, len(contributors))
	for i, contributor := range contributors {
		contributorPath := fmt.Sprintf("%s[%d]", path, i)
		position := contributor.Position
		value := model.PersonValue{
			FirstName:  quality.text(contributorPath+".first_name", contributor.FirstName),
			MiddleName: quality.text(contributorPath+".middle_name", contributor.MiddleName),
			LastName:   quality.text(contributorPath+".last_name", contributor.LastName),
			NickName:   quality.text(contributorPath+".nick_name", contributor.NickName),
			Email:      quality.text(contributorPath+".email", contributor.Email),
			Homepage:   quality.text(contributorPath+".homepage", contributor.Homepage),
			Gender:     quality.text(contributorPath+".gender", contributor.Gender),
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
		rawRenderable := contributor.FirstName != "" || contributor.MiddleName != "" || contributor.LastName != ""
		if hasRenderablePersonName(value) || !rawRenderable {
			values = append(values, value)
		}
	}
	return values
}

func genreValues(genres []model.DBGenre, quality metadataQuality) []model.GenreValue {
	values := make([]model.GenreValue, 0, len(genres))
	for i, genre := range genres {
		genrePath := fmt.Sprintf("genres[%d]", i)
		code := quality.text(genrePath+".code", genre.Code)
		translatedCode := quality.text(genrePath+".translated_code", genre.TranslatedCode)
		description := quality.text(genrePath+".description", genre.Description)
		meta := quality.text(genrePath+".meta", genre.Meta)
		rawText := genre.Code != "" || genre.TranslatedCode != "" || genre.Description != "" || genre.Meta != ""
		if rawText && code == "" && translatedCode == "" && description == "" && meta == "" {
			continue
		}
		values = append(values, model.GenreValue{
			Code:           code,
			TranslatedCode: translatedCode,
			Description:    description,
			Meta:           meta,
		})
	}
	return values
}

func sequenceValues(sequences []model.DBSequence, quality metadataQuality) []model.SequenceValue {
	values := make([]model.SequenceValue, 0, len(sequences))
	for i, sequence := range sequences {
		name := quality.text(fmt.Sprintf("sequences[%d].name", i), sequence.Name)
		if sequence.Name != "" && name == "" {
			continue
		}
		number := float64(sequence.Number)
		sequenceType := sequence.Type
		value := model.SequenceValue{
			Name:   name,
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

func aliasValues(names []string, quality metadataQuality) []model.AliasValue {
	values := make([]model.AliasValue, 0, len(names))
	for i, name := range names {
		alias := quality.text(fmt.Sprintf("filenames[%d]", i), name)
		if name == "" || alias != "" {
			values = append(values, model.AliasValue{Name: alias})
		}
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

func appendFB2Claims(out *model.DatasetRecord, src model.FB2Source, fb2ReplacementQualityCheck bool) {
	if !src.Present || src.Description == nil {
		return
	}
	appendDescriptionClaims(out, src.Description, "fb2", fb2ReplacementQualityCheck)
}

func appendSidecarClaims(out *model.DatasetRecord, sidecars []model.SidecarSource, fb2ReplacementQualityCheck bool) {
	for _, sidecar := range sidecars {
		if !sidecar.Present || sidecar.Description == nil {
			continue
		}
		appendDescriptionClaims(out, sidecar.Description, sidecar.Kind, fb2ReplacementQualityCheck)
	}
}

func appendDescriptionClaims(out *model.DatasetRecord, desc *model.FB2Description, observation string, fb2ReplacementQualityCheck bool) {
	quality := metadataQuality{enabled: fb2ReplacementQualityCheck, observation: observation, source: "FB2", out: out}
	appendFB2Identities(out, desc, observation)
	if hasFB2TitleInfoClaims(desc.TitleInfo) {
		appendFB2TitleInfoClaims(desc.TitleInfo, bibliographicClaims(out), observation, quality, "description.title_info")
	}
	if hasFB2TitleInfoClaims(desc.SrcTitleInfo) {
		appendFB2TitleInfoClaims(desc.SrcTitleInfo, originalClaims(out), observation, quality, "description.src_title_info")
	}
	appendFB2DocumentClaims(out, desc, observation, quality)
	appendFB2PublicationClaims(out, desc.PublishInfo, observation, quality)
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

func appendFB2TitleInfoClaims(
	titleInfo *model.FB2TitleInfo,
	claims *model.BibliographicClaims,
	observation string,
	quality metadataQuality,
	path string,
) {
	if title := quality.text(path+".title", titleInfo.Title); title != "" {
		claims.Title = append(claims.Title, model.Claim{Observation: observation, Value: title})
	}
	if authors := fb2PersonValues(titleInfo.Authors, quality, path+".authors"); len(authors) > 0 {
		claims.Authors = append(claims.Authors, model.Claim{Observation: observation, Value: authors})
	}
	if translators := fb2PersonValues(titleInfo.Translators, quality, path+".translators"); len(translators) > 0 {
		claims.Translators = append(claims.Translators, model.Claim{Observation: observation, Value: translators})
	}
	if len(titleInfo.Genres) > 0 {
		claims.Genres = append(claims.Genres, model.Claim{Observation: observation, Value: fb2GenreValues(titleInfo.Genres)})
	}
	if annotation := quality.text(path+".annotation", titleInfo.Annotation); annotation != "" {
		claims.Annotation = append(claims.Annotation, model.Claim{Observation: observation, Value: annotation})
	}
	if keywords := quality.text(path+".keywords", titleInfo.Keywords); keywords != "" {
		claims.Keywords = append(claims.Keywords, model.Claim{Observation: observation, Value: keywords})
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
	if sequences := fb2SequenceValues(titleInfo.Sequences, quality, path+".sequences"); len(sequences) > 0 {
		claims.Sequences = append(claims.Sequences, model.Claim{Observation: observation, Value: sequences})
	}
}

func appendFB2DocumentClaims(out *model.DatasetRecord, desc *model.FB2Description, observation string, quality metadataQuality) {
	if desc.DocumentInfo != nil {
		docInfo := desc.DocumentInfo
		if authors := fb2PersonValues(docInfo.Authors, quality, "description.document_info.authors"); len(authors) > 0 {
			claims := documentClaims(out)
			claims.Authors = append(claims.Authors, model.Claim{Observation: observation, Value: authors})
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
		if srcOCR := quality.text("description.document_info.src_ocr", docInfo.SrcOCR); srcOCR != "" {
			claims := documentClaims(out)
			claims.SourceOCR = append(claims.SourceOCR, model.Claim{Observation: observation, Value: srcOCR})
		}
		if docInfo.Version != "" {
			claims := documentClaims(out)
			claims.Version = append(claims.Version, model.Claim{Observation: observation, Value: docInfo.Version})
		}
		if history := quality.text("description.document_info.history", docInfo.History); history != "" {
			claims := documentClaims(out)
			claims.History = append(claims.History, model.Claim{Observation: observation, Value: history})
		}
		if publishers := fb2PersonValues(docInfo.Publishers, quality, "description.document_info.publishers"); len(publishers) > 0 {
			claims := documentClaims(out)
			claims.Publishers = append(claims.Publishers, model.Claim{Observation: observation, Value: publishers})
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

func appendFB2PublicationClaims(out *model.DatasetRecord, publishInfo *model.FB2PublishInfo, observation string, quality metadataQuality) {
	if publishInfo == nil {
		return
	}
	if bookName := quality.text("description.publish_info.book_name", publishInfo.BookName); bookName != "" {
		claims := publicationClaims(out)
		claims.BookName = append(claims.BookName, model.Claim{Observation: observation, Value: bookName})
	}
	if publisher := quality.text("description.publish_info.publisher", publishInfo.Publisher); publisher != "" {
		claims := publicationClaims(out)
		claims.Publisher = append(claims.Publisher, model.Claim{Observation: observation, Value: publisher})
	}
	if city := quality.text("description.publish_info.city", publishInfo.City); city != "" {
		claims := publicationClaims(out)
		claims.City = append(claims.City, model.Claim{Observation: observation, Value: city})
	}
	if publishInfo.Year != "" {
		claims := publicationClaims(out)
		claims.Year = append(claims.Year, model.Claim{Observation: observation, Value: yearValue(publishInfo.Year)})
	}
	if publishInfo.ISBN != "" {
		claims := publicationClaims(out)
		claims.ISBN = append(claims.ISBN, model.Claim{Observation: observation, Value: publishInfo.ISBN})
	}
	if sequences := fb2SequenceValues(publishInfo.Sequences, quality, "description.publish_info.sequences"); len(sequences) > 0 {
		claims := publicationClaims(out)
		claims.Sequences = append(claims.Sequences, model.Claim{Observation: observation, Value: sequences})
	}
}

func fb2PersonValues(people []model.FB2Person, quality metadataQuality, path string) []model.PersonValue {
	values := make([]model.PersonValue, 0, len(people))
	for i, person := range people {
		position := int64(i + 1)
		personPath := fmt.Sprintf("%s[%d]", path, i)
		value := model.PersonValue{
			FirstName:  quality.text(personPath+".first_name", person.FirstName),
			MiddleName: quality.text(personPath+".middle_name", person.MiddleName),
			LastName:   quality.text(personPath+".last_name", person.LastName),
			NickName:   quality.text(personPath+".nick_name", person.NickName),
			Emails:     person.Emails,
			Homepages:  person.HomePages,
			Position:   &position,
		}
		if person.ID != "" {
			value.Identities = append(value.Identities, model.IdentityTarget{Scheme: "fb2.person", Value: person.ID})
		}
		if hasRenderablePersonName(value) {
			values = append(values, value)
		}
	}
	return values
}

func hasRenderablePersonName(value model.PersonValue) bool {
	return value.FirstName != "" || value.MiddleName != "" || value.LastName != ""
}

func fb2GenreValues(genres []model.FB2Genre) []model.GenreValue {
	values := make([]model.GenreValue, 0, len(genres))
	for _, genre := range genres {
		values = append(values, model.GenreValue{Code: genre.Code, Match: genre.Match})
	}
	return values
}

func fb2SequenceValues(sequences []model.FB2Sequence, quality metadataQuality, path string) []model.SequenceValue {
	values := make([]model.SequenceValue, 0, len(sequences))
	for i, sequence := range sequences {
		value := fb2SequenceValue(sequence, quality, fmt.Sprintf("%s[%d]", path, i))
		if value.Name != "" || len(value.Sequences) > 0 {
			values = append(values, value)
		}
	}
	return values
}

func fb2SequenceValue(sequence model.FB2Sequence, quality metadataQuality, path string) model.SequenceValue {
	return model.SequenceValue{
		Name:      quality.text(path+".name", sequence.Name),
		Number:    numberValue(sequence.Number),
		Language:  sequence.Lang,
		Sequences: fb2SequenceValues(sequence.Nested, quality, path+".sequences"),
	}
}

type metadataQuality struct {
	enabled     bool
	observation string
	source      string
	out         *model.DatasetRecord
	seen        map[string]struct{}
}

func (q metadataQuality) text(path string, value string) string {
	if !q.enabled || value == "" {
		return value
	}
	meaningful, replacement := replacementOnlyTextStats(value)
	if meaningful == 0 || meaningful != replacement {
		return value
	}
	if q.seen != nil {
		key := q.observation + "\x00" + path
		if _, ok := q.seen[key]; ok {
			return ""
		}
		q.seen[key] = struct{}{}
	}
	q.out.Issues = append(q.out.Issues, model.Issue{
		Observation: q.observation,
		Stage:       "quality",
		Code:        "unicode_replacement_only",
		Path:        path,
		Message:     q.source + " metadata field contains only Unicode replacement characters",
		Details: map[string]any{
			"replacement_characters": replacement,
		},
		Retryable: false,
	})
	return ""
}

func replacementOnlyTextStats(value string) (int, int) {
	meaningful := 0
	replacement := 0
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		meaningful++
		if r == utf8.RuneError {
			replacement++
		}
	}
	return meaningful, replacement
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
