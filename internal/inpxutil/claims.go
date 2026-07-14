package inpxutil

import (
	"fmt"
	"strconv"

	jsonv2 "encoding/json/v2"

	"metabib/model"
)

type DatasetRecordView struct {
	Database            DatasetBibliographicView
	FB2                 DatasetBibliographicView
	Original            DatasetBibliographicView
	DatabasePublication DatasetPublicationView
	FB2Publication      DatasetPublicationView
	Catalog             DatasetCatalogView
	Artifact            DatasetArtifactView
	HasDatabase         bool
	HasFB2              bool
}

type DatasetBibliographicView struct {
	Title          string
	Authors        []model.PersonValue
	Translators    []model.PersonValue
	Genres         []model.GenreValue
	Sequences      []model.SequenceValue
	Language       string
	SourceLanguage string
	Keywords       string
}

type DatasetPublicationView struct {
	BookName  string
	Publisher string
	City      string
	Year      string
	ISBN      string
	Sequences []model.SequenceValue
}

type DatasetCatalogView struct {
	Time     string
	Deleted  string
	Rating   string
	FileType string
	MD5      string
}

type DatasetArtifactView struct {
	Name string
	Size uint64
	Date string
}

func DatasetRecordClaims(rec model.DatasetRecord) (DatasetRecordView, error) {
	view := DatasetRecordView{
		HasDatabase: hasObservation(rec.Observations, "db"),
		HasFB2:      hasObservation(rec.Observations, "fb2"),
	}
	if rec.Claims.Bibliographic != nil {
		var err error
		view.Database, err = bibliographicView(*rec.Claims.Bibliographic, "db")
		if err != nil {
			return DatasetRecordView{}, err
		}
		view.FB2, err = bibliographicView(*rec.Claims.Bibliographic, "fb2")
		if err != nil {
			return DatasetRecordView{}, err
		}
	}
	if rec.Claims.Original != nil {
		var err error
		view.Original, err = bibliographicView(*rec.Claims.Original, "fb2")
		if err != nil {
			return DatasetRecordView{}, err
		}
	}
	if rec.Claims.Publication != nil {
		var err error
		view.DatabasePublication, err = publicationView(*rec.Claims.Publication, "db")
		if err != nil {
			return DatasetRecordView{}, err
		}
		view.FB2Publication, err = publicationView(*rec.Claims.Publication, "fb2")
		if err != nil {
			return DatasetRecordView{}, err
		}
	}
	if rec.Claims.Catalog != nil {
		var err error
		view.Catalog, err = catalogView(*rec.Claims.Catalog, "db")
		if err != nil {
			return DatasetRecordView{}, err
		}
	}
	view.Artifact = artifactView(rec.Artifacts)
	return view, nil
}

func bibliographicView(claims model.BibliographicClaims, observation string) (DatasetBibliographicView, error) {
	var view DatasetBibliographicView
	var err error
	view.Title = claimString(claims.Title, observation)
	view.Authors, err = claimValues[model.PersonValue](claims.Authors, observation)
	if err != nil {
		return DatasetBibliographicView{}, fmt.Errorf("decode %s authors: %w", observation, err)
	}
	view.Translators, err = claimValues[model.PersonValue](claims.Translators, observation)
	if err != nil {
		return DatasetBibliographicView{}, fmt.Errorf("decode %s translators: %w", observation, err)
	}
	view.Genres, err = claimValues[model.GenreValue](claims.Genres, observation)
	if err != nil {
		return DatasetBibliographicView{}, fmt.Errorf("decode %s genres: %w", observation, err)
	}
	view.Sequences, err = claimValues[model.SequenceValue](claims.Sequences, observation)
	if err != nil {
		return DatasetBibliographicView{}, fmt.Errorf("decode %s sequences: %w", observation, err)
	}
	view.Language = claimString(claims.Language, observation)
	view.SourceLanguage = claimString(claims.SourceLanguage, observation)
	view.Keywords = claimString(claims.Keywords, observation)
	return view, nil
}

func publicationView(claims model.PublicationClaims, observation string) (DatasetPublicationView, error) {
	var view DatasetPublicationView
	var err error
	view.BookName = claimString(claims.BookName, observation)
	view.Publisher = claimString(claims.Publisher, observation)
	view.City = claimString(claims.City, observation)
	view.Year, err = claimYear(claims.Year, observation)
	if err != nil {
		return DatasetPublicationView{}, err
	}
	view.ISBN = claimString(claims.ISBN, observation)
	view.Sequences, err = claimValues[model.SequenceValue](claims.Sequences, observation)
	if err != nil {
		return DatasetPublicationView{}, fmt.Errorf("decode %s publication sequences: %w", observation, err)
	}
	return view, nil
}

func catalogView(claims model.CatalogClaims, observation string) (DatasetCatalogView, error) {
	var view DatasetCatalogView
	var err error
	view.Time = claimString(claims.Time, observation)
	view.Deleted, err = claimDeleted(claims.Deleted, observation)
	if err != nil {
		return DatasetCatalogView{}, err
	}
	view.Rating, err = claimRating(claims.Rating, observation)
	if err != nil {
		return DatasetCatalogView{}, err
	}
	view.FileType, view.MD5, err = claimStatus(claims.Status, observation)
	if err != nil {
		return DatasetCatalogView{}, err
	}
	return view, nil
}

func artifactView(artifacts []model.Artifact) DatasetArtifactView {
	if len(artifacts) == 0 {
		return DatasetArtifactView{}
	}
	artifact := artifacts[0]
	view := DatasetArtifactView{Name: artifact.Name}
	for _, size := range artifact.Size {
		if size.Value > 0 {
			view.Size = size.Value
			break
		}
	}
	if view.Size == 0 && len(artifact.Occurrences) > 0 {
		view.Size = artifact.Occurrences[0].UncompressedSize
	}
	if len(artifact.Occurrences) > 0 {
		view.Date = DateOnly(artifact.Occurrences[0].Modified)
	}
	return view
}

func claimString(claims []model.Claim, observation string) string {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		if value, ok := claim.Value.(string); ok {
			return value
		}
	}
	return ""
}

func claimValues[T any](claims []model.Claim, observation string) ([]T, error) {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		return decodeSlice[T](claim.Value)
	}
	return nil, nil
}

func claimYear(claims []model.Claim, observation string) (string, error) {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		value, err := decodeValue[model.YearValue](claim.Value)
		if err != nil {
			return "", fmt.Errorf("decode %s publication year: %w", observation, err)
		}
		if value.Value != nil {
			return strconv.FormatInt(*value.Value, 10), nil
		}
		return value.Text, nil
	}
	return "", nil
}

func claimDeleted(claims []model.Claim, observation string) (string, error) {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		value, err := decodeValue[model.DeletionValue](claim.Value)
		if err != nil {
			return "", fmt.Errorf("decode %s deletion: %w", observation, err)
		}
		return value.Raw, nil
	}
	return "", nil
}

func claimRating(claims []model.Claim, observation string) (string, error) {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		value, err := decodeValue[model.RatingValue](claim.Value)
		if err != nil {
			return "", fmt.Errorf("decode %s rating: %w", observation, err)
		}
		if value.Average == nil || value.Count <= 0 {
			return "", nil
		}
		return strconv.FormatFloat(*value.Average, 'f', -1, 64), nil
	}
	return "", nil
}

func claimStatus(claims []model.Claim, observation string) (string, string, error) {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		value, err := decodeValue[model.CatalogStatusValue](claim.Value)
		if err != nil {
			return "", "", fmt.Errorf("decode %s catalog status: %w", observation, err)
		}
		return value.FileType, value.MD5, nil
	}
	return "", "", nil
}

func decodeSlice[T any](value any) ([]T, error) {
	if typed, ok := value.([]T); ok {
		return typed, nil
	}
	data, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded []T
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func decodeValue[T any](value any) (T, error) {
	if typed, ok := value.(T); ok {
		return typed, nil
	}
	data, err := jsonv2.Marshal(value)
	if err != nil {
		var zero T
		return zero, err
	}
	var decoded T
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return decoded, err
	}
	return decoded, nil
}

func hasObservation(observations []model.Observation, id string) bool {
	for _, observation := range observations {
		if observation.ID == id && observation.Status == "present" {
			return true
		}
	}
	return false
}
