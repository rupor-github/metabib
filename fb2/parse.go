package fb2

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html/charset"

	"metabib/model"
)

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
		node, err := readNode(dec, start)
		if err != nil {
			return model.FB2Source{}, err
		}
		return model.FB2Source{Present: true, Description: &node, TitleInfo: titleInfo(node)}, nil
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
				node, err := readNode(dec, t)
				if err != nil {
					return model.FB2Source{}, err
				}
				info := parseTitleInfo(node)
				return model.FB2Source{Present: true, TitleInfo: &info}, nil
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

func readNode(dec *xml.Decoder, start xml.StartElement) (model.XMLNode, error) {
	node := model.XMLNode{Name: start.Name.Local}
	if len(start.Attr) > 0 {
		node.Attrs = make(map[string]string, len(start.Attr))
		for _, attr := range start.Attr {
			node.Attrs[attrKey(attr.Name)] = attr.Value
		}
	}
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return node, fmt.Errorf("parse FB2 node %q: %w", node.Name, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "coverpage" {
				if err := dec.Skip(); err != nil {
					return node, fmt.Errorf("skip FB2 coverpage: %w", err)
				}
				continue
			}
			child, err := readNode(dec, t)
			if err != nil {
				return node, err
			}
			node.Children = append(node.Children, child)
			text.WriteString(collectText(child))
		case xml.CharData:
			text.WriteString(string(t))
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				node.Text = strings.TrimSpace(text.String())
				if node.Name == "annotation" {
					node.Text = collectText(node)
					node.Children = nil
				}
				return node, nil
			}
		}
	}
}

func attrKey(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	return "{" + name.Space + "}" + name.Local
}

func titleInfo(description model.XMLNode) *model.FB2TitleInfo {
	for _, child := range description.Children {
		if child.Name == "title-info" {
			info := parseTitleInfo(child)
			return &info
		}
	}
	return nil
}

func parseTitleInfo(node model.XMLNode) model.FB2TitleInfo {
	var info model.FB2TitleInfo
	for _, child := range node.Children {
		switch child.Name {
		case "genre":
			info.Genres = append(info.Genres, model.FB2Genre{Code: collectText(child), Match: findAttr(child, "match")})
		case "author":
			info.Authors = append(info.Authors, parsePerson(child))
		case "translator":
			info.Translators = append(info.Translators, parsePerson(child))
		case "book-title":
			info.Title = collectText(child)
		case "annotation":
			info.Annotation = collectText(child)
		case "keywords":
			info.Keywords = collectText(child)
		case "date":
			info.Date = &model.FB2Date{Text: collectText(child), Value: findAttr(child, "value")}
		case "lang":
			info.Language = collectText(child)
		case "src-lang":
			info.SourceLang = collectText(child)
		case "sequence":
			info.Sequences = append(info.Sequences, parseSequences(child)...)
		}
	}
	return info
}

func parsePerson(node model.XMLNode) model.FB2Person {
	var person model.FB2Person
	for _, child := range node.Children {
		switch child.Name {
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
		}
	}
	return person
}

func parseSequences(node model.XMLNode) []model.FB2Sequence {
	seq := model.FB2Sequence{Name: findAttr(node, "name"), Number: findAttr(node, "number")}
	out := []model.FB2Sequence{seq}
	for _, child := range node.Children {
		if child.Name == "sequence" {
			out = append(out, parseSequences(child)...)
		}
	}
	return out
}

func collectText(node model.XMLNode) string {
	if text := strings.TrimSpace(node.Text); text != "" {
		return text
	}
	parts := make([]string, 0, len(node.Children)+1)
	for _, child := range node.Children {
		if text := collectText(child); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func findAttr(node model.XMLNode, local string) string {
	for key, value := range node.Attrs {
		if key == local || strings.HasSuffix(key, "}"+local) {
			return value
		}
	}
	return ""
}
