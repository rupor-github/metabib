package model

type Record struct {
	Schema string        `json:"schema"`
	ID     RecordID      `json:"id"`
	Source RecordSources `json:"sources"`
	Errors []string      `json:"errors,omitempty"`
}

type RecordID struct {
	Library   string       `json:"library"`
	BookID    int64        `json:"book_id,omitempty"`
	FileName  string       `json:"file_name,omitempty"`
	Extension string       `json:"extension,omitempty"`
	Archive   *ArchiveInfo `json:"archive,omitempty"`
}

type ArchiveInfo struct {
	Path             string `json:"path"`
	Entry            string `json:"entry"`
	Index            int    `json:"index,omitempty"`
	CompressedSize   uint64 `json:"compressed_size,omitempty"`
	UncompressedSize uint64 `json:"uncompressed_size,omitempty"`
	ContentMD5       string `json:"content_md5,omitempty"`
	Modified         string `json:"modified,omitempty"`
}

type MergeMetadata struct {
	Schema      string                 `json:"schema"`
	Library     string                 `json:"library"`
	Database    MergeDatabaseMetadata  `json:"database"`
	Archives    []MergeArchiveMetadata `json:"archives,omitempty"`
	Parts       []string               `json:"parts"`
	Compression string                 `json:"compression,omitempty"`
	Created     string                 `json:"created,omitempty"`
}

type MergeDatabaseMetadata struct {
	DumpDir     string `json:"dump_dir,omitempty"`
	DumpDate    string `json:"dump_date,omitempty"`
	DumpDateISO string `json:"dump_date_iso,omitempty"`
}

type MergeArchiveMetadata struct {
	Path         string       `json:"path"`
	Name         string       `json:"name"`
	Entries      int          `json:"entries"`
	FB2Entries   int          `json:"fb2_entries"`
	Ignored      []IndexRange `json:"ignored,omitempty"`
	Dummy        []IndexRange `json:"dummy,omitempty"`
	ManifestPath string       `json:"manifest_path,omitempty"`
}

type IndexRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type RecordSources struct {
	Database DatabaseSource `json:"database"`
	FB2      FB2Source      `json:"fb2"`
}

type DatabaseSource struct {
	Present     bool           `json:"present"`
	Book        *DBBook        `json:"book,omitempty"`
	Authors     []Contributor  `json:"authors,omitempty"`
	Translators []Contributor  `json:"translators,omitempty"`
	Genres      []DBGenre      `json:"genres,omitempty"`
	Sequences   []DBSequence   `json:"sequences,omitempty"`
	Rating      *DBRating      `json:"rating,omitempty"`
	Filenames   []string       `json:"filenames,omitempty"`
	JoinedBooks []DBJoinedBook `json:"joined_books,omitempty"`
}

type DBBook struct {
	BookID     int64  `json:"book_id"`
	FileSize   int64  `json:"file_size"`
	Time       string `json:"time,omitempty"`
	Title      string `json:"title"`
	Lang       string `json:"lang,omitempty"`
	SrcLang    string `json:"src_lang,omitempty"`
	FileType   string `json:"file_type,omitempty"`
	Year       int64  `json:"year,omitempty"`
	Deleted    string `json:"deleted,omitempty"`
	FileAuthor string `json:"file_author,omitempty"`
	Keywords   string `json:"keywords,omitempty"`
	MD5        string `json:"md5,omitempty"`
	Modified   string `json:"modified,omitempty"`
	ReplacedBy int64  `json:"replaced_by,omitempty"`
}

type Contributor struct {
	ID         int64  `json:"id,omitempty"`
	FirstName  string `json:"first_name,omitempty"`
	MiddleName string `json:"middle_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	NickName   string `json:"nick_name,omitempty"`
	UID        int64  `json:"uid,omitempty"`
	Email      string `json:"email,omitempty"`
	Homepage   string `json:"homepage,omitempty"`
	Gender     string `json:"gender,omitempty"`
	MasterID   int64  `json:"master_id,omitempty"`
	Position   int64  `json:"position,omitempty"`
}

type DBGenre struct {
	ID             int64  `json:"id"`
	Code           string `json:"code"`
	TranslatedCode string `json:"translated_code,omitempty"`
	Description    string `json:"description,omitempty"`
	Meta           string `json:"meta,omitempty"`
}

type DBSequence struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Number int64  `json:"number,omitempty"`
	Level  int64  `json:"level,omitempty"`
	Type   int64  `json:"type,omitempty"`
}

type DBRating struct {
	Average float64 `json:"average,omitempty"`
	Count   int64   `json:"count"`
	Min     int64   `json:"min,omitempty"`
	Max     int64   `json:"max,omitempty"`
}

type DBJoinedBook struct {
	ID     int64  `json:"id"`
	Time   string `json:"time,omitempty"`
	BadID  int64  `json:"bad_id,omitempty"`
	GoodID int64  `json:"good_id,omitempty"`
	RealID int64  `json:"real_id,omitempty"`
}

type FB2Source struct {
	Present     bool            `json:"present"`
	Description *FB2Description `json:"description,omitempty"`
}

type FB2Description struct {
	TitleInfo    *FB2TitleInfo    `json:"title_info,omitempty"`
	SrcTitleInfo *FB2TitleInfo    `json:"src_title_info,omitempty"`
	DocumentInfo *FB2DocumentInfo `json:"document_info,omitempty"`
	PublishInfo  *FB2PublishInfo  `json:"publish_info,omitempty"`
	CustomInfo   []FB2CustomInfo  `json:"custom_info,omitempty"`
	Output       []FB2Output      `json:"output,omitempty"`
}

type FB2TitleInfo struct {
	Genres      []FB2Genre    `json:"genres,omitempty"`
	Authors     []FB2Person   `json:"authors,omitempty"`
	Title       string        `json:"title,omitempty"`
	Annotation  string        `json:"annotation,omitempty"`
	Keywords    string        `json:"keywords,omitempty"`
	Date        *FB2Date      `json:"date,omitempty"`
	Language    string        `json:"language,omitempty"`
	SourceLang  string        `json:"source_language,omitempty"`
	Translators []FB2Person   `json:"translators,omitempty"`
	Sequences   []FB2Sequence `json:"sequences,omitempty"`
}

type FB2Genre struct {
	Code  string `json:"code"`
	Match string `json:"match,omitempty"`
}

type FB2Person struct {
	ID         string   `json:"id,omitempty"`
	FirstName  string   `json:"first_name,omitempty"`
	MiddleName string   `json:"middle_name,omitempty"`
	LastName   string   `json:"last_name,omitempty"`
	NickName   string   `json:"nick_name,omitempty"`
	HomePages  []string `json:"home_pages,omitempty"`
	Emails     []string `json:"emails,omitempty"`
}

type FB2Date struct {
	Text  string `json:"text,omitempty"`
	Value string `json:"value,omitempty"`
}

type FB2Sequence struct {
	Name   string        `json:"name,omitempty"`
	Number string        `json:"number,omitempty"`
	Lang   string        `json:"language,omitempty"`
	Nested []FB2Sequence `json:"sequences,omitempty"`
}

type FB2DocumentInfo struct {
	Authors     []FB2Person `json:"authors,omitempty"`
	ProgramUsed string      `json:"program_used,omitempty"`
	Date        *FB2Date    `json:"date,omitempty"`
	SrcURLs     []string    `json:"src_urls,omitempty"`
	SrcOCR      string      `json:"src_ocr,omitempty"`
	ID          string      `json:"id,omitempty"`
	Version     string      `json:"version,omitempty"`
	History     string      `json:"history,omitempty"`
	Publishers  []FB2Person `json:"publishers,omitempty"`
}

type FB2PublishInfo struct {
	BookName  string        `json:"book_name,omitempty"`
	Publisher string        `json:"publisher,omitempty"`
	City      string        `json:"city,omitempty"`
	Year      string        `json:"year,omitempty"`
	ISBN      string        `json:"isbn,omitempty"`
	Sequences []FB2Sequence `json:"sequences,omitempty"`
}

type FB2CustomInfo struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type FB2Output struct {
	Mode                  string                   `json:"mode,omitempty"`
	IncludeAll            string                   `json:"include_all,omitempty"`
	Price                 string                   `json:"price,omitempty"`
	Currency              string                   `json:"currency,omitempty"`
	Parts                 []FB2OutputPart          `json:"parts,omitempty"`
	OutputDocumentClasses []FB2OutputDocumentClass `json:"output_document_classes,omitempty"`
}

type FB2OutputPart struct {
	Type    string `json:"type,omitempty"`
	Href    string `json:"href,omitempty"`
	Include string `json:"include,omitempty"`
}

type FB2OutputDocumentClass struct {
	Name   string          `json:"name,omitempty"`
	Create string          `json:"create,omitempty"`
	Price  string          `json:"price,omitempty"`
	Parts  []FB2OutputPart `json:"parts,omitempty"`
}
