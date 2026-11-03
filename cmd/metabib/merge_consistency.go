package main

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"metabib/config"
	"metabib/internal/inpxutil"
	"metabib/model"
)

type mergeConsistencyChecker struct {
	checks   []string
	language *inpxutil.LanguageResolver
	counts   map[string]int64
}

func newMergeConsistencyChecker(
	policy config.MergeConsistencyConfig,
	languages config.INPXLanguageConfig,
) (*mergeConsistencyChecker, error) {
	checker := &mergeConsistencyChecker{checks: policy.ActiveChecks(), counts: make(map[string]int64)}
	if slices.Contains(checker.checks, "language") {
		resolver, err := inpxutil.NewLanguageResolver(inpxutil.LanguageResolverOptions{
			Enabled:         true,
			Aliases:         languages.Aliases,
			FallbackLocales: languages.FallbackLocales,
			IgnorePatterns:  languages.IgnorePatterns,
		})
		if err != nil {
			return nil, fmt.Errorf("create merge consistency language resolver: %w", err)
		}
		checker.language = resolver
	}
	return checker, nil
}

// Apply uses converted claims, so quality-filtered metadata cannot produce new
// disagreements. The original physical ID must be captured before DB enrichment.
func (c *mergeConsistencyChecker) Apply(id model.RecordID, rec *model.DatasetRecord) {
	if c == nil || len(c.checks) == 0 || id.Archive == nil {
		return
	}
	var database *model.Observation
	for i := range rec.Observations {
		obs := &rec.Observations[i]
		if obs.ID == "db" && obs.Status == "present" {
			database = obs
			break
		}
	}
	if database == nil {
		return
	}
	for _, check := range c.checks {
		switch check {
		case "content_md5":
			if !consistencyDirectContent(id) {
				continue
			}
			archive, db := "", ""
			for _, artifact := range rec.Artifacts {
				for _, checksum := range artifact.Checksums {
					if checksum.Algorithm != "md5" || checksum.Scope != "content" {
						continue
					}
					switch checksum.Observation {
					case "archive":
						archive = checksum.Value
					case "db":
						db = checksum.Value
					}
				}
			}
			a, b := consistencyMD5(archive), consistencyMD5(db)
			if a != "" && b != "" && a != b {
				c.add(rec, database, check, "high", "artifacts", "archive", archive, db, a, b)
			}
		case "document_type":
			archive, db := id.Extension, ""
			if archive == "" {
				archive = strings.TrimPrefix(filepath.Ext(id.Archive.Entry), ".")
			}
			if rec.Claims.Catalog != nil {
				for _, claim := range rec.Claims.Catalog.Status {
					if status, ok := claim.Value.(model.CatalogStatusValue); ok && claim.Observation == "db" {
						db = status.FileType
					}
				}
			}
			a, b := consistencyDocumentType(archive), consistencyDocumentType(db)
			if a != "" && b != "" && a != b {
				c.add(rec, database, check, "high", "artifacts", "archive", archive, db, a, b)
			}
		case "content_size":
			if !consistencyDirectContent(id) {
				continue
			}
			var archive, db uint64
			for _, artifact := range rec.Artifacts {
				for _, size := range artifact.Size {
					if size.Observation == "archive" && size.Kind == "uncompressed" {
						archive = size.Value
					} else if size.Observation == "db" && size.Kind == "reported" {
						db = size.Value
					}
				}
			}
			if archive > 0 && db > 0 && archive != db {
				c.add(rec, database, check, "high", "artifacts", "archive", archive, db, archive, db)
			}
		case "title", "authors", "language":
			c.checkBibliographic(rec, database, check)
		}
	}
}

func (c *mergeConsistencyChecker) checkBibliographic(rec *model.DatasetRecord, database *model.Observation, check string) {
	bib := rec.Claims.Bibliographic
	if bib == nil {
		return
	}
	var claims []model.Claim
	switch check {
	case "title":
		claims = bib.Title
	case "authors":
		claims = bib.Authors
	case "language":
		claims = bib.Language
	}
	var db *model.Claim
	for i := range claims {
		if claims[i].Observation == "db" {
			db = &claims[i]
			break
		}
	}
	if db == nil {
		return
	}
	for _, claim := range claims {
		if claim.Observation == "db" {
			continue
		}
		var a, b any
		mismatch := false
		switch check {
		case "title":
			archive, archiveOK := claim.Value.(string)
			database, databaseOK := db.Value.(string)
			an, bn := consistencyText(archive), consistencyText(database)
			a, b = an, bn
			mismatch = archiveOK && databaseOK && an != "" && bn != "" && an != bn
		case "authors":
			archive, archiveOK := claim.Value.([]model.PersonValue)
			database, databaseOK := db.Value.([]model.PersonValue)
			an, aOK := consistencyAuthorNames(archive)
			bn, bOK := consistencyAuthorNames(database)
			a, b = an, bn
			mismatch = archiveOK && databaseOK && aOK && bOK && !consistencySharedAuthor(an, bn)
		case "language":
			archive, archiveOK := claim.Value.(string)
			database, databaseOK := db.Value.(string)
			an, aOK := c.language.ConsistencyLanguage(archive, consistencySourceLanguage(bib, claim.Observation))
			bn, bOK := c.language.ConsistencyLanguage(database, consistencySourceLanguage(bib, "db"))
			a, b = an, bn
			mismatch = archiveOK && databaseOK && aOK && bOK && an != bn
		}
		if mismatch {
			c.add(rec, database, check, "low", "claims.bibliographic."+check, claim.Observation, claim.Value, db.Value, a, b)
		}
	}
}

func (c *mergeConsistencyChecker) add(
	rec *model.DatasetRecord,
	database *model.Observation,
	check string,
	confidence string,
	path string,
	observation string,
	archive any,
	db any,
	archiveNormalized any,
	dbNormalized any,
) {
	code := check + "_mismatch"
	details := map[string]any{
		"confidence": confidence, "policy": model.MergeConsistencyPolicyV1,
		"archive_observation": observation, "database_observation": "db",
		"archive_value": archive, "database_value": db,
		"archive_normalized": archiveNormalized, "database_normalized": dbNormalized,
	}
	if database.Match != nil {
		details["match_method"] = database.Match.Method
	}
	if database.Locator != nil && database.Locator.BookID != nil {
		details["book_id"] = *database.Locator.BookID
	}
	rec.Issues = append(rec.Issues, model.Issue{
		Observation: "db", Stage: "consistency", Code: code, Path: path,
		Message: "Archive " + strings.ReplaceAll(check, "_", " ") + " differs from database-reported value",
		Details: details, Retryable: false,
	})
	c.counts[code]++
}

func consistencyDirectContent(id model.RecordID) bool {
	if id.ContainerExtension != "" || id.Archive == nil {
		return false
	}
	ext := strings.TrimPrefix(filepath.Ext(id.Archive.Entry), ".")
	return consistencyDocumentType(ext) != ""
}

func consistencyMD5(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 32 || value == strings.Repeat("0", 32) || value == strings.Repeat("f", 32) {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func consistencyDocumentType(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	switch value {
	case "", "zip", "rar", "7z", "gz", "bz2", "xz", "zst", "tar", "tgz", "tbz2", "txz":
		return ""
	case "htm":
		return "html"
	case "djv":
		return "djvu"
	case "jpeg":
		return "jpg"
	default:
		return value
	}
}

func consistencyText(value string) string {
	value = cases.Fold().String(norm.NFKC.String(value))
	var out strings.Builder
	meaningful := false
	for _, r := range value {
		switch r {
		case '‘', '’', 'ʼ', '＇':
			r = '\''
		case '“', '”', '„', '«', '»':
			r = '"'
		case '‐', '‑', '‒', '–', '—', '−':
			r = '-'
		}
		meaningful = meaningful || unicode.IsLetter(r) || unicode.IsDigit(r)
		out.WriteRune(r)
	}
	if !meaningful {
		return ""
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

type consistencyAuthorName struct {
	First string `json:"first_name"`
	Last  string `json:"last_name"`
	Nick  string `json:"nick_name,omitempty"`
}

func consistencyAuthorNames(people []model.PersonValue) ([]consistencyAuthorName, bool) {
	names := make([]consistencyAuthorName, 0, len(people))
	complete := len(people) > 0
	for _, person := range people {
		name := consistencyAuthorName{
			First: consistencyText(person.FirstName), Last: consistencyText(person.LastName), Nick: consistencyText(person.NickName),
		}
		// Incomplete names and initials make a disjoint-set verdict inconclusive.
		for _, part := range []string{name.First, name.Last} {
			tokens := strings.FieldsFunc(part, func(r rune) bool { return !unicode.IsLetter(r) })
			if len(tokens) == 0 {
				complete = false
			}
			for _, token := range tokens {
				if len([]rune(token)) < 2 {
					complete = false
				}
			}
		}
		names = append(names, name)
	}
	return names, complete
}

func consistencySharedAuthor(a []consistencyAuthorName, b []consistencyAuthorName) bool {
	for _, left := range a {
		for _, right := range b {
			if left.First == right.First && left.Last == right.Last || left.Nick != "" && left.Nick == right.Nick {
				return true
			}
		}
	}
	return false
}

func consistencySourceLanguage(bib *model.BibliographicClaims, observation string) string {
	for _, claim := range bib.SourceLanguage {
		if claim.Observation == observation {
			value, _ := claim.Value.(string)
			return value
		}
	}
	return ""
}
