package model

import (
	"encoding/base64"
	"encoding/binary"
	jsonstd "encoding/json"
	"fmt"
)

const (
	FB2BodyFingerprintModel            = "fb2-body-sections/1"
	FB2BodySectionEncoding             = "binary-base64url"
	FB2BodyFingerprintCoverageNone     = "none"
	FB2BodyFingerprintCoveragePartial  = "partial"
	FB2BodyFingerprintCoverageComplete = "complete"
	md5DigestSize                      = 16
)

type ArtifactFingerprints struct {
	FB2Body *FB2BodyFingerprint `json:"fb2_body,omitempty"`
}

func (f ArtifactFingerprints) MarshalJSON() ([]byte, error) {
	if f.FB2Body == nil || len(f.FB2Body.Sections) == 0 {
		return jsonstd.Marshal("")
	}
	payload, err := encodeFB2BodyFingerprint(f.FB2Body)
	if err != nil {
		return nil, err
	}
	return jsonstd.Marshal(payload)
}

func (f *ArtifactFingerprints) UnmarshalJSON(data []byte) error {
	var payload string
	if err := jsonstd.Unmarshal(data, &payload); err != nil {
		return err
	}
	body, err := decodeFB2BodyFingerprint(payload)
	if err != nil {
		return err
	}
	f.FB2Body = body
	return nil
}

type FB2BodyFingerprint struct {
	Sections []FB2BodySectionFingerprint `json:"sections,omitempty"`
}

type FB2BodySectionFingerprint struct {
	Depth int
	Key   string
	Leaf  bool
}

func (s FB2BodySectionFingerprint) MarshalJSON() ([]byte, error) {
	leaf := 0
	if s.Leaf {
		leaf = 1
	}
	return jsonstd.Marshal([3]any{s.Depth, s.Key, leaf})
}

func (s *FB2BodySectionFingerprint) UnmarshalJSON(data []byte) error {
	var values []jsonstd.RawMessage
	if err := jsonstd.Unmarshal(data, &values); err != nil {
		return err
	}
	if len(values) != 3 {
		return fmt.Errorf("FB2 body section fingerprint has %d fields, want 3", len(values))
	}
	if err := jsonstd.Unmarshal(values[0], &s.Depth); err != nil {
		return fmt.Errorf("decode FB2 body section depth: %w", err)
	}
	if err := jsonstd.Unmarshal(values[1], &s.Key); err != nil {
		return fmt.Errorf("decode FB2 body section key: %w", err)
	}
	var leaf int
	if err := jsonstd.Unmarshal(values[2], &leaf); err != nil {
		return fmt.Errorf("decode FB2 body section leaf: %w", err)
	}
	s.Leaf = leaf != 0
	return nil
}

func encodeFB2BodyFingerprint(fingerprint *FB2BodyFingerprint) (string, error) {
	sections := fingerprint.Sections
	if len(sections) == 0 {
		return "", nil
	}
	out := make([]byte, 0, md5DigestSize+(len(sections)-1)*(md5DigestSize+1))
	for idx, section := range sections {
		key, err := base64.RawURLEncoding.DecodeString(section.Key)
		if err != nil {
			return "", fmt.Errorf("decode FB2 body section key: %w", err)
		}
		if len(key) != md5DigestSize {
			return "", fmt.Errorf("decode FB2 body section key: got %d bytes, want %d", len(key), md5DigestSize)
		}
		if idx == 0 {
			out = append(out, key...)
			continue
		}
		if section.Depth < 0 {
			return "", fmt.Errorf("encode FB2 body section depth: got %d", section.Depth)
		}
		tag := uint64(section.Depth << 1)
		if section.Leaf {
			tag |= 1
		}
		var buf [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(buf[:], tag)
		out = append(out, buf[:n]...)
		out = append(out, key...)
	}
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func decodeFB2BodyFingerprint(payload string) (*FB2BodyFingerprint, error) {
	if payload == "" {
		return &FB2BodyFingerprint{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode FB2 body fingerprint payload: %w", err)
	}
	if len(raw) < md5DigestSize {
		return nil, fmt.Errorf("decode FB2 body fingerprint payload: got %d bytes, want at least %d", len(raw), md5DigestSize)
	}
	sections := []FB2BodySectionFingerprint{{
		Depth: 0,
		Key:   base64.RawURLEncoding.EncodeToString(raw[:md5DigestSize]),
	}}
	for offset := md5DigestSize; offset < len(raw); {
		tag, n := binary.Uvarint(raw[offset:])
		if n <= 0 {
			return nil, fmt.Errorf("decode FB2 body section tag at byte %d", offset)
		}
		offset += n
		if offset+md5DigestSize > len(raw) {
			return nil, fmt.Errorf("decode FB2 body section key at byte %d: truncated payload", offset)
		}
		sections = append(sections, FB2BodySectionFingerprint{
			Depth: int(tag >> 1),
			Key:   base64.RawURLEncoding.EncodeToString(raw[offset : offset+md5DigestSize]),
			Leaf:  tag&1 == 1,
		})
		offset += md5DigestSize
	}
	return &FB2BodyFingerprint{Sections: sections}, nil
}

type DatasetFB2BodyFingerprints struct {
	Coverage        string `json:"coverage"`
	Model           string `json:"model,omitempty"`
	SectionEncoding string `json:"section_encoding,omitempty"`
}
