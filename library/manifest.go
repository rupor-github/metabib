package library

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"

	"metabib/config"
	"metabib/db"
	"metabib/jsonl"
	"metabib/model"
)

const (
	archiveManifestSchema  = "metabib.archive_manifest/1"
	databaseManifestSchema = "metabib.database_manifest/1"
	manifestExt            = ".manifest.zst"
)

type ArchiveManifestDecision struct {
	ArchivePath  string
	ManifestPath string
	Use          bool
	Create       bool
	ArchiveMD5   string
	Records      int64
}

type DatabaseManifestDecision struct {
	ManifestPath string
	DumpDir      string
	DumpDate     string
	Dumps        []DumpManifestSource
	Use          bool
	Create       bool
	Records      int64
}

type ManifestReport struct {
	Kind             string
	SourcePath       string
	ManifestPath     string
	Records          int64
	Valid            bool
	Fresh            bool
	Missing          bool
	ChecksumVerified bool
	Reason           string
}

func (r ManifestReport) Ready(allowStale bool) bool {
	return r.Valid && !r.Missing && (r.Fresh || allowStale)
}

type manifestProcessing struct {
	Process            string `json:"process"`
	ParseFB2           bool   `json:"parse_fb2"`
	FB2DescriptionTree bool   `json:"fb2_description_tree"`
	ArchiveContentMD5  bool   `json:"archive_content_md5"`
}

type archiveManifestHeader struct {
	Schema     string                `json:"schema"`
	Source     ArchiveManifestSource `json:"source"`
	Processing manifestProcessing    `json:"processing"`
	Created    string                `json:"created"`
	Records    int64                 `json:"records"`
}

type ArchiveManifestSource struct {
	Path     string `json:"path"`
	Modified string `json:"modified"`
	MD5      string `json:"md5"`
}

type databaseManifestHeader struct {
	Schema     string                 `json:"schema"`
	Source     DatabaseManifestSource `json:"source"`
	Processing manifestProcessing     `json:"processing"`
	Created    string                 `json:"created"`
	Records    int64                  `json:"records"`
}

type DatabaseManifestSource struct {
	DumpDir  string               `json:"dump_dir"`
	DumpDate string               `json:"dump_date,omitempty"`
	Dumps    []DumpManifestSource `json:"dumps"`
}

type DumpManifestSource struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	DumpDate string `json:"dump_date,omitempty"`
	Modified string `json:"modified"`
	MD5      string `json:"md5"`
}

type manifestWriter struct {
	path        string
	tmpRecords  string
	recordsFile *os.File
	recordsBuf  *bufio.Writer
	count       int64
}

type manifestReadCloser struct {
	file    *os.File
	decoder *zstd.Decoder
}

func (r *manifestReadCloser) Read(p []byte) (int, error) {
	return r.decoder.Read(p)
}

func (r *manifestReadCloser) Close() error {
	r.decoder.Close()
	return r.file.Close()
}

func PlanArchives(ctx context.Context, cfg *config.Config, log *zap.Logger) ([]ArchiveManifestDecision, bool, error) {
	archives, err := expandArchives(cfg.Processing.Archives)
	if err != nil {
		return nil, false, err
	}
	decisions := make([]ArchiveManifestDecision, 0, len(archives))
	allReady := len(archives) > 0
	for _, archive := range archives {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		decision := ArchiveManifestDecision{ArchivePath: archive}
		if cfg.Processing.Manifests.Enabled {
			decision, err = planArchiveManifest(ctx, cfg, archive, log)
			if err != nil {
				return nil, false, err
			}
		}
		if !decision.Use {
			allReady = false
		}
		decisions = append(decisions, decision)
	}
	return decisions, allReady, nil
}

func PlanDatabaseManifest(
	ctx context.Context,
	cfg *config.Config,
	dumpDir string,
	dumps []db.DumpFile,
	dumpDate string,
	log *zap.Logger,
) (DatabaseManifestDecision, error) {
	decision := DatabaseManifestDecision{DumpDir: dumpDir, DumpDate: dumpDate}
	if !cfg.Processing.Manifests.Enabled || dumpDir == "" {
		return decision, nil
	}
	start := time.Now()
	manifestPath := databaseManifestPath(cfg, dumpDir)
	decision.ManifestPath = manifestPath
	dumpSources, latest, err := collectDumpSources(ctx, dumps, false)
	if err != nil {
		return decision, err
	}
	decision.Dumps = dumpSources
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return decision, fmt.Errorf("stat database manifest %q: %w", manifestPath, err)
		}
		decision.Create = cfg.Processing.Rebuild
		if log != nil {
			log.Info(
				"Database manifest missing",
				zap.String("manifest", manifestPath),
				zap.Bool("create", decision.Create),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		if decision.Create {
			decision.Dumps, err = dumpSourcesWithMD5(ctx, dumps)
		}
		return decision, err
	}

	fresh := manifestInfo.ModTime().After(latest)
	if !fresh && log != nil {
		log.Warn(
			"Database manifest is older than source dumps",
			zap.String("manifest", manifestPath),
			zap.Time("manifest_modified", manifestInfo.ModTime()),
			zap.Time("latest_dump_modified", latest),
		)
	}
	if fresh && !cfg.Processing.Rebuild {
		header, err := readDatabaseManifestHeader(manifestPath)
		if err != nil {
			return decision, err
		}
		if !databaseManifestLightMatches(header, cfg, dumpDir, dumpDate, dumpSources, true) {
			if log != nil {
				log.Warn("Database manifest does not match current processing inputs", zap.String("manifest", manifestPath))
			}
			return decision, nil
		}
		decision.Use = true
		decision.Records = header.Records
		if log != nil {
			log.Info(
				"Database manifest selected",
				zap.String("manifest", manifestPath),
				zap.Int64("records", decision.Records),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		return decision, nil
	}

	if !cfg.Processing.Rebuild {
		if log != nil {
			log.Info("Database manifest not used", zap.String("manifest", manifestPath), zap.Duration("elapsed", time.Since(start)))
		}
		return decision, nil
	}

	dumpSources, err = dumpSourcesWithMD5(ctx, dumps)
	if err != nil {
		return decision, err
	}
	decision.Dumps = dumpSources
	header, err := readDatabaseManifestHeader(manifestPath)
	if err == nil && databaseManifestMatches(header, cfg, dumpDir, dumpDate, dumpSources) {
		if !fresh {
			header.Source.Dumps = dumpSources
			if err := rewriteManifestHeader(manifestPath, header); err != nil {
				return decision, err
			}
		}
		decision.Use = true
		decision.Records = header.Records
		if log != nil {
			log.Info(
				"Database manifest checksum verified",
				zap.String("manifest", manifestPath),
				zap.Int64("records", decision.Records),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		return decision, nil
	}
	if err != nil && log != nil {
		log.Warn("Database manifest header could not be read; rebuilding", zap.String("manifest", manifestPath), zap.Error(err))
	}
	decision.Create = true
	if log != nil {
		log.Info("Database manifest will be rebuilt", zap.String("manifest", manifestPath), zap.Duration("elapsed", time.Since(start)))
	}
	return decision, nil
}

func CopyManifestRecords(ctx context.Context, manifestPath string, out *jsonl.Writer, log *zap.Logger) (int64, error) {
	start := time.Now()
	records, err := ForEachManifestRecord(ctx, manifestPath, func(rec model.Record) error {
		if out == nil {
			return nil
		}
		return out.Write(rec)
	})
	if err != nil {
		return records, err
	}
	if log != nil {
		log.Info("Manifest records copied", zap.String("manifest", manifestPath), zap.Int64("records", records), zap.Duration("elapsed", time.Since(start)))
	}
	return records, nil
}

func ForEachManifestRecord(ctx context.Context, manifestPath string, handle func(model.Record) error) (int64, error) {
	r, err := openManifestReader(manifestPath)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	dec := json.NewDecoder(r)
	var header json.RawMessage
	if err := dec.Decode(&header); err != nil {
		if err == io.EOF {
			return 0, fmt.Errorf("manifest %q is empty", manifestPath)
		}
		return 0, fmt.Errorf("read manifest header %q: %w", manifestPath, err)
	}
	var records int64
	for {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		var rec model.Record
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			return records, fmt.Errorf("decode manifest record %q: %w", manifestPath, err)
		}
		if handle != nil {
			if err := handle(rec); err != nil {
				return records, err
			}
		}
		records++
	}
	return records, nil
}

func ValidateArchiveManifests(
	ctx context.Context,
	cfg *config.Config,
	checkMD5 bool,
	log *zap.Logger,
) ([]ArchiveManifestDecision, []ManifestReport, error) {
	archives, err := expandArchives(cfg.Processing.Archives)
	if err != nil {
		return nil, nil, err
	}
	decisions := make([]ArchiveManifestDecision, 0, len(archives))
	reports := make([]ManifestReport, 0, len(archives))
	for _, archive := range archives {
		decision, report, err := validateArchiveManifest(ctx, cfg, archive, checkMD5, log)
		if err != nil {
			return nil, nil, err
		}
		decisions = append(decisions, decision)
		reports = append(reports, report)
	}
	return decisions, reports, nil
}

func ValidateDatabaseManifest(
	ctx context.Context,
	cfg *config.Config,
	dumpDir string,
	dumps []db.DumpFile,
	dumpDate string,
	checkMD5 bool,
	log *zap.Logger,
) (DatabaseManifestDecision, ManifestReport, error) {
	decision := DatabaseManifestDecision{DumpDir: dumpDir, DumpDate: dumpDate}
	report := ManifestReport{Kind: "database", SourcePath: dumpDir}
	if !cfg.Processing.Manifests.Enabled {
		report.Reason = "manifests are disabled"
		logManifestReport(log, report)
		return decision, report, nil
	}
	if dumpDir == "" {
		report.Reason = "dump directory not specified"
		logManifestReport(log, report)
		return decision, report, nil
	}

	manifestPath := databaseManifestPath(cfg, dumpDir)
	decision.ManifestPath = manifestPath
	report.ManifestPath = manifestPath
	dumpSources, latest, err := collectDumpSources(ctx, dumps, false)
	if err != nil {
		return decision, report, err
	}
	decision.Dumps = dumpSources
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return decision, report, fmt.Errorf("stat database manifest %q: %w", manifestPath, err)
		}
		report.Missing = true
		report.Reason = "manifest missing"
		logManifestReport(log, report)
		return decision, report, nil
	}

	header, err := readDatabaseManifestHeader(manifestPath)
	if err != nil {
		report.Reason = err.Error()
		logManifestReport(log, report)
		return decision, report, nil
	}
	report.Valid = true
	report.Records = header.Records
	decision.Records = header.Records
	decision.Use = true
	if !databaseManifestLightMatches(header, cfg, dumpDir, dumpDate, dumpSources, false) {
		report.Valid = false
		report.Reason = "manifest does not match current database inputs"
		logManifestReport(log, report)
		return decision, report, nil
	}
	if manifestInfo.ModTime().After(latest) && databaseManifestLightMatches(header, cfg, dumpDir, dumpDate, dumpSources, true) {
		report.Fresh = true
	} else {
		report.Reason = "manifest is older than source dumps or source timestamps changed"
	}
	if checkMD5 {
		dumpSources, err = dumpSourcesWithMD5(ctx, dumps)
		if err != nil {
			return decision, report, err
		}
		decision.Dumps = dumpSources
		if !databaseManifestMatches(header, cfg, dumpDir, dumpDate, dumpSources) {
			report.Valid = false
			report.Reason = "manifest checksum does not match source dumps"
		} else {
			report.ChecksumVerified = true
		}
	}
	logManifestReport(log, report)
	return decision, report, nil
}

func newManifestWriter(path string) (*manifestWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create manifest directory: %w", err)
	}
	tmpRecords := path + ".records.tmp"
	f, err := os.Create(tmpRecords)
	if err != nil {
		return nil, fmt.Errorf("create manifest records %q: %w", tmpRecords, err)
	}
	return &manifestWriter{path: path, tmpRecords: tmpRecords, recordsFile: f, recordsBuf: bufio.NewWriter(f)}, nil
}

func (w *manifestWriter) Write(rec model.Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal manifest record: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.recordsBuf.Write(data); err != nil {
		return fmt.Errorf("write manifest record: %w", err)
	}
	w.count++
	return nil
}

func (w *manifestWriter) Close(header any) error {
	if w == nil {
		return nil
	}
	if w.recordsBuf != nil {
		if err := w.recordsBuf.Flush(); err != nil {
			return fmt.Errorf("flush manifest records %q: %w", w.tmpRecords, err)
		}
	}
	if w.recordsFile != nil {
		if err := w.recordsFile.Close(); err != nil {
			return fmt.Errorf("close manifest records %q: %w", w.tmpRecords, err)
		}
	}

	tmpManifest := w.path + ".tmp"
	out, enc, err := createCompressedManifest(tmpManifest)
	if err != nil {
		return err
	}
	jsonEnc := json.NewEncoder(enc)
	if err := jsonEnc.Encode(header); err != nil {
		enc.Close()
		out.Close()
		return fmt.Errorf("write manifest header %q: %w", tmpManifest, err)
	}
	records, err := os.Open(w.tmpRecords)
	if err != nil {
		out.Close()
		return fmt.Errorf("open manifest records %q: %w", w.tmpRecords, err)
	}
	if _, err := io.Copy(enc, records); err != nil {
		records.Close()
		enc.Close()
		out.Close()
		return fmt.Errorf("write manifest records %q: %w", tmpManifest, err)
	}
	if err := records.Close(); err != nil {
		enc.Close()
		out.Close()
		return fmt.Errorf("close manifest records %q: %w", w.tmpRecords, err)
	}
	if err := enc.Close(); err != nil {
		out.Close()
		return fmt.Errorf("close manifest compressor %q: %w", tmpManifest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close manifest %q: %w", tmpManifest, err)
	}
	if err := os.Rename(tmpManifest, w.path); err != nil {
		return fmt.Errorf("rename manifest %q to %q: %w", tmpManifest, w.path, err)
	}
	if err := os.Remove(w.tmpRecords); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove manifest records %q: %w", w.tmpRecords, err)
	}
	return nil
}

func (w *manifestWriter) Abort() error {
	if w == nil {
		return nil
	}
	if w.recordsFile != nil {
		_ = w.recordsFile.Close()
	}
	if err := os.Remove(w.tmpRecords); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temporary manifest records %q: %w", w.tmpRecords, err)
	}
	return nil
}

func planArchiveManifest(ctx context.Context, cfg *config.Config, archive string, log *zap.Logger) (ArchiveManifestDecision, error) {
	start := time.Now()
	manifestPath := archiveManifestPath(cfg, archive)
	decision := ArchiveManifestDecision{ArchivePath: archive, ManifestPath: manifestPath}
	archiveInfo, err := os.Stat(archive)
	if err != nil {
		return decision, fmt.Errorf("stat archive %q: %w", archive, err)
	}
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return decision, fmt.Errorf("stat archive manifest %q: %w", manifestPath, err)
		}
		decision.Create = cfg.Processing.Rebuild
		if log != nil {
			log.Info(
				"Archive manifest missing",
				zap.String("archive", archive),
				zap.String("manifest", manifestPath),
				zap.Bool("create", decision.Create),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		return decision, nil
	}

	fresh := manifestInfo.ModTime().After(archiveInfo.ModTime())
	if !fresh && log != nil {
		log.Warn(
			"Archive manifest is older than source archive",
			zap.String("archive", archive),
			zap.String("manifest", manifestPath),
			zap.Time("archive_modified", archiveInfo.ModTime()),
			zap.Time("manifest_modified", manifestInfo.ModTime()),
		)
	}
	if fresh && !cfg.Processing.Rebuild {
		header, err := readArchiveManifestHeader(manifestPath)
		if err != nil {
			return decision, err
		}
		if !archiveManifestLightMatches(header, cfg, archive, archiveInfo.ModTime(), true) {
			if log != nil {
				log.Warn("Archive manifest does not match current processing inputs", zap.String("archive", archive), zap.String("manifest", manifestPath))
			}
			return decision, nil
		}
		decision.Use = true
		decision.Records = header.Records
		if log != nil {
			log.Info(
				"Archive manifest selected",
				zap.String("archive", archive),
				zap.String("manifest", manifestPath),
				zap.Int64("records", decision.Records),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		return decision, nil
	}

	if !cfg.Processing.Rebuild {
		if log != nil {
			log.Info(
				"Archive manifest not used",
				zap.String("archive", archive),
				zap.String("manifest", manifestPath),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		return decision, nil
	}

	archiveMD5, err := fileMD5(ctx, archive)
	if err != nil {
		return decision, err
	}
	decision.ArchiveMD5 = archiveMD5
	header, err := readArchiveManifestHeader(manifestPath)
	if err == nil && archiveManifestMatches(header, cfg, archive, archiveMD5) {
		if !fresh {
			header.Source.Modified = archiveInfo.ModTime().Format(time.RFC3339Nano)
			if err := rewriteManifestHeader(manifestPath, header); err != nil {
				return decision, err
			}
		}
		decision.Use = true
		decision.Records = header.Records
		if log != nil {
			log.Info(
				"Archive manifest checksum verified",
				zap.String("archive", archive),
				zap.String("manifest", manifestPath),
				zap.Int64("records", decision.Records),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
		return decision, nil
	}
	if err != nil && log != nil {
		log.Warn("Archive manifest header could not be read; rebuilding", zap.String("manifest", manifestPath), zap.Error(err))
	}
	decision.Create = true
	if log != nil {
		log.Info(
			"Archive manifest will be rebuilt",
			zap.String("archive", archive),
			zap.String("manifest", manifestPath),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return decision, nil
}

func validateArchiveManifest(
	ctx context.Context,
	cfg *config.Config,
	archive string,
	checkMD5 bool,
	log *zap.Logger,
) (ArchiveManifestDecision, ManifestReport, error) {
	manifestPath := archiveManifestPath(cfg, archive)
	decision := ArchiveManifestDecision{ArchivePath: archive, ManifestPath: manifestPath}
	report := ManifestReport{Kind: "archive", SourcePath: archive, ManifestPath: manifestPath}
	if !cfg.Processing.Manifests.Enabled {
		report.Reason = "manifests are disabled"
		logManifestReport(log, report)
		return decision, report, nil
	}
	archiveInfo, err := os.Stat(archive)
	if err != nil {
		return decision, report, fmt.Errorf("stat archive %q: %w", archive, err)
	}
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return decision, report, fmt.Errorf("stat archive manifest %q: %w", manifestPath, err)
		}
		report.Missing = true
		report.Reason = "manifest missing"
		logManifestReport(log, report)
		return decision, report, nil
	}

	header, err := readArchiveManifestHeader(manifestPath)
	if err != nil {
		report.Reason = err.Error()
		logManifestReport(log, report)
		return decision, report, nil
	}
	report.Valid = true
	report.Records = header.Records
	decision.Records = header.Records
	decision.Use = true
	if !archiveManifestLightMatches(header, cfg, archive, archiveInfo.ModTime(), false) {
		report.Valid = false
		report.Reason = "manifest does not match current archive inputs"
		logManifestReport(log, report)
		return decision, report, nil
	}
	if manifestInfo.ModTime().After(archiveInfo.ModTime()) && archiveManifestLightMatches(header, cfg, archive, archiveInfo.ModTime(), true) {
		report.Fresh = true
	} else {
		report.Reason = "manifest is older than source archive or source timestamp changed"
	}
	if checkMD5 {
		archiveMD5, err := fileMD5(ctx, archive)
		if err != nil {
			return decision, report, err
		}
		decision.ArchiveMD5 = archiveMD5
		if !archiveManifestMatches(header, cfg, archive, archiveMD5) {
			report.Valid = false
			report.Reason = "manifest checksum does not match source archive"
		} else {
			report.ChecksumVerified = true
		}
	}
	logManifestReport(log, report)
	return decision, report, nil
}

func logManifestReport(log *zap.Logger, report ManifestReport) {
	if log == nil {
		return
	}
	fields := []zap.Field{
		zap.String("kind", report.Kind),
		zap.String("source", report.SourcePath),
		zap.String("manifest", report.ManifestPath),
		zap.Int64("records", report.Records),
		zap.Bool("valid", report.Valid),
		zap.Bool("fresh", report.Fresh),
		zap.Bool("missing", report.Missing),
		zap.Bool("checksum_verified", report.ChecksumVerified),
	}
	if report.Reason != "" {
		fields = append(fields, zap.String("reason", report.Reason))
	}
	if report.Ready(false) {
		log.Info("Manifest ready", fields...)
		return
	}
	log.Warn("Manifest not ready", fields...)
}

func archiveManifestPath(cfg *config.Config, archive string) string {
	base := strings.TrimSuffix(filepath.Base(archive), filepath.Ext(archive)) + manifestExt
	if cfg.Processing.Manifests.ArchiveDir != "" {
		return filepath.Join(cfg.Processing.Manifests.ArchiveDir, base)
	}
	return filepath.Join(filepath.Dir(archive), base)
}

func databaseManifestPath(cfg *config.Config, dumpDir string) string {
	if cfg.Processing.Manifests.DatabaseDir != "" {
		return filepath.Join(cfg.Processing.Manifests.DatabaseDir, "database"+manifestExt)
	}
	return filepath.Join(dumpDir, "database"+manifestExt)
}

func readArchiveManifestHeader(path string) (archiveManifestHeader, error) {
	var header archiveManifestHeader
	if err := readManifestHeader(path, &header); err != nil {
		return header, err
	}
	if header.Schema != archiveManifestSchema {
		return header, fmt.Errorf("manifest %q has unexpected schema %q", path, header.Schema)
	}
	return header, nil
}

func readDatabaseManifestHeader(path string) (databaseManifestHeader, error) {
	var header databaseManifestHeader
	if err := readManifestHeader(path, &header); err != nil {
		return header, err
	}
	if header.Schema != databaseManifestSchema {
		return header, fmt.Errorf("manifest %q has unexpected schema %q", path, header.Schema)
	}
	return header, nil
}

func openManifestReader(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest %q: %w", path, err)
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("open manifest compressor %q: %w", path, err)
	}
	return &manifestReadCloser{file: f, decoder: dec}, nil
}

func createCompressedManifest(path string) (*os.File, *zstd.Encoder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("create manifest %q: %w", path, err)
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("create manifest compressor %q: %w", path, err)
	}
	return f, enc, nil
}

func readManifestHeader(path string, header any) error {
	r, err := openManifestReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 8*1024*1024)
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return fmt.Errorf("read manifest header %q: %w", path, err)
		}
		return fmt.Errorf("manifest %q is empty", path)
	}
	if err := json.Unmarshal(s.Bytes(), header); err != nil {
		return fmt.Errorf("decode manifest header %q: %w", path, err)
	}
	return nil
}

func rewriteManifestHeader(path string, header any) error {
	r, err := openManifestReader(path)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(r)
	if _, err := reader.ReadBytes('\n'); err != nil {
		r.Close()
		if err == io.EOF {
			return fmt.Errorf("manifest %q has no header line", path)
		}
		return fmt.Errorf("read manifest header %q: %w", path, err)
	}

	tmpPath := path + ".tmp"
	out, enc, err := createCompressedManifest(tmpPath)
	if err != nil {
		r.Close()
		return err
	}
	jsonEnc := json.NewEncoder(enc)
	if err := jsonEnc.Encode(header); err != nil {
		r.Close()
		enc.Close()
		out.Close()
		return fmt.Errorf("write manifest header %q: %w", tmpPath, err)
	}
	if _, err := io.Copy(enc, reader); err != nil {
		r.Close()
		enc.Close()
		out.Close()
		return fmt.Errorf("copy manifest records %q: %w", path, err)
	}
	if err := r.Close(); err != nil {
		enc.Close()
		out.Close()
		return fmt.Errorf("close manifest %q: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		out.Close()
		return fmt.Errorf("close manifest compressor %q: %w", tmpPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close manifest %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename manifest %q to %q: %w", tmpPath, path, err)
	}
	return nil
}

func processingManifest(cfg *config.Config) manifestProcessing {
	return manifestProcessing{
		Process:            cfg.Processing.Process,
		ParseFB2:           cfg.Processing.ParseFB2,
		FB2DescriptionTree: cfg.Processing.FB2DescriptionTree,
		ArchiveContentMD5:  cfg.Processing.ArchiveContentMD5,
	}
}

func archiveManifestMatches(header archiveManifestHeader, cfg *config.Config, archive string, md5sum string) bool {
	return archiveManifestLightMatches(header, cfg, archive, time.Time{}, false) &&
		header.Source.MD5 == md5sum
}

func archiveManifestLightMatches(header archiveManifestHeader, cfg *config.Config, archive string, modified time.Time, compareModified bool) bool {
	if header.Source.Path != archive || header.Processing != processingManifest(cfg) {
		return false
	}
	return !compareModified || header.Source.Modified == modified.Format(time.RFC3339Nano)
}

func databaseManifestMatches(
	header databaseManifestHeader,
	cfg *config.Config,
	dumpDir string,
	dumpDate string,
	dumps []DumpManifestSource,
) bool {
	if header.Source.DumpDir != dumpDir || header.Source.DumpDate != dumpDate || header.Processing != processingManifest(cfg) {
		return false
	}
	if len(header.Source.Dumps) != len(dumps) {
		return false
	}
	for idx := range dumps {
		stored := header.Source.Dumps[idx]
		current := dumps[idx]
		stored.Modified = ""
		current.Modified = ""
		if stored != current {
			return false
		}
	}
	return true
}

func databaseManifestLightMatches(
	header databaseManifestHeader,
	cfg *config.Config,
	dumpDir string,
	dumpDate string,
	dumps []DumpManifestSource,
	compareModified bool,
) bool {
	if header.Source.DumpDir != dumpDir || header.Source.DumpDate != dumpDate || header.Processing != processingManifest(cfg) {
		return false
	}
	if len(header.Source.Dumps) != len(dumps) {
		return false
	}
	for idx := range dumps {
		stored := header.Source.Dumps[idx]
		current := dumps[idx]
		if !compareModified {
			stored.Modified = ""
			current.Modified = ""
		}
		stored.MD5 = ""
		current.MD5 = ""
		if stored != current {
			return false
		}
	}
	return true
}

func archiveManifestHeaderFor(cfg *config.Config, decision ArchiveManifestDecision, records int64) (archiveManifestHeader, error) {
	info, err := os.Stat(decision.ArchivePath)
	if err != nil {
		return archiveManifestHeader{}, fmt.Errorf("stat archive %q: %w", decision.ArchivePath, err)
	}
	return archiveManifestHeader{
		Schema: archiveManifestSchema,
		Source: ArchiveManifestSource{
			Path:     decision.ArchivePath,
			Modified: info.ModTime().Format(time.RFC3339Nano),
			MD5:      decision.ArchiveMD5,
		},
		Processing: processingManifest(cfg),
		Created:    time.Now().Format(time.RFC3339Nano),
		Records:    records,
	}, nil
}

func databaseManifestHeaderFor(cfg *config.Config, decision DatabaseManifestDecision, records int64) databaseManifestHeader {
	return databaseManifestHeader{
		Schema: databaseManifestSchema,
		Source: DatabaseManifestSource{
			DumpDir:  decision.DumpDir,
			DumpDate: decision.DumpDate,
			Dumps:    decision.Dumps,
		},
		Processing: processingManifest(cfg),
		Created:    time.Now().Format(time.RFC3339Nano),
		Records:    records,
	}
}

func collectDumpSources(ctx context.Context, dumps []db.DumpFile, includeMD5 bool) ([]DumpManifestSource, time.Time, error) {
	sources := make([]DumpManifestSource, 0, len(dumps))
	var latest time.Time
	for _, dump := range dumps {
		if err := ctx.Err(); err != nil {
			return nil, latest, err
		}
		info, err := os.Stat(dump.Path)
		if err != nil {
			return nil, latest, fmt.Errorf("stat SQL dump %q: %w", dump.Path, err)
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		source := DumpManifestSource{
			Path:     dump.Path,
			Name:     dump.Name,
			DumpDate: dump.DumpDate,
			Modified: info.ModTime().Format(time.RFC3339Nano),
		}
		if includeMD5 {
			sum, err := fileMD5(ctx, dump.Path)
			if err != nil {
				return nil, latest, err
			}
			source.MD5 = sum
		}
		sources = append(sources, source)
	}
	return sources, latest, nil
}

func dumpSourcesWithMD5(ctx context.Context, dumps []db.DumpFile) ([]DumpManifestSource, error) {
	sources, _, err := collectDumpSources(ctx, dumps, true)
	return sources, err
}

func fileMD5(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for MD5 %q: %w", path, err)
	}
	defer f.Close()
	hash := md5.New()
	if _, err := copyWithBuffer(hash, &contextReader{ctx: ctx, reader: f}, 1024*1024); err != nil {
		return "", fmt.Errorf("calculate MD5 for %q: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
