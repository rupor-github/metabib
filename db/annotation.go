package db

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"metabib/fb2"
	"metabib/model"
)

var (
	bbcodeDropContentTags = map[string]bool{
		"img":     true,
		"image":   true,
		"video":   true,
		"youtube": true,
	}
	bbcodeStripTags = map[string]bool{
		"align":     true,
		"b":         true,
		"bg":        true,
		"br":        true,
		"center":    true,
		"code":      true,
		"color":     true,
		"collapse":  true,
		"collapsed": true,
		"em":        true,
		"float":     true,
		"font":      true,
		"h1":        true,
		"h2":        true,
		"hr":        true,
		"i":         true,
		"justify":   true,
		"left":      true,
		"li":        true,
		"list":      true,
		"ol":        true,
		"quote":     true,
		"right":     true,
		"s":         true,
		"size":      true,
		"spoiler":   true,
		"strong":    true,
		"sub":       true,
		"sup":       true,
		"u":         true,
		"ul":        true,
	}
)

// AnnotationText extracts plain text from a database annotation body and removes forum markup.
func AnnotationText(body string, placeholders []string) (string, error) {
	return annotationText(body, annotationPlaceholderSet(placeholders))
}

func annotationText(body string, placeholders map[string]bool) (string, error) {
	text, err := fb2.HTMLAnnotationText(body)
	if err != nil {
		return "", err
	}
	text = cleanAnnotationMarkup(text)
	if isEmptyAnnotationText(text, placeholders) {
		return "", nil
	}
	return text, nil
}

func filterAnnotationTitles(annotations []model.DBAnnotation) []model.DBAnnotation {
	if len(annotations) <= 1 {
		return annotations
	}
	// Heuristic from database manifest analysis: when a book has several retained
	// annotation rows, untitled rows are usually unrelated noise. Keep an untitled
	// row only when it is the sole retained annotation for the book.
	out := annotations[:0]
	for _, annotation := range annotations {
		if strings.TrimSpace(annotation.Title) != "" {
			out = append(out, annotation)
		}
	}
	return out
}

func annotationPlaceholderSet(placeholders []string) map[string]bool {
	out := make(map[string]bool, len(placeholders))
	for _, placeholder := range placeholders {
		words := annotationWords(placeholder)
		if len(words) == 0 {
			continue
		}
		out[strings.Join(words, " ")] = true
	}
	return out
}

func isEmptyAnnotationText(text string, placeholders map[string]bool) bool {
	words := annotationWords(text)
	if len(words) == 0 {
		return true
	}
	return placeholders[strings.Join(words, " ")]
}

func annotationWords(text string) []string {
	var words []string
	var word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		words = append(words, word.String())
		word.Reset()
	}
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			word.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return words
}

func cleanAnnotationMarkup(text string) string {
	if !strings.Contains(text, "[") {
		return collapseAnnotationText(text)
	}
	var out strings.Builder
	for idx := 0; idx < len(text); {
		if text[idx] != '[' {
			out.WriteByte(text[idx])
			idx++
			continue
		}
		tag, ok := parseBBCodeTag(text, idx)
		if !ok {
			out.WriteByte(text[idx])
			idx++
			continue
		}
		if bbcodeDropContentTags[tag.name] {
			if !tag.closing {
				if _, end, found := matchingBBCodeClose(text, tag.end, tag.name); found {
					idx = end
					continue
				}
				if tag.name == "img" || tag.name == "image" || tag.name == "video" || tag.name == "youtube" {
					idx = skipFollowingToken(text, tag.end)
					continue
				}
			}
			idx = tag.end
			continue
		}
		if tag.name == "url" {
			if tag.closing {
				out.WriteByte(' ')
				idx = tag.end
				continue
			}
			if start, end, found := matchingBBCodeClose(text, tag.end, tag.name); found && bbcodeTagHasLinkTarget(text[tag.end:start]) {
				idx = end
				continue
			}
			out.WriteByte(' ')
			idx = tag.end
			continue
		}
		if tag.name == "a" {
			if !tag.closing && !bbcodeTagHasLinkTarget(tag.content) {
				out.WriteString(text[idx:tag.end])
				idx = tag.end
				continue
			}
			out.WriteByte(' ')
			idx = tag.end
			continue
		}
		if bbcodeStripTags[tag.name] {
			out.WriteByte(' ')
			idx = tag.end
			continue
		}
		out.WriteString(text[idx:tag.end])
		idx = tag.end
	}
	return collapseAnnotationText(out.String())
}

func collapseAnnotationText(text string) string {
	var out strings.Builder
	changed := false
	for idx, r := range text {
		if isRemovableASCIIControl(r) {
			if !changed {
				out.WriteString(text[:idx])
			}
			changed = true
			continue
		}
		if changed {
			out.WriteRune(r)
		}
	}
	if changed {
		text = out.String()
	}
	return strings.Join(strings.Fields(text), " ")
}

func isRemovableASCIIControl(r rune) bool {
	return (r < ' ' && !unicode.IsSpace(r)) || r == 0x7f
}

type bbcodeTag struct {
	name    string
	content string
	closing bool
	end     int
}

func parseBBCodeTag(text string, start int) (bbcodeTag, bool) {
	end := strings.IndexByte(text[start:], ']')
	if end < 0 {
		return bbcodeTag{}, false
	}
	end += start
	content := strings.TrimSpace(text[start+1 : end])
	if content == "" {
		return bbcodeTag{}, false
	}
	closing := false
	if content[0] == '/' {
		closing = true
		content = strings.TrimSpace(content[1:])
	}
	if content == "" {
		return bbcodeTag{}, false
	}
	nameEnd := len(content)
	for idx, r := range content {
		if r == '=' || r == ':' || unicode.IsSpace(r) {
			nameEnd = idx
			break
		}
	}
	name := strings.ToLower(content[:nameEnd])
	if name == "" || !isBBCodeTagName(name) {
		return bbcodeTag{}, false
	}
	return bbcodeTag{name: name, content: content, closing: closing, end: end + 1}, true
}

func bbcodeTagHasLinkTarget(content string) bool {
	content = strings.ToLower(content)
	return strings.Contains(content, "http://") || strings.Contains(content, "https://") || strings.Contains(content, "ftp://") ||
		strings.Contains(content, "www.")
}

func isBBCodeTagName(name string) bool {
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '*' {
			continue
		}
		return false
	}
	return true
}

func matchingBBCodeClose(text string, start int, name string) (int, int, bool) {
	depth := 0
	for idx := start; idx < len(text); idx++ {
		if text[idx] != '[' {
			continue
		}
		tag, ok := parseBBCodeTag(text, idx)
		if !ok || tag.name != name {
			continue
		}
		if tag.closing {
			if depth == 0 {
				return idx, tag.end, true
			}
			depth--
		} else {
			depth++
		}
		idx = tag.end - 1
	}
	return 0, 0, false
}

func skipFollowingToken(text string, start int) int {
	idx := start
	for idx < len(text) {
		r, size := utf8.DecodeRuneInString(text[idx:])
		if !unicode.IsSpace(r) {
			break
		}
		idx += size
	}
	for idx < len(text) {
		r, size := utf8.DecodeRuneInString(text[idx:])
		if unicode.IsSpace(r) || r == '[' {
			break
		}
		idx += size
	}
	return idx
}
