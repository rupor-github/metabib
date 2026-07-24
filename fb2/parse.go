package fb2

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/net/html/charset"

	"metabib/model"
)

const (
	// Defensive limits for FB2 metadata parsing. These are intentionally much larger
	// than normal library metadata needs; they exist to stop pathological or malicious
	// inputs from consuming unbounded stack, memory, or decompression work.
	MaxXMLDepth                     = 256
	MaxXMLNodes                     = 1_000_000
	MaxTextBytes                    = 64 * 1024 * 1024
	MaxDecompressedBytes            = 256 * 1024 * 1024
	MaxNestedSequenceDepth          = 64
	bodyFingerprintSectionThreshold = 100
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

type ParseOptions struct {
	PreserveDescription bool
	BodyFingerprints    bool
}

func Parse(r io.Reader, preserveDescription bool) (model.FB2Source, error) {
	return ParseWithOptions(r, ParseOptions{PreserveDescription: preserveDescription})
}

func ParseWithOptions(r io.Reader, opts ParseOptions) (model.FB2Source, error) {
	if !opts.BodyFingerprints {
		return parseMetadataOnly(r, opts.PreserveDescription)
	}
	return parseWithBodyFingerprints(r, opts)
}

func parseMetadataOnly(r io.Reader, preserveDescription bool) (model.FB2Source, error) {
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

func parseWithBodyFingerprints(r io.Reader, opts ParseOptions) (model.FB2Source, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charset.NewReaderLabel
	dec.Strict = false
	state := &parseState{}
	fingerprints := newBodyFingerprintBuilder()
	var description *model.FB2Description
	var stack []string
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return fb2ParseResult(description, fingerprints.finish()), nil
			}
			return model.FB2Source{}, fmt.Errorf("parse FB2 XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := state.addNode(); err != nil {
				return model.FB2Source{}, err
			}
			if len(stack)+1 > MaxXMLDepth {
				return model.FB2Source{}, fmt.Errorf("%w: XML depth exceeds %d", ErrLimitExceeded, MaxXMLDepth)
			}
			if t.Name.Local == "description" && len(stack) == 1 && stack[0] == "FictionBook" {
				node, err := readElement(dec, t, len(stack)+1, 0, state)
				if err != nil {
					return model.FB2Source{}, err
				}
				desc := parseDescription(node, opts.PreserveDescription)
				description = &desc
				continue
			}
			if t.Name.Local == "body" && len(stack) == 1 && stack[0] == "FictionBook" {
				fingerprints.openBody()
			} else if t.Name.Local == "section" && fingerprints.inBody() {
				fingerprints.openSection()
			}
			stack = append(stack, t.Name.Local)
		case xml.CharData:
			if fingerprints.inBody() {
				if err := state.addText(len(t)); err != nil {
					return model.FB2Source{}, err
				}
				fingerprints.addText(string(t))
			}
		case xml.EndElement:
			if t.Name.Local == "section" && fingerprints.inBody() {
				fingerprints.closeSection()
			} else if t.Name.Local == "body" && fingerprints.inBody() {
				fingerprints.closeBody()
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

func fb2ParseResult(description *model.FB2Description, fingerprint *model.FB2BodyFingerprint) model.FB2Source {
	out := model.FB2Source{Present: description != nil || fingerprint != nil, Description: description}
	if fingerprint != nil {
		out.Fingerprints = &model.ArtifactFingerprints{FB2Body: fingerprint}
	}
	return out
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

func (s *parseState) addNode() error {
	s.nodes++
	if s.nodes > MaxXMLNodes {
		return fmt.Errorf("%w: XML node count exceeds %d", ErrLimitExceeded, MaxXMLNodes)
	}
	return nil
}

type bodyFingerprintBuilder struct {
	root      *sectionAccumulator
	stack     []*sectionAccumulator
	preorder  []*sectionAccumulator
	bodyDepth int
}

type sectionAccumulator struct {
	depth    int
	hist     map[string]int
	children []*sectionAccumulator
}

func newBodyFingerprintBuilder() *bodyFingerprintBuilder {
	return &bodyFingerprintBuilder{}
}

func (b *bodyFingerprintBuilder) openBody() {
	if b.root == nil {
		b.root = newSectionAccumulator(0)
		b.preorder = append(b.preorder, b.root)
	}
	b.bodyDepth++
	b.stack = []*sectionAccumulator{b.root}
}

func (b *bodyFingerprintBuilder) closeBody() {
	if b.bodyDepth > 0 {
		b.bodyDepth--
	}
	b.stack = nil
}

func (b *bodyFingerprintBuilder) inBody() bool {
	return b.bodyDepth > 0
}

func (b *bodyFingerprintBuilder) openSection() {
	if len(b.stack) == 0 {
		b.openBody()
	}
	parent := b.stack[len(b.stack)-1]
	section := newSectionAccumulator(len(b.stack))
	parent.children = append(parent.children, section)
	b.preorder = append(b.preorder, section)
	b.stack = append(b.stack, section)
}

func (b *bodyFingerprintBuilder) closeSection() {
	if len(b.stack) <= 1 {
		return
	}
	section := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	mergeHistogram(b.stack[len(b.stack)-1].hist, section.hist)
}

func (b *bodyFingerprintBuilder) addText(text string) {
	if len(b.stack) == 0 {
		return
	}
	section := b.stack[len(b.stack)-1]
	for _, word := range normalizedWords(text) {
		section.hist[word]++
	}
}

func (b *bodyFingerprintBuilder) finish() *model.FB2BodyFingerprint {
	if b.root == nil {
		return nil
	}
	for len(b.stack) > 1 {
		b.closeSection()
	}
	sections := make([]model.FB2BodySectionFingerprint, 0, len(b.preorder))
	for _, section := range b.preorder {
		fingerprint := sectionFingerprint(section)
		if section.depth == 0 || fingerprint.count >= bodyFingerprintSectionThreshold {
			sections = append(sections, fingerprint.section)
		}
	}
	return &model.FB2BodyFingerprint{Sections: sections}
}

func newSectionAccumulator(depth int) *sectionAccumulator {
	return &sectionAccumulator{depth: depth, hist: make(map[string]int)}
}

func mergeHistogram(dst map[string]int, src map[string]int) {
	for word, count := range src {
		dst[word] += count
	}
}

func normalizedWords(text string) []string {
	normalized := normalizeBodyText(text)
	parts := strings.Split(normalized, " ")
	words := make([]string, 0, len(parts))
	for _, part := range parts {
		word := keepLetters(part)
		if word != "" {
			words = append(words, word)
		}
	}
	return words
}

func normalizeBodyText(text string) string {
	text = strings.ToLower(text)
	text = strings.NewReplacer("ё", "е", "й", "и", "ъ", "ь").Replace(text)
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		if unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp) || unicode.IsControl(r) || unicode.IsPunct(r) {
			out.WriteByte(' ')
			continue
		}
		out.WriteRune(r)
	}
	return strings.ReplaceAll(out.String(), "ыо", "ью")
}

func keepLetters(word string) string {
	var out strings.Builder
	for _, r := range word {
		if unicode.IsLetter(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

type rankedWord struct {
	word  string
	count int
}

type sectionFingerprintResult struct {
	section model.FB2BodySectionFingerprint
	count   int
}

func sectionFingerprint(section *sectionAccumulator) sectionFingerprintResult {
	words := make([]rankedWord, 0, len(section.hist))
	for word, count := range section.hist {
		words = append(words, rankedWord{word: word, count: count})
	}
	sort.Slice(words, func(i int, j int) bool {
		left := words[i]
		right := words[j]
		if leftBucket, rightBucket := wordLengthBucket(left.word), wordLengthBucket(right.word); leftBucket != rightBucket {
			return leftBucket > rightBucket
		}
		if left.count != right.count {
			return left.count > right.count
		}
		return left.word > right.word
	})
	hash := md5.New()
	for idx, word := range words {
		if idx == 10 {
			break
		}
		_, _ = hash.Write([]byte(word.word))
	}
	return sectionFingerprintResult{
		section: model.FB2BodySectionFingerprint{
			Depth: section.depth,
			Key:   base64.RawURLEncoding.EncodeToString(hash.Sum(nil)),
			Leaf:  len(section.children) == 0,
		},
		count: len(section.hist),
	}
}

func wordLengthBucket(word string) int {
	length := len([]rune(word))
	if length > 8 {
		return 8
	}
	return length
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
