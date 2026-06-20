package model

import (
	"testing"

	jsonv2 "encoding/json/v2"
)

func TestRecordJSONShape(t *testing.T) {
	t.Parallel()

	rec := Record{Schema: "metabib.record/1", ID: RecordID{Library: "lib", BookID: 1}, Source: RecordSources{Database: DatabaseSource{Present: true}}}
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Marshal() returned empty data")
	}
}
