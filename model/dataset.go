package model

const (
	DatasetSchemaV1 = "metabib.dataset/1"
	RecordSchemaV2  = "metabib.record/2"
)

type Dataset struct {
	Schema        string               `json:"schema"`
	ID            string               `json:"id"`
	RecordSchema  string               `json:"record_schema"`
	Library       string               `json:"library"`
	Created       string               `json:"created"`
	Records       int64                `json:"records"`
	Generator     DatasetGenerator     `json:"generator"`
	Normalization DatasetNormalization `json:"normalization"`
	Database      *DatasetDatabase     `json:"database,omitempty"`
	Archives      []DatasetArchive     `json:"archives,omitempty"`
	Ordering      DatasetOrdering      `json:"ordering"`
	Processing    DatasetProcessing    `json:"processing"`
}

type DatasetGenerator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type DatasetNormalization struct {
	Model string `json:"model"`
}

type DatasetDatabase struct {
	ID          string        `json:"id"`
	Format      string        `json:"format,omitempty"`
	DumpDirHint string        `json:"dump_dir_hint,omitempty"`
	DumpDate    string        `json:"dump_date,omitempty"`
	Dumps       []DatasetDump `json:"dumps,omitempty"`
}

type DatasetDump struct {
	Name          string    `json:"name"`
	PathHint      string    `json:"path_hint,omitempty"`
	DumpCompleted string    `json:"dump_completed,omitempty"`
	Modified      string    `json:"modified,omitempty"`
	Checksum      *Checksum `json:"checksum,omitempty"`
}

type DatasetArchive struct {
	ID         string       `json:"id"`
	Ordinal    int          `json:"ordinal"`
	Name       string       `json:"name"`
	PathHint   string       `json:"path_hint,omitempty"`
	Modified   string       `json:"modified,omitempty"`
	Checksum   *Checksum    `json:"checksum,omitempty"`
	Entries    int          `json:"entries"`
	FB2Entries int          `json:"fb2_entries"`
	Ignored    []IndexRange `json:"ignored,omitempty"`
	Dummy      []IndexRange `json:"dummy,omitempty"`
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Scope     string `json:"scope,omitempty"`
	Value     string `json:"value"`
}

type DatasetOrdering struct {
	Mode       string `json:"mode"`
	ArchiveKey string `json:"archive_key,omitempty"`
	EntryKey   string `json:"entry_key,omitempty"`
	Source     string `json:"source,omitempty"`
	Direction  string `json:"direction"`
}

type DatasetProcessing struct {
	ParseFB2               bool                  `json:"parse_fb2"`
	FB2Coverage            string                `json:"fb2_coverage,omitempty"`
	ArchiveContentChecksum DatasetChecksumOption `json:"archive_content_checksum"`
}

type DatasetChecksumOption struct {
	Enabled   bool   `json:"enabled"`
	Algorithm string `json:"algorithm,omitempty"`
}

type DatasetRecord struct {
	Schema       string           `json:"schema"`
	Record       RecordDescriptor `json:"record"`
	Identities   *Identities      `json:"identities,omitempty"`
	Artifacts    []Artifact       `json:"artifacts,omitempty"`
	Observations []Observation    `json:"observations"`
	Claims       Claims           `json:"claims"`
	Relations    []Relation       `json:"relations,omitempty"`
	Issues       []Issue          `json:"issues,omitempty"`
}

type RecordDescriptor struct {
	Library string        `json:"library"`
	Locator RecordLocator `json:"locator"`
}

type RecordLocator struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Index  *int   `json:"index,omitempty"`
	BookID *int64 `json:"book_id,omitempty"`
}

type Identities struct {
	Catalog     []Identity `json:"catalog,omitempty"`
	Document    []Identity `json:"document,omitempty"`
	Publication []Identity `json:"publication,omitempty"`
}

type Identity struct {
	Scheme      string `json:"scheme"`
	Value       string `json:"value"`
	Observation string `json:"observation"`
	Basis       string `json:"basis,omitempty"`
}

type Observation struct {
	ID       string              `json:"id"`
	Source   string              `json:"source"`
	Kind     string              `json:"kind"`
	Status   string              `json:"status"`
	Locator  *ObservationLocator `json:"locator,omitempty"`
	Coverage string              `json:"coverage,omitempty"`
	Parent   string              `json:"parent,omitempty"`
	Match    *Match              `json:"match,omitempty"`
}

type ObservationLocator struct {
	Entry  string `json:"entry,omitempty"`
	Index  *int   `json:"index,omitempty"`
	BookID *int64 `json:"book_id,omitempty"`
}

type Match struct {
	Method     string `json:"method"`
	Input      string `json:"input,omitempty"`
	Normalized string `json:"normalized,omitempty"`
	Candidate  *int64 `json:"candidate,omitempty"`
	BookID     *int64 `json:"book_id,omitempty"`
}

type Claims struct {
	Bibliographic *BibliographicClaims `json:"bibliographic,omitempty"`
	Original      *BibliographicClaims `json:"original,omitempty"`
	Publication   *PublicationClaims   `json:"publication,omitempty"`
	Document      *DocumentClaims      `json:"document,omitempty"`
	Catalog       *CatalogClaims       `json:"catalog,omitempty"`
}

type Claim struct {
	Observation string `json:"observation"`
	Value       any    `json:"value"`
	Raw         any    `json:"raw,omitempty"`
}

type BibliographicClaims struct {
	Title             []Claim `json:"title,omitempty"`
	Authors           []Claim `json:"authors,omitempty"`
	Translators       []Claim `json:"translators,omitempty"`
	Illustrators      []Claim `json:"illustrators,omitempty"`
	Genres            []Claim `json:"genres,omitempty"`
	Annotation        []Claim `json:"annotation,omitempty"`
	Keywords          []Claim `json:"keywords,omitempty"`
	Language          []Claim `json:"language,omitempty"`
	SourceLanguage    []Claim `json:"source_language,omitempty"`
	BibliographicDate []Claim `json:"bibliographic_date,omitempty"`
	Sequences         []Claim `json:"sequences,omitempty"`
}

type PublicationClaims struct {
	BookName  []Claim `json:"book_name,omitempty"`
	Publisher []Claim `json:"publisher,omitempty"`
	City      []Claim `json:"city,omitempty"`
	Year      []Claim `json:"year,omitempty"`
	ISBN      []Claim `json:"isbn,omitempty"`
	Sequences []Claim `json:"sequences,omitempty"`
}

type DocumentClaims struct {
	Authors     []Claim `json:"authors,omitempty"`
	ProgramUsed []Claim `json:"program_used,omitempty"`
	Date        []Claim `json:"date,omitempty"`
	SourceURLs  []Claim `json:"source_urls,omitempty"`
	SourceOCR   []Claim `json:"source_ocr,omitempty"`
	Version     []Claim `json:"version,omitempty"`
	History     []Claim `json:"history,omitempty"`
	Publishers  []Claim `json:"publishers,omitempty"`
	CustomInfo  []Claim `json:"custom_info,omitempty"`
	Output      []Claim `json:"output,omitempty"`
}

type CatalogClaims struct {
	Time       []Claim `json:"time,omitempty"`
	Modified   []Claim `json:"modified,omitempty"`
	Rating     []Claim `json:"rating,omitempty"`
	Deleted    []Claim `json:"deleted,omitempty"`
	Aliases    []Claim `json:"aliases,omitempty"`
	FileAuthor []Claim `json:"file_author,omitempty"`
	Status     []Claim `json:"status,omitempty"`
}

type Artifact struct {
	Name        string             `json:"name"`
	MediaType   string             `json:"media_type,omitempty"`
	Size        []ArtifactSize     `json:"size,omitempty"`
	Checksums   []ArtifactChecksum `json:"checksums,omitempty"`
	Occurrences []Occurrence       `json:"occurrences,omitempty"`
}

type ArtifactSize struct {
	Observation string `json:"observation"`
	Value       uint64 `json:"value"`
	Kind        string `json:"kind"`
}

type ArtifactChecksum struct {
	Observation string `json:"observation"`
	Algorithm   string `json:"algorithm"`
	Scope       string `json:"scope"`
	Origin      string `json:"origin"`
	Value       string `json:"value"`
}

type Occurrence struct {
	Archive          string `json:"archive"`
	Entry            string `json:"entry"`
	Index            int    `json:"index"`
	CompressedSize   uint64 `json:"compressed_size,omitempty"`
	UncompressedSize uint64 `json:"uncompressed_size,omitempty"`
	Modified         string `json:"modified,omitempty"`
}

type Relation struct {
	Type         string            `json:"type"`
	Observation  string            `json:"observation"`
	Target       *IdentityTarget   `json:"target,omitempty"`
	EventID      string            `json:"event_id,omitempty"`
	Time         string            `json:"time,omitempty"`
	Participants map[string]string `json:"participants,omitempty"`
}

type IdentityTarget struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

type Issue struct {
	Observation string `json:"observation,omitempty"`
	Stage       string `json:"stage"`
	Code        string `json:"code"`
	Path        string `json:"path,omitempty"`
	Message     string `json:"message,omitempty"`
	Retryable   bool   `json:"retryable"`
}
