package inpxutil

import (
	"testing"

	"metabib/model"
)

func TestGenresString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		db   []model.GenreValue
		fb2  []model.GenreValue
		want string
	}{
		{
			name: "cleansed separators", db: []model.GenreValue{{Code: "sf:history"}, {Code: ":"}, {Code: "sf,comma"}},
			want: "sf：history:sf,comma:",
		},
		{
			name: "FB2 fallback after cleansing", db: []model.GenreValue{{Code: "�"}},
			fb2: []model.GenreValue{{Code: "fb2:genre"}}, want: "fb2：genre:",
		},
		{name: "invalid DB", db: []model.GenreValue{{Code: "�"}}, want: "other:"},
		{name: "empty", want: "other:"},
		{
			name: "DB wins", db: []model.GenreValue{{Code: "db"}}, fb2: []model.GenreValue{{Code: "fb2"}}, want: "db:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GenresString(tt.db, tt.fb2); got != tt.want {
				t.Fatalf("GenresString() = %q, want %q", got, tt.want)
			}
		})
	}
}
