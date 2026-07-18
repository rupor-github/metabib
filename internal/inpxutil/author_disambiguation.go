package inpxutil

import (
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"metabib/model"
)

type DBAuthorAmbiguityCollector struct {
	groups map[string]*authorCollisionGroup
}

type AuthorDisambiguator struct {
	suffixes map[string]string
}

type authorCollisionGroup struct {
	key     string
	authors map[string]authorCollisionAuthor
}

type authorCollisionAuthor struct {
	id       string
	nickName string
	person   model.PersonValue
}

func NewDBAuthorAmbiguityCollector() *DBAuthorAmbiguityCollector {
	return &DBAuthorAmbiguityCollector{groups: make(map[string]*authorCollisionGroup)}
}

func (c *DBAuthorAmbiguityCollector) AddContributor(contributor model.Contributor) {
	if c == nil || contributor.ID <= 0 {
		return
	}
	person := model.PersonValue{
		FirstName:  contributor.FirstName,
		MiddleName: contributor.MiddleName,
		LastName:   contributor.LastName,
		NickName:   contributor.NickName,
	}
	key := authorKey(person, "")
	if key == "" {
		return
	}
	id := strconv.FormatInt(contributor.ID, 10)
	group := c.groups[key]
	if group == nil {
		group = &authorCollisionGroup{key: key, authors: make(map[string]authorCollisionAuthor)}
		c.groups[key] = group
	}
	if _, exists := group.authors[id]; exists {
		return
	}
	group.authors[id] = authorCollisionAuthor{
		id:       id,
		nickName: CleanseAuthorComponent(contributor.NickName),
		person:   person,
	}
}

func (c *DBAuthorAmbiguityCollector) Metadata() *model.INPXMetadata {
	if c == nil || len(c.groups) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.groups))
	for key := range c.groups {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	metadata := &model.INPXMetadata{}
	for _, key := range keys {
		group := c.groups[key]
		if len(group.authors) < 2 {
			continue
		}
		metadata.AmbiguousDBAuthors = append(metadata.AmbiguousDBAuthors, metadataGroup(group))
	}
	if len(metadata.AmbiguousDBAuthors) == 0 {
		return nil
	}
	return metadata
}

func NewAuthorDisambiguator(metadata *model.INPXMetadata, log *zap.Logger, verbose bool) *AuthorDisambiguator {
	if metadata == nil || len(metadata.AmbiguousDBAuthors) == 0 {
		if verbose && log != nil {
			log.Debug("INPX DB author disambiguation metadata is absent")
		}
		return nil
	}
	d := &AuthorDisambiguator{suffixes: make(map[string]string)}
	assignedKeys := make(map[string]string)
	for _, group := range metadata.AmbiguousDBAuthors {
		nickCounts := make(map[string]int)
		for _, author := range group.Authors {
			nickName := CleanseAuthorComponent(author.NickName)
			if nickName != "" {
				nickCounts[nickName]++
			}
		}
		groupAuthors := make(map[string]authorCollisionAuthor, len(group.Authors))
		for _, author := range group.Authors {
			if author.ID == "" {
				continue
			}
			groupAuthors[author.ID] = authorFromMetadata(author)
		}
		for _, author := range group.Authors {
			if author.ID == "" {
				continue
			}
			collisionAuthor := authorFromMetadata(author)
			suffix := selectAuthorSuffix(collisionAuthor, nickCounts, groupAuthors, assignedKeys)
			d.suffixes[author.ID] = suffix
			assignedKeys[authorKey(collisionAuthor.person, suffix)] = author.ID
			if verbose && log != nil {
				log.Debug(
					"INPX DB author disambiguated",
					zap.String("author_key", group.Key),
					zap.String("flibusta_person_id", author.ID),
					zap.String("nick_name", author.NickName),
					zap.String("suffix", suffix),
				)
			}
		}
	}
	if len(d.suffixes) == 0 {
		return nil
	}
	if verbose && log != nil {
		log.Debug(
			"INPX DB author disambiguation map built",
			zap.Int("authors", len(d.suffixes)),
			zap.Int("groups", len(metadata.AmbiguousDBAuthors)),
		)
	}
	return d
}

func (d *AuthorDisambiguator) LastName(person model.PersonValue) string {
	if d == nil {
		return person.LastName
	}
	suffix := d.Suffix(person)
	if suffix == "" {
		return person.LastName
	}
	return strings.TrimSpace(person.LastName + " " + suffix)
}

func (d *AuthorDisambiguator) Suffix(person model.PersonValue) string {
	if d == nil {
		return ""
	}
	return d.suffixes[FlibustaPersonID(person)]
}

func FlibustaPersonID(person model.PersonValue) string {
	for _, identity := range person.Identities {
		if identity.Scheme == "flibusta.person" {
			return identity.Value
		}
	}
	return ""
}

func metadataGroup(group *authorCollisionGroup) model.INPXAmbiguousDBAuthorGroup {
	ids := make([]string, 0, len(group.authors))
	for id := range group.authors {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := model.INPXAmbiguousDBAuthorGroup{Key: group.key, Authors: make([]model.INPXAmbiguousDBAuthor, 0, len(ids))}
	for _, id := range ids {
		author := group.authors[id]
		out.Authors = append(out.Authors, model.INPXAmbiguousDBAuthor{
			ID:         id,
			FirstName:  author.person.FirstName,
			MiddleName: author.person.MiddleName,
			LastName:   author.person.LastName,
			NickName:   author.nickName,
		})
	}
	return out
}

func authorFromMetadata(author model.INPXAmbiguousDBAuthor) authorCollisionAuthor {
	return authorCollisionAuthor{
		id:       author.ID,
		nickName: CleanseAuthorComponent(author.NickName),
		person: model.PersonValue{
			FirstName:  author.FirstName,
			MiddleName: author.MiddleName,
			LastName:   author.LastName,
			NickName:   author.NickName,
		},
	}
}

func selectAuthorSuffix(
	author authorCollisionAuthor,
	nickCounts map[string]int,
	groupAuthors map[string]authorCollisionAuthor,
	assignedKeys map[string]string,
) string {
	candidates := []string{authorSuffix(author, nickCounts)}
	if author.nickName != "" {
		candidates = append(candidates, "["+author.nickName+" "+authorIDLabel(author.id)+"]")
	}
	idSuffix := "[" + authorIDLabel(author.id) + "]"
	if candidates[len(candidates)-1] != idSuffix {
		candidates = append(candidates, idSuffix)
	}
	for _, candidate := range candidates {
		if !keyCollides(author.id, authorKey(author.person, candidate), groupAuthors, assignedKeys) {
			return candidate
		}
	}
	return idSuffix
}

func authorSuffix(author authorCollisionAuthor, nickCounts map[string]int) string {
	if author.nickName != "" && nickCounts[author.nickName] == 1 {
		return "[" + author.nickName + "]"
	}
	return "[" + authorIDLabel(author.id) + "]"
}

func authorIDLabel(id string) string {
	return "#" + id
}

func authorKey(person model.PersonValue, suffix string) string {
	lastName := CleanseAuthorComponent(person.LastName)
	suffix = CleanseAuthorComponent(suffix)
	if suffix != "" {
		lastName = strings.TrimSpace(lastName + " " + suffix)
	}
	firstName := CleanseAuthorComponent(person.FirstName)
	middleName := CleanseAuthorComponent(person.MiddleName)
	if lastName == "" && firstName == "" && middleName == "" {
		return ""
	}
	return lastName + "," + firstName + "," + middleName
}

func keyCollides(id string, key string, groupAuthors map[string]authorCollisionAuthor, assignedKeys map[string]string) bool {
	if key == "" {
		return true
	}
	if existing := assignedKeys[key]; existing != "" && existing != id {
		return true
	}
	_, sameAuthor := groupAuthors[id]
	return !sameAuthor
}
