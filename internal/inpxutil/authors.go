package inpxutil

import (
	"strings"

	"go.uber.org/zap"

	"metabib/model"
)

type AuthorLimits struct {
	First  int
	Middle int
	Last   int
}

type AuthorOptions struct {
	Preference          FB2Preference
	Disambiguator       *AuthorDisambiguator
	DisambiguationField AuthorDisambiguationField
	QuickFix            bool
	Limits              AuthorLimits
	Verbose             bool
}

func (o AuthorOptions) AuthorsString(dbPresent bool, authors []model.PersonValue, fb2Authors []model.PersonValue) string {
	if o.Preference == PreferReplace && len(fb2Authors) > 0 {
		return o.PeopleString(fb2Authors)
	}
	if dbPresent && len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	if len(authors) == 0 && len(fb2Authors) > 0 {
		return o.PeopleString(fb2Authors)
	}
	if len(authors) == 0 {
		return "неизвестный,автор,:"
	}
	return o.PeopleString(authors)
}

func (o AuthorOptions) PeopleString(people []model.PersonValue) string {
	var b strings.Builder
	for _, person := range people {
		suffix := o.Disambiguator.Suffix(person)
		lastName := o.LastName(person, suffix)
		firstName := o.FirstName(person, suffix)
		middleName := o.MiddleName(person, suffix)
		if lastName == "" && firstName == "" && middleName == "" {
			continue
		}
		b.WriteString(lastName)
		b.WriteByte(',')
		b.WriteString(firstName)
		b.WriteByte(',')
		b.WriteString(middleName)
		b.WriteByte(':')
	}
	if b.Len() == 0 {
		return "неизвестный,автор,:"
	}
	return b.String()
}

func (o AuthorOptions) LastName(person model.PersonValue, suffix string) string {
	return o.nameComponent(person.LastName, suffix, AuthorDisambiguationLast, o.Limits.Last)
}

func (o AuthorOptions) FirstName(person model.PersonValue, suffix string) string {
	return o.nameComponent(person.FirstName, suffix, AuthorDisambiguationFirst, o.Limits.First)
}

func (o AuthorOptions) MiddleName(person model.PersonValue, suffix string) string {
	return o.nameComponent(person.MiddleName, suffix, AuthorDisambiguationMiddle, o.Limits.Middle)
}

func (o AuthorOptions) nameComponent(value string, suffix string, field AuthorDisambiguationField, limit int) string {
	value = CleanseAuthorComponent(value)
	suffix = CleanseAuthorComponent(suffix)
	if suffix == "" || o.Field() != field {
		return TruncateField(value, o.QuickFix, limit)
	}
	suffix = " " + suffix
	if !o.QuickFix || limit <= 0 {
		return strings.TrimSpace(value + suffix)
	}
	limit = max(limit-1, 0)
	suffixRunes := []rune(suffix)
	if len(suffixRunes) >= limit {
		return strings.TrimSpace(suffix)
	}
	valueRunes := []rune(value)
	valueLimit := limit - len(suffixRunes)
	if len(valueRunes) > valueLimit {
		value = strings.TrimRight(string(valueRunes[:valueLimit]), " \t")
	}
	return strings.TrimSpace(value + suffix)
}

func (o AuthorOptions) Field() AuthorDisambiguationField {
	if o.Disambiguator != nil {
		return o.Disambiguator.Field()
	}
	return NormalizeAuthorDisambiguationField(o.DisambiguationField)
}

func (o AuthorOptions) LogDisambiguatedDBAuthors(
	rec model.DatasetRecord,
	view DatasetRecordView,
	renderedAuthors string,
	log *zap.Logger,
) int {
	if o.Disambiguator == nil || len(view.Database.Authors) == 0 ||
		(o.Preference == PreferReplace && len(view.FB2.Authors) > 0) {
		return 0
	}
	count := 0
	for _, person := range view.Database.Authors {
		suffix := o.Disambiguator.Suffix(person)
		if suffix == "" {
			continue
		}
		count++
		if log == nil || !o.Verbose {
			continue
		}
		fields := []zap.Field{
			zap.String("book_id", DatasetBookID(rec)),
			zap.String("flibusta_person_id", FlibustaPersonID(person)),
			zap.String("first_name", person.FirstName),
			zap.String("middle_name", person.MiddleName),
			zap.String("last_name", person.LastName),
			zap.String("nick_name", person.NickName),
			zap.String("suffix", suffix),
			zap.String("disambiguation_field", string(o.Field())),
			zap.String("rendered_first_name", o.FirstName(person, suffix)),
			zap.String("rendered_middle_name", o.MiddleName(person, suffix)),
			zap.String("rendered_last_name", o.LastName(person, suffix)),
			zap.String("rendered_authors", renderedAuthors),
			zap.String("locator_kind", rec.Record.Locator.Kind),
			zap.String("locator_source", rec.Record.Locator.Source),
			zap.String("artifact", view.Artifact.Name),
		}
		if person.Position != nil {
			fields = append(fields, zap.Int64("position", *person.Position))
		}
		if rec.Record.Locator.Index != nil {
			fields = append(fields, zap.Int("locator_index", *rec.Record.Locator.Index))
		}
		log.Debug("Disambiguated INPX DB author", fields...)
	}
	return count
}
