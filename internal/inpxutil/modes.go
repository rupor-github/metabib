package inpxutil

import (
	"fmt"
	"strings"
)

type SequenceMode string

const (
	SequenceAuthor    SequenceMode = "author"
	SequencePublisher SequenceMode = "publisher"
	SequenceAll       SequenceMode = "all"
	SequenceIgnore    SequenceMode = "ignore"
)

type FB2Preference string

const (
	PreferIgnore     FB2Preference = "ignore"
	PreferMerge      FB2Preference = "merge"
	PreferComplement FB2Preference = "complement"
	PreferReplace    FB2Preference = "replace"
)

type FlattenMode string

const (
	FlattenAll      FlattenMode = "all"
	FlattenLeaf     FlattenMode = "leaf"
	FlattenPath     FlattenMode = "path"
	FlattenPathLeaf FlattenMode = "path-leaf"
)

type DedupMode string

const (
	DedupCaseInsensitive DedupMode = "case-insensitive"
	DedupCaseSensitive   DedupMode = "case-sensitive"
)

func ParseSequenceMode(value string, label string) (SequenceMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "author":
		return SequenceAuthor, nil
	case "publisher":
		return SequencePublisher, nil
	case "all":
		return SequenceAll, nil
	case "ignore":
		return SequenceIgnore, nil
	default:
		return "", fmt.Errorf("invalid %s sequence mode %q", label, value)
	}
}

func ParseFB2Preference(value string, label string) (FB2Preference, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "complement":
		return PreferComplement, nil
	case "ignore":
		return PreferIgnore, nil
	case "merge":
		return PreferMerge, nil
	case "replace":
		return PreferReplace, nil
	default:
		return "", fmt.Errorf("invalid %s FB2 preference %q", label, value)
	}
}

func ParseFlattenMode(value string, label string) (FlattenMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return FlattenAll, nil
	case "leaf":
		return FlattenLeaf, nil
	case "path":
		return FlattenPath, nil
	case "path-leaf":
		return FlattenPathLeaf, nil
	default:
		return "", fmt.Errorf("invalid %s FB2 flatten mode %q", label, value)
	}
}

func ParseDedupMode(value string, label string) (DedupMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "case-insensitive":
		return DedupCaseInsensitive, nil
	case "case-sensitive":
		return DedupCaseSensitive, nil
	default:
		return "", fmt.Errorf("invalid %s sequence dedup mode %q", label, value)
	}
}
