package inpxutil

import (
	"io"
	"strings"

	"metabib/model"
)

func RecordAnnotation(rec model.DatasetRecord, preference FB2Preference) string {
	if rec.Claims.Bibliographic == nil {
		return ""
	}
	dbAnnotation := annotationClaimValue(rec.Claims.Bibliographic.Annotation, "db")
	fb2Annotation := annotationClaimValue(rec.Claims.Bibliographic.Annotation, "fb2")
	fbdAnnotation := annotationClaimValue(rec.Claims.Bibliographic.Annotation, "fbd")
	if dbAnnotation == "" {
		return firstNonEmpty(fb2Annotation, fbdAnnotation)
	}
	switch preference {
	case PreferIgnore, PreferComplement:
		return dbAnnotation
	case PreferMerge, PreferReplace:
		return firstNonEmpty(fb2Annotation, fbdAnnotation, dbAnnotation)
	default:
		return firstNonEmpty(dbAnnotation, fb2Annotation, fbdAnnotation)
	}
}

func annotationClaimValue(claims []model.Claim, observation string) string {
	for _, claim := range claims {
		if claim.Observation != observation {
			continue
		}
		annotation, ok := claim.Value.(string)
		if ok && strings.TrimSpace(annotation) != "" {
			return annotation
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}

func WriteAnnotationHeader(w io.StringWriter, name string) error {
	if _, err := w.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<folder name=\""); err != nil {
		return err
	}
	if _, err := w.WriteString(xmlEscape(name)); err != nil {
		return err
	}
	_, err := w.WriteString("\">\n")
	return err
}

func WriteAnnotationRecord(w io.StringWriter, name string, annotation string) error {
	if strings.TrimSpace(annotation) == "" {
		return nil
	}
	if _, err := w.WriteString("\t<file name=\""); err != nil {
		return err
	}
	if _, err := w.WriteString(xmlEscape(name)); err != nil {
		return err
	}
	if _, err := w.WriteString("\">\n\t\t<p>"); err != nil {
		return err
	}
	if _, err := w.WriteString(xmlEscape(strings.TrimSpace(annotation))); err != nil {
		return err
	}
	_, err := w.WriteString("</p>\n\t</file>\n")
	return err
}
