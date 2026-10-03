package inpxutil

import (
	"testing"

	"metabib/model"
)

func TestRecordAnnotation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		preference FB2Preference
		db         string
		fb2        string
		fbd        string
		want       string
	}{
		{name: "ignore", preference: PreferIgnore, db: "DB", fb2: "FB2", fbd: "FBD", want: "DB"},
		{name: "complement", preference: PreferComplement, db: "DB", fb2: "FB2", fbd: "FBD", want: "DB"},
		{name: "merge", preference: PreferMerge, db: "DB", fb2: "FB2", fbd: "FBD", want: "FB2"},
		{name: "replace", preference: PreferReplace, db: "DB", fb2: "FB2", fbd: "FBD", want: "FB2"},
		{name: "ignore without DB", preference: PreferIgnore, fb2: "FB2", fbd: "FBD", want: "FB2"},
		{name: "complement without DB", preference: PreferComplement, fb2: "FB2", fbd: "FBD", want: "FB2"},
		{name: "merge without DB", preference: PreferMerge, fb2: "FB2", fbd: "FBD", want: "FB2"},
		{name: "replace without DB", preference: PreferReplace, fb2: "FB2", fbd: "FBD", want: "FB2"},
		{name: "FBD fallback", preference: PreferReplace, db: "DB", fb2: " \t", fbd: "FBD", want: "FBD"},
		{name: "DB fallback", preference: PreferReplace, db: "DB", want: "DB"},
		{name: "preserves whitespace", fb2: " FB2 ", want: " FB2 "},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := model.DatasetRecord{Claims: model.Claims{Bibliographic: &model.BibliographicClaims{
				Annotation: []model.Claim{
					{Observation: "db", Value: tt.db},
					{Observation: "fb2", Value: tt.fb2},
					{Observation: "fbd", Value: tt.fbd},
				},
			}}}
			if got := RecordAnnotation(rec, tt.preference); got != tt.want {
				t.Fatalf("RecordAnnotation() = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("missing claims", func(t *testing.T) {
		if got := RecordAnnotation(model.DatasetRecord{}, PreferComplement); got != "" {
			t.Fatalf("RecordAnnotation() = %q, want empty", got)
		}
	})
}
