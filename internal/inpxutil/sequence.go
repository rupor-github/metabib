package inpxutil

import (
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"metabib/model"
)

type Sequence struct {
	Name   string
	Number string
	Source string
}

type SequenceOptions struct {
	Mode                SequenceMode
	Preference          FB2Preference
	Flatten             FlattenMode
	Dedup               DedupMode
	PathSeparator       string
	DuplicateLogMessage string
}

func RecordSequences(rec model.DatasetRecord, view DatasetRecordView, opts SequenceOptions, log *zap.Logger) []Sequence {
	dbSeqs := dbSequences(view.Database.Sequences, opts.Mode)
	fb2Seqs := fb2Sequences(view.FB2.Sequences, view.FB2Publication.Sequences, opts)
	var selected []Sequence
	switch opts.Preference {
	case PreferIgnore:
		selected = dbSeqs
	case PreferMerge:
		selected = append(append([]Sequence{}, dbSeqs...), fb2Seqs...)
	case PreferReplace:
		if len(fb2Seqs) > 0 {
			selected = fb2Seqs
		} else {
			selected = dbSeqs
		}
	default:
		if len(dbSeqs) > 0 {
			selected = dbSeqs
		} else {
			selected = fb2Seqs
		}
	}
	return DedupSequences(rec, selected, opts.Dedup, opts.DuplicateLogMessage, log)
}

func dbSequences(sequences []model.SequenceValue, mode SequenceMode) []Sequence {
	if mode == SequenceIgnore || len(sequences) == 0 {
		return nil
	}
	filtered := slices.DeleteFunc(slices.Clone(sequences), func(seq model.SequenceValue) bool {
		if seq.Type == nil {
			return true
		}
		switch mode {
		case SequenceAuthor:
			return *seq.Type != 0
		case SequencePublisher:
			return *seq.Type != 1
		default:
			return *seq.Type != 0 && *seq.Type != 1
		}
	})
	slices.SortFunc(filtered, func(a, b model.SequenceValue) int {
		return CompareSequences(a, b, mode)
	})
	result := make([]Sequence, 0, len(filtered))
	for _, seq := range filtered {
		result = append(result, Sequence{Name: seq.Name, Number: SequenceNumber(seq.Number), Source: "db"})
	}
	return result
}

// CompareSequences orders typed DB sequences by preferred type, level, and name.
func CompareSequences(a, b model.SequenceValue, mode SequenceMode) int {
	if *a.Type != *b.Type {
		if mode == SequencePublisher {
			return int(*b.Type - *a.Type)
		}
		return int(*a.Type - *b.Type)
	}
	if SequenceLevel(a) != SequenceLevel(b) {
		return int(SequenceLevel(a) - SequenceLevel(b))
	}
	return strings.Compare(a.Name, b.Name)
}

func fb2Sequences(titleSequences []model.SequenceValue, publicationSequences []model.SequenceValue, opts SequenceOptions) []Sequence {
	var result []Sequence
	if opts.Mode == SequenceAuthor || opts.Mode == SequenceAll || opts.Mode == SequenceIgnore {
		result = append(result, FlattenFB2Sequences(titleSequences, opts.Flatten, opts.PathSeparator)...)
	}
	if opts.Mode == SequencePublisher || opts.Mode == SequenceAll {
		result = append(result, FlattenFB2Sequences(publicationSequences, opts.Flatten, opts.PathSeparator)...)
	}
	return result
}

func FlattenFB2Sequences(sequences []model.SequenceValue, mode FlattenMode, separator string) []Sequence {
	var result []Sequence
	var walk func(seq model.SequenceValue, path []string)
	walk = func(seq model.SequenceValue, path []string) {
		name := strings.TrimSpace(seq.Name)
		if name == "" {
			return
		}
		path = append(path, name)
		isLeaf := len(seq.Sequences) == 0
		number := SequenceNumber(seq.Number)
		switch mode {
		case FlattenLeaf:
			if isLeaf {
				result = append(result, Sequence{Name: name, Number: number, Source: "fb2"})
			}
		case FlattenPath:
			if isLeaf {
				result = append(result, Sequence{Name: strings.Join(path, separator), Number: number, Source: "fb2"})
			}
		case FlattenPathLeaf:
			if isLeaf {
				result = append(result, Sequence{Name: strings.Join(path, separator), Number: number, Source: "fb2"})
				result = append(result, Sequence{Name: name, Number: number, Source: "fb2"})
			}
		default:
			result = append(result, Sequence{Name: name, Number: number, Source: "fb2"})
		}
		for _, nested := range seq.Sequences {
			walk(nested, path)
		}
	}
	for _, seq := range sequences {
		walk(seq, nil)
	}
	return result
}

func DedupSequences(rec model.DatasetRecord, sequences []Sequence, mode DedupMode, message string, log *zap.Logger) []Sequence {
	seen := make(map[string]Sequence, len(sequences))
	result := make([]Sequence, 0, len(sequences))
	for _, seq := range sequences {
		seq.Name = strings.TrimSpace(seq.Name)
		if seq.Name == "" {
			continue
		}
		key := seq.Name
		if mode == DedupCaseInsensitive {
			key = strings.ToLower(key)
		}
		if kept, ok := seen[key]; ok {
			if log != nil {
				fields := []zap.Field{
					zap.String("book_id", DatasetBookID(rec)),
					zap.String("locator_kind", rec.Record.Locator.Kind),
					zap.String("locator_source", rec.Record.Locator.Source),
					zap.String("name", seq.Name),
					zap.String("number", seq.Number),
					zap.String("source", seq.Source),
					zap.String("kept_name", kept.Name),
					zap.String("kept_number", kept.Number),
					zap.String("kept_source", kept.Source),
				}
				if rec.Record.Locator.Index != nil {
					fields = append(fields, zap.Int("archive_index", *rec.Record.Locator.Index))
				}
				log.Debug(message, fields...)
			}
			continue
		}
		seen[key] = seq
		result = append(result, seq)
	}
	return result
}

// SequenceNumber renders numeric values as integers, truncating toward zero.
// Text-only values are preserved verbatim.
func SequenceNumber(value *model.NumberValue) string {
	if value == nil {
		return ""
	}
	if value.Value != nil {
		return strconv.Itoa(int(*value.Value))
	}
	return value.Text
}

func SequenceLevel(seq model.SequenceValue) int64 {
	if seq.Level == nil {
		return 0
	}
	return *seq.Level
}
