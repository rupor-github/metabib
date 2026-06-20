package fb2

import (
	"strings"
	"testing"
)

const sampleFB2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <genre match="80">sf</genre>
      <author><first-name>Arkady</first-name><last-name>Strugatsky</last-name></author>
      <translator><nickname>translator</nickname><email>t@example.org</email></translator>
      <book-title>Roadside Picnic</book-title>
      <annotation><p>Hello <strong>world</strong>.</p></annotation>
      <keywords>aliens, zone</keywords>
      <date value="1972-01-01">1972</date>
      <lang>ru</lang>
      <src-lang>ru</src-lang>
      <sequence name="Cycle" number="1"><sequence name="Nested" number="2"/></sequence>
    </title-info>
    <document-info><id>doc</id></document-info>
  </description>
</FictionBook>`

func TestParseTitleInfoOnly(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(sampleFB2), false)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !src.Present || src.TitleInfo == nil {
		t.Fatalf("source not present or title info missing: %#v", src)
	}
	if src.Description != nil {
		t.Fatalf("Description present when preserveDescription=false")
	}
	if src.TitleInfo.Title != "Roadside Picnic" {
		t.Fatalf("Title = %q", src.TitleInfo.Title)
	}
	if got := src.TitleInfo.Authors[0].LastName; got != "Strugatsky" {
		t.Fatalf("author last name = %q", got)
	}
	if got := src.TitleInfo.Annotation; got != "Hello . world" {
		t.Fatalf("annotation = %q", got)
	}
	if len(src.TitleInfo.Sequences) != 2 {
		t.Fatalf("sequences = %#v", src.TitleInfo.Sequences)
	}
}

func TestParsePreservesDescription(t *testing.T) {
	t.Parallel()

	src, err := Parse(strings.NewReader(sampleFB2), true)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !src.Present || src.Description == nil || src.TitleInfo == nil {
		t.Fatalf("unexpected source: %#v", src)
	}
	if src.Description.Name != "description" {
		t.Fatalf("description node name = %q", src.Description.Name)
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
