package main

import (
	"context"
	jsonstd "encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	jsonv2 "encoding/json/v2"
	cli "github.com/urfave/cli/v3"

	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
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
	JSON     bool
	Validate bool
}

type inspectSummary struct {
	Input           string `json:"input"`
	Schema          string `json:"schema"`
	ID              string `json:"id,omitempty"`
	RecordSchema    string `json:"record_schema"`
	Library         string `json:"library,omitempty"`
	Created         string `json:"created,omitempty"`
	Records         int64  `json:"records"`
	Generator       string `json:"generator,omitempty"`
	Database        string `json:"database,omitempty"`
	DumpDate        string `json:"dump_date,omitempty"`
	Archives        int    `json:"archives"`
	ArchiveEntries  int    `json:"archive_entries"`
	FB2Entries      int    `json:"fb2_entries"`
	Ordering        string `json:"ordering,omitempty"`
	ParseFB2        bool   `json:"parse_fb2"`
	FB2Coverage     string `json:"fb2_coverage,omitempty"`
	ContentChecksum string `json:"content_checksum,omitempty"`
	RecordsRead     int64  `json:"records_read,omitempty"`
	Validation      string `json:"validation,omitempty"`
}

type inspectRecordResult struct {
	Input        string              `json:"input"`
	RecordNumber int64               `json:"record_number"`
	Record       model.DatasetRecord `json:"record"`
}

type inspectArchivesResult struct {
	Input    string                 `json:"input"`
	Archives []model.DatasetArchive `json:"archives"`
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
		JSON:     cmd.Bool("json"),
		Validate: cmd.Bool("validate"),
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
	var recordsRead int64
	for value, err := range jsonl.DatasetValues(ctx, inputPath) {
		if err != nil {
			return err
		}
		if value.Header {
			dataset = value.Dataset
			summary = datasetInspectSummary(inputPath, dataset)
			if opts.Archives {
				return writeInspectArchives(
					out,
					inspectArchivesResult{Input: inputPath, Archives: dataset.Archives},
					opts.JSON,
				)
			}
			if filter == nil && !opts.Validate {
				return writeInspectSummary(out, summary, opts.JSON)
			}
			continue
		}
		recordsRead++
		if filter != nil && filter(value.Record) {
			return writeInspectRecord(
				out,
				inspectRecordResult{Input: inputPath, RecordNumber: recordsRead, Record: value.Record},
				opts.JSON,
			)
		}
	}
	if filter != nil {
		return errInspectNoMatch
	}
	summary.RecordsRead = recordsRead
	summary.Validation = "ok"
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

func datasetInspectSummary(inputPath string, dataset model.Dataset) inspectSummary {
	var database, dumpDate string
	if dataset.Database != nil {
		database = dataset.Database.ID
		dumpDate = dataset.Database.DumpDate
	}
	var entries, fb2Entries int
	for _, archive := range dataset.Archives {
		entries += archive.Entries
		fb2Entries += archive.FB2Entries
	}
	return inspectSummary{
		Input:           inputPath,
		Schema:          dataset.Schema,
		ID:              dataset.ID,
		RecordSchema:    dataset.RecordSchema,
		Library:         dataset.Library,
		Created:         dataset.Created,
		Records:         dataset.Records,
		Generator:       strings.TrimSpace(dataset.Generator.Name + " " + dataset.Generator.Version),
		Database:        database,
		DumpDate:        dumpDate,
		Archives:        len(dataset.Archives),
		ArchiveEntries:  entries,
		FB2Entries:      fb2Entries,
		Ordering:        dataset.Ordering.Mode,
		ParseFB2:        dataset.Processing.ParseFB2,
		FB2Coverage:     dataset.Processing.FB2Coverage,
		ContentChecksum: dataset.Processing.ArchiveContentChecksum.Algorithm,
	}
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
			"  archives: %d\n"+
			"  archive entries: %d\n"+
			"  fb2 entries: %d\n"+
			"  ordering: %s\n"+
			"  parse fb2: %t\n"+
			"  fb2 coverage: %s\n"+
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
		summary.Archives,
		summary.ArchiveEntries,
		summary.FB2Entries,
		summary.Ordering,
		summary.ParseFB2,
		summary.FB2Coverage,
		summary.ContentChecksum,
	)
	if err != nil {
		return err
	}
	if summary.Validation != "" {
		_, err = fmt.Fprintf(out, "  validation: %s\n  records read: %d\n", summary.Validation, summary.RecordsRead)
	}
	return err
}

func writeInspectRecord(out io.Writer, result inspectRecordResult, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	data, err := jsonstd.MarshalIndent(result.Record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal inspect record: %w", err)
	}
	_, err = fmt.Fprintf(out, "Record\n  input: %s\n  record number: %d\n%s\n", result.Input, result.RecordNumber, data)
	return err
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
