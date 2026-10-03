package inpxutil

import (
	"strings"

	"go.uber.org/zap"

	"metabib/model"
)

// RecordFields holds selected values before format-specific cleansing and limits.
type RecordFields struct {
	Title    string
	Authors  string
	Genres   string
	File     string
	Ext      string
	Date     string
	Language string
	Keywords string
	Year     string
}

func PrepareRecordFields(
	rec model.DatasetRecord,
	view DatasetRecordView,
	authors AuthorOptions,
	language *LanguageResolver,
	log *zap.Logger,
) (RecordFields, EntryDiagnostics, bool) {
	fields := RecordFields{Title: view.Database.Title}
	if fields.Title == "" {
		fields.Title = view.FB2.Title
	}
	if fields.Title == "" {
		return RecordFields{}, EntryDiagnostics{}, false
	}
	fields.File, fields.Ext = RecordFileNameAndExtension(rec, view)
	if log != nil && (FileNameEscapeAmbiguous(fields.File) || FileNameEscapeAmbiguous(fields.Ext)) {
		log.Warn(
			"Ambiguous INPX filename escape sequence",
			zap.String("book_id", DatasetBookID(rec)),
			zap.String("file", fields.File),
			zap.String("ext", fields.Ext),
		)
	}
	fields.Date = DateOnly(view.Catalog.Time)
	if fields.Date == "" {
		fields.Date = view.Artifact.Date
	}
	var selection LanguageSelection
	fields.Language, selection = language.SelectLanguageWithReport(rec, view)
	diagnostics := EntryDiagnostics{}
	if selection.Canonicalized {
		diagnostics.CanonicalizedLangBooks = 1
	}
	fields.Keywords = view.Database.Keywords
	if fields.Keywords == "" {
		fields.Keywords = view.FB2.Keywords
	}
	fields.Year = view.DatabasePublication.Year
	if fields.Year == "" {
		fields.Year = view.FB2Publication.Year
	}
	fields.Authors = authors.AuthorsString(view.HasDatabase, view.Database.Authors, view.FB2.Authors)
	if count := authors.LogDisambiguatedDBAuthors(rec, view, fields.Authors, log); count > 0 {
		diagnostics.DisambiguatedAuthorBooks = 1
		diagnostics.DisambiguatedAuthors = int64(count)
	}
	fields.Genres = GenresString(view.Database.Genres, view.FB2.Genres)
	return fields, diagnostics, true
}

type EntryDiagnostics struct {
	DisambiguatedAuthorBooks int64
	DisambiguatedAuthors     int64
	CanonicalizedLangBooks   int64
}

func (d *EntryDiagnostics) Add(other EntryDiagnostics) {
	d.DisambiguatedAuthorBooks += other.DisambiguatedAuthorBooks
	d.DisambiguatedAuthors += other.DisambiguatedAuthors
	d.CanonicalizedLangBooks += other.CanonicalizedLangBooks
}

// TruncateField applies MHL's rune limit, reserving one character for the delimiter.
func TruncateField(value string, enabled bool, maxLen int) string {
	value = Cleanse(value)
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
