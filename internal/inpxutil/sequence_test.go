package inpxutil

import (
	"slices"
	"testing"

	"metabib/model"
)

func TestSequenceNumber(t *testing.T) {
	t.Parallel()
	positive, negative, small, zero := 7.9, -2.7, 0.5, 0.0
	tests := []struct {
		name  string
		value *model.NumberValue
		want  string
	}{
		{name: "missing"},
		{name: "positive fraction", value: &model.NumberValue{Text: "7.9", Value: &positive}, want: "7"},
		{name: "negative fraction", value: &model.NumberValue{Value: &negative}, want: "-2"},
		{name: "less than one", value: &model.NumberValue{Value: &small}, want: "0"},
		{name: "zero overrides text", value: &model.NumberValue{Text: "unknown", Value: &zero}, want: "0"},
		{name: "text fraction", value: &model.NumberValue{Text: "7.5"}, want: "7.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SequenceNumber(tt.value); got != tt.want {
				t.Fatalf("SequenceNumber() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecordSequencesSelection(t *testing.T) {
	t.Parallel()
	authorType, publisherType, unsupportedType := int64(0), int64(1), int64(2)
	view := DatasetRecordView{
		Database: DatasetBibliographicView{Sequences: []model.SequenceValue{
			{Name: "Publisher", Type: &publisherType},
			{Name: "Cycle", Number: &model.NumberValue{Text: "1"}, Type: &authorType},
			{Name: "Unsupported", Type: &unsupportedType},
			{Name: "Untyped"},
		}},
		FB2: DatasetBibliographicView{Sequences: []model.SequenceValue{
			{Name: "cycle", Number: &model.NumberValue{Text: "2"}},
			{Name: "FB2"},
		}},
		FB2Publication: DatasetPublicationView{Sequences: []model.SequenceValue{{Name: "FB2 Publisher"}}},
	}
	tests := []struct {
		name       string
		mode       SequenceMode
		preference FB2Preference
		want       []Sequence
	}{
		{
			name: "author complement", mode: SequenceAuthor, preference: PreferComplement,
			want: []Sequence{{Name: "Cycle", Number: "1", Source: "db"}},
		},
		{
			name: "publisher replace", mode: SequencePublisher, preference: PreferReplace,
			want: []Sequence{{Name: "FB2 Publisher", Source: "fb2"}},
		},
		{
			name: "merge keeps DB number for duplicate name", mode: SequenceAuthor, preference: PreferMerge,
			want: []Sequence{{Name: "Cycle", Number: "1", Source: "db"}, {Name: "FB2", Source: "fb2"}},
		},
		{
			name: "all database", mode: SequenceAll, preference: PreferIgnore,
			want: []Sequence{{Name: "Cycle", Number: "1", Source: "db"}, {Name: "Publisher", Source: "db"}},
		},
		{
			name: "ignore DB still complements from FB2", mode: SequenceIgnore, preference: PreferComplement,
			want: []Sequence{{Name: "cycle", Number: "2", Source: "fb2"}, {Name: "FB2", Source: "fb2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RecordSequences(model.DatasetRecord{}, view, SequenceOptions{
				Mode: tt.mode, Preference: tt.preference, Flatten: FlattenAll, Dedup: DedupCaseInsensitive,
			}, nil)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("RecordSequences() = %#v, want %#v", got, tt.want)
			}
			if view.Database.Sequences[0].Name != "Publisher" {
				t.Fatal("RecordSequences mutated the input ordering")
			}
		})
	}
}
