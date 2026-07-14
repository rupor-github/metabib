package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	cli "github.com/urfave/cli/v3"
	"go.uber.org/zap"

	"metabib/config"
	"metabib/db"
	"metabib/fetch"
	"metabib/flibinpx"
	"metabib/jsonl"
	"metabib/library"
	"metabib/mhlinpx"
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
	env.Log.Debug(
		"Program started",
		zap.Strings("args", os.Args),
		zap.String("version", misc.GetVersion()),
		zap.String("runtime", runtime.Version()),
		zap.String("git", misc.GetGitHash()),
	)
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
		errWasHandled = true
		return
	}
	if _, ok := err.(rollupExitCode); ok {
		errWasHandled = true
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
			mhlINPXCommand(),
			flibINPXCommand(),
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
			&cli.StringFlag{
				Name:     "archives",
				Aliases:  []string{"a"},
				Usage:    "directory for finalized fb2-*.zip archives and active .merging archive",
				Required: true,
			},
			&cli.StringSliceFlag{
				Name:    "updates",
				Aliases: []string{"u"},
				Usage:   "directory containing daily update ZIPs; can be repeated; defaults to --archives",
			},
			&cli.IntFlag{Name: "size", Value: 2000, Usage: "finalized archive target size in decimal megabytes"},
		},
		Action: runRollup,
	}
}

func mhlINPXCommand() *cli.Command {
	return &cli.Command{
		Name:  "mhl-inpx",
		Usage: "Build MyHomeLib-compatible INPX from merged JSONL parts",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "input",
				Aliases:  []string{"i"},
				Usage:    "read merged JSONL parts and metadata using `PREFIX`",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "output",
				Aliases:  []string{"o"},
				Usage:    "write INPX using `PREFIX` plus dump date",
				Required: true,
			},
			&cli.StringFlag{Name: "format", Value: string(mhlinpx.Format2X), Usage: "INPX format `MODE` (2x, ruks)"},
			&cli.StringFlag{
				Name:  "sequence",
				Value: string(mhlinpx.SequenceAuthor),
				Usage: "sequence selection `MODE` (author, publisher, ignore)",
			},
			&cli.StringFlag{
				Name:  "prefer-fb2",
				Value: string(mhlinpx.PreferComplement),
				Usage: "FB2 sequence preference `MODE` (ignore, merge, complement, replace)",
			},
		},
		Action: runMHLINPX,
	}
}

func flibINPXCommand() *cli.Command {
	return &cli.Command{
		Name:  "flib-inpx",
		Usage: "Build FLibrary-compatible INPX from merged JSONL parts",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "input",
				Aliases:  []string{"i"},
				Usage:    "read merged JSONL parts and metadata using `PREFIX`",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "output",
				Aliases:  []string{"o"},
				Usage:    "write INPX using `PREFIX` plus dump date",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "sequence",
				Value: string(flibinpx.SequenceAuthor),
				Usage: "sequence selection `MODE` (author, publisher, all, ignore)",
			},
			&cli.StringFlag{
				Name:  "prefer-fb2",
				Value: string(flibinpx.PreferComplement),
				Usage: "FB2 sequence preference `MODE` (ignore, merge, complement, replace)",
			},
			&cli.StringFlag{
				Name:  "fb2-flatten",
				Value: string(flibinpx.FlattenAll),
				Usage: "FB2 sequence flattening `MODE` (all, leaf, path, path-leaf)",
			},
			&cli.StringFlag{Name: "source-lib", Usage: "SOURCELIB field value; defaults to merge metadata library name"},
		},
		Action: runFLibINPX,
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
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "write JSONL output using `PREFIX`", Required: true},
			&cli.StringFlag{Name: "output-compression", Value: string(jsonl.CompressionZstd), Usage: "compress JSONL output as `MODE` (zstd, gz, zip, none)"},
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
		ValidateCRC: env.Cfg.Rollup.ValidateCRC,
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

func runCache(ctx context.Context, cmd *cli.Command) (retErr error) {
	cfg := state.EnvFromContext(ctx).Cfg
	applyCacheOverrides(cfg, cmd)
	env := state.EnvFromContext(ctx)

	dumpDir := cmd.String("database-dumps")
	selectedDatabase := dumpDir != ""
	archives := cmd.StringSlice("archives")
	selectedArchives := len(archives) > 0
	importDumps := !cmd.Bool("no-import")
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
			env.Log.Info(
				"SQL dump date detected",
				zap.String("dump_date", dumpDate),
				zap.String("directory", dumpDir),
				zap.Duration("elapsed", time.Since(discoverStart)),
			)
		}
	}

	var reports []library.ManifestReport
	checkMD5 := cmd.Bool("check-md5")
	if selectedArchives {
		archivePlan, _, err := library.PlanArchives(ctx, cfg, archives, checkMD5, env.Log, env.Verbose)
		if err != nil {
			return err
		}
		if err := library.BuildArchiveManifests(ctx, cfg, env.Log, env.Verbose, archivePlan); err != nil {
			return err
		}
		_, archiveReports, err := library.ValidateArchiveManifests(ctx, cfg, archives, checkMD5, env.Log, env.Verbose)
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
			runtime, err := db.PrepareRuntime(ctx, cfg.Database, importDumps, env.Log, logOut)
			if err != nil {
				return err
			}
			defer func() {
				retErr = errors.Join(retErr, runtime.Close())
			}()
			cfg.Database = runtime.Config

			if importDumps {
				importer := db.NewImporter(cfg.Database, runtime.Client, env.Log, logOut, env.Verbose, true)
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
			if importDumps {
				if err := repo.WriteImportProvenance(ctx, importProvenanceFromDatabaseManifest(databaseManifest)); err != nil {
					return err
				}
			}
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

func importProvenanceFromDatabaseManifest(manifest library.DatabaseManifestDecision) db.ImportProvenance {
	dumps := make([]db.ImportDumpProvenance, 0, len(manifest.Dumps))
	for _, dump := range manifest.Dumps {
		dumps = append(dumps, db.ImportDumpProvenance{
			Path:          dump.Path,
			Name:          dump.Name,
			DumpDate:      dump.DumpDate,
			DumpCompleted: dump.DumpCompleted,
			Modified:      dump.Modified,
			MD5:           dump.MD5,
		})
	}
	return db.ImportProvenance{
		DumpDir:  manifest.DumpDir,
		DumpDate: manifest.DumpDate,
		Dumps:    dumps,
	}
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
		archivePlan, archiveReports, err = library.ValidateArchiveManifests(ctx, cfg, archives, checkMD5, env.Log, env.Verbose)
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
	_, err := jsonl.ParseCompression(compressionValue)
	if err != nil {
		return err
	}
	dataset, err := library.DatasetFor(ctx, cfg.Database.Name, databaseManifest, archivePlan, cfg.Processing, misc.GetVersion())
	if err != nil {
		return err
	}
	err = writeOutput(ctx, outputPrefix, compressionValue, env.Log, func(out *jsonl.Writer) error {
		if err := out.WriteValue(dataset); err != nil {
			return err
		}
		var records int64
		if selectedArchives {
			dbIndex, err := loadDatabaseIndex(ctx, databaseManifest.ManifestPath, env.Log)
			if err != nil {
				return err
			}
			records, err = mergeArchiveManifests(ctx, archivePlan, dbIndex, datasetArchiveSources(dataset), out, env.Log)
		} else {
			records, err = writeDatabaseManifestRecords(ctx, databaseManifest.ManifestPath, out, env.Log)
		}
		if err != nil {
			return err
		}
		if records != dataset.Records {
			return fmt.Errorf("merge record count mismatch: declared %d, wrote %d", dataset.Records, records)
		}
		return nil
	})
	return err
}

func runMHLINPX(ctx context.Context, cmd *cli.Command) error {
	start := time.Now()
	cfg := state.EnvFromContext(ctx).Cfg
	env := state.EnvFromContext(ctx)
	format, err := mhlinpx.ParseFormat(cmd.String("format"))
	if err != nil {
		return err
	}
	sequence, err := mhlinpx.ParseSequenceMode(cmd.String("sequence"))
	if err != nil {
		return err
	}
	preference, err := mhlinpx.ParseFB2Preference(cmd.String("prefer-fb2"))
	if err != nil {
		return err
	}
	limits := mhlinpx.Limits{
		AuthorName:   cfg.INPX.Limits.AuthorName,
		AuthorMiddle: cfg.INPX.Limits.AuthorMiddle,
		AuthorFamily: cfg.INPX.Limits.AuthorFamily,
		Title:        cfg.INPX.Limits.Title,
		Keywords:     cfg.INPX.Limits.Keywords,
		Sequence:     cfg.INPX.Limits.Sequence,
	}
	stats, err := mhlinpx.Generate(ctx, mhlinpx.Options{
		InputPrefix:     cmd.String("input"),
		OutputPrefix:    cmd.String("output"),
		Format:          format,
		SequenceMode:    sequence,
		FB2Preference:   preference,
		QuickFix:        cfg.INPX.QuickFix,
		Limits:          limits,
		CommentTemplate: cfg.INPX.CommentTemplate,
		VersionTemplate: cfg.INPX.VersionTemplate,
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

func runFLibINPX(ctx context.Context, cmd *cli.Command) error {
	start := time.Now()
	cfg := state.EnvFromContext(ctx).Cfg
	env := state.EnvFromContext(ctx)
	sequence, err := flibinpx.ParseSequenceMode(cmd.String("sequence"))
	if err != nil {
		return err
	}
	preference, err := flibinpx.ParseFB2Preference(cmd.String("prefer-fb2"))
	if err != nil {
		return err
	}
	flatten, err := flibinpx.ParseFlattenMode(cmd.String("fb2-flatten"))
	if err != nil {
		return err
	}
	dedup, err := flibinpx.ParseDedupMode(cfg.INPX.FLibrary.SequenceDedup)
	if err != nil {
		return err
	}
	stats, err := flibinpx.Generate(ctx, flibinpx.Options{
		InputPrefix:      cmd.String("input"),
		OutputPrefix:     cmd.String("output"),
		SequenceMode:     sequence,
		FB2Preference:    preference,
		FlattenMode:      flatten,
		DedupMode:        dedup,
		FB2PathSeparator: cfg.INPX.FLibrary.FB2PathSeparator,
		SourceLib:        cmd.String("source-lib"),
		CommentTemplate:  cfg.INPX.CommentTemplate,
		VersionTemplate:  cfg.INPX.VersionTemplate,
		Log:              env.Log,
	})
	if err != nil {
		return err
	}
	if env.Log != nil {
		env.Log.Info(
			"FLibrary INPX created",
			zap.String("file", stats.OutputPath),
			zap.String("dump_date", stats.DumpDate),
			zap.Int("archives", stats.Archives),
			zap.Int("files", stats.Files),
			zap.Int64("records", stats.Records),
			zap.Int64("db_records", stats.DBRecords),
			zap.Int64("fb2_records", stats.FB2Records),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
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

func writeOutput(
	ctx context.Context,
	path string,
	compressionValue string,
	log *zap.Logger,
	write func(*jsonl.Writer) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	compression, err := jsonl.ParseCompression(compressionValue)
	if err != nil {
		return err
	}
	out, err := jsonl.CreateCompressed(path, compression)
	if err != nil {
		return err
	}
	out.WithLogger(log)
	writeErr := write(out)
	if writeErr != nil {
		return errors.Join(writeErr, out.Abort())
	}
	if err := out.Stage(); err != nil {
		return errors.Join(err, out.Abort())
	}
	parts := out.StagedFinalPaths()
	if len(parts) == 0 {
		return errors.Join(errors.New("JSONL output did not produce any parts"), out.Abort())
	}
	if err := out.Commit(); err != nil {
		return errors.Join(err, out.Abort())
	}
	return nil
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

func datasetArchiveSources(dataset model.Dataset) map[string]string {
	sources := make(map[string]string, len(dataset.Archives))
	for _, archive := range dataset.Archives {
		if archive.PathHint != "" {
			sources[archive.PathHint] = archive.ID
		}
	}
	return sources
}

func writeDatabaseManifestRecords(
	ctx context.Context,
	manifestPath string,
	out *jsonl.Writer,
	log *zap.Logger,
) (int64, error) {
	start := time.Now()
	records, err := library.ForEachManifestRecord(ctx, manifestPath, func(rec model.Record) error {
		converted, err := datasetRecordFromRecord(rec, nil)
		if err != nil {
			return err
		}
		return out.WriteValue(converted)
	})
	if err != nil {
		return records, err
	}
	if log != nil {
		log.Info(
			"Database manifest records merged",
			zap.String("manifest", manifestPath),
			zap.Int64("records", records),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return records, nil
}

func mergeArchiveManifests(
	ctx context.Context,
	archivePlan []library.ArchiveManifestDecision,
	dbIndex databaseIndex,
	archiveSources map[string]string,
	out *jsonl.Writer,
	log *zap.Logger,
) (int64, error) {
	start := time.Now()
	var records int64
	for _, decision := range archivePlan {
		if err := ctx.Err(); err != nil {
			return records, err
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
			converted, err := datasetRecordFromRecord(rec, archiveSources)
			if err != nil {
				return err
			}
			return out.WriteValue(converted)
		})
		if err != nil {
			return records, err
		}
		records += count
	}
	if log != nil {
		log.Info(
			"Archive manifests merged",
			zap.Int("manifests", len(archivePlan)),
			zap.Int64("records", records),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return records, nil
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
