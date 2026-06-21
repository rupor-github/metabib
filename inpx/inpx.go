package inpx

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"go.uber.org/zap"

	"metabib/jsonl"
	"metabib/model"
)

const fieldSep = "\x04"

type Format string

const (
	Format2X   Format = "2x"
	FormatRUKS Format = "ruks"
)

type SequenceMode string

const (
	SequenceAuthor    SequenceMode = "author"
	SequencePublisher SequenceMode = "publisher"
	SequenceIgnore    SequenceMode = "ignore"
)

type FB2Preference string

const (
	PreferIgnore     FB2Preference = "ignore"
	PreferMerge      FB2Preference = "merge"
	PreferComplement FB2Preference = "complement"
	PreferReplace    FB2Preference = "replace"
)

type Limits struct {
	AuthorName   int
	AuthorMiddle int
	AuthorFamily int
	Title        int
	Keywords     int
	Sequence     int
}

type Options struct {
	InputPrefix     string
	OutputPrefix    string
	Format          Format
	SequenceMode    SequenceMode
	FB2Preference   FB2Preference
	QuickFix        bool
	Limits          Limits
	CommentTemplate string
	Log             *zap.Logger
}

type archiveRows struct {
	Meta    model.MergeArchiveMetadata
	Records map[int]model.Record
}

func DefaultLimits() Limits {
	return Limits{AuthorName: 128, AuthorMiddle: 128, AuthorFamily: 128, Title: 150, Keywords: 255, Sequence: 80}
}

func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "2x", "2.x":
		return Format2X, nil
	case "ruks":
		return FormatRUKS, nil
	default:
		return "", fmt.Errorf("invalid INPX format %q", value)
	}
}

func ParseSequenceMode(value string) (SequenceMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "author":
		return SequenceAuthor, nil
	case "publisher":
		return SequencePublisher, nil
	case "ignore":
		return SequenceIgnore, nil
	default:
		return "", fmt.Errorf("invalid INPX sequence mode %q", value)
	}
}

func ParseFB2Preference(value string) (FB2Preference, error) {
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
		return "", fmt.Errorf("invalid INPX FB2 preference %q", value)
	}
}

func Generate(ctx context.Context, opts Options) (string, error) {
	if opts.InputPrefix == "" {
		return "", errors.New("INPX input prefix is required")
	}
	if opts.OutputPrefix == "" {
		return "", errors.New("INPX output prefix is required")
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits()
	}
	metaPath, err := discoverMetadata(opts.InputPrefix)
	if err != nil {
		return "", err
	}
	meta, err := readMetadata(metaPath)
	if err != nil {
		return "", err
	}
	parts, err := discoverInputParts(opts.InputPrefix)
	if err != nil {
		return "", err
	}
	archives := make(map[string]*archiveRows, len(meta.Archives))
	for _, archive := range meta.Archives {
		archives[archive.Path] = &archiveRows{Meta: archive, Records: make(map[int]model.Record)}
	}
	if err := readRecords(ctx, parts, archives); err != nil {
		return "", err
	}
	outputPath := inpxOutputPath(opts.OutputPrefix, meta)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", fmt.Errorf("create INPX output directory: %w", err)
	}
	tmpPath := outputPath + ".tmp"
	_ = os.Remove(tmpPath)
	if _, err := os.Stat(outputPath); err == nil && opts.Log != nil {
		opts.Log.Warn("Overwriting existing INPX output", zap.String("file", outputPath))
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat INPX output %q: %w", outputPath, err)
	}
	if err := writeINPX(ctx, tmpPath, meta, archives, opts); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := jsonl.ReplaceOutputFile(tmpPath, outputPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("replace INPX output %q: %w", outputPath, err)
	}
	return outputPath, nil
}

func discoverMetadata(prefix string) (string, error) {
	matches, err := filepath.Glob(prefix + ".meta.json*")
	if err != nil {
		return "", err
	}
	matches = slices.DeleteFunc(matches, func(path string) bool { return strings.HasSuffix(path, ".tmp") })
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one metadata sidecar for %q, found %d", prefix, len(matches))
	}
	return matches[0], nil
}

func discoverInputParts(prefix string) ([]string, error) {
	matches, err := filepath.Glob(prefix + ".*.jsonl*")
	if err != nil {
		return nil, err
	}
	matches = slices.DeleteFunc(matches, func(path string) bool {
		base := filepath.Base(path)
		return strings.Contains(base, ".meta.json") || strings.HasSuffix(base, ".tmp")
	})
	sort.Strings(matches)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no JSONL input parts found for %q", prefix)
	}
	return matches, nil
}

func readMetadata(path string) (model.MergeMetadata, error) {
	r, err := jsonl.OpenCompressedFile(path)
	if err != nil {
		return model.MergeMetadata{}, err
	}
	defer r.Close()
	var meta model.MergeMetadata
	if err := jsonv2.UnmarshalRead(r, &meta); err != nil {
		return meta, fmt.Errorf("decode merge metadata %q: %w", path, err)
	}
	return meta, nil
}

func readRecords(ctx context.Context, parts []string, archives map[string]*archiveRows) error {
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := jsonl.OpenCompressedFile(part)
		if err != nil {
			return err
		}
		dec := jsontext.NewDecoder(r)
		for {
			var rec model.Record
			if err := jsonv2.UnmarshalDecode(dec, &rec); err != nil {
				if err == io.EOF {
					break
				}
				r.Close()
				return fmt.Errorf("decode JSONL part %q: %w", part, err)
			}
			if rec.ID.Archive == nil {
				continue
			}
			archive := archives[rec.ID.Archive.Path]
			if archive == nil {
				archive = &archiveRows{Meta: model.MergeArchiveMetadata{Path: rec.ID.Archive.Path, Name: filepath.Base(rec.ID.Archive.Path)}, Records: make(map[int]model.Record)}
				archives[rec.ID.Archive.Path] = archive
			}
			archive.Records[rec.ID.Archive.Index] = rec
		}
		if err := r.Close(); err != nil {
			return err
		}
	}
	return nil
}

func writeINPX(ctx context.Context, path string, meta model.MergeMetadata, archives map[string]*archiveRows, opts Options) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create INPX %q: %w", path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(zipComment(meta))
	archiveList := make([]*archiveRows, 0, len(archives))
	for _, archive := range archives {
		archiveList = append(archiveList, archive)
	}
	sort.Slice(archiveList, func(i, j int) bool { return archiveList[i].Meta.Name < archiveList[j].Meta.Name })
	for _, archive := range archiveList {
		if err := ctx.Err(); err != nil {
			zw.Close()
			f.Close()
			return err
		}
		if err := writeArchiveINP(zw, archive, opts); err != nil {
			zw.Close()
			f.Close()
			return err
		}
	}
	if err := writeZipText(zw, "collection.info", collectionInfo(meta, opts)); err != nil {
		zw.Close()
		f.Close()
		return err
	}
	if err := writeZipText(zw, "version.info", meta.Database.DumpDate+"\r\n"); err != nil {
		zw.Close()
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return fmt.Errorf("close INPX zip %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close INPX %q: %w", path, err)
	}
	return nil
}

func writeArchiveINP(zw *zip.Writer, archive *archiveRows, opts Options) error {
	name := strings.TrimSuffix(archive.Meta.Name, filepath.Ext(archive.Meta.Name)) + ".inp"
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create INPX entry %q: %w", name, err)
	}
	bw := bufio.NewWriter(w)
	for idx := 0; idx < archive.Meta.Entries; idx++ {
		if inRanges(archive.Meta.Ignored, idx) {
			continue
		}
		rec, ok := archive.Records[idx]
		line := ""
		if ok {
			line = recordLine(rec, opts)
		}
		if line == "" {
			line = dummyLine(idx + 1)
		}
		if _, err := bw.WriteString(line); err != nil {
			return fmt.Errorf("write INPX entry %q: %w", name, err)
		}
	}
	return bw.Flush()
}

func writeZipText(zw *zip.Writer, name string, text string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create INPX entry %q: %w", name, err)
	}
	_, err = io.WriteString(w, text)
	return err
}

func recordLine(rec model.Record, opts Options) string {
	db := rec.Source.Database
	fb2 := rec.Source.FB2
	book := db.Book
	if book == nil && fb2.TitleInfo == nil {
		return ""
	}
	title := ""
	if book != nil {
		title = book.Title
	}
	if title == "" && fb2.TitleInfo != nil {
		title = fb2.TitleInfo.Title
	}
	authors := authorsString(db.Authors, fb2.TitleInfo, opts)
	genres := genresString(db.Genres, fb2.TitleInfo)
	sequence, seqNum := sequenceString(db.Sequences, fb2.TitleInfo, opts)
	fileName := rec.ID.FileName
	if fileName == "" && rec.ID.BookID > 0 {
		fileName = strconv.FormatInt(rec.ID.BookID, 10)
	}
	size := int64(0)
	deleted := ""
	ext := rec.ID.Extension
	date := ""
	lang := ""
	rate := ""
	keywords := ""
	md5 := ""
	replaced := ""
	if book != nil {
		size = book.FileSize
		deleted = book.Deleted
		if ext == "" {
			ext = book.FileType
		}
		date = dateOnly(book.Time)
		lang = book.Lang
		keywords = book.Keywords
		md5 = book.MD5
		if book.ReplacedBy > 0 {
			replaced = strconv.FormatInt(book.ReplacedBy, 10)
		}
	}
	if size == 0 && rec.ID.Archive != nil {
		size = int64(rec.ID.Archive.UncompressedSize)
	}
	if date == "" && rec.ID.Archive != nil {
		date = dateOnly(rec.ID.Archive.Modified)
	}
	if lang == "" && fb2.TitleInfo != nil {
		lang = fb2.TitleInfo.Language
	}
	if keywords == "" && fb2.TitleInfo != nil {
		keywords = fb2.TitleInfo.Keywords
	}
	if db.Rating != nil && db.Rating.Count > 0 {
		rate = strconv.FormatInt(int64(math.Round(db.Rating.Average)), 10)
	}
	fields := []string{
		authors,
		genres,
		fix(title, opts.QuickFix, opts.Limits.Title),
		fix(sequence, opts.QuickFix, opts.Limits.Sequence),
		seqNum,
		cleanse(fileName),
		strconv.FormatInt(size, 10),
		strconv.FormatInt(rec.ID.BookID, 10),
		deleted,
		ext,
		date,
		strings.TrimSpace(lang),
		rate,
		fix(keywords, opts.QuickFix, opts.Limits.Keywords),
	}
	if opts.Format == FormatRUKS {
		fields = append(fields, md5, replaced)
	}
	return strings.Join(fields, fieldSep) + fieldSep + "\r\n"
}

func authorsString(authors []model.Contributor, titleInfo *model.FB2TitleInfo, opts Options) string {
	if opts.FB2Preference == PreferReplace && titleInfo != nil && len(titleInfo.Authors) > 0 {
		return fb2AuthorsString(titleInfo.Authors, opts)
	}
	if len(authors) == 0 && titleInfo != nil && len(titleInfo.Authors) > 0 {
		return fb2AuthorsString(titleInfo.Authors, opts)
	}
	if len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	var b strings.Builder
	for _, author := range authors {
		b.WriteString(fix(cleanse(author.LastName), opts.QuickFix, opts.Limits.AuthorFamily))
		b.WriteByte(',')
		b.WriteString(fix(cleanse(author.FirstName), opts.QuickFix, opts.Limits.AuthorName))
		b.WriteByte(',')
		b.WriteString(fix(cleanse(author.MiddleName), opts.QuickFix, opts.Limits.AuthorMiddle))
		b.WriteByte(':')
	}
	return b.String()
}

func fb2AuthorsString(authors []model.FB2Person, opts Options) string {
	var b strings.Builder
	for _, author := range authors {
		b.WriteString(fix(cleanse(author.LastName), opts.QuickFix, opts.Limits.AuthorFamily))
		b.WriteByte(',')
		b.WriteString(fix(cleanse(author.FirstName), opts.QuickFix, opts.Limits.AuthorName))
		b.WriteByte(',')
		b.WriteString(fix(cleanse(author.MiddleName), opts.QuickFix, opts.Limits.AuthorMiddle))
		b.WriteByte(':')
	}
	if b.Len() == 0 {
		return "неизвестный,автор,:"
	}
	return b.String()
}

func genresString(genres []model.DBGenre, titleInfo *model.FB2TitleInfo) string {
	if len(genres) > 0 {
		var b strings.Builder
		for _, genre := range genres {
			b.WriteString(cleanse(genre.Code))
			b.WriteByte(':')
		}
		return b.String()
	}
	if titleInfo != nil && len(titleInfo.Genres) > 0 {
		var b strings.Builder
		for _, genre := range titleInfo.Genres {
			b.WriteString(cleanse(genre.Code))
			b.WriteByte(':')
		}
		return b.String()
	}
	return "other:"
}

func sequenceString(sequences []model.DBSequence, titleInfo *model.FB2TitleInfo, opts Options) (string, string) {
	dbName, dbNum := dbSequence(sequences, opts.SequenceMode)
	fbName, fbNum := fb2Sequence(titleInfo)
	switch opts.FB2Preference {
	case PreferReplace:
		if fbName != "" {
			return fbName, fbNum
		}
	case PreferMerge:
		if fbName != "" {
			return fbName, fbNum
		}
	case PreferComplement:
		if dbName == "" && fbName != "" {
			return fbName, fbNum
		}
	}
	return dbName, dbNum
}

func dbSequence(sequences []model.DBSequence, mode SequenceMode) (string, string) {
	if mode == SequenceIgnore || len(sequences) == 0 {
		return "", ""
	}
	slices.SortFunc(sequences, func(a, b model.DBSequence) int {
		if a.Type != b.Type {
			if mode == SequencePublisher {
				return int(b.Type - a.Type)
			}
			return int(a.Type - b.Type)
		}
		if a.Level != b.Level {
			return int(a.Level - b.Level)
		}
		return strings.Compare(a.Name, b.Name)
	})
	seq := sequences[0]
	num := ""
	if seq.Number > 0 {
		num = strconv.FormatInt(seq.Number, 10)
	}
	return seq.Name, num
}

func fb2Sequence(titleInfo *model.FB2TitleInfo) (string, string) {
	if titleInfo == nil || len(titleInfo.Sequences) == 0 {
		return "", ""
	}
	seq := titleInfo.Sequences[0]
	num := ""
	if seq.Number != "" {
		if value, err := strconv.ParseFloat(seq.Number, 64); err == nil {
			num = strconv.Itoa(int(value))
		} else {
			num = seq.Number
		}
	}
	return seq.Name, num
}

func dummyLine(index int) string {
	return "dummy:" + fieldSep + "other:" + fieldSep + "dummy record" + fieldSep + fieldSep + fieldSep + fieldSep + "1" + fieldSep + strconv.Itoa(index) + fieldSep + "1" + fieldSep + "EXT" + fieldSep + "2000-01-01" + fieldSep + "en" + fieldSep + "0" + fieldSep + fieldSep + "\r\n"
}

func inRanges(ranges []model.IndexRange, idx int) bool {
	for _, r := range ranges {
		if idx >= r.Start && idx <= r.End {
			return true
		}
	}
	return false
}

func inpxOutputPath(prefix string, meta model.MergeMetadata) string {
	base := prefix
	date := meta.Database.DumpDate
	if date != "" && !strings.HasSuffix(base, "_"+date) {
		base += "_" + date
	}
	return base + ".inpx"
}

func zipComment(meta model.MergeMetadata) string {
	return meta.Library + " - " + displayDate(meta)
}

func collectionInfo(meta model.MergeMetadata, opts Options) string {
	name := filepath.Base(strings.TrimSuffix(inpxOutputPath(meta.Library, meta), ".inpx"))
	date := displayDate(meta)
	comment := opts.CommentTemplate
	if comment == "" {
		comment = "%s FB2 - %s\r\n%s\r\n65536\r\nЛокальные архивы библиотеки %s (FB2) %s"
		return fmt.Sprintf(comment, meta.Library, date, name, meta.Library, date)
	}
	return fmt.Sprintf(comment, name)
}

func displayDate(meta model.MergeMetadata) string {
	if meta.Database.DumpDateISO != "" {
		return meta.Database.DumpDateISO
	}
	date := meta.Database.DumpDate
	if len(date) == 8 {
		return date[:4] + "-" + date[4:6] + "-" + date[6:8]
	}
	return date
}

func cleanse(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "\u00a0", " ")
	return value
}

func fix(value string, enabled bool, maxLen int) string {
	value = cleanse(value)
	if !enabled || maxLen <= 0 {
		return value
	}
	runes := []rune(value)
	limit := max(maxLen-1, 0)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimRight(string(runes[:limit]), " \t")
}

func dateOnly(value string) string {
	if len(value) >= 10 {
		if _, err := time.Parse("2006-01-02", value[:10]); err == nil {
			return value[:10]
		}
	}
	return value
}
