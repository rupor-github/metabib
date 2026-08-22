package inpxutil

import "testing"

func TestRecordTemplateRangeHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tmpl string
		data any
		want string
	}{
		{name: "range", tmpl: `{{rangeName .Counter 10000 "other"}}`, data: map[string]any{"Counter": 10001}, want: "0000010001-0000020000"},
		{name: "libid", tmpl: `{{rangeName .LibID 10000 "other"}}`, data: map[string]any{"LibID": "103995"}, want: "0000100001-0000110000"},
		{name: "fallback", tmpl: `{{rangeName .LibID 10000 "other"}}`, data: map[string]any{"LibID": "abc"}, want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpl, err := NewRecordTemplate(tt.name, tt.tmpl)
			if err != nil {
				t.Fatalf("NewRecordTemplate() error = %v", err)
			}
			got, err := tmpl.ExecuteString(tt.data)
			if err != nil {
				t.Fatalf("ExecuteString() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ExecuteString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecordTemplateRangeHelperRejectsInvalidSize(t *testing.T) {
	t.Parallel()

	tmpl, err := NewRecordTemplate("range", `{{rangeName 1 0 "other"}}`)
	if err != nil {
		t.Fatalf("NewRecordTemplate() error = %v", err)
	}
	if _, err := tmpl.ExecuteString(nil); err == nil {
		t.Fatal("ExecuteString() error = nil, want invalid size error")
	}
}
