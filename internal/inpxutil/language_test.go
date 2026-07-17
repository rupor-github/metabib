package inpxutil

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/model"
)

func TestLanguageResolverCanonicalizesSelectedLanguage(t *testing.T) {
	t.Parallel()

	resolver := testLanguageResolver(t, nil, false)
	rec := testLanguageRecord()
	view := DatasetRecordView{
		Database: DatasetBibliographicView{Language: "?"},
		FB2:      DatasetBibliographicView{Language: "RU"},
	}
	if got := resolver.SelectLanguage(rec, view); got != "ru" {
		t.Fatalf("SelectLanguage() = %q, want ru", got)
	}
}

func TestLanguageResolverDerivesKnownNoisyValues(t *testing.T) {
	t.Parallel()

	resolver := testLanguageResolver(t, nil, false)
	tests := []struct {
		name            string
		field           string
		value           string
		contextLanguage string
		sourceLanguages []string
		want            string
		wantMethod      string
	}{
		{name: "alias", field: "language", value: "sp", want: "es", wantMethod: "alias"},
		{name: "unknown alias", field: "language", value: "un", want: "und", wantMethod: "alias"},
		{name: "valid region stem", field: "language", value: "en-US", want: "en", wantMethod: "valid_tag"},
		{name: "valid script stem", field: "language", value: "sr-Latn", want: "sr", wantMethod: "valid_tag"},
		{name: "full phrase alias before split", field: "language", value: "Человеческое, слишком человеческое", want: "ru", wantMethod: "alias"},
		{name: "split valid", field: "language", value: "ru, engl", want: "ru", wantMethod: "split_valid_tag"},
		{name: "trim garbage", field: "language", value: "ru~", want: "ru", wantMethod: "split_valid_tag"},
		{name: "english display", field: "language", value: "russian", want: "ru", wantMethod: "english_display_name"},
		{name: "bulgarian display", field: "language", value: "български", want: "bg", wantMethod: "bulgarian_display_name"},
		{
			name:            "context display",
			field:           "source_language",
			value:           "английски",
			contextLanguage: "bg",
			want:            "en",
			wantMethod:      "context_display_name",
		},
		{
			name:            "source language context rule",
			field:           "source_language",
			value:           "xa",
			sourceLanguages: []string{"xa", "xal"},
			want:            "xal",
			wantMethod:      "context_source_language",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolved := resolver.resolve(
				languageCandidate{Field: tt.field, Observation: "fb2", Value: tt.value, ContextLanguage: tt.contextLanguage},
				languageRecordContext{SourceLanguages: tt.sourceLanguages},
			)
			got, method, ignored := resolved.Value, resolved.Method, resolved.Ignored
			if ignored || got != tt.want || method != tt.wantMethod {
				t.Fatalf("ResolveForTest() = %q, %q, ignored=%v; want %q, %q, false", got, method, ignored, tt.want, tt.wantMethod)
			}
		})
	}
}

func TestLanguageResolverWarnsAndReturnsRawUnresolved(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	resolver := testLanguageResolver(t, zap.New(core), false)
	view := DatasetRecordView{Database: DatasetBibliographicView{Language: "not@language"}}
	got := resolver.SelectLanguage(testLanguageRecord(), view)
	if got != "not@language" {
		t.Fatalf("SelectLanguage() = %q, want raw unresolved value", got)
	}
	if logs.FilterMessage("Unresolved INPX language").Len() != 1 {
		t.Fatalf("warning logs = %#v", logs.All())
	}
}

func TestLanguageResolverDoesNotResolveUnknownLanguageDisplayNameWithoutAlias(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	resolver, err := NewLanguageResolver(LanguageResolverOptions{
		Enabled:         true,
		FallbackLocales: []string{"en", "ru", "bg"},
		IgnorePatterns:  []string{`^\?+$`},
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("NewLanguageResolver() error = %v", err)
	}
	view := DatasetRecordView{Database: DatasetBibliographicView{Language: "un"}}
	got := resolver.SelectLanguage(testLanguageRecord(), view)
	if got != "un" {
		t.Fatalf("SelectLanguage() = %q, want raw unresolved value", got)
	}
	if logs.FilterMessage("Unresolved INPX language").Len() != 1 {
		t.Fatalf("warning logs = %#v", logs.All())
	}
}

func TestLanguageResolverVerboseLogsResolvedAndIgnored(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.InfoLevel)
	resolver := testLanguageResolver(t, zap.New(core), true)
	view := DatasetRecordView{
		Database: DatasetBibliographicView{Language: "?"},
		FB2:      DatasetBibliographicView{Language: "sp"},
	}
	if got := resolver.SelectLanguage(testLanguageRecord(), view); got != "es" {
		t.Fatalf("SelectLanguage() = %q, want es", got)
	}
	if logs.FilterMessage("Ignored INPX language").Len() != 1 || logs.FilterMessage("Canonicalized INPX language").Len() != 1 {
		t.Fatalf("verbose logs = %#v", logs.All())
	}
}

func TestLanguageResolverVerboseSkipsUnchangedResolvedLanguage(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.InfoLevel)
	resolver := testLanguageResolver(t, zap.New(core), true)
	view := DatasetRecordView{Database: DatasetBibliographicView{Language: "ru"}}
	if got := resolver.SelectLanguage(testLanguageRecord(), view); got != "ru" {
		t.Fatalf("SelectLanguage() = %q, want ru", got)
	}
	if logs.FilterMessage("Canonicalized INPX language").Len() != 0 {
		t.Fatalf("verbose logs = %#v", logs.All())
	}
}

func testLanguageResolver(t *testing.T, log *zap.Logger, verbose bool) *LanguageResolver {
	t.Helper()
	resolver, err := NewLanguageResolver(LanguageResolverOptions{
		Enabled: true,
		Aliases: map[string]string{
			"sp": "es",
			"un": "und",
			"Человеческое, слишком человеческое": "ru",
		},
		FallbackLocales: []string{"en", "ru", "bg"},
		IgnorePatterns:  []string{`^\?+$`},
		ContextRules: []LanguageContextRule{{
			From:                  "xa",
			To:                    "xal",
			WhenAnySourceLanguage: []string{"xal"},
		}},
		Log:     log,
		Verbose: verbose,
	})
	if err != nil {
		t.Fatalf("NewLanguageResolver() error = %v", err)
	}
	return resolver
}

func testLanguageRecord() model.DatasetRecord {
	bookID := int64(42)
	index := 7
	return model.DatasetRecord{
		Record: model.RecordDescriptor{
			Locator: model.RecordLocator{Kind: "archive_entry", Source: "archive", Index: &index, BookID: &bookID},
		},
		Artifacts: []model.Artifact{{Name: "42.fb2"}},
		Identities: &model.Identities{Catalog: []model.Identity{{
			Scheme: "flibusta.book",
			Value:  "42",
		}}},
	}
}
