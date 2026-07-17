package inpxutil

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"go.uber.org/zap"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"metabib/model"
)

type LanguageResolverOptions struct {
	Enabled         bool
	Aliases         map[string]string
	FallbackLocales []string
	IgnorePatterns  []string
	ContextRules    []LanguageContextRule
	Log             *zap.Logger
	Verbose         bool
}

type LanguageContextRule struct {
	From                  string
	To                    string
	WhenAnySourceLanguage []string
}

type LanguageResolver struct {
	enabled         bool
	aliases         map[string]string
	fallbackLocales []language.Tag
	ignorePatterns  []*regexp.Regexp
	contextRules    []languageContextRule
	displayNames    map[string]languageNames
	log             *zap.Logger
	verbose         bool
}

type languageContextRule struct {
	from                  string
	to                    string
	whenAnySourceLanguage map[string]struct{}
}

type languageNames struct {
	exact map[string]languageMatch
	all   []languageMatch
}

type languageMatch struct {
	tag     string
	name    string
	unknown bool
}

type languageCandidate struct {
	Field           string
	Observation     string
	Value           string
	ContextLanguage string
}

type languageRecordContext struct {
	BookID          string
	LocatorKind     string
	LocatorSource   string
	LocatorIndex    *int
	ArtifactName    string
	SourceLanguages []string
}

type languageResolution struct {
	Value      string
	DisplayTag string
	Name       string
	Method     string
	Ignored    bool
	Resolved   bool
	Candidate  string
}

func NewLanguageResolver(opts LanguageResolverOptions) (*LanguageResolver, error) {
	resolver := &LanguageResolver{
		enabled:      opts.Enabled,
		aliases:      make(map[string]string, len(opts.Aliases)),
		displayNames: make(map[string]languageNames),
		log:          opts.Log,
		verbose:      opts.Verbose,
	}
	if !opts.Enabled {
		return resolver, nil
	}
	for raw, canonical := range opts.Aliases {
		tag, err := language.Parse(canonical)
		if err != nil {
			return nil, fmt.Errorf("parse INPX language alias %q target %q: %w", raw, canonical, err)
		}
		resolver.aliases[languageKey(raw)] = canonicalLanguageTag(tag)
	}
	if len(opts.FallbackLocales) == 0 {
		opts.FallbackLocales = []string{"en", "ru", "bg"}
	}
	resolver.fallbackLocales = make([]language.Tag, 0, len(opts.FallbackLocales))
	for _, value := range opts.FallbackLocales {
		tag, err := language.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("parse INPX language fallback locale %q: %w", value, err)
		}
		resolver.fallbackLocales = append(resolver.fallbackLocales, tag)
	}
	for _, pattern := range opts.IgnorePatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile INPX language ignore pattern %q: %w", pattern, err)
		}
		resolver.ignorePatterns = append(resolver.ignorePatterns, re)
	}
	for _, rule := range opts.ContextRules {
		to, err := language.Parse(rule.To)
		if err != nil {
			return nil, fmt.Errorf("parse INPX language context rule %q target %q: %w", rule.From, rule.To, err)
		}
		compiled := languageContextRule{
			from:                  languageKey(rule.From),
			to:                    canonicalLanguageTag(to),
			whenAnySourceLanguage: make(map[string]struct{}, len(rule.WhenAnySourceLanguage)),
		}
		for _, value := range rule.WhenAnySourceLanguage {
			compiled.whenAnySourceLanguage[languageKey(value)] = struct{}{}
		}
		resolver.contextRules = append(resolver.contextRules, compiled)
	}
	return resolver, nil
}

func (r *LanguageResolver) SelectLanguage(rec model.DatasetRecord, view DatasetRecordView) string {
	if r == nil || !r.enabled {
		return firstLanguage(view.Database.Language, view.FB2.Language)
	}
	ctx := languageContext(rec, view)
	candidates := []languageCandidate{
		{Field: "language", Observation: "db", Value: view.Database.Language, ContextLanguage: view.Database.SourceLanguage},
		{Field: "language", Observation: "fb2", Value: view.FB2.Language, ContextLanguage: view.FB2.SourceLanguage},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Value) == "" {
			continue
		}
		resolved := r.resolve(candidate, ctx)
		if resolved.Ignored {
			r.logVerbose("Ignored INPX language", candidate, ctx, resolved)
			continue
		}
		if resolved.Resolved {
			if resolved.Value != strings.TrimSpace(candidate.Value) {
				r.logVerbose("Canonicalized INPX language", candidate, ctx, resolved)
			}
			return resolved.Value
		}
		r.logUnresolved(candidate, ctx, resolved)
		return strings.TrimSpace(candidate.Value)
	}
	return ""
}

func firstLanguage(db string, fb2 string) string {
	if db != "" {
		return db
	}
	return fb2
}

func languageContext(rec model.DatasetRecord, view DatasetRecordView) languageRecordContext {
	ctx := languageRecordContext{
		BookID:          DatasetBookID(rec),
		LocatorKind:     rec.Record.Locator.Kind,
		LocatorSource:   rec.Record.Locator.Source,
		LocatorIndex:    rec.Record.Locator.Index,
		ArtifactName:    view.Artifact.Name,
		SourceLanguages: compactStrings(view.Database.SourceLanguage, view.FB2.SourceLanguage),
	}
	return ctx
}

func compactStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (r *LanguageResolver) resolve(candidate languageCandidate, ctx languageRecordContext) languageResolution {
	original := strings.TrimSpace(candidate.Value)
	if tag, ok := r.aliases[languageKey(original)]; ok {
		return languageResolution{Value: tag, Method: "alias", Resolved: true, Candidate: original}
	}
	candidates, ignored := r.splitCandidates(original)
	if len(candidates) == 0 {
		return languageResolution{Ignored: ignored, Candidate: original}
	}
	for _, value := range candidates {
		if candidate.Field == "source_language" {
			if tag, ok := r.contextRule(value, ctx.SourceLanguages); ok {
				return languageResolution{Value: tag, Method: "context_source_language", Resolved: true, Candidate: value}
			}
		}
		if tag, err := language.Parse(value); err == nil {
			return languageResolution{Value: canonicalLanguageTag(tag), Method: splitMethod("valid_tag", original, value), Resolved: true, Candidate: value}
		}
		if tag, ok := r.aliases[languageKey(value)]; ok {
			return languageResolution{Value: tag, Method: splitMethod("alias", original, value), Resolved: true, Candidate: value}
		}
		if resolved, ok := r.displayName(value, candidate.ContextLanguage, original); ok {
			return resolved
		}
	}
	return languageResolution{Value: original, Method: "unresolved", Candidate: original}
}

func (r *LanguageResolver) splitCandidates(value string) ([]string, bool) {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	ignoredAny := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if r.ignored(part) {
			ignoredAny = true
			continue
		}
		part = trimLanguageTagGarbage(part)
		if part == "" {
			continue
		}
		if r.ignored(part) {
			ignoredAny = true
			continue
		}
		out = append(out, part)
	}
	return out, ignoredAny
}

func (r *LanguageResolver) ignored(value string) bool {
	for _, pattern := range r.ignorePatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func (r *LanguageResolver) contextRule(value string, sourceLanguages []string) (string, bool) {
	key := languageKey(value)
	for _, rule := range r.contextRules {
		if rule.from != key {
			continue
		}
		for _, sourceLanguage := range sourceLanguages {
			if _, ok := rule.whenAnySourceLanguage[languageKey(sourceLanguage)]; ok {
				return rule.to, true
			}
		}
	}
	return "", false
}

func (r *LanguageResolver) displayName(value string, contextLanguage string, original string) (languageResolution, bool) {
	if contextLanguage != "" {
		if tag, err := language.Parse(trimLanguageTagGarbage(contextLanguage)); err == nil {
			if resolved, ok := r.displayNameWithLocale(value, tag, "context_display_name", original); ok {
				return resolved, true
			}
		}
	}
	for _, tag := range r.fallbackLocales {
		if resolved, ok := r.displayNameWithLocale(value, tag, fallbackMethod(tag), original); ok {
			return resolved, true
		}
	}
	return languageResolution{}, false
}

func (r *LanguageResolver) displayNameWithLocale(value string, locale language.Tag, method string, original string) (languageResolution, bool) {
	names := r.names(locale)
	key := languageKey(value)
	if match, ok := names.exact[key]; ok {
		if match.unknown {
			return languageResolution{}, false
		}
		return languageResolution{
			Value:      match.tag,
			DisplayTag: locale.String(),
			Name:       match.name,
			Method:     splitMethod(method, original, value),
			Resolved:   true,
			Candidate:  value,
		}, true
	}
	if len([]rune(key)) < 2 {
		return languageResolution{}, false
	}
	for _, match := range names.all {
		if match.unknown {
			continue
		}
		if strings.HasPrefix(languageKey(match.name), key) {
			return languageResolution{
				Value:      match.tag,
				DisplayTag: locale.String(),
				Name:       match.name,
				Method:     splitMethod(method+"_prefix", original, value),
				Resolved:   true,
				Candidate:  value,
			}, true
		}
	}
	return languageResolution{}, false
}

func (r *LanguageResolver) names(locale language.Tag) languageNames {
	key := locale.String()
	if names, ok := r.displayNames[key]; ok {
		return names
	}
	namer := display.Languages(locale)
	names := languageNames{exact: map[string]languageMatch{}}
	if namer != nil {
		for _, base := range language.Supported.BaseLanguages() {
			tag := language.Make(base.String())
			name := namer.Name(base)
			if name == "" {
				continue
			}
			match := languageMatch{tag: canonicalLanguageTag(tag), name: name, unknown: base.String() == "und"}
			if _, exists := names.exact[languageKey(name)]; !exists {
				names.exact[languageKey(name)] = match
			}
			names.all = append(names.all, match)
		}
	}
	r.displayNames[key] = names
	return names
}

func splitMethod(method string, original string, candidate string) string {
	if candidate != strings.TrimSpace(original) {
		return "split_" + method
	}
	return method
}

func fallbackMethod(tag language.Tag) string {
	switch tag.String() {
	case "en":
		return "english_display_name"
	case "ru":
		return "russian_display_name"
	case "bg":
		return "bulgarian_display_name"
	default:
		return "fallback_display_name"
	}
}

func canonicalLanguageTag(tag language.Tag) string {
	if tag == language.Und {
		return "und"
	}
	base, _ := tag.Base()
	return base.String()
}

func trimLanguageTagGarbage(value string) string {
	value = strings.NewReplacer(`\n`, " ", `\r`, " ", `\t`, " ").Replace(value)
	return strings.TrimFunc(strings.TrimSpace(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func languageKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func (r *LanguageResolver) logUnresolved(candidate languageCandidate, ctx languageRecordContext, resolved languageResolution) {
	if r.log == nil {
		return
	}
	r.log.Warn("Unresolved INPX language", languageLogFields(candidate, ctx, resolved)...)
}

func (r *LanguageResolver) logVerbose(message string, candidate languageCandidate, ctx languageRecordContext, resolved languageResolution) {
	if r.log == nil || !r.verbose {
		return
	}
	r.log.Info(message, languageLogFields(candidate, ctx, resolved)...)
}

func languageLogFields(candidate languageCandidate, ctx languageRecordContext, resolved languageResolution) []zap.Field {
	fields := []zap.Field{
		zap.String("book_id", ctx.BookID),
		zap.String("field", candidate.Field),
		zap.String("observation", candidate.Observation),
		zap.String("original", candidate.Value),
		zap.String("candidate", resolved.Candidate),
		zap.String("resolved", resolved.Value),
		zap.String("method", resolved.Method),
		zap.String("display_language", resolved.DisplayTag),
		zap.String("display_name", resolved.Name),
		zap.String("context_language", candidate.ContextLanguage),
		zap.Strings("source_languages", ctx.SourceLanguages),
		zap.String("locator_kind", ctx.LocatorKind),
		zap.String("locator_source", ctx.LocatorSource),
		zap.String("artifact", ctx.ArtifactName),
	}
	if ctx.LocatorIndex != nil {
		fields = append(fields, zap.Int("locator_index", *ctx.LocatorIndex))
	}
	return fields
}
