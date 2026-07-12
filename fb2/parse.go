package fb2

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html/charset"

	"metabib/model"
)

const (
	// Defensive limits for FB2 metadata parsing. These are intentionally much larger
	// than normal library metadata needs; they exist to stop pathological or malicious
	// inputs from consuming unbounded stack, memory, or decompression work.
	MaxXMLDepth            = 256
	MaxXMLNodes            = 1_000_000
	MaxTextBytes           = 64 * 1024 * 1024
	MaxDecompressedBytes   = 256 * 1024 * 1024
	MaxNestedSequenceDepth = 64
)

var ErrLimitExceeded = errors.New("FB2 parsing limit exceeded")

type parseState struct {
	nodes     int
	textBytes int
}

type element struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []element
}

func Parse(r io.Reader, preserveDescription bool) (model.FB2Source, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charset.NewReaderLabel
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return model.FB2Source{}, nil
			}
			return model.FB2Source{}, fmt.Errorf("parse FB2 XML: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "description" {
			continue
		}
		if !preserveDescription {
			return parseTitleInfoOnly(dec)
		}
		state := &parseState{}
		node, err := readElement(dec, start, 1, 0, state)
		if err != nil {
			return model.FB2Source{}, err
		}
		description := parseDescription(node, true)
		return model.FB2Source{Present: true, Description: &description}, nil
	}
}

func parseTitleInfoOnly(dec *xml.Decoder) (model.FB2Source, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return model.FB2Source{}, nil
			}
			return model.FB2Source{}, fmt.Errorf("parse FB2 description: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "title-info" {
				state := &parseState{}
				node, err := readElement(dec, t, 1, 0, state)
				if err != nil {
					return model.FB2Source{}, err
				}
				description := model.FB2Description{TitleInfo: parseTitleInfo(node)}
				return model.FB2Source{Present: true, Description: &description}, nil
			}
			if err := dec.Skip(); err != nil {
				return model.FB2Source{}, fmt.Errorf("skip FB2 description node %q: %w", t.Name.Local, err)
			}
		case xml.EndElement:
			if t.Name.Local == "description" {
				return model.FB2Source{Present: true}, nil
			}
		}
	}
}

func readElement(dec *xml.Decoder, start xml.StartElement, depth int, sequenceDepth int, state *parseState) (element, error) {
	if depth > MaxXMLDepth {
		return element{}, fmt.Errorf("%w: XML depth exceeds %d", ErrLimitExceeded, MaxXMLDepth)
	}
	if start.Name.Local == "sequence" {
		sequenceDepth++
		if sequenceDepth > MaxNestedSequenceDepth {
			return element{}, fmt.Errorf("%w: nested sequence depth exceeds %d", ErrLimitExceeded, MaxNestedSequenceDepth)
		}
	}
	state.nodes++
	if state.nodes > MaxXMLNodes {
		return element{}, fmt.Errorf("%w: XML node count exceeds %d", ErrLimitExceeded, MaxXMLNodes)
	}
	node := element{Name: start.Name, Attrs: append([]xml.Attr(nil), start.Attr...)}
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return node, fmt.Errorf("parse FB2 node %q: %w", node.Name.Local, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := readElement(dec, t, depth+1, sequenceDepth, state)
			if err != nil {
				return node, err
			}
			node.Children = append(node.Children, child)
			if err := appendChildText(&text, child, state); err != nil {
				return node, err
			}
		case xml.CharData:
			if err := state.addText(len(t)); err != nil {
				return node, err
			}
			text.WriteString(string(t))
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				node.Text = strings.TrimSpace(text.String())
				return node, nil
			}
		}
	}
}

func (s *parseState) addText(bytes int) error {
	s.textBytes += bytes
	if s.textBytes > MaxTextBytes {
		return fmt.Errorf("%w: text size exceeds %d bytes", ErrLimitExceeded, MaxTextBytes)
	}
	return nil
}

func appendChildText(text *strings.Builder, child element, state *parseState) error {
	value := collectText(child)
	if value == "" {
		return nil
	}
	if !isInlineTextElement(child.Name.Local) && text.Len() > 0 {
		if err := state.addText(1); err != nil {
			return err
		}
		text.WriteByte(' ')
	}
	if err := state.addText(len(value)); err != nil {
		return err
	}
	text.WriteString(value)
	return nil
}

func isInlineTextElement(name string) bool {
	switch name {
	case "strong", "emphasis", "style", "a", "strikethrough", "sub", "sup", "code":
		return true
	default:
		return false
	}
}

func parseDescription(node element, full bool) model.FB2Description {
	var description model.FB2Description
	for _, child := range node.Children {
		switch child.Name.Local {
		case "title-info":
			description.TitleInfo = parseTitleInfo(child)
		case "src-title-info":
			if full {
				description.SrcTitleInfo = parseTitleInfo(child)
			}
		case "document-info":
			if full {
				description.DocumentInfo = parseDocumentInfo(child)
			}
		case "publish-info":
			if full {
				description.PublishInfo = parsePublishInfo(child)
			}
		case "custom-info":
			if full {
				description.CustomInfo = append(description.CustomInfo, parseCustomInfo(child))
			}
		case "output":
			if full {
				description.Output = append(description.Output, parseOutput(child))
			}
		}
	}
	return description
}

func parseTitleInfo(node element) *model.FB2TitleInfo {
	info := model.FB2TitleInfo{}
	for _, child := range node.Children {
		switch child.Name.Local {
		case "genre":
			info.Genres = append(info.Genres, model.FB2Genre{Code: collectText(child), Match: attr(child, "match")})
		case "author":
			info.Authors = append(info.Authors, parsePerson(child))
		case "book-title":
			info.Title = collectText(child)
		case "annotation":
			info.Annotation = collectText(child)
		case "keywords":
			info.Keywords = collectText(child)
		case "date":
			info.Date = parseDate(child)
		case "lang":
			info.Language = collectText(child)
		case "src-lang":
			info.SourceLang = collectText(child)
		case "translator":
			info.Translators = append(info.Translators, parsePerson(child))
		case "sequence":
			info.Sequences = append(info.Sequences, parseSequence(child))
		}
	}
	return &info
}

func parseDocumentInfo(node element) *model.FB2DocumentInfo {
	info := model.FB2DocumentInfo{}
	for _, child := range node.Children {
		switch child.Name.Local {
		case "author":
			info.Authors = append(info.Authors, parsePerson(child))
		case "program-used":
			info.ProgramUsed = collectText(child)
		case "date":
			info.Date = parseDate(child)
		case "src-url":
			info.SrcURLs = append(info.SrcURLs, collectText(child))
		case "src-ocr":
			info.SrcOCR = collectText(child)
		case "id":
			info.ID = collectText(child)
		case "version":
			info.Version = collectText(child)
		case "history":
			info.History = collectText(child)
		case "publisher":
			info.Publishers = append(info.Publishers, parsePerson(child))
		}
	}
	return &info
}

func parsePublishInfo(node element) *model.FB2PublishInfo {
	info := model.FB2PublishInfo{}
	for _, child := range node.Children {
		switch child.Name.Local {
		case "book-name":
			info.BookName = collectText(child)
		case "publisher":
			info.Publisher = collectText(child)
		case "city":
			info.City = collectText(child)
		case "year":
			info.Year = collectText(child)
		case "isbn":
			info.ISBN = collectText(child)
		case "sequence":
			info.Sequences = append(info.Sequences, parseSequence(child))
		}
	}
	return &info
}

func parsePerson(node element) model.FB2Person {
	person := model.FB2Person{}
	for _, child := range node.Children {
		switch child.Name.Local {
		case "first-name":
			person.FirstName = collectText(child)
		case "middle-name":
			person.MiddleName = collectText(child)
		case "last-name":
			person.LastName = collectText(child)
		case "nickname":
			person.NickName = collectText(child)
		case "home-page":
			person.HomePages = append(person.HomePages, collectText(child))
		case "email":
			person.Emails = append(person.Emails, collectText(child))
		case "id":
			person.ID = collectText(child)
		}
	}
	return person
}

func parseDate(node element) *model.FB2Date {
	return &model.FB2Date{Text: collectText(node), Value: attr(node, "value")}
}

func parseSequence(node element) model.FB2Sequence {
	seq := model.FB2Sequence{Name: attr(node, "name"), Number: attr(node, "number"), Lang: attrNS(node, xmlLangSpace, "lang")}
	for _, child := range node.Children {
		if child.Name.Local == "sequence" {
			seq.Nested = append(seq.Nested, parseSequence(child))
		}
	}
	return seq
}

func parseCustomInfo(node element) model.FB2CustomInfo {
	return model.FB2CustomInfo{Type: attr(node, "info-type"), Text: collectText(node)}
}

func parseOutput(node element) model.FB2Output {
	output := model.FB2Output{
		Mode:       attr(node, "mode"),
		IncludeAll: attr(node, "include-all"),
		Price:      attr(node, "price"),
		Currency:   attr(node, "currency"),
	}
	for _, child := range node.Children {
		switch child.Name.Local {
		case "part":
			output.Parts = append(output.Parts, parseOutputPart(child))
		case "output-document-class":
			output.OutputDocumentClasses = append(output.OutputDocumentClasses, parseOutputDocumentClass(child))
		}
	}
	return output
}

func parseOutputDocumentClass(node element) model.FB2OutputDocumentClass {
	class := model.FB2OutputDocumentClass{Name: attr(node, "name"), Create: attr(node, "create"), Price: attr(node, "price")}
	for _, child := range node.Children {
		if child.Name.Local == "part" {
			class.Parts = append(class.Parts, parseOutputPart(child))
		}
	}
	return class
}

func parseOutputPart(node element) model.FB2OutputPart {
	return model.FB2OutputPart{Type: attr(node, "type"), Href: attr(node, "href"), Include: attr(node, "include")}
}

func collectText(node element) string {
	return strings.Join(textTokens(node), " ")
}

func textTokens(node element) []string {
	fields := strings.Fields(node.Text)
	if len(fields) > 0 || len(node.Children) == 0 {
		return fields
	}
	parts := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		parts = append(parts, textTokens(child)...)
	}
	return parts
}

func attr(node element, local string) string {
	for _, attr := range node.Attrs {
		if attr.Name.Local == local {
			return attr.Value
		}
	}
	return ""
}

func attrNS(node element, space string, local string) string {
	for _, attr := range node.Attrs {
		if attr.Name.Space == space && attr.Name.Local == local {
			return attr.Value
		}
	}
	return ""
}

const xmlLangSpace = "http://www.w3.org/XML/1998/namespace"
