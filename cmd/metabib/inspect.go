package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	jsonstd "encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	cli "github.com/urfave/cli/v3"

	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
	"metabib/state"
)

const inspectNoMatchExitCode = 4

var errInspectNoMatch = errors.New("no dataset record matches inspect filter")

type inspectOptions struct {
	Input    string
	BookID   int64
	Archive  string
	Index    int
	File     string
	Archives bool
	Issues   bool
	JSON     bool
	Validate bool
	Verbose  bool
	DecodeFP bool
}

type inspectSummary struct {
	Input                      string                             `json:"input"`
	Schema                     string                             `json:"schema"`
	ID                         string                             `json:"id,omitempty"`
	RecordSchema               string                             `json:"record_schema"`
	Library                    string                             `json:"library,omitempty"`
	Created                    string                             `json:"created,omitempty"`
	Records                    int64                              `json:"records"`
	Generator                  string                             `json:"generator,omitempty"`
	Database                   string                             `json:"database,omitempty"`
	DumpDate                   string                             `json:"dump_date,omitempty"`
	ScopedDBAuthorAmbiguity    bool                               `json:"scoped_db_author_ambiguity,omitempty"`
	AmbiguousDBAuthorGroups    int                                `json:"ambiguous_db_author_groups"`
	AmbiguousDBAuthors         int                                `json:"ambiguous_db_authors"`
	AmbiguousDBAuthorGroupsFB2 int                                `json:"ambiguous_db_author_groups_fb2"`
	AmbiguousDBAuthorsFB2      int                                `json:"ambiguous_db_authors_fb2"`
	AmbiguousDBAuthorGroupsUSR int                                `json:"ambiguous_db_author_groups_usr"`
	AmbiguousDBAuthorsUSR      int                                `json:"ambiguous_db_authors_usr"`
	AmbiguousDBAuthorMap       []model.INPXAmbiguousDBAuthorGroup `json:"ambiguous_db_author_map,omitempty"`
	AmbiguousDBAuthorMapFB2    []model.INPXAmbiguousDBAuthorGroup `json:"ambiguous_db_author_map_fb2,omitempty"`
	AmbiguousDBAuthorMapUSR    []model.INPXAmbiguousDBAuthorGroup `json:"ambiguous_db_author_map_usr,omitempty"`
	Archives                   int                                `json:"archives"`
	ArchiveEntries             int                                `json:"archive_entries"`
	FB2Entries                 int                                `json:"fb2_entries"`
	Ordering                   string                             `json:"ordering,omitempty"`
	ParseFB2                   bool                               `json:"parse_fb2"`
	FB2Coverage                string                             `json:"fb2_coverage,omitempty"`
	FB2BodyFingerprints        string                             `json:"fb2_body_fingerprints,omitempty"`
	ContentChecksum            string                             `json:"content_checksum,omitempty"`
	RecordsRead                int64                              `json:"records_read,omitempty"`
	Validation                 string                             `json:"validation,omitempty"`
	IssueRecords               int64                              `json:"issue_records,omitempty"`
	Issues                     int64                              `json:"issues,omitempty"`
	IssuesByStage              map[string]int64                   `json:"issues_by_stage,omitempty"`
	IssuesByCode               map[string]int64                   `json:"issues_by_code,omitempty"`
}

type inspectRecordResult struct {
	Input               string                      `json:"input"`
	RecordNumber        int64                       `json:"record_number"`
	Record              model.DatasetRecord         `json:"record"`
	DecodedFingerprints []inspectDecodedFingerprint `json:"decoded_fingerprints,omitempty"`
}

type inspectDecodedFingerprint struct {
	Artifact string                  `json:"artifact"`
	Sections []inspectDecodedSection `json:"sections"`
}

type inspectDecodedSection struct {
	Depth int    `json:"depth"`
	Key   string `json:"key"`
	Leaf  bool   `json:"leaf,omitempty"`
}

type inspectArchivesResult struct {
	Input    string                 `json:"input"`
	Archives []model.DatasetArchive `json:"archives"`
}

type inspectIssuesResult struct {
	Input         string               `json:"input"`
	RecordsRead   int64                `json:"records_read"`
	IssueRecords  int64                `json:"issue_records"`
	Issues        int64                `json:"issues"`
	IssuesByStage map[string]int64     `json:"issues_by_stage,omitempty"`
	IssuesByCode  map[string]int64     `json:"issues_by_code,omitempty"`
	Records       []inspectIssueRecord `json:"records,omitempty"`
}

type inspectIssueRecord struct {
	RecordNumber int64               `json:"record_number"`
	Locator      model.RecordLocator `json:"locator"`
	Artifacts    []string            `json:"artifacts,omitempty"`
	Issues       []model.Issue       `json:"issues"`
}

type inspectIssueStats struct {
	IssueRecords  int64
	Issues        int64
	IssuesByStage map[string]int64
	IssuesByCode  map[string]int64
}

func (s *inspectIssueStats) Add(rec model.DatasetRecord) {
	if len(rec.Issues) == 0 {
		return
	}
	s.IssueRecords++
	for _, issue := range rec.Issues {
		s.Issues++
		s.IssuesByStage[issue.Stage]++
		s.IssuesByCode[issue.Code]++
	}
}

func (r *inspectIssuesResult) addRecord(recordNumber int64, rec model.DatasetRecord) {
	if len(rec.Issues) == 0 {
		return
	}
	r.IssueRecords++
	for _, issue := range rec.Issues {
		r.Issues++
		r.IssuesByStage[issue.Stage]++
		r.IssuesByCode[issue.Code]++
	}
	r.Records = append(r.Records, inspectIssueRecord{
		RecordNumber: recordNumber,
		Locator:      rec.Record.Locator,
		Artifacts:    inspectArtifactNames(rec),
		Issues:       rec.Issues,
	})
}

func inspectArtifactNames(rec model.DatasetRecord) []string {
	names := make([]string, 0, len(rec.Artifacts))
	for _, artifact := range rec.Artifacts {
		if artifact.Name != "" {
			names = append(names, artifact.Name)
		}
	}
	return names
}

func inspectCommand() *cli.Command {
	return &cli.Command{
		Name:  "inspect",
		Usage: "Inspect merged dataset JSONL metadata and records",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "input",
				Aliases:  []string{"i"},
				Usage:    "read merged dataset JSONL using `PREFIX` or exact path",
				Required: true,
			},
			&cli.Int64Flag{Name: "book-id", Usage: "show first record matching catalog book `ID`"},
			&cli.StringFlag{Name: "archive", Usage: "show record from archive source `ID`; requires --index"},
			&cli.IntFlag{Name: "index", Value: -1, Usage: "show record at zero-based archive entry `INDEX`; requires --archive"},
			&cli.StringFlag{Name: "file", Usage: "show first record with artifact or occurrence file `NAME`"},
			&cli.BoolFlag{Name: "archives", Usage: "list dataset archive source IDs and path hints"},
			&cli.BoolFlag{Name: "issues", Usage: "list records with dataset issues"},
			&cli.BoolFlag{Name: "decode-fp", Usage: "decode compact artifact fp payloads in record lookup output"},
			&cli.BoolFlag{Name: "json", Usage: "write machine-readable JSON output"},
			&cli.BoolFlag{Name: "validate", Usage: "consume the whole dataset and report validation status"},
		},
		Action: runInspect,
	}
}

func runInspect(ctx context.Context, cmd *cli.Command) error {
	err := inspectDataset(ctx, inspectOptions{
		Input:    cmd.String("input"),
		BookID:   cmd.Int64("book-id"),
		Archive:  cmd.String("archive"),
		Index:    cmd.Int("index"),
		File:     cmd.String("file"),
		Archives: cmd.Bool("archives"),
		Issues:   cmd.Bool("issues"),
		JSON:     cmd.Bool("json"),
		Validate: cmd.Bool("validate"),
		Verbose:  state.EnvFromContext(ctx).Verbose,
		DecodeFP: cmd.Bool("decode-fp"),
	}, os.Stdout)
	if errors.Is(err, errInspectNoMatch) {
		return cli.Exit(err, inspectNoMatchExitCode)
	}
	return err
}

func inspectDataset(ctx context.Context, opts inspectOptions, out io.Writer) error {
	if err := validateInspectOptions(opts); err != nil {
		return err
	}
	inputPath, err := inpxutil.DiscoverDatasetInput(opts.Input)
	if err != nil {
		return err
	}
	filter := inspectFilter(opts)
	var dataset model.Dataset
	var summary inspectSummary
	issueStats := inspectIssueStats{IssuesByStage: map[string]int64{}, IssuesByCode: map[string]int64{}}
	issuesResult := inspectIssuesResult{Input: inputPath, IssuesByStage: map[string]int64{}, IssuesByCode: map[string]int64{}}
	var recordsRead int64
	for value, err := range jsonl.DatasetValues(ctx, inputPath) {
		if err != nil {
			return err
		}
		if value.Header {
			dataset = value.Dataset
			summary = datasetInspectSummary(inputPath, dataset, opts.Verbose)
			issuesResult.Input = inputPath
			if opts.Archives {
				return writeInspectArchives(
					out,
					inspectArchivesResult{Input: inputPath, Archives: dataset.Archives},
					opts.JSON,
				)
			}
			if filter == nil && !opts.Validate && !opts.Issues {
				return writeInspectSummary(out, summary, opts.JSON)
			}
			continue
		}
		recordsRead++
		if opts.Validate {
			issueStats.Add(value.Record)
		}
		if opts.Issues {
			issuesResult.RecordsRead = recordsRead
			issuesResult.addRecord(recordsRead, value.Record)
			continue
		}
		if filter != nil && filter(value.Record) {
			result := inspectRecordResult{Input: inputPath, RecordNumber: recordsRead, Record: value.Record}
			if opts.DecodeFP {
				result.DecodedFingerprints = decodedRecordFingerprints(value.Record)
			}
			return writeInspectRecord(
				out,
				result,
				opts.JSON,
			)
		}
	}
	if filter != nil {
		return errInspectNoMatch
	}
	if opts.Issues {
		return writeInspectIssues(out, issuesResult, opts.JSON)
	}
	summary.RecordsRead = recordsRead
	summary.Validation = "ok"
	if opts.Validate {
		summary.IssueRecords = issueStats.IssueRecords
		summary.Issues = issueStats.Issues
		summary.IssuesByStage = issueStats.IssuesByStage
		summary.IssuesByCode = issueStats.IssuesByCode
	}
	return writeInspectSummary(out, summary, opts.JSON)
}

func validateInspectOptions(opts inspectOptions) error {
	if opts.Input == "" {
		return errors.New("inspect input is required")
	}
	lookupModes := 0
	if opts.BookID > 0 {
		lookupModes++
	}
	if opts.File != "" {
		lookupModes++
	}
	if opts.Archives {
		lookupModes++
	}
	if opts.Issues {
		lookupModes++
	}
	archiveLookup := opts.Archive != "" || opts.Index >= 0
	if archiveLookup {
		lookupModes++
		if opts.Archive == "" || opts.Index < 0 {
			return errors.New("inspect archive lookup requires both --archive and --index")
		}
	}
	if lookupModes > 1 {
		return errors.New("inspect accepts only one lookup mode at a time")
	}
	if opts.Validate && lookupModes > 0 {
		return errors.New("inspect --validate cannot be combined with lookup filters")
	}
	return nil
}

func inspectFilter(opts inspectOptions) func(model.DatasetRecord) bool {
	if opts.BookID > 0 {
		bookID := strconv.FormatInt(opts.BookID, 10)
		return func(rec model.DatasetRecord) bool { return datasetRecordHasBookID(rec, opts.BookID, bookID) }
	}
	if opts.Archive != "" {
		return func(rec model.DatasetRecord) bool {
			locator := rec.Record.Locator
			return locator.Kind == "archive_entry" &&
				locator.Source == opts.Archive &&
				locator.Index != nil &&
				*locator.Index == opts.Index
		}
	}
	if opts.File != "" {
		key := fileKey(opts.File)
		return func(rec model.DatasetRecord) bool { return datasetRecordHasFile(rec, key) }
	}
	return nil
}

func datasetRecordHasBookID(rec model.DatasetRecord, bookID int64, bookIDText string) bool {
	if rec.Record.Locator.BookID != nil && *rec.Record.Locator.BookID == bookID {
		return true
	}
	if rec.Identities != nil {
		for _, identity := range rec.Identities.Catalog {
			if identity.Scheme == "flibusta.book" && identity.Value == bookIDText {
				return true
			}
		}
	}
	for _, observation := range rec.Observations {
		if observation.Locator != nil && observation.Locator.BookID != nil && *observation.Locator.BookID == bookID {
			return true
		}
	}
	return false
}

func datasetRecordHasFile(rec model.DatasetRecord, key string) bool {
	for _, artifact := range rec.Artifacts {
		if fileKey(artifact.Name) == key {
			return true
		}
		for _, occurrence := range artifact.Occurrences {
			if fileKey(occurrence.Entry) == key {
				return true
			}
		}
	}
	return false
}

func datasetInspectSummary(inputPath string, dataset model.Dataset, verbose bool) inspectSummary {
	var database, dumpDate string
	var ambiguousGroups, ambiguousAuthors, ambiguousGroupsFB2, ambiguousAuthorsFB2, ambiguousGroupsUSR, ambiguousAuthorsUSR int
	var scoped bool
	var ambiguousMap, ambiguousMapFB2, ambiguousMapUSR []model.INPXAmbiguousDBAuthorGroup
	if dataset.Database != nil {
		database = dataset.Database.ID
		dumpDate = dataset.Database.DumpDate
		if dataset.Database.INPX != nil {
			scoped = dataset.Database.INPX.ScopedDBAuthorAmbiguity
			ambiguousGroups, ambiguousAuthors = ambiguousDBAuthorCounts(dataset.Database.INPX.AmbiguousDBAuthors)
			ambiguousGroupsFB2, ambiguousAuthorsFB2 = ambiguousDBAuthorCounts(dataset.Database.INPX.AmbiguousDBAuthorsFB2)
			ambiguousGroupsUSR, ambiguousAuthorsUSR = ambiguousDBAuthorCounts(dataset.Database.INPX.AmbiguousDBAuthorsUSR)
			if verbose {
				ambiguousMap = dataset.Database.INPX.AmbiguousDBAuthors
				ambiguousMapFB2 = dataset.Database.INPX.AmbiguousDBAuthorsFB2
				ambiguousMapUSR = dataset.Database.INPX.AmbiguousDBAuthorsUSR
			}
		}
	}
	var entries, fb2Entries int
	for _, archive := range dataset.Archives {
		entries += archive.Entries
		fb2Entries += archive.FB2Entries
	}
	return inspectSummary{
		Input:                      inputPath,
		Schema:                     dataset.Schema,
		ID:                         dataset.ID,
		RecordSchema:               dataset.RecordSchema,
		Library:                    dataset.Library,
		Created:                    dataset.Created,
		Records:                    dataset.Records,
		Generator:                  strings.TrimSpace(dataset.Generator.Name + " " + dataset.Generator.Version),
		Database:                   database,
		DumpDate:                   dumpDate,
		ScopedDBAuthorAmbiguity:    scoped,
		AmbiguousDBAuthorGroups:    ambiguousGroups,
		AmbiguousDBAuthors:         ambiguousAuthors,
		AmbiguousDBAuthorGroupsFB2: ambiguousGroupsFB2,
		AmbiguousDBAuthorsFB2:      ambiguousAuthorsFB2,
		AmbiguousDBAuthorGroupsUSR: ambiguousGroupsUSR,
		AmbiguousDBAuthorsUSR:      ambiguousAuthorsUSR,
		AmbiguousDBAuthorMap:       ambiguousMap,
		AmbiguousDBAuthorMapFB2:    ambiguousMapFB2,
		AmbiguousDBAuthorMapUSR:    ambiguousMapUSR,
		Archives:                   len(dataset.Archives),
		ArchiveEntries:             entries,
		FB2Entries:                 fb2Entries,
		Ordering:                   dataset.Ordering.Mode,
		ParseFB2:                   dataset.Processing.ParseFB2,
		FB2Coverage:                dataset.Processing.FB2Coverage,
		FB2BodyFingerprints:        fb2BodyFingerprintCoverage(dataset),
		ContentChecksum:            dataset.Processing.ArchiveContentChecksum.Algorithm,
	}
}

func ambiguousDBAuthorCounts(groups []model.INPXAmbiguousDBAuthorGroup) (int, int) {
	authors := 0
	for _, group := range groups {
		authors += len(group.Authors)
	}
	return len(groups), authors
}

func fb2BodyFingerprintCoverage(dataset model.Dataset) string {
	if dataset.Processing.FB2BodyFingerprints == nil {
		return ""
	}
	return dataset.Processing.FB2BodyFingerprints.Coverage
}

func writeInspectSummary(out io.Writer, summary inspectSummary, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, summary)
	}
	_, err := fmt.Fprintf(
		out,
		"Dataset\n"+
			"  input: %s\n"+
			"  schema: %s\n"+
			"  record schema: %s\n"+
			"  id: %s\n"+
			"  library: %s\n"+
			"  created: %s\n"+
			"  records: %d\n"+
			"  generator: %s\n"+
			"  database: %s\n"+
			"  dump date: %s\n"+
			"  scoped db author ambiguity: %t\n"+
			"  ambiguous db author groups: %d\n"+
			"  ambiguous db authors: %d\n"+
			"  ambiguous db author groups fb2: %d\n"+
			"  ambiguous db authors fb2: %d\n"+
			"  ambiguous db author groups usr: %d\n"+
			"  ambiguous db authors usr: %d\n"+
			"  archives: %d\n"+
			"  archive entries: %d\n"+
			"  fb2 entries: %d\n"+
			"  ordering: %s\n"+
			"  parse fb2: %t\n"+
			"  fb2 coverage: %s\n"+
			"  fb2 body fingerprints: %s\n"+
			"  content checksum: %s\n",
		summary.Input,
		summary.Schema,
		summary.RecordSchema,
		summary.ID,
		summary.Library,
		summary.Created,
		summary.Records,
		summary.Generator,
		summary.Database,
		summary.DumpDate,
		summary.ScopedDBAuthorAmbiguity,
		summary.AmbiguousDBAuthorGroups,
		summary.AmbiguousDBAuthors,
		summary.AmbiguousDBAuthorGroupsFB2,
		summary.AmbiguousDBAuthorsFB2,
		summary.AmbiguousDBAuthorGroupsUSR,
		summary.AmbiguousDBAuthorsUSR,
		summary.Archives,
		summary.ArchiveEntries,
		summary.FB2Entries,
		summary.Ordering,
		summary.ParseFB2,
		summary.FB2Coverage,
		summary.FB2BodyFingerprints,
		summary.ContentChecksum,
	)
	if err != nil {
		return err
	}
	if len(summary.AmbiguousDBAuthorMap) > 0 {
		if err := writeInspectAmbiguousDBAuthors(out, "  ambiguous db author map:", summary.AmbiguousDBAuthorMap); err != nil {
			return err
		}
	}
	if len(summary.AmbiguousDBAuthorMapFB2) > 0 {
		if err := writeInspectAmbiguousDBAuthors(out, "  ambiguous db author map fb2:", summary.AmbiguousDBAuthorMapFB2); err != nil {
			return err
		}
	}
	if len(summary.AmbiguousDBAuthorMapUSR) > 0 {
		if err := writeInspectAmbiguousDBAuthors(out, "  ambiguous db author map usr:", summary.AmbiguousDBAuthorMapUSR); err != nil {
			return err
		}
	}
	if summary.Validation != "" {
		if _, err = fmt.Fprintf(
			out,
			"  validation: %s\n  records read: %d\n  issue records: %d\n  issues: %d\n",
			summary.Validation,
			summary.RecordsRead,
			summary.IssueRecords,
			summary.Issues,
		); err != nil {
			return err
		}
		if err = writeIssueCounts(out, "  issues by stage", summary.IssuesByStage); err != nil {
			return err
		}
		if err = writeIssueCounts(out, "  issues by code", summary.IssuesByCode); err != nil {
			return err
		}
	}
	return err
}

func writeIssueCounts(out io.Writer, title string, counts map[string]int64) error {
	if len(counts) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, title+":"); err != nil {
		return err
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(out, "    %s: %d\n", key, counts[key]); err != nil {
			return err
		}
	}
	return nil
}

func writeInspectAmbiguousDBAuthors(out io.Writer, title string, groups []model.INPXAmbiguousDBAuthorGroup) error {
	if _, err := fmt.Fprintln(out, title); err != nil {
		return err
	}
	for _, group := range groups {
		if _, err := fmt.Fprintf(out, "    %s\n", group.Key); err != nil {
			return err
		}
		for _, author := range group.Authors {
			if _, err := fmt.Fprintf(
				out,
				"      %s: %s, %s, %s (%s)\n",
				author.ID,
				author.LastName,
				author.FirstName,
				author.MiddleName,
				author.NickName,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeInspectRecord(out io.Writer, result inspectRecordResult, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	data, err := jsonstd.MarshalIndent(result.Record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal inspect record: %w", err)
	}
	if _, err = fmt.Fprintf(out, "Record\n  input: %s\n  record number: %d\n%s\n", result.Input, result.RecordNumber, data); err != nil {
		return err
	}
	if len(result.DecodedFingerprints) > 0 {
		return writeInspectDecodedFingerprints(out, result.DecodedFingerprints)
	}
	return nil
}

func writeInspectIssues(out io.Writer, result inspectIssuesResult, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	if _, err := fmt.Fprintf(
		out,
		"Issue Records\n  input: %s\n  records read: %d\n  issue records: %d\n  issues: %d\n",
		result.Input,
		result.RecordsRead,
		result.IssueRecords,
		result.Issues,
	); err != nil {
		return err
	}
	if err := writeIssueCounts(out, "  issues by stage", result.IssuesByStage); err != nil {
		return err
	}
	if err := writeIssueCounts(out, "  issues by code", result.IssuesByCode); err != nil {
		return err
	}
	if len(result.Records) == 0 {
		_, err := fmt.Fprintln(out, "  none")
		return err
	}
	for _, rec := range result.Records {
		if _, err := fmt.Fprintf(out, "  record %d: %s\n", rec.RecordNumber, inspectLocatorString(rec.Locator)); err != nil {
			return err
		}
		if len(rec.Artifacts) > 0 {
			if _, err := fmt.Fprintf(out, "    artifacts: %s\n", strings.Join(rec.Artifacts, ", ")); err != nil {
				return err
			}
		}
		for _, issue := range rec.Issues {
			if _, err := fmt.Fprintf(out, "    %s/%s", issue.Stage, issue.Code); err != nil {
				return err
			}
			if issue.Path != "" {
				if _, err := fmt.Fprintf(out, " path=%s", issue.Path); err != nil {
					return err
				}
			}
			if issue.Message != "" {
				if _, err := fmt.Fprintf(out, ": %s", issue.Message); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectLocatorString(locator model.RecordLocator) string {
	parts := []string{locator.Kind, locator.Source}
	if locator.Index != nil {
		parts = append(parts, "index="+strconv.Itoa(*locator.Index))
	}
	if locator.BookID != nil {
		parts = append(parts, "book_id="+strconv.FormatInt(*locator.BookID, 10))
	}
	return strings.Join(parts, " ")
}

func decodedRecordFingerprints(rec model.DatasetRecord) []inspectDecodedFingerprint {
	out := make([]inspectDecodedFingerprint, 0)
	for _, artifact := range rec.Artifacts {
		if artifact.Fingerprints == nil || artifact.Fingerprints.FB2Body == nil || len(artifact.Fingerprints.FB2Body.Sections) == 0 {
			continue
		}
		decoded := inspectDecodedFingerprint{Artifact: artifact.Name}
		for _, section := range artifact.Fingerprints.FB2Body.Sections {
			decoded.Sections = append(decoded.Sections, inspectDecodedSection{
				Depth: section.Depth,
				Key:   fingerprintKeyHex(section.Key),
				Leaf:  section.Leaf,
			})
		}
		out = append(out, decoded)
	}
	return out
}

func fingerprintKeyHex(key string) string {
	data, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return key
	}
	return hex.EncodeToString(data)
}

func writeInspectDecodedFingerprints(out io.Writer, fingerprints []inspectDecodedFingerprint) error {
	if _, err := fmt.Fprintln(out, "Decoded fingerprints"); err != nil {
		return err
	}
	for _, fingerprint := range fingerprints {
		if _, err := fmt.Fprintf(out, "  artifact: %s\n", fingerprint.Artifact); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, "  DEPTH  LEAF  KEY"); err != nil {
			return err
		}
		for _, section := range fingerprint.Sections {
			leaf := 0
			if section.Leaf {
				leaf = 1
			}
			if _, err := fmt.Fprintf(out, "  %-5d  %-4d  %s\n", section.Depth, leaf, section.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeInspectArchives(out io.Writer, result inspectArchivesResult, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	if _, err := fmt.Fprintf(out, "Archives\n  input: %s\n", result.Input); err != nil {
		return err
	}
	if len(result.Archives) == 0 {
		_, err := fmt.Fprintln(out, "  none")
		return err
	}
	_, err := fmt.Fprintln(out, "ID            ORDINAL  ENTRIES  FB2       NAME                         PATH")
	if err != nil {
		return err
	}
	for _, archive := range result.Archives {
		if _, err := fmt.Fprintf(
			out,
			"%-13s %-8d %-8d %-9d %-28s %s\n",
			archive.ID,
			archive.Ordinal,
			archive.Entries,
			archive.FB2Entries,
			archive.Name,
			archive.PathHint,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(out io.Writer, value any) error {
	if err := jsonv2.MarshalWrite(out, value); err != nil {
		return err
	}
	_, err := out.Write([]byte{'\n'})
	return err
}
