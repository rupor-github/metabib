package main

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/hex"
	jsonstd "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	cli "github.com/urfave/cli/v3"

	"metabib/config"
	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
	"metabib/state"
)

const (
	traceSchemaV1             = "metabib.trace/1"
	traceArchiveManifestV1    = "metabib.archive_manifest/1"
	traceArchiveManifestV2    = "metabib.archive_manifest/2"
	traceDatabaseManifestV2   = "metabib.database_manifest/2"
	traceManifestExt          = ".manifest.zst"
	traceStatusFound          = "found"
	traceStatusMissing        = "missing"
	traceStatusNotApplicable  = "not_applicable"
	traceStatusError          = "error"
	traceStatusAmbiguous      = "ambiguous"
	traceKindDataset          = "dataset"
	traceKindMergedRecord     = "merged_record"
	traceKindArchive          = "archive"
	traceKindArchiveManifest  = "archive_manifest"
	traceKindDatabaseManifest = "database_manifest"
	traceKindDatabaseDump     = "database_dump"
	traceKindDeepArchive      = "deep_archive"
)

type traceOptions struct {
	Input               string
	BookID              int64
	Archive             string
	Index               int
	File                string
	Issues              bool
	JSON                bool
	Deep                bool
	ArchiveManifest     string
	DatabaseManifest    string
	ArchivePath         string
	ArchiveDir          string
	ArchiveManifestDir  string
	DatabaseManifestDir string
	DatabaseDumpDir     string
	RecordsFull         bool
	Cfg                 *config.Config
}

type traceResult struct {
	Schema  string              `json:"schema"`
	Input   string              `json:"input"`
	Dataset traceDatasetSummary `json:"dataset"`
	Records []traceRecordResult `json:"records"`
}

type traceDatasetSummary struct {
	Schema   string `json:"schema"`
	ID       string `json:"id,omitempty"`
	Library  string `json:"library,omitempty"`
	Records  int64  `json:"records"`
	Archives int    `json:"archives"`
	Database string `json:"database,omitempty"`
}

type traceRecordResult struct {
	Target                 traceTarget          `json:"target"`
	Steps                  []traceStep          `json:"steps"`
	Merged                 traceMergedSummary   `json:"merged"`
	ArchiveManifest        *traceManifestMatch  `json:"archive_manifest,omitempty"`
	DatabaseManifest       *traceManifestMatch  `json:"database_manifest,omitempty"`
	MergedRecord           *model.DatasetRecord `json:"merged_record,omitempty"`
	ArchiveManifestRecord  *model.Record        `json:"archive_manifest_record,omitempty"`
	DatabaseManifestRecord *model.Record        `json:"database_manifest_record,omitempty"`
}

type traceTarget struct {
	Mode    string `json:"mode"`
	BookID  int64  `json:"book_id,omitempty"`
	Archive string `json:"archive,omitempty"`
	Index   *int   `json:"index,omitempty"`
	File    string `json:"file,omitempty"`
	Issues  bool   `json:"issues,omitempty"`
}

type traceStep struct {
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	Path             string `json:"path,omitempty"`
	Message          string `json:"message,omitempty"`
	RecordNumber     int64  `json:"record_number,omitempty"`
	Source           string `json:"source,omitempty"`
	Entry            string `json:"entry,omitempty"`
	Index            *int   `json:"index,omitempty"`
	BookID           *int64 `json:"book_id,omitempty"`
	CompressedSize   uint64 `json:"compressed_size,omitempty"`
	UncompressedSize uint64 `json:"uncompressed_size,omitempty"`
	ExpectedMD5      string `json:"expected_md5,omitempty"`
	ActualMD5        string `json:"actual_md5,omitempty"`
	SidecarsChecked  int    `json:"sidecars_checked,omitempty"`
	SidecarsMissing  int    `json:"sidecars_missing,omitempty"`
	Size             int64  `json:"size,omitempty"`
	Modified         string `json:"modified,omitempty"`
}

type traceMergedSummary struct {
	RecordNumber int64    `json:"record_number"`
	Locator      string   `json:"locator"`
	Artifacts    []string `json:"artifacts,omitempty"`
	BookID       *int64   `json:"book_id,omitempty"`
	Issues       int      `json:"issues,omitempty"`
}

type traceManifestMatch struct {
	Path         string                `json:"path"`
	Header       traceManifestHeader   `json:"header"`
	RecordNumber int64                 `json:"record_number,omitempty"`
	Summary      traceRawRecordSummary `json:"summary,omitempty"`
}

type traceRawRecordSummary struct {
	BookID           int64  `json:"book_id,omitempty"`
	FileName         string `json:"file_name,omitempty"`
	Extension        string `json:"extension,omitempty"`
	ArchivePath      string `json:"archive_path,omitempty"`
	Entry            string `json:"entry,omitempty"`
	Index            *int   `json:"index,omitempty"`
	CompressedSize   uint64 `json:"compressed_size,omitempty"`
	UncompressedSize uint64 `json:"uncompressed_size,omitempty"`
	ContentMD5       string `json:"content_md5,omitempty"`
	DatabasePresent  bool   `json:"database_present,omitempty"`
	FB2Present       bool   `json:"fb2_present,omitempty"`
	Sidecars         int    `json:"sidecars,omitempty"`
	Issues           int    `json:"issues,omitempty"`
}

type traceManifestHeader struct {
	Schema  string              `json:"schema"`
	Source  traceManifestSource `json:"source"`
	Scope   string              `json:"scope,omitempty"`
	Created string              `json:"created,omitempty"`
	Records int64               `json:"records"`
}

type traceManifestSource struct {
	Path     string            `json:"path,omitempty"`
	Modified string            `json:"modified,omitempty"`
	MD5      string            `json:"md5,omitempty"`
	DumpDir  string            `json:"dump_dir,omitempty"`
	DumpDate string            `json:"dump_date,omitempty"`
	Format   string            `json:"database_format,omitempty"`
	Dumps    []traceDumpSource `json:"dumps,omitempty"`
}

type traceDumpSource struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	DumpDate      string `json:"dump_date,omitempty"`
	DumpCompleted string `json:"dump_completed,omitempty"`
	Modified      string `json:"modified,omitempty"`
	MD5           string `json:"md5,omitempty"`
}

type traceCandidate struct {
	RecordNumber int64
	Record       model.DatasetRecord
}

type traceManifestCandidatePath struct {
	Path               string
	RequireSourceMatch bool
}

func traceCommand() *cli.Command {
	return &cli.Command{
		Name:  "trace",
		Usage: "Trace merged dataset records back to cache manifests and source archives",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "input", Aliases: []string{"i"}, Usage: "read merged dataset JSONL using `PREFIX` or exact path", Required: true},
			&cli.Int64Flag{Name: "book-id", Usage: "trace record matching catalog book `ID`"},
			&cli.StringFlag{Name: "archive", Usage: "trace record from archive source `ID`; requires --index"},
			&cli.IntFlag{Name: "index", Value: -1, Usage: "trace record at zero-based archive entry `INDEX`; requires --archive"},
			&cli.StringFlag{Name: "file", Usage: "trace record with artifact or occurrence file `NAME`"},
			&cli.BoolFlag{Name: "issues", Usage: "trace all records with dataset issues"},
			&cli.BoolFlag{Name: "json", Usage: "write stable machine-readable JSON output"},
			&cli.BoolFlag{Name: "deep", Usage: "open source archive and verify ZIP entry, sizes, content MD5, and direct sidecars when supported"},
			&cli.StringFlag{Name: "archive-manifest", Usage: "use explicit archive manifest `FILE`"},
			&cli.StringFlag{Name: "database-manifest", Usage: "use explicit database manifest `FILE`"},
			&cli.StringFlag{Name: "archive-path", Usage: "use explicit source archive `FILE`"},
			&cli.StringFlag{Name: "archive-dir", Usage: "rewrite dataset archive path hints to source archive `DIR` by basename"},
			&cli.StringFlag{Name: "archive-manifest-dir", Usage: "search and derive archive manifests under `DIR`"},
			&cli.StringFlag{Name: "database-manifest-dir", Usage: "search and derive database manifest under `DIR`"},
			&cli.StringFlag{Name: "database-dump-dir", Usage: "rewrite database dump paths to `DIR` by basename"},
			&cli.BoolFlag{Name: "records-full", Usage: "include full merged and raw manifest records in output"},
		},
		Action: runTrace,
	}
}

func runTrace(ctx context.Context, cmd *cli.Command) error {
	err := traceDataset(ctx, traceOptions{
		Input:               cmd.String("input"),
		BookID:              cmd.Int64("book-id"),
		Archive:             cmd.String("archive"),
		Index:               cmd.Int("index"),
		File:                cmd.String("file"),
		Issues:              cmd.Bool("issues"),
		JSON:                cmd.Bool("json"),
		Deep:                cmd.Bool("deep"),
		ArchiveManifest:     cmd.String("archive-manifest"),
		DatabaseManifest:    cmd.String("database-manifest"),
		ArchivePath:         cmd.String("archive-path"),
		ArchiveDir:          cmd.String("archive-dir"),
		ArchiveManifestDir:  cmd.String("archive-manifest-dir"),
		DatabaseManifestDir: cmd.String("database-manifest-dir"),
		DatabaseDumpDir:     cmd.String("database-dump-dir"),
		RecordsFull:         cmd.Bool("records-full"),
		Cfg:                 state.EnvFromContext(ctx).Cfg,
	}, os.Stdout)
	if errors.Is(err, errInspectNoMatch) {
		return cli.Exit(err, inspectNoMatchExitCode)
	}
	return err
}

func traceDataset(ctx context.Context, opts traceOptions, out io.Writer) error {
	if err := validateTraceOptions(opts); err != nil {
		return err
	}
	inputPath, err := inpxutil.DiscoverDatasetInput(opts.Input)
	if err != nil {
		return err
	}
	opts.Input = inputPath
	dataset, matches, err := traceDatasetMatches(ctx, inputPath, opts)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return errInspectNoMatch
	}
	if !opts.Issues && len(matches) > 1 {
		return traceAmbiguousMatchesError(matches)
	}
	result := traceResult{
		Schema:  traceSchemaV1,
		Input:   inputPath,
		Dataset: traceDatasetSummaryFor(dataset),
		Records: make([]traceRecordResult, 0, len(matches)),
	}
	for _, match := range matches {
		result.Records = append(result.Records, traceRecord(ctx, inputPath, dataset, match, opts))
	}
	return writeTraceResult(out, result, opts.JSON, opts.RecordsFull)
}

func validateTraceOptions(opts traceOptions) error {
	if opts.Input == "" {
		return errors.New("trace input is required")
	}
	lookupModes := 0
	if opts.BookID > 0 {
		lookupModes++
	}
	if opts.File != "" {
		lookupModes++
	}
	if opts.Issues {
		lookupModes++
	}
	archiveLookup := opts.Archive != ""
	if archiveLookup {
		lookupModes++
		if opts.Index < 0 {
			return errors.New("trace archive lookup requires both --archive and --index")
		}
	}
	if opts.Archive == "" && opts.Index >= 0 && lookupModes == 0 {
		return errors.New("trace archive lookup requires both --archive and --index")
	}
	if lookupModes != 1 {
		return errors.New("trace requires exactly one lookup mode")
	}
	return nil
}

func traceDatasetMatches(ctx context.Context, inputPath string, opts traceOptions) (model.Dataset, []traceCandidate, error) {
	filter := traceFilter(opts)
	var dataset model.Dataset
	var matches []traceCandidate
	var recordsRead int64
	for value, err := range jsonl.DatasetValues(ctx, inputPath) {
		if err != nil {
			return dataset, nil, err
		}
		if value.Header {
			dataset = value.Dataset
			continue
		}
		recordsRead++
		if filter(value.Record) {
			matches = append(matches, traceCandidate{RecordNumber: recordsRead, Record: value.Record})
		}
	}
	return dataset, matches, nil
}

func traceFilter(opts traceOptions) func(model.DatasetRecord) bool {
	if opts.Issues {
		return func(rec model.DatasetRecord) bool { return len(rec.Issues) > 0 }
	}
	return inspectFilter(inspectOptions{BookID: opts.BookID, Archive: opts.Archive, Index: opts.Index, File: opts.File})
}

func traceDatasetSummaryFor(dataset model.Dataset) traceDatasetSummary {
	database := ""
	if dataset.Database != nil {
		database = dataset.Database.ID
	}
	return traceDatasetSummary{
		Schema:   dataset.Schema,
		ID:       dataset.ID,
		Library:  dataset.Library,
		Records:  dataset.Records,
		Archives: len(dataset.Archives),
		Database: database,
	}
}

func traceAmbiguousMatchesError(matches []traceCandidate) error {
	var b strings.Builder
	fmt.Fprintf(&b, "trace selector matched %d records; narrow selector", len(matches))
	limit := min(len(matches), 20)
	for _, match := range matches[:limit] {
		fmt.Fprintf(&b, "\n  record %d: %s", match.RecordNumber, inspectLocatorString(match.Record.Record.Locator))
		if names := inspectArtifactNames(match.Record); len(names) > 0 {
			fmt.Fprintf(&b, " artifacts=%s", strings.Join(names, ","))
		}
	}
	if len(matches) > limit {
		fmt.Fprintf(&b, "\n  ... %d more", len(matches)-limit)
	}
	return errors.New(b.String())
}

func traceRecord(ctx context.Context, inputPath string, dataset model.Dataset, match traceCandidate, opts traceOptions) traceRecordResult {
	merged := match.Record
	result := traceRecordResult{
		Target: traceTargetFor(opts),
		Merged: traceMergedSummary{
			RecordNumber: match.RecordNumber,
			Locator:      inspectLocatorString(merged.Record.Locator),
			Artifacts:    inspectArtifactNames(merged),
			BookID:       traceMergedBookID(merged),
			Issues:       len(merged.Issues),
		},
		MergedRecord: &merged,
	}
	result.Steps = append(result.Steps,
		traceStep{Kind: traceKindDataset, Status: traceStatusFound, Path: inputPath, Message: "merged dataset loaded"},
		traceStep{Kind: traceKindMergedRecord, Status: traceStatusFound, RecordNumber: match.RecordNumber, Message: result.Merged.Locator},
	)
	archiveID, archiveIndex := traceArchiveLocator(merged)
	archivePath := traceArchivePath(dataset, archiveID, opts.ArchivePath, opts.ArchiveDir)
	if archiveID == "" || archiveIndex == nil {
		result.Steps = append(result.Steps, traceStep{Kind: traceKindArchive, Status: traceStatusNotApplicable, Message: "merged record has no archive locator"})
	} else {
		result.Steps = append(result.Steps, traceFileStep(traceKindArchive, archivePath, "source archive", archiveID))
		result.traceArchiveManifest(ctx, dataset, archiveID, *archiveIndex, archivePath, opts)
		if opts.Deep {
			result.Steps = append(result.Steps, traceDeepArchive(archivePath, merged, result.ArchiveManifestRecord))
		}
	}
	if bookID := traceMergedBookID(merged); bookID != nil {
		result.traceDatabaseManifest(ctx, dataset, *bookID, opts)
	} else {
		result.Steps = append(result.Steps, traceStep{Kind: traceKindDatabaseManifest, Status: traceStatusNotApplicable, Message: "merged record has no final database book ID"})
	}
	return result
}

func traceTargetFor(opts traceOptions) traceTarget {
	switch {
	case opts.Issues:
		return traceTarget{Mode: "issues", Issues: true}
	case opts.BookID > 0:
		return traceTarget{Mode: "book_id", BookID: opts.BookID}
	case opts.File != "":
		return traceTarget{Mode: "file", File: opts.File}
	default:
		index := opts.Index
		return traceTarget{Mode: "archive_index", Archive: opts.Archive, Index: &index}
	}
}

func traceArchiveLocator(rec model.DatasetRecord) (string, *int) {
	locator := rec.Record.Locator
	if locator.Kind == "archive_entry" && locator.Source != "" && locator.Index != nil {
		return locator.Source, locator.Index
	}
	for _, artifact := range rec.Artifacts {
		for _, occurrence := range artifact.Occurrences {
			idx := occurrence.Index
			return occurrence.Archive, &idx
		}
	}
	return "", nil
}

func traceArchivePath(dataset model.Dataset, archiveID string, override string, archiveDir string) string {
	if override != "" {
		return filepath.Clean(override)
	}
	for _, archive := range dataset.Archives {
		if archive.ID == archiveID {
			if archiveDir != "" {
				name := archive.Name
				if name == "" {
					name = pathBase(archive.PathHint)
				}
				if name != "" {
					return filepath.Join(archiveDir, name)
				}
			}
			return archive.PathHint
		}
	}
	return ""
}

func traceMergedBookID(rec model.DatasetRecord) *int64 {
	for _, observation := range rec.Observations {
		if observation.ID == "db" && observation.Locator != nil && observation.Locator.BookID != nil {
			return observation.Locator.BookID
		}
	}
	return nil
}

func (r *traceRecordResult) traceArchiveManifest(
	ctx context.Context,
	dataset model.Dataset,
	archiveID string,
	archiveIndex int,
	archivePath string,
	opts traceOptions,
) {
	manifestPath, header, step := traceFindArchiveManifest(dataset, archivePath, opts)
	r.Steps = append(r.Steps, step)
	if manifestPath == "" || step.Status != traceStatusFound {
		return
	}
	recordNumber, rec, found, err := traceFindManifestRecord(ctx, manifestPath, func(rec model.Record) bool {
		return rec.ID.Archive != nil && rec.ID.Archive.Index == archiveIndex
	})
	if err != nil {
		r.Steps = append(r.Steps, traceStep{Kind: traceKindArchiveManifest, Status: traceStatusError, Path: manifestPath, Message: err.Error()})
		return
	}
	if !found {
		idx := archiveIndex
		r.Steps = append(r.Steps, traceStep{
			Kind: traceKindArchiveManifest, Status: traceStatusMissing, Path: manifestPath, Source: archiveID, Index: &idx,
			Message: "archive manifest record not found",
		})
		return
	}
	r.ArchiveManifest = &traceManifestMatch{Path: manifestPath, Header: header, RecordNumber: recordNumber, Summary: traceRawRecordSummaryFor(rec)}
	r.ArchiveManifestRecord = &rec
	r.Steps = append(r.Steps, traceStep{
		Kind: traceKindArchiveManifest, Status: traceStatusFound, Path: manifestPath, RecordNumber: recordNumber,
		Entry: traceRecordEntry(rec), Index: traceRecordArchiveIndex(rec), Message: "raw archive manifest record found",
	})
}

func (r *traceRecordResult) traceDatabaseManifest(ctx context.Context, dataset model.Dataset, bookID int64, opts traceOptions) {
	manifestPath, header, step := traceFindDatabaseManifest(dataset, opts)
	r.Steps = append(r.Steps, step)
	if manifestPath == "" || step.Status != traceStatusFound {
		return
	}
	recordNumber, rec, found, err := traceFindManifestRecord(ctx, manifestPath, func(rec model.Record) bool {
		return rec.ID.BookID == bookID
	})
	if err != nil {
		r.Steps = append(r.Steps, traceStep{Kind: traceKindDatabaseManifest, Status: traceStatusError, Path: manifestPath, Message: err.Error()})
		return
	}
	if !found {
		r.Steps = append(r.Steps, traceStep{
			Kind: traceKindDatabaseManifest, Status: traceStatusMissing, Path: manifestPath, BookID: &bookID,
			Message: "database manifest record not found",
		})
		return
	}
	r.DatabaseManifest = &traceManifestMatch{Path: manifestPath, Header: header, RecordNumber: recordNumber, Summary: traceRawRecordSummaryFor(rec)}
	r.DatabaseManifestRecord = &rec
	dumpDir := traceDatabaseDumpDir(header, opts)
	if dumpDir != "" {
		r.Steps = append(r.Steps, traceFileStep(traceKindDatabaseDump, dumpDir, "database dump directory", "database"))
	}
	for _, dump := range header.Source.Dumps {
		r.Steps = append(r.Steps, traceFileStep(traceKindDatabaseDump, traceDatabaseDumpPath(dump, dumpDir), "database dump "+dump.Name, dump.Name))
	}
	r.Steps = append(r.Steps, traceStep{
		Kind: traceKindDatabaseManifest, Status: traceStatusFound, Path: manifestPath, RecordNumber: recordNumber,
		BookID: &bookID, Message: "raw database manifest record found",
	})
}

func traceDatabaseDumpDir(header traceManifestHeader, opts traceOptions) string {
	if opts.DatabaseDumpDir != "" {
		return filepath.Clean(opts.DatabaseDumpDir)
	}
	if opts.DatabaseManifestDir != "" {
		if _, err := os.Stat(filepath.Join(opts.DatabaseManifestDir, "database"+traceManifestExt)); err == nil {
			return filepath.Clean(opts.DatabaseManifestDir)
		}
	}
	return header.Source.DumpDir
}

func traceDatabaseDumpPath(dump traceDumpSource, dumpDir string) string {
	if dumpDir == "" || dump.Path == "" {
		return dump.Path
	}
	name := dump.Name
	if name == "" {
		name = pathBase(dump.Path)
	}
	if name == "" {
		return dump.Path
	}
	return filepath.Join(dumpDir, name)
}

func traceFileStep(kind string, path string, label string, source string) traceStep {
	step := traceStep{Kind: kind, Path: path, Source: source}
	if path == "" {
		step.Status = traceStatusMissing
		step.Message = label + " path is unknown"
		return step
	}
	info, err := os.Stat(path)
	if err != nil {
		step.Status = traceStatusMissing
		step.Message = err.Error()
		return step
	}
	step.Status = traceStatusFound
	step.Size = info.Size()
	step.Modified = info.ModTime().Format("2006-01-02T15:04:05Z07:00")
	step.Message = label + " found"
	return step
}

func traceFindArchiveManifest(dataset model.Dataset, archivePath string, opts traceOptions) (string, traceManifestHeader, traceStep) {
	if opts.ArchiveManifest != "" {
		return traceCheckManifestCandidate(opts.ArchiveManifest, traceKindArchiveManifest, archivePath, "", false)
	}
	if archivePath == "" {
		return "", traceManifestHeader{}, traceStep{
			Kind: traceKindArchiveManifest, Status: traceStatusMissing,
			Message: "archive path is unknown; cannot resolve archive manifest safely",
		}
	}
	candidates := traceArchiveManifestCandidates(dataset, archivePath, opts)
	return traceFindManifest(candidates, traceKindArchiveManifest, archivePath, "")
}

func traceFindDatabaseManifest(dataset model.Dataset, opts traceOptions) (string, traceManifestHeader, traceStep) {
	dumpDir := ""
	if dataset.Database != nil {
		dumpDir = dataset.Database.DumpDirHint
	}
	if opts.DatabaseManifest != "" {
		return traceCheckManifestCandidate(opts.DatabaseManifest, traceKindDatabaseManifest, "", dumpDir, false)
	}
	candidates := traceDatabaseManifestCandidates(dataset, opts, dumpDir)
	return traceFindManifest(candidates, traceKindDatabaseManifest, "", dumpDir)
}

func traceFindManifest(candidates []traceManifestCandidatePath, kind string, archivePath string, dumpDir string) (string, traceManifestHeader, traceStep) {
	seen := make(map[string]struct{}, len(candidates))
	var errorsSeen []string
	for _, candidate := range candidates {
		if candidate.Path == "" {
			continue
		}
		path := filepath.Clean(candidate.Path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		path, header, step := traceCheckManifestCandidate(path, kind, archivePath, dumpDir, candidate.RequireSourceMatch)
		if step.Status == traceStatusFound {
			return path, header, step
		}
		if step.Status == traceStatusError {
			errorsSeen = append(errorsSeen, fmt.Sprintf("%s: %s", path, step.Message))
		}
	}
	message := "manifest not found"
	if len(errorsSeen) > 0 {
		message = "manifest not found; errors: " + strings.Join(errorsSeen, "; ")
	}
	return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusMissing, Message: message}
}

func traceCheckManifestCandidate(candidate string, kind string, archivePath string, dumpDir string, requireSourceMatch bool) (string, traceManifestHeader, traceStep) {
	header, err := traceReadManifestHeader(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusMissing, Path: candidate, Message: err.Error()}
		}
		return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusError, Path: candidate, Message: err.Error()}
	}
	if kind == traceKindArchiveManifest {
		if header.Schema != traceArchiveManifestV1 && header.Schema != traceArchiveManifestV2 {
			return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusError, Path: candidate, Message: "unexpected manifest schema " + header.Schema}
		}
		if requireSourceMatch && archivePath != "" && !traceSamePath(header.Source.Path, archivePath) {
			return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusMissing, Path: candidate, Message: "source archive mismatch"}
		}
	} else {
		if header.Schema != traceDatabaseManifestV2 {
			return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusError, Path: candidate, Message: "unexpected manifest schema " + header.Schema}
		}
		if requireSourceMatch && dumpDir != "" && !traceSamePath(header.Source.DumpDir, dumpDir) {
			return "", traceManifestHeader{}, traceStep{Kind: kind, Status: traceStatusMissing, Path: candidate, Message: "source dump directory mismatch"}
		}
	}
	return filepath.Clean(candidate), header, traceStep{Kind: kind, Status: traceStatusFound, Path: filepath.Clean(candidate), Message: "manifest found"}
}

func traceArchiveManifestCandidates(dataset model.Dataset, archivePath string, opts traceOptions) []traceManifestCandidatePath {
	var candidates []traceManifestCandidatePath
	if archivePath != "" {
		base := strings.TrimSuffix(filepath.Base(archivePath), filepath.Ext(archivePath)) + traceManifestExt
		if opts.ArchiveManifestDir != "" {
			candidates = append(candidates, traceManifestCandidatePath{Path: filepath.Join(opts.ArchiveManifestDir, base)})
		}
		if opts.Cfg != nil && opts.Cfg.Processing.Manifests.ArchiveDir != "" {
			candidates = append(candidates, traceManifestCandidatePath{Path: filepath.Join(opts.Cfg.Processing.Manifests.ArchiveDir, base), RequireSourceMatch: true})
		}
		candidates = append(candidates, traceManifestCandidatePath{Path: filepath.Join(filepath.Dir(archivePath), base), RequireSourceMatch: true})
	}
	return append(candidates, traceManifestScanCandidates(traceManifestDirs(dataset, archivePath, "", opts, true), traceArchiveManifestV1, traceArchiveManifestV2)...)
}

func traceDatabaseManifestCandidates(dataset model.Dataset, opts traceOptions, dumpDir string) []traceManifestCandidatePath {
	var candidates []traceManifestCandidatePath
	if opts.DatabaseManifestDir != "" {
		candidates = append(candidates, traceManifestCandidatePath{Path: filepath.Join(opts.DatabaseManifestDir, "database"+traceManifestExt)})
	}
	if opts.Cfg != nil && opts.Cfg.Processing.Manifests.DatabaseDir != "" {
		candidates = append(candidates, traceManifestCandidatePath{Path: filepath.Join(opts.Cfg.Processing.Manifests.DatabaseDir, "database"+traceManifestExt), RequireSourceMatch: true})
	}
	if dumpDir != "" {
		candidates = append(candidates, traceManifestCandidatePath{Path: filepath.Join(dumpDir, "database"+traceManifestExt), RequireSourceMatch: true})
	}
	return append(candidates, traceManifestScanCandidates(traceManifestDirs(dataset, "", dumpDir, opts, false), traceDatabaseManifestV2)...)
}

func traceManifestDirs(dataset model.Dataset, archivePath string, dumpDir string, opts traceOptions, archive bool) []string {
	var dirs []string
	if archive && opts.ArchiveManifestDir != "" {
		dirs = append(dirs, opts.ArchiveManifestDir)
	}
	if !archive && opts.DatabaseManifestDir != "" {
		dirs = append(dirs, opts.DatabaseManifestDir)
	}
	if opts.Cfg != nil {
		if archive && opts.Cfg.Processing.Manifests.ArchiveDir != "" {
			dirs = append(dirs, opts.Cfg.Processing.Manifests.ArchiveDir)
		}
		if !archive && opts.Cfg.Processing.Manifests.DatabaseDir != "" {
			dirs = append(dirs, opts.Cfg.Processing.Manifests.DatabaseDir)
		}
	}
	if archivePath != "" {
		dirs = append(dirs, filepath.Dir(archivePath))
	}
	if dumpDir != "" {
		dirs = append(dirs, dumpDir)
	}
	if opts.Input != "" {
		dirs = append(dirs, filepath.Dir(opts.Input))
	}
	return dirs
}

func traceManifestScanCandidates(dirs []string, schemas ...string) []traceManifestCandidatePath {
	var candidates []traceManifestCandidatePath
	seenDirs := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		if _, ok := seenDirs[dir]; ok {
			continue
		}
		seenDirs[dir] = struct{}{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), traceManifestExt) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			header, err := traceReadManifestHeader(path)
			if err != nil || !slices.Contains(schemas, header.Schema) {
				continue
			}
			candidates = append(candidates, traceManifestCandidatePath{Path: path, RequireSourceMatch: true})
		}
	}
	return candidates
}

func traceReadManifestHeader(path string) (traceManifestHeader, error) {
	r, err := jsonl.OpenCompressedFile(path)
	if err != nil {
		return traceManifestHeader{}, err
	}
	defer r.Close()
	dec := jsontext.NewDecoder(r)
	var header traceManifestHeader
	if err := jsonv2.UnmarshalDecode(dec, &header); err != nil {
		if err == io.EOF {
			return traceManifestHeader{}, fmt.Errorf("manifest %q is empty", path)
		}
		return traceManifestHeader{}, fmt.Errorf("read manifest header %q: %w", path, err)
	}
	if header.Records < 0 {
		return traceManifestHeader{}, fmt.Errorf("manifest %q declares negative record count %d", path, header.Records)
	}
	return header, nil
}

func traceFindManifestRecord(ctx context.Context, path string, match func(model.Record) bool) (int64, model.Record, bool, error) {
	r, err := jsonl.OpenCompressedFile(path)
	if err != nil {
		return 0, model.Record{}, false, err
	}
	defer r.Close()
	dec := jsontext.NewDecoder(r)
	var header traceManifestHeader
	if err := jsonv2.UnmarshalDecode(dec, &header); err != nil {
		return 0, model.Record{}, false, fmt.Errorf("read manifest header %q: %w", path, err)
	}
	var records int64
	for records < header.Records {
		if err := ctx.Err(); err != nil {
			return 0, model.Record{}, false, err
		}
		var rec model.Record
		if err := jsonv2.UnmarshalDecode(dec, &rec); err != nil {
			return 0, model.Record{}, false, fmt.Errorf("decode manifest record %q: %w", path, err)
		}
		records++
		if match(rec) {
			return records, rec, true, nil
		}
	}
	return 0, model.Record{}, false, nil
}

func traceRawRecordSummaryFor(rec model.Record) traceRawRecordSummary {
	summary := traceRawRecordSummary{
		BookID:          rec.ID.BookID,
		FileName:        rec.ID.FileName,
		Extension:       rec.ID.Extension,
		DatabasePresent: rec.Source.Database.Present,
		FB2Present:      rec.Source.FB2.Present,
		Sidecars:        len(rec.Source.Sidecars),
		Issues:          len(rec.Issues),
	}
	if rec.ID.Archive != nil {
		idx := rec.ID.Archive.Index
		summary.ArchivePath = rec.ID.Archive.Path
		summary.Entry = rec.ID.Archive.Entry
		summary.Index = &idx
		summary.CompressedSize = rec.ID.Archive.CompressedSize
		summary.UncompressedSize = rec.ID.Archive.UncompressedSize
		summary.ContentMD5 = rec.ID.Archive.ContentMD5
	}
	return summary
}

func traceRecordEntry(rec model.Record) string {
	if rec.ID.Archive == nil {
		return ""
	}
	return rec.ID.Archive.Entry
}

func traceRecordArchiveIndex(rec model.Record) *int {
	if rec.ID.Archive == nil {
		return nil
	}
	idx := rec.ID.Archive.Index
	return &idx
}

func traceDeepArchive(archivePath string, rec model.DatasetRecord, raw *model.Record) traceStep {
	step := traceStep{Kind: traceKindDeepArchive, Path: archivePath}
	if archivePath == "" {
		step.Status = traceStatusMissing
		step.Message = "source archive path is unknown"
		return step
	}
	entry, index, ok := tracePrimaryOccurrence(rec)
	if !ok {
		step.Status = traceStatusNotApplicable
		step.Message = "merged record has no archive occurrence"
		return step
	}
	step.Entry = entry.Entry
	step.Index = &index
	step.UncompressedSize = entry.UncompressedSize
	step.CompressedSize = entry.CompressedSize
	if !strings.EqualFold(filepath.Ext(archivePath), ".zip") {
		step.Status = traceStatusNotApplicable
		step.Message = "deep archive verification currently supports zip archives only"
		return step
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		step.Status = traceStatusError
		step.Message = err.Error()
		return step
	}
	defer zr.Close()
	if index < 0 || index >= len(zr.File) {
		step.Status = traceStatusMissing
		step.Message = "archive index outside zip entry list"
		return step
	}
	zf := zr.File[index]
	if zf.Name != entry.Entry {
		for _, candidate := range zr.File {
			if candidate.Name == entry.Entry {
				zf = candidate
				break
			}
		}
	}
	if zf.Name != entry.Entry {
		step.Status = traceStatusMissing
		step.Message = "archive entry not found"
		return step
	}
	step.SidecarsChecked, step.SidecarsMissing = traceVerifyDirectZipSidecars(zr, raw)
	step.CompressedSize = uint64(zf.CompressedSize64)
	step.UncompressedSize = uint64(zf.UncompressedSize64)
	if entry.CompressedSize > 0 && uint64(zf.CompressedSize64) != entry.CompressedSize {
		step.Status = traceStatusError
		step.Message = fmt.Sprintf("compressed size mismatch: manifest %d, archive %d", entry.CompressedSize, zf.CompressedSize64)
		return step
	}
	if entry.UncompressedSize > 0 && uint64(zf.UncompressedSize64) != entry.UncompressedSize {
		step.Status = traceStatusError
		step.Message = fmt.Sprintf("uncompressed size mismatch: manifest %d, archive %d", entry.UncompressedSize, zf.UncompressedSize64)
		return step
	}
	if expected := traceContentMD5(rec); expected != "" {
		step.ExpectedMD5 = expected
		actual, err := traceZipEntryMD5(zf)
		if err != nil {
			step.Status = traceStatusError
			step.Message = err.Error()
			return step
		}
		step.ActualMD5 = actual
		if actual != expected {
			step.Status = traceStatusError
			step.Message = "content MD5 mismatch"
			return step
		}
	}
	step.Status = traceStatusFound
	step.Message = "archive entry verified"
	if step.SidecarsChecked > 0 && step.SidecarsMissing > 0 {
		step.Status = traceStatusError
		step.Message = fmt.Sprintf("archive entry verified; %d sidecar entries missing", step.SidecarsMissing)
	}
	return step
}

func traceVerifyDirectZipSidecars(zr *zip.ReadCloser, raw *model.Record) (int, int) {
	if raw == nil || len(raw.Source.Sidecars) == 0 {
		return 0, 0
	}
	entries := make(map[string]struct{}, len(zr.File))
	for _, file := range zr.File {
		entries[file.Name] = struct{}{}
	}
	var checked, missing int
	for _, sidecar := range raw.Source.Sidecars {
		if !sidecar.Present || sidecar.Entry == "" {
			continue
		}
		checked++
		if _, ok := entries[sidecar.Entry]; !ok {
			missing++
		}
	}
	return checked, missing
}

func tracePrimaryOccurrence(rec model.DatasetRecord) (model.Occurrence, int, bool) {
	for _, artifact := range rec.Artifacts {
		for _, occurrence := range artifact.Occurrences {
			return occurrence, occurrence.Index, true
		}
	}
	return model.Occurrence{}, 0, false
}

func traceContentMD5(rec model.DatasetRecord) string {
	for _, artifact := range rec.Artifacts {
		for _, checksum := range artifact.Checksums {
			if checksum.Algorithm == "md5" && checksum.Scope == "content" && checksum.Value != "" {
				return checksum.Value
			}
		}
	}
	return ""
}

func traceZipEntryMD5(file *zip.File) (string, error) {
	r, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open archive entry %q: %w", file.Name, err)
	}
	defer r.Close()
	h := md5.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("read archive entry %q: %w", file.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func traceSamePath(left string, right string) bool {
	if left == "" || right == "" {
		return left == right
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func writeTraceResult(out io.Writer, result traceResult, jsonOutput bool, recordsFull bool) error {
	if !recordsFull {
		for idx := range result.Records {
			result.Records[idx].MergedRecord = nil
			result.Records[idx].ArchiveManifestRecord = nil
			result.Records[idx].DatabaseManifestRecord = nil
		}
	}
	if jsonOutput {
		return writeJSON(out, result)
	}
	style := terminalOutputStyle(out)
	if _, err := fmt.Fprintf(
		out,
		"%s\n  %s %s\n  %s %s records=%d archives=%d database=%s\n",
		style.header("Trace"),
		style.label("input:"),
		result.Input,
		style.label("dataset:"),
		result.Dataset.ID,
		result.Dataset.Records,
		result.Dataset.Archives,
		result.Dataset.Database,
	); err != nil {
		return err
	}
	for idx, rec := range result.Records {
		if err := writeTraceRecord(out, idx+1, rec, recordsFull, style); err != nil {
			return err
		}
	}
	return nil
}

func writeTraceRecord(out io.Writer, ordinal int, rec traceRecordResult, recordsFull bool, style terminalStyle) error {
	if _, err := fmt.Fprintf(
		out,
		"\n%s\n  %s %s\n  %s record=%d %s\n",
		style.header(fmt.Sprintf("Record %d", ordinal)),
		style.label("target:"),
		traceTargetString(rec.Target),
		style.label("merged:"),
		rec.Merged.RecordNumber,
		rec.Merged.Locator,
	); err != nil {
		return err
	}
	if rec.Merged.BookID != nil {
		if _, err := fmt.Fprintf(out, "  %s %d\n", style.label("database book:"), *rec.Merged.BookID); err != nil {
			return err
		}
	}
	if len(rec.Merged.Artifacts) > 0 {
		if _, err := fmt.Fprintf(out, "  %s %s\n", style.label("artifacts:"), strings.Join(rec.Merged.Artifacts, ", ")); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "\n%s\n", style.header("Steps")); err != nil {
		return err
	}
	for _, step := range rec.Steps {
		if _, err := fmt.Fprintf(out, "  %-18s %s", step.Kind, style.status(step.Status)); err != nil {
			return err
		}
		if step.Path != "" {
			if _, err := fmt.Fprintf(out, "  %s", style.path(step.Path)); err != nil {
				return err
			}
		}
		if step.RecordNumber > 0 {
			if _, err := fmt.Fprintf(out, "  record=%d", step.RecordNumber); err != nil {
				return err
			}
		}
		if step.Entry != "" {
			if _, err := fmt.Fprintf(out, "  entry=%s", step.Entry); err != nil {
				return err
			}
		}
		if step.BookID != nil {
			if _, err := fmt.Fprintf(out, "  book_id=%d", *step.BookID); err != nil {
				return err
			}
		}
		if step.Message != "" {
			if _, err := fmt.Fprintf(out, "  %s", step.Message); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	if recordsFull {
		if err := writeTraceJSONBlock(out, "merged dataset record", rec.MergedRecord, style); err != nil {
			return err
		}
		if err := writeTraceJSONBlock(out, "archive manifest record", rec.ArchiveManifestRecord, style); err != nil {
			return err
		}
		if err := writeTraceJSONBlock(out, "database manifest record", rec.DatabaseManifestRecord, style); err != nil {
			return err
		}
	}
	return nil
}

func traceTargetString(target traceTarget) string {
	switch target.Mode {
	case "book_id":
		return "book_id=" + strconv.FormatInt(target.BookID, 10)
	case "file":
		return "file=" + target.File
	case "issues":
		return "issues"
	default:
		index := ""
		if target.Index != nil {
			index = strconv.Itoa(*target.Index)
		}
		return "archive=" + target.Archive + " index=" + index
	}
}

func writeTraceJSONBlock(out io.Writer, title string, value any, style terminalStyle) error {
	if value == nil {
		return nil
	}
	data, err := jsonstd.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal%s: %w", title, err)
	}
	_, err = fmt.Fprintf(out, "\n%s\n%s\n", style.header("--- "+title+" ---"), style.json(data))
	return err
}
