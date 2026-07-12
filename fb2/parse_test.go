package fb2

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

const sampleFB2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <genre match="80">sf</genre>
      <author><first-name>Arkady</first-name><middle-name>Natanovich</middle-name><last-name>Strugatsky</last-name><nickname>ABS</nickname><home-page>https://example.org/a</home-page><email>a@example.org</email><id>a1</id></author>
      <translator><first-name>Tr</first-name><last-name>Person</last-name><nickname>translator</nickname><email>t@example.org</email><id>t1</id></translator>
      <book-title>Roadside Picnic</book-title>
      <annotation><p>Hello <strong>world</strong>.</p></annotation>
      <keywords>aliens, zone</keywords>
      <date value="1972-01-01">1972</date>
      <lang>ru</lang>
      <src-lang>ru</src-lang>
      <sequence name="Cycle" number="1" xml:lang="ru"><sequence name="Nested" number="2"/></sequence>
    </title-info>
    <src-title-info><genre>sf_history</genre><author><nickname>Original</nickname></author><book-title>Original Title</book-title><lang>en</lang></src-title-info>
    <document-info><author><nickname>doc author</nickname><id>d1</id></author><program-used>metabib</program-used><date value="2020-01-02">2020</date><src-url>https://example.org/1</src-url><src-url>https://example.org/2</src-url><src-ocr>ocr person</src-ocr><id>doc</id><version>1.0</version><history><p>Created <em>now</em>.</p></history><publisher><nickname>publisher person</nickname></publisher></document-info>
    <publish-info><book-name>Paper Book</book-name><publisher>Pub</publisher><city>City</city><year>1973</year><isbn>isbn</isbn><sequence name="Paper" number="3"/></publish-info>
    <custom-info info-type="source">Custom <strong>text</strong></custom-info>
    <output mode="free" include-all="allow" price="1.25" currency="USD"><part l:type="simple" l:href="#section1" include="require"/><output-document-class name="reader" create="allow" price="0"><part l:href="#section2" include="deny"/></output-document-class></output>
  </description>
</FictionBook>`

func TestParseTitleInfoOnly(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(sampleFB2), false)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !src.Present || src.Description == nil || src.Description.TitleInfo == nil {
		t.Fatalf("source not present or title info missing: %#v", src)
	}
	if src.Description.SrcTitleInfo != nil || src.Description.DocumentInfo != nil || src.Description.PublishInfo != nil {
		t.Fatalf("full description present when preserveDescription=false: %#v", src.Description)
	}
	titleInfo := src.Description.TitleInfo
	if titleInfo.Title != "Roadside Picnic" {
		t.Fatalf("Title = %q", titleInfo.Title)
	}
	if got := titleInfo.Authors[0].LastName; got != "Strugatsky" {
		t.Fatalf("author last name = %q", got)
	}
	if got := titleInfo.Annotation; got != "Hello world." {
		t.Fatalf("annotation = %q", got)
	}
	if len(titleInfo.Sequences) != 1 || len(titleInfo.Sequences[0].Nested) != 1 {
		t.Fatalf("sequences = %#v", titleInfo.Sequences)
	}
}

func TestParsePreservesDescription(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(sampleFB2), true)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !src.Present || src.Description == nil || src.Description.TitleInfo == nil {
		t.Fatalf("unexpected source: %#v", src)
	}
	if src.Description.DocumentInfo == nil || src.Description.DocumentInfo.ID != "doc" {
		t.Fatalf("document info = %#v", src.Description.DocumentInfo)
	}
	if src.Description.PublishInfo == nil || src.Description.PublishInfo.BookName != "Paper Book" {
		t.Fatalf("publish info = %#v", src.Description.PublishInfo)
	}
	if len(src.Description.CustomInfo) != 1 || src.Description.CustomInfo[0].Text != "Custom text" {
		t.Fatalf("custom info = %#v", src.Description.CustomInfo)
	}
	if len(src.Description.Output) != 1 || len(src.Description.Output[0].Parts) != 1 || src.Description.Output[0].Parts[0].Href != "#section1" {
		t.Fatalf("output = %#v", src.Description.Output)
	}
}

func TestParsePreservesAllTextualDescriptionFields(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(sampleFB2), true)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	description := src.Description
	titleInfo := description.TitleInfo
	if titleInfo.Genres[0].Code != "sf" || titleInfo.Genres[0].Match != "80" {
		t.Fatalf("genres = %#v", titleInfo.Genres)
	}
	if author := titleInfo.Authors[0]; author.ID != "a1" || author.FirstName != "Arkady" || author.MiddleName != "Natanovich" || author.LastName != "Strugatsky" || author.NickName != "ABS" || author.HomePages[0] != "https://example.org/a" || author.Emails[0] != "a@example.org" {
		t.Fatalf("author = %#v", author)
	}
	if translator := titleInfo.Translators[0]; translator.ID != "t1" || translator.FirstName != "Tr" || translator.LastName != "Person" || translator.NickName != "translator" || translator.Emails[0] != "t@example.org" {
		t.Fatalf("translator = %#v", translator)
	}
	if titleInfo.Title != "Roadside Picnic" || titleInfo.Annotation != "Hello world." || titleInfo.Keywords != "aliens, zone" {
		t.Fatalf("title info text = %#v", titleInfo)
	}
	if titleInfo.Date == nil || titleInfo.Date.Text != "1972" || titleInfo.Date.Value != "1972-01-01" {
		t.Fatalf("date = %#v", titleInfo.Date)
	}
	if titleInfo.Language != "ru" || titleInfo.SourceLang != "ru" {
		t.Fatalf("languages = %q, %q", titleInfo.Language, titleInfo.SourceLang)
	}
	if seq := titleInfo.Sequences[0]; seq.Name != "Cycle" || seq.Number != "1" || seq.Lang != "ru" || seq.Nested[0].Name != "Nested" || seq.Nested[0].Number != "2" {
		t.Fatalf("sequence = %#v", seq)
	}

	if srcTitle := description.SrcTitleInfo; srcTitle.Title != "Original Title" || srcTitle.Language != "en" || srcTitle.Genres[0].Code != "sf_history" || srcTitle.Authors[0].NickName != "Original" {
		t.Fatalf("src title info = %#v", srcTitle)
	}
	doc := description.DocumentInfo
	if doc.Authors[0].NickName != "doc author" || doc.Authors[0].ID != "d1" || doc.ProgramUsed != "metabib" || doc.SrcOCR != "ocr person" || doc.ID != "doc" || doc.Version != "1.0" || doc.History != "Created now." || doc.Publishers[0].NickName != "publisher person" {
		t.Fatalf("document info = %#v", doc)
	}
	if doc.Date == nil || doc.Date.Text != "2020" || doc.Date.Value != "2020-01-02" {
		t.Fatalf("document date = %#v", doc.Date)
	}
	if len(doc.SrcURLs) != 2 || doc.SrcURLs[0] != "https://example.org/1" || doc.SrcURLs[1] != "https://example.org/2" {
		t.Fatalf("src urls = %#v", doc.SrcURLs)
	}
	publish := description.PublishInfo
	if publish.BookName != "Paper Book" || publish.Publisher != "Pub" || publish.City != "City" || publish.Year != "1973" || publish.ISBN != "isbn" || publish.Sequences[0].Name != "Paper" || publish.Sequences[0].Number != "3" {
		t.Fatalf("publish info = %#v", publish)
	}
	if description.CustomInfo[0].Type != "source" || description.CustomInfo[0].Text != "Custom text" {
		t.Fatalf("custom info = %#v", description.CustomInfo)
	}
	output := description.Output[0]
	if output.Mode != "free" || output.IncludeAll != "allow" || output.Price != "1.25" || output.Currency != "USD" {
		t.Fatalf("output attrs = %#v", output)
	}
	if output.Parts[0].Type != "simple" || output.Parts[0].Href != "#section1" || output.Parts[0].Include != "require" {
		t.Fatalf("output parts = %#v", output.Parts)
	}
	if class := output.OutputDocumentClasses[0]; class.Name != "reader" || class.Create != "allow" || class.Price != "0" || class.Parts[0].Href != "#section2" || class.Parts[0].Include != "deny" {
		t.Fatalf("output classes = %#v", output.OutputDocumentClasses)
	}
}

func TestParseMixedTextOrder(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(`<FictionBook><description><title-info><book-title>A <em>B</em> C <strong>D</strong></book-title><annotation>Hello <strong>world</strong>.</annotation></title-info></description></FictionBook>`), false)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := src.Description.TitleInfo.Title; got != "A B C D" {
		t.Fatalf("Title = %q", got)
	}
	if got := src.Description.TitleInfo.Annotation; got != "Hello world." {
		t.Fatalf("Annotation = %q", got)
	}
}

func TestParseNoDescription(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(`<FictionBook/>`), false)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if src.Present {
		t.Fatalf("Present = true, want false")
	}
}

func TestParseRejectsExcessiveXMLDepth(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString(`<FictionBook><description>`)
	for range MaxXMLDepth {
		b.WriteString(`<a>`)
	}
	for range MaxXMLDepth {
		b.WriteString(`</a>`)
	}
	b.WriteString(`</description></FictionBook>`)

	_, err := Parse(strings.NewReader(b.String()), true)
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "XML depth") {
		t.Fatalf("Parse() error = %v, want XML depth limit", err)
	}
}

func TestParseRejectsExcessiveSequenceDepth(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString(`<FictionBook><description><title-info>`)
	for range MaxNestedSequenceDepth + 1 {
		b.WriteString(`<sequence name="s">`)
	}
	for range MaxNestedSequenceDepth + 1 {
		b.WriteString(`</sequence>`)
	}
	b.WriteString(`</title-info></description></FictionBook>`)

	_, err := Parse(strings.NewReader(b.String()), false)
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "nested sequence depth") {
		t.Fatalf("Parse() error = %v, want sequence depth limit", err)
	}
}

func TestReadElementRejectsNodeAndTextLimits(t *testing.T) {
	t.Parallel()

	dec := xml.NewDecoder(strings.NewReader(`<a/>`))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	start := tok.(xml.StartElement)
	_, err = readElement(dec, start, 1, 0, &parseState{nodes: MaxXMLNodes})
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "node count") {
		t.Fatalf("readElement() error = %v, want node limit", err)
	}

	state := &parseState{textBytes: MaxTextBytes}
	if err := state.addText(1); !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "text size") {
		t.Fatalf("addText() error = %v, want text limit", err)
	}
}
