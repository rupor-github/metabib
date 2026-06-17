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
	CompressedSize   uint64 `json:"compressed_size,omitempty"`
	UncompressedSize uint64 `json:"uncompressed_size,omitempty"`
	ContentMD5       string `json:"content_md5,omitempty"`
	Modified         string `json:"modified,omitempty"`
}

type RecordSources struct {
	Database DatabaseSource `json:"database"`
	FB2      FB2Source      `json:"fb2"`
}

type DatabaseSource struct {
	Present         bool               `json:"present"`
	Book            *DBBook            `json:"book,omitempty"`
	Authors         []Contributor      `json:"authors,omitempty"`
	Translators     []Contributor      `json:"translators,omitempty"`
	Genres          []DBGenre          `json:"genres,omitempty"`
	Sequences       []DBSequence       `json:"sequences,omitempty"`
	Rating          *DBRating          `json:"rating,omitempty"`
	Filenames       []string           `json:"filenames,omitempty"`
	JoinedBooks     []DBJoinedBook     `json:"joined_books,omitempty"`
	Recommendations []DBRecommendation `json:"recommendations,omitempty"`
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

type DBRecommendation struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id,omitempty"`
	BookID    int64  `json:"book_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type FB2Source struct {
	Present     bool          `json:"present"`
	TitleInfo   *FB2TitleInfo `json:"title_info,omitempty"`
	Description *XMLNode      `json:"description,omitempty"`
}

type FB2TitleInfo struct {
	Genres      []FB2Genre    `json:"genres,omitempty"`
	Authors     []FB2Person   `json:"authors,omitempty"`
	Translators []FB2Person   `json:"translators,omitempty"`
	Title       string        `json:"title,omitempty"`
	Annotation  string        `json:"annotation,omitempty"`
	Keywords    string        `json:"keywords,omitempty"`
	Date        *FB2Date      `json:"date,omitempty"`
	CoverImages []string      `json:"cover_images,omitempty"`
	Language    string        `json:"language,omitempty"`
	SourceLang  string        `json:"source_language,omitempty"`
	Sequences   []FB2Sequence `json:"sequences,omitempty"`
}

type FB2Genre struct {
	Code  string `json:"code"`
	Match string `json:"match,omitempty"`
}

type FB2Person struct {
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
	Name   string `json:"name,omitempty"`
	Number string `json:"number,omitempty"`
}

type XMLNode struct {
	Name     string            `json:"name"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Text     string            `json:"text,omitempty"`
	Children []XMLNode         `json:"children,omitempty"`
}
