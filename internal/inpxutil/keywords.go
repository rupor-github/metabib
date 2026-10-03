package inpxutil

import "strings"

func KeywordsString(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ',', ';', '/', '.', '(', ')', '[', ']', ':':
			return true
		default:
			return false
		}
	})
	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(Cleanse(part))
		if part == "" {
			continue
		}
		b.WriteString(part)
		b.WriteByte(':')
	}
	return b.String()
}
