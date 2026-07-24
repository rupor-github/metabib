package model

import (
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
)

func TestArtifactFingerprintsJSONBinaryPayload(t *testing.T) {
	t.Parallel()

	rec := DatasetRecord{Artifacts: []Artifact{{
		Name: "1.fb2",
		Fingerprints: &ArtifactFingerprints{FB2Body: &FB2BodyFingerprint{Sections: []FB2BodySectionFingerprint{
			{Depth: 0, Key: "J3rVF8VpX0h_n_3gcn1exw"},
			{Depth: 1, Key: "U8mlcdBO8QrsFoNbAMe5CA", Leaf: true},
			{Depth: 2, Key: "21f3LVmC74wpouGmavaSeA"},
		}}},
	}}}
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"fp":"`) || strings.Contains(text, `"fingerprints"`) || strings.Contains(text, `"fb2_body"`) {
		t.Fatalf("encoded record = %s", text)
	}

	var decoded DatasetRecord
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	sections := decoded.Artifacts[0].Fingerprints.FB2Body.Sections
	if len(sections) != 3 || sections[0].Depth != 0 || sections[1].Depth != 1 || !sections[1].Leaf || sections[2].Depth != 2 || sections[2].Leaf {
		t.Fatalf("decoded sections = %#v", sections)
	}
	if sections[0].Key != "J3rVF8VpX0h_n_3gcn1exw" || sections[1].Key != "U8mlcdBO8QrsFoNbAMe5CA" {
		t.Fatalf("decoded keys = %#v", sections)
	}
}
