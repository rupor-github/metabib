package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	jsonv2 "encoding/json/v2"
	mysql "github.com/go-sql-driver/mysql"
	cli "github.com/urfave/cli/v3"
	"go.uber.org/zap"

	"metabib/config"
	"metabib/db"
	"metabib/fetch"
	"metabib/inpx"
	"metabib/internal/fileutil"
	"metabib/jsonl"
	"metabib/library"
	"metabib/misc"
	"metabib/model"
	"metabib/rollup"
	"metabib/state"
)

const staleManifestExitCode = 3

var errManifestNotReady = errors.New("one or more cache manifests are missing, stale, or invalid")

var errWasHandled bool

type databaseIndex struct {
	byID   map[int64]model.DatabaseSource
	byFile map[string]model.DatabaseSource
}

func initializeAppContext(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	cfg, err := config.LoadConfiguration(cmd.String("config"))
	if err != nil {
		return ctx, fmt.Errorf("unable to prepare configuration: %w", err)
	}
	env := state.EnvFromContext(ctx)
	env.Cfg = cfg
	env.Verbose = cmd.Bool("verbose")
	if env.Log, env.LogIO, err = cfg.Logging.Prepare(misc.GetAppName()); err != nil {
		return ctx, fmt.Errorf("unable to prepare logs: %w", err)
	}
	mysql.SetLogger(mysqlDebugLogger{log: env.Log.Named("mysql")})
	env.Log.Debug("Program started", zap.Strings("args", os.Args), zap.String("version", misc.GetVersion()), zap.String("runtime", runtime.Version()), zap.String("git", misc.GetGitHash()))
	return ctx, nil
}

type mysqlDebugLogger struct {
	log *zap.Logger
}

func (l mysqlDebugLogger) Print(v ...any) {
	if l.log != nil {
		l.log.Debug(fmt.Sprint(v...))
	}
}

func destroyAppContext(ctx context.Context, cmd *cli.Command) error {
	env := state.EnvFromContext(ctx)
	if env.Log != nil {
		env.Log.Debug("Program ended", zap.Duration("elapsed", env.Uptime()), zap.Strings("parsed args", cmd.Args().Slice()))
	}
	return env.Close()
}

func exitErrHandler(ctx context.Context, _ *cli.Command, err error) {
	if err == nil {
		return
	}
	if _, ok := err.(fetchExitCode); ok {
		return
	}
	if _, ok := err.(rollupExitCode); ok {
		return
	}
	env := state.EnvFromContext(ctx)
	if env.Log != nil {
		env.Log.Error("Program ended with error", zap.Error(err))
		errWasHandled = true
	}
}

func main() {
	ctx, stop := signal.NotifyContext(state.ContextWithEnv(context.Background()), os.Interrupt, syscall.SIGTERM)
	app := &cli.Command{
		Name:            misc.GetAppName(),
		Usage:           "extract Flibusta/FB2 metadata into JSONL",
		Version:         misc.GetVersion() + " (" + runtime.Version() + ") : " + misc.GetGitHash(),
		HideHelpCommand: true,
		ExitErrHandler:  exitErrHandler,
		Before:          initializeAppContext,
		After:           destroyAppContext,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, DefaultText: "", Usage: "load configuration from `FILE` (YAML)"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"V"}, Usage: "enable detailed progress reporting"},
		},
		Commands: []*cli.Command{
			fetchCommand(),
			rollupCommand(),
			cacheCommand(),
			mergeCommand(),
			inpxCommand(),
			{
				Name:      "dumpconfig",
				Usage:     "Dumps default or actual configuration (YAML)",
				ArgsUsage: "DESTINATION",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "default", Usage: "output default embedded configuration"},
				},
				Action: outputConfiguration,
			},
		},
	}

	var err error
	defer func() {
		stop()
		if err != nil {
			if !errWasHandled {
				fmt.Fprintf(os.Stderr, "Program ended with error: %v\n", err)
			}
			if exitErr, ok := err.(interface{ ExitCode() int }); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
	}()
	err = app.Run(ctx, os.Args)
}

func fetchCommand() *cli.Command {
	return &cli.Command{
		Name:  "fetch",
		Usage: "Download new daily archives and SQL dumps from a configured library",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "library", Aliases: []string{"l"}, Value: "flibusta", Usage: "fetch profile `NAME` from configuration"},
			&cli.IntFlag{Name: "retry", Value: 3, Usage: "number of download attempts"},
			&cli.IntFlag{Name: "timeout", Value: 20, Usage: "per-request timeout in seconds"},
			&cli.IntFlag{Name: "chunksize", Value: 10, Usage: "download chunk size in megabytes"},
			&cli.BoolFlag{Name: "nosql", Usage: "do not download SQL dumps"},
			&cli.BoolFlag{Name: "sticky", Usage: "ignore HTTP redirects and keep using the original host"},
			&cli.BoolFlag{Name: "continue", Usage: "continue partially downloaded files when the server supports ranges"},
			&cli.StringFlag{Name: "to", Aliases: []string{"o"}, Usage: "destination directory for daily archive ZIP files", Required: true},
			&cli.StringFlag{Name: "tosql", Usage: "destination directory for SQL dump files"},
		},
		Action: runFetch,
	}
}

func rollupCommand() *cli.Command {
	return &cli.Command{
		Name:  "rollup",
		Usage: "Roll daily FB2 update archives into size-bounded archive ZIPs",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "archives", Aliases: []string{"a"}, Usage: "directory for finalized fb2-*.zip archives and active .merging archive", Required: true},
			&cli.StringSliceFlag{Name: "updates", Aliases: []string{"u"}, Usage: "directory containing daily update ZIPs; can be repeated; defaults to --archives"},
			&cli.IntFlag{Name: "size", Value: 2000, Usage: "finalized archive target size in decimal megabytes"},
			&cli.BoolFlag{Name: "keep-updates", Usage: "keep consumed daily update archives"},
		},
		Action: runRollup,
	}
}

func inpxCommand() *cli.Command {
	return &cli.Command{
		Name:  "mhl-inpx",
		Usage: "Build MyHomeLib-compatible INPX from merged JSONL parts",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "input", Aliases: []string{"i"}, Usage: "read merged JSONL parts and metadata using `PREFIX`", Required: true},
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "write INPX using `PREFIX` plus dump date", Required: true},
			&cli.StringFlag{Name: "format", Value: string(inpx.Format2X), Usage: "INPX format `MODE` (2x, ruks)"},
			&cli.StringFlag{Name: "sequence", Value: string(inpx.SequenceAuthor), Usage: "sequence selection `MODE` (author, publisher, ignore)"},
			&cli.StringFlag{Name: "prefer-fb2", Value: string(inpx.PreferComplement), Usage: "FB2 sequence preference `MODE` (ignore, merge, complement, replace)"},
		},
		Action: runINPX,
	}
}

func cacheCommand() *cli.Command {
	return &cli.Command{
		Name:  "cache",
		Usage: "Build database and archive manifests without writing final JSONL",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "database-dumps", Usage: "directory containing SQL dumps for database manifest"},
			&cli.StringSliceFlag{Name: "archives", Aliases: []string{"a"}, Usage: "archive file or directory with archives; can be repeated"},
			&cli.BoolFlag{Name: "allow-dump-date-mismatch", Usage: "allow SQL dump files to report different dump dates"},
			&cli.BoolFlag{Name: "check-md5", Usage: "verify source MD5 checksums recorded in existing manifests"},
			&cli.BoolFlag{Name: "rebuild", Usage: "rebuild stale or invalid existing manifests after checksum verification"},
			&cli.BoolFlag{Name: "no-import", Usage: "skip SQL dump import and use existing database"},
			&cli.BoolFlag{Name: "db-overwrite", Usage: "overwrite managed data directory and drop database before import"},
		},
		Action: runCache,
	}
}

func mergeCommand() *cli.Command {
	return &cli.Command{
		Name:  "merge",
		Usage: "Write final JSONL from existing manifests only",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "database-dumps", Usage: "directory containing SQL dumps for database manifest validation"},
			&cli.StringSliceFlag{Name: "archives", Aliases: []string{"a"}, Usage: "archive file or directory with archives; can be repeated"},
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "write range-named JSONL files using `PREFIX`", Required: true},
			&cli.StringFlag{Name: "output-compression", Value: string(jsonl.CompressionZstd), Usage: "compress JSONL output as `MODE` (zstd, gz, zip, none)"},
			&cli.StringFlag{Name: "output-part-size", Usage: "split JSONL into range-named parts of approximate `SIZE` (supports k, m, g)"},
			&cli.BoolFlag{Name: "check-md5", Usage: "verify source MD5 checksums recorded in manifests"},
			&cli.BoolFlag{Name: "allow-stale", Usage: "warn but continue when manifests are stale"},
		},
		Action: runMerge,
	}
}

func runFetch(ctx context.Context, cmd *cli.Command) error {
	cfg := state.EnvFromContext(ctx).Cfg
	env := state.EnvFromContext(ctx)
	library, ok := cfg.Fetch.FindLibrary(cmd.String("library"))
	if !ok {
		return fmt.Errorf("unable to find fetch profile %q", cmd.String("library"))
	}
	res, err := fetch.Run(ctx, fetch.Options{
		Library:       library,
		ArchiveDir:    cmd.String("to"),
		SQLDir:        cmd.String("tosql"),
		DownloadSQL:   !cmd.Bool("nosql"),
		Retry:         cmd.Int("retry"),
		Timeout:       time.Duration(cmd.Int("timeout")) * time.Second,
		ChunkSize:     int64(cmd.Int("chunksize")) * 1024 * 1024,
		Continue:      cmd.Bool("continue"),
		Sticky:        cmd.Bool("sticky"),
		Verbose:       env.Verbose,
		Log:           env.Log,
		UserAgentName: misc.GetAppName(),
	})
	if err != nil {
		return err
	}
	if env.Log != nil {
		env.Log.Info(
			"Fetch completed",
			zap.String("library", res.LibraryName),
			zap.Int("last_book_id", res.LastBookID),
			zap.Int("archives", res.Archives),
			zap.Int("sql_tables", res.SQLTables),
			zap.String("sql_directory", res.SQLDir),
		)
	}
	if res.Archives > 0 {
		return fetchExitCode(fetch.NewArchivesExitCode)
	}
	return nil
}

type fetchExitCode int

func (c fetchExitCode) Error() string {
	return fmt.Sprintf("fetch completed with exit code %d", c)
}

func (c fetchExitCode) ExitCode() int {
	return int(c)
}

func runRollup(ctx context.Context, cmd *cli.Command) error {
	env := state.EnvFromContext(ctx)
	res, err := rollup.Run(ctx, rollup.Options{
		ArchiveDir:  cmd.String("archives"),
		UpdateDirs:  cmd.StringSlice("updates"),
		SizeBytes:   int64(cmd.Int("size")) * 1000 * 1000,
		KeepUpdates: cmd.Bool("keep-updates"),
		Log:         env.Log,
	})
	if err != nil {
		return err
	}
	if env.Log != nil {
		env.Log.Info(
			"Rollup completed",
			zap.Int("updates", res.Updates),
			zap.Int("finalized", res.Finalized),
			zap.String("active_merge", res.ActiveMerge),
			zap.Strings("finalized_archives", res.FinalizedArchives),
		)
	}
	if res.Finalized > 0 {
		return rollupExitCode(rollup.NewArchiveExitCode)
	}
	return nil
}

type rollupExitCode int

func (c rollupExitCode) Error() string {
	return fmt.Sprintf("rollup completed with exit code %d", c)
}

func (c rollupExitCode) ExitCode() int {
	return int(c)
}

func runCache(ctx context.Context, cmd *cli.Command) error {
	cfg := state.EnvFromContext(ctx).Cfg
	applyCacheOverrides(cfg, cmd)
	env := state.EnvFromContext(ctx)

	dumpDir := cmd.String("database-dumps")
	selectedDatabase := dumpDir != ""
	archives := cmd.StringSlice("archives")
	selectedArchives := len(archives) > 0
	importDumps := !cmd.Bool("no-import")
	overwriteDB := cmd.Bool("db-overwrite")
	if !selectedDatabase && !selectedArchives {
		return errors.New("nothing to cache: specify --database-dumps, --archives, or both")
	}
	var dumps []db.DumpFile
	var dumpDate string
	if selectedDatabase {
		discoverStart := time.Now()
		var err error
		dumps, dumpDate, err = db.DiscoverDumps(dumpDir, cmd.Bool("allow-dump-date-mismatch"))
		if err != nil {
			return err
		}
		if dumpDirDatesDiffer(dumps) && env.Log != nil {
			env.Log.Warn("SQL dump dates differ; top-level dump_date will be omitted", zap.String("directory", dumpDir))
		}
		if env.Log != nil {
			env.Log.Info("SQL dump date detected", zap.String("dump_date", dumpDate), zap.String("directory", dumpDir), zap.Duration("elapsed", time.Since(discoverStart)))
		}
	}

	var reports []library.ManifestReport
	checkMD5 := cmd.Bool("check-md5")
	if selectedArchives {
		archivePlan, _, err := library.PlanArchives(ctx, cfg, archives, checkMD5, env.Log)
		if err != nil {
			return err
		}
		if err := library.BuildArchiveManifests(ctx, cfg, env.Log, env.Verbose, archivePlan); err != nil {
			return err
		}
		_, archiveReports, err := library.ValidateArchiveManifests(ctx, cfg, archives, checkMD5, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, archiveReports...)
	}

	if selectedDatabase {
		databaseManifest, err := library.PlanDatabaseManifest(ctx, cfg, dumpDir, dumps, dumpDate, checkMD5, env.Log)
		if err != nil {
			return err
		}
		if !databaseManifest.Use && databaseManifest.Create {
			var logOut io.Writer = os.Stderr
			if env.LogIO != nil {
				logOut = env.LogIO
			}
			runtime, err := db.PrepareRuntime(ctx, cfg.Database, importDumps, overwriteDB, env.Log, logOut)
			if err != nil {
				return err
			}
			defer runtime.Close()
			cfg.Database = runtime.Config

			if importDumps {
				dropBeforeImport := shouldDropDatabaseBeforeImport(overwriteDB, runtime.Managed(), env.Log)
				importer := db.NewImporter(cfg.Database, runtime.Client, env.Log, logOut, env.Verbose, true, dropBeforeImport)
				if err := importer.PrepareDatabase(ctx); err != nil {
					return err
				}
				if err := importer.ImportDumps(ctx, dumps); err != nil {
					return err
				}
			}

			repo, err := db.Open(ctx, cfg.Database)
			if err != nil {
				return err
			}
			defer repo.Close()
			if err := library.ProcessDatabase(ctx, repo, cfg, nil, env.Log, env.Verbose, databaseManifest); err != nil {
				return err
			}
		}
		_, report, err := library.ValidateDatabaseManifest(ctx, cfg, dumpDir, dumps, dumpDate, checkMD5, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}
	return failIfReportsNotReady(reports, false)
}

func shouldDropDatabaseBeforeImport(overwriteDB bool, managed bool, log *zap.Logger) bool {
	if !overwriteDB {
		return false
	}
	if managed {
		return true
	}
	if log != nil {
		log.Warn("Ignoring --db-overwrite database drop for external database runtime")
	}
	return false
}

func runMerge(ctx context.Context, cmd *cli.Command) error {
	cfg := state.EnvFromContext(ctx).Cfg
	env := state.EnvFromContext(ctx)

	dumpDir := cmd.String("database-dumps")
	selectedDatabase := dumpDir != ""
	archives := cmd.StringSlice("archives")
	selectedArchives := len(archives) > 0
	if !selectedDatabase && !selectedArchives {
		return errors.New("nothing to merge: specify --database-dumps, --archives, or both")
	}
	allowStale := cmd.Bool("allow-stale")
	checkMD5 := cmd.Bool("check-md5")
	var reports []library.ManifestReport
	var databaseManifest library.DatabaseManifestDecision
	if selectedDatabase {
		dumps, dumpDate, err := db.DiscoverDumps(dumpDir, true)
		if err != nil {
			return err
		}
		if dumpDirDatesDiffer(dumps) && env.Log != nil {
			env.Log.Warn("SQL dump dates differ; top-level dump_date is omitted", zap.String("directory", dumpDir))
		}
		var report library.ManifestReport
		databaseManifest, report, err = library.ValidateDatabaseManifest(ctx, cfg, dumpDir, dumps, dumpDate, checkMD5, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}

	var archivePlan []library.ArchiveManifestDecision
	if selectedArchives {
		var archiveReports []library.ManifestReport
		var err error
		archivePlan, archiveReports, err = library.ValidateArchiveManifests(ctx, cfg, archives, checkMD5, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, archiveReports...)
	}
	if err := failIfReportsNotReady(reports, allowStale); err != nil {
		return err
	}

	outputPrefix := cmd.String("output")
	compressionValue := cmd.String("output-compression")
	compression, err := jsonl.ParseCompression(compressionValue)
	if err != nil {
		return err
	}
	meta, err := library.MergeMetadataFor(ctx, cfg.Database.Name, databaseManifest, archivePlan, string(compression))
	if err != nil {
		return err
	}
	if err := writeMergeMetadata(outputPrefix, compression, meta, env.Log); err != nil {
		return err
	}
	return writeOutput(ctx, outputPrefix, cmd.String("output-part-size"), compressionValue, env.Log, func(out *jsonl.Writer) error {
		if selectedArchives {
			dbIndex, err := loadDatabaseIndex(ctx, databaseManifest.ManifestPath, env.Log)
			if err != nil {
				return err
			}
			return mergeArchiveManifests(ctx, archivePlan, dbIndex, out, env.Log)
		}
		_, err := library.CopyManifestRecords(ctx, databaseManifest.ManifestPath, out, env.Log)
		return err
	})
}

func runINPX(ctx context.Context, cmd *cli.Command) error {
	start := time.Now()
	cfg := state.EnvFromContext(ctx).Cfg
	env := state.EnvFromContext(ctx)
	format, err := inpx.ParseFormat(cmd.String("format"))
	if err != nil {
		return err
	}
	sequence, err := inpx.ParseSequenceMode(cmd.String("sequence"))
	if err != nil {
		return err
	}
	preference, err := inpx.ParseFB2Preference(cmd.String("prefer-fb2"))
	if err != nil {
		return err
	}
	limits := inpx.Limits{
		AuthorName:   cfg.INPX.Limits.AuthorName,
		AuthorMiddle: cfg.INPX.Limits.AuthorMiddle,
		AuthorFamily: cfg.INPX.Limits.AuthorFamily,
		Title:        cfg.INPX.Limits.Title,
		Keywords:     cfg.INPX.Limits.Keywords,
		Sequence:     cfg.INPX.Limits.Sequence,
	}
	stats, err := inpx.Generate(ctx, inpx.Options{
		InputPrefix:     cmd.String("input"),
		OutputPrefix:    cmd.String("output"),
		Format:          format,
		SequenceMode:    sequence,
		FB2Preference:   preference,
		QuickFix:        cfg.INPX.QuickFix,
		Limits:          limits,
		CommentTemplate: cfg.INPX.CommentTemplate,
		Log:             env.Log,
	})
	if err != nil {
		return err
	}
	if env.Log != nil {
		env.Log.Info(
			"INPX created",
			zap.String("file", stats.OutputPath),
			zap.String("dump_date", stats.DumpDate),
			zap.Int("archives", stats.Archives),
			zap.Int("files", stats.Files),
			zap.Int64("records", stats.Records),
			zap.Int64("db_records", stats.DBRecords),
			zap.Int64("fb2_records", stats.FB2Records),
			zap.Int64("dummy_records", stats.Dummy),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return nil
}

func writeMergeMetadata(prefix string, compression jsonl.Compression, meta model.MergeMetadata, log *zap.Logger) error {
	path := prefix + ".meta.json"
	finalPath := jsonl.CompressedPath(path, compression)
	f, w, closer, err := jsonl.CreateCompressedFile(path, compression)
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := jsonv2.MarshalWrite(w, meta); err != nil {
		f.Close()
		return fmt.Errorf("write merge metadata %q: %w", tmpPath, err)
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		f.Close()
		return fmt.Errorf("write merge metadata newline %q: %w", tmpPath, err)
	}
	if closer != nil {
		if err := closer.Close(); err != nil {
			f.Close()
			return fmt.Errorf("close merge metadata compressor %q: %w", tmpPath, err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close merge metadata %q: %w", tmpPath, err)
	}
	if _, err := os.Stat(finalPath); err == nil {
		if log != nil {
			log.Warn("Overwriting existing merge metadata", zap.String("file", finalPath))
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat merge metadata %q: %w", finalPath, err)
	}
	if err := fileutil.ReplaceOutputFile(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename merge metadata %q to %q: %w", tmpPath, finalPath, err)
	}
	cleanup = false
	return nil
}

func dumpDirDatesDiffer(dumps []db.DumpFile) bool {
	date := ""
	for _, dump := range dumps {
		if dump.DumpDate == "" {
			continue
		}
		if date == "" {
			date = dump.DumpDate
			continue
		}
		if date != dump.DumpDate {
			return true
		}
	}
	return false
}

func writeOutput(ctx context.Context, path string, partSizeValue string, compressionValue string, log *zap.Logger, write func(*jsonl.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	partSize, err := parseSize(partSizeValue)
	if err != nil {
		return err
	}
	compression, err := jsonl.ParseCompression(compressionValue)
	if err != nil {
		return err
	}
	out, err := jsonl.CreateCompressed(path, partSize, compression)
	if err != nil {
		return err
	}
	out.WithLogger(log)
	writeErr := write(out)
	closeErr := out.Close()
	if writeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return closeErr
}

func loadDatabaseIndex(ctx context.Context, manifestPath string, log *zap.Logger) (databaseIndex, error) {
	if manifestPath == "" {
		return databaseIndex{}, nil
	}
	start := time.Now()
	index := databaseIndex{byID: make(map[int64]model.DatabaseSource), byFile: make(map[string]model.DatabaseSource)}
	records, err := library.ForEachManifestRecord(ctx, manifestPath, func(rec model.Record) error {
		if rec.ID.BookID > 0 && rec.Source.Database.Present {
			index.byID[rec.ID.BookID] = rec.Source.Database
		}
		for _, key := range recordFileKeys(rec) {
			if rec.Source.Database.Present {
				index.byFile[key] = rec.Source.Database
			}
		}
		return nil
	})
	if err != nil {
		return databaseIndex{}, err
	}
	if log != nil {
		log.Info(
			"Database manifest indexed",
			zap.String("manifest", manifestPath),
			zap.Int64("records", records),
			zap.Int("book_ids", len(index.byID)),
			zap.Int("file_names", len(index.byFile)),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return index, nil
}

func mergeArchiveManifests(
	ctx context.Context,
	archivePlan []library.ArchiveManifestDecision,
	dbIndex databaseIndex,
	out *jsonl.Writer,
	log *zap.Logger,
) error {
	start := time.Now()
	var records int64
	for _, decision := range archivePlan {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := library.ForEachManifestRecord(ctx, decision.ManifestPath, func(rec model.Record) error {
			if rec.ID.Archive != nil {
				rec.ID.Archive.Path = decision.ArchivePath
			}
			rec.Source.Database = model.DatabaseSource{}
			if rec.ID.BookID > 0 {
				if source, ok := dbIndex.byID[rec.ID.BookID]; ok {
					rec.Source.Database = source
				}
			}
			if !rec.Source.Database.Present {
				for _, key := range recordFileKeys(rec) {
					if source, ok := dbIndex.byFile[key]; ok {
						rec.Source.Database = source
						if source.Book != nil {
							rec.ID.BookID = source.Book.BookID
						}
						break
					}
				}
			}
			return out.Write(rec)
		})
		if err != nil {
			return err
		}
		records += count
	}
	if log != nil {
		log.Info("Archive manifests merged", zap.Int("manifests", len(archivePlan)), zap.Int64("records", records), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func recordFileKeys(rec model.Record) []string {
	keys := make([]string, 0, len(rec.Source.Database.Filenames)+2)
	if rec.ID.FileName != "" {
		keys = append(keys, fileKey(rec.ID.FileName))
		if rec.ID.Extension != "" {
			keys = append(keys, fileKey(rec.ID.FileName+"."+rec.ID.Extension))
		}
	}
	for _, name := range rec.Source.Database.Filenames {
		keys = append(keys, fileKey(name))
	}
	return keys
}

func fileKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func failIfReportsNotReady(reports []library.ManifestReport, allowStale bool) error {
	for _, report := range reports {
		if !report.Ready(allowStale) {
			return cli.Exit(errManifestNotReady, staleManifestExitCode)
		}
	}
	return nil
}

func applyCacheOverrides(cfg *config.Config, cmd *cli.Command) {
	if cmd.Bool("rebuild") {
		cfg.Processing.Rebuild = true
	}
}

func parseSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return 0, nil
	}
	multiplier := int64(1)
	for _, suffix := range []struct {
		suffix string
		mul    int64
	}{
		{"kb", 1024}, {"k", 1024},
		{"mb", 1024 * 1024}, {"m", 1024 * 1024},
		{"gb", 1024 * 1024 * 1024}, {"g", 1024 * 1024 * 1024},
		{"b", 1},
	} {
		if strings.HasSuffix(value, suffix.suffix) {
			multiplier = suffix.mul
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix.suffix))
			break
		}
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid output part size %q", value)
	}
	return int64(n * float64(multiplier)), nil
}

func outputConfiguration(ctx context.Context, cmd *cli.Command) (retErr error) {
	var data []byte
	var err error
	if cmd.Bool("default") {
		data, err = config.Prepare()
	} else {
		data, err = config.Dump(state.EnvFromContext(ctx).Cfg)
	}
	if err != nil {
		return fmt.Errorf("unable to get configuration: %w", err)
	}

	destination := cmd.Args().Get(0)
	if destination == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}
