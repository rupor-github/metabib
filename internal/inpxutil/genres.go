package inpxutil

import (
	"strings"

	"metabib/model"
)

func GenresString(genres []model.GenreValue, fb2Genres []model.GenreValue) string {
	var b strings.Builder
	for _, genre := range SelectedGenreValues(genres, fb2Genres) {
		b.WriteString(genre)
		b.WriteByte(':')
	}
	return b.String()
}

func SelectedGenreValues(genres []model.GenreValue, fb2Genres []model.GenreValue) []string {
	if values := genreValues(genres); len(values) > 0 {
		return values
	}
	if values := genreValues(fb2Genres); len(values) > 0 {
		return values
	}
	return []string{"other"}
}

func genreValues(genres []model.GenreValue) []string {
	values := make([]string, 0, len(genres))
	for _, genre := range genres {
		code := CleanseGenreCode(genre.Code)
		if code != "" {
			values = append(values, code)
		}
	}
	return values
}
