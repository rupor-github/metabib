package model

import (
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
)

func TestRecordJSONShape(t *testing.T) {
	t.Parallel()

	rec := Record{
		Schema: "metabib.record/1",
		ID:     RecordID{Library: "lib", BookID: 1},
		Source: RecordSources{Database: DatabaseSource{
			Present: true,
			Book:    &DBBook{BookID: 1, FileSize: 123, Title: "Title", MD5: "abc", ReplacedBy: 2},
		}},
	}
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Marshal() returned empty data")
	}
	text := string(data)
	for _, want := range []string{`"book"`, `"md5":"abc"`, `"replaced_by":2`} {
		if !strings.Contains(text, want) {
			t.Fatalf("record JSON missing %s: %s", want, text)
		}
	}
}
