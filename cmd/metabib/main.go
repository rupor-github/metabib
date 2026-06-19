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

	mysql "github.com/go-sql-driver/mysql"
	cli "github.com/urfave/cli/v3"
	"go.uber.org/zap"

	"metabib/config"
	"metabib/db"
	"metabib/jsonl"
	"metabib/library"
	"metabib/misc"
	"metabib/model"
	"metabib/state"
)

const staleManifestExitCode = 3

var errManifestNotReady = errors.New("one or more cache manifests are missing, stale, or invalid")

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

func main() {
	ctx, stop := signal.NotifyContext(state.ContextWithEnv(context.Background()), os.Interrupt, syscall.SIGTERM)
	app := &cli.Command{
		Name:            misc.GetAppName(),
		Usage:           "extract Flibusta/FB2 metadata into JSONL",
		Version:         misc.GetVersion() + " (" + runtime.Version() + ") : " + misc.GetGitHash(),
		HideHelpCommand: true,
		ExitErrHandler:  func(context.Context, *cli.Command, error) {},
		Before:          initializeAppContext,
		After:           destroyAppContext,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, DefaultText: "", Usage: "load configuration from `FILE` (YAML)"},
		},
		Commands: []*cli.Command{
			cacheCommand(),
			mergeCommand(),
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
		if err != nil {
			fmt.Fprintf(os.Stderr, "Program ended with error: %v\n", err)
			if exitErr, ok := err.(interface{ ExitCode() int }); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
	}()
	err = app.Run(ctx, os.Args)
	stop()
}

func cacheCommand() *cli.Command {
	return &cli.Command{
		Name:  "cache",
		Usage: "Build database and archive manifests without writing final JSONL",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "database-dumps", Usage: "directory containing SQL dumps for database manifest"},
			&cli.StringSliceFlag{Name: "archives", Aliases: []string{"a"}, Usage: "archive file or directory with archives; can be repeated"},
			&cli.BoolFlag{Name: "progress", Usage: "periodically log processing progress at info level"},
			&cli.BoolFlag{Name: "rebuild", Usage: "rebuild stale, missing, or invalid manifests and verify manifest checksums"},
			&cli.BoolFlag{Name: "no-import", Usage: "skip SQL dump import and use existing database"},
			&cli.StringFlag{Name: "db-name", Usage: "database name"},
			&cli.StringFlag{Name: "db-host", Usage: "database host or socket path"},
			&cli.IntFlag{Name: "db-port", Usage: "database TCP port"},
			&cli.StringFlag{Name: "db-user", Usage: "database user"},
			&cli.StringFlag{Name: "db-password", Usage: "database password"},
			&cli.StringFlag{Name: "db-dsn", Usage: "use existing database service DSN"},
			&cli.BoolFlag{Name: "db-use-service", Usage: "do not start managed MariaDB, use configured host/port/socket"},
			&cli.BoolFlag{Name: "db-overwrite", Usage: "overwrite managed data directory and drop database before import"},
			&cli.StringFlag{Name: "db-server", Usage: "mariadbd/mysqld server path"},
			&cli.StringFlag{Name: "db-install-db", Usage: "mariadb-install-db/mysql_install_db path"},
			&cli.StringFlag{Name: "db-client", Usage: "mariadb/mysql client path"},
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
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "write JSONL to `FILE`"},
			&cli.StringFlag{Name: "output-part-size", Usage: "split JSONL into range-named parts of approximate `SIZE` (supports k, m, g)"},
			&cli.BoolFlag{Name: "check-md5", Usage: "verify source MD5 checksums recorded in manifests"},
			&cli.BoolFlag{Name: "allow-stale", Usage: "warn but continue when manifests are stale"},
		},
		Action: runMerge,
	}
}

func runCache(ctx context.Context, cmd *cli.Command) error {
	cfg := state.EnvFromContext(ctx).Cfg
	applyCacheOverrides(cfg, cmd)
	env := state.EnvFromContext(ctx)

	dumpDir := cmd.String("database-dumps")
	selectedDatabase := dumpDir != ""
	selectedArchives := len(cfg.Processing.Archives) > 0
	if !selectedDatabase && !selectedArchives {
		return errors.New("nothing to cache: specify --database-dumps, --archives, or both")
	}
	if !cfg.Processing.Manifests.Enabled {
		return errors.New("manifest caching requires processing.manifests.enabled=true")
	}

	var dumps []db.DumpFile
	var dumpDate string
	if selectedDatabase {
		discoverStart := time.Now()
		var err error
		dumps, dumpDate, err = db.DiscoverDumps(dumpDir)
		if err != nil {
			return err
		}
		if env.Log != nil {
			env.Log.Info("SQL dump date detected", zap.String("dump_date", dumpDate), zap.String("directory", dumpDir), zap.Duration("elapsed", time.Since(discoverStart)))
		}
	}

	var reports []library.ManifestReport
	if selectedArchives {
		if cfg.Processing.Rebuild {
			archivePlan, _, err := library.PlanArchives(ctx, cfg, env.Log)
			if err != nil {
				return err
			}
			if err := library.BuildArchiveManifests(ctx, cfg, env.Log, archivePlan); err != nil {
				return err
			}
		}
		_, archiveReports, err := library.ValidateArchiveManifests(ctx, cfg, false, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, archiveReports...)
	}

	if selectedDatabase {
		if cfg.Processing.Rebuild {
			databaseManifest, err := library.PlanDatabaseManifest(ctx, cfg, dumpDir, dumps, dumpDate, env.Log)
			if err != nil {
				return err
			}
			if !databaseManifest.Use && databaseManifest.Create {
				var logOut io.Writer = os.Stderr
				if env.LogIO != nil {
					logOut = env.LogIO
				}
				runtime, err := db.PrepareRuntime(ctx, cfg.Database, env.Log, logOut)
				if err != nil {
					return err
				}
				defer runtime.Close()
				cfg.Database = runtime.Config

				if cfg.Database.Import {
					importer := db.NewImporter(cfg.Database, runtime.Client, env.Log, logOut)
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
				if err := library.ProcessDatabase(ctx, repo, cfg, nil, env.Log, databaseManifest); err != nil {
					return err
				}
			}
		}
		_, report, err := library.ValidateDatabaseManifest(ctx, cfg, dumpDir, dumps, dumpDate, false, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}
	return failIfReportsNotReady(reports, false)
}

func runMerge(ctx context.Context, cmd *cli.Command) error {
	cfg := state.EnvFromContext(ctx).Cfg
	applyMergeOverrides(cfg, cmd)
	env := state.EnvFromContext(ctx)

	dumpDir := cmd.String("database-dumps")
	selectedDatabase := dumpDir != ""
	selectedArchives := len(cfg.Processing.Archives) > 0
	if !selectedDatabase && !selectedArchives {
		return errors.New("nothing to merge: specify --database-dumps, --archives, or both")
	}
	if !cfg.Processing.Manifests.Enabled {
		return errors.New("merge requires processing.manifests.enabled=true")
	}

	allowStale := cmd.Bool("allow-stale")
	checkMD5 := cmd.Bool("check-md5")
	var reports []library.ManifestReport
	var databaseManifest library.DatabaseManifestDecision
	if selectedDatabase {
		dumps, dumpDate, err := db.DiscoverDumps(dumpDir)
		if err != nil {
			return err
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
		archivePlan, archiveReports, err = library.ValidateArchiveManifests(ctx, cfg, checkMD5, env.Log)
		if err != nil {
			return err
		}
		reports = append(reports, archiveReports...)
	}
	if err := failIfReportsNotReady(reports, allowStale); err != nil {
		return err
	}

	return writeOutput(ctx, cfg, func(out *jsonl.Writer) error {
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

func writeOutput(ctx context.Context, cfg *config.Config, write func(*jsonl.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	partSize, err := parseSize(cfg.Output.PartSize)
	if err != nil {
		return err
	}
	out, err := jsonl.Create(cfg.Output.JSONL, partSize)
	if err != nil {
		return err
	}
	defer out.Close()
	return write(out)
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
	if archives := cmd.StringSlice("archives"); len(archives) > 0 {
		cfg.Processing.Archives = archives
	}
	if cmd.Bool("progress") {
		cfg.Processing.Progress = true
	}
	if cmd.Bool("rebuild") {
		cfg.Processing.Rebuild = true
	}
	if cmd.Bool("no-import") {
		cfg.Database.Import = false
	}
	applyDatabaseOverrides(cfg, cmd)
}

func applyMergeOverrides(cfg *config.Config, cmd *cli.Command) {
	if v := cmd.String("output"); v != "" {
		cfg.Output.JSONL = v
	}
	if v := cmd.String("output-part-size"); v != "" {
		cfg.Output.PartSize = v
	}
	if archives := cmd.StringSlice("archives"); len(archives) > 0 {
		cfg.Processing.Archives = archives
	}
}

func applyDatabaseOverrides(cfg *config.Config, cmd *cli.Command) {
	if v := cmd.String("db-name"); v != "" {
		cfg.Database.Name = v
	}
	if v := cmd.String("db-host"); v != "" {
		cfg.Database.Host = v
	}
	if v := cmd.Int("db-port"); v > 0 {
		cfg.Database.Port = v
	}
	if v := cmd.String("db-user"); v != "" {
		cfg.Database.User = v
	}
	if v := cmd.String("db-password"); v != "" {
		cfg.Database.Password = v
	}
	if v := cmd.String("db-client"); v != "" {
		cfg.Database.ClientPath = v
	}
	if v := cmd.String("db-dsn"); v != "" {
		cfg.Database.DSN = v
		cfg.Database.Managed = false
	}
	if v := cmd.String("db-server"); v != "" {
		cfg.Database.ServerPath = v
	}
	if v := cmd.String("db-install-db"); v != "" {
		cfg.Database.InstallDBPath = v
	}
	if cmd.Bool("db-use-service") {
		cfg.Database.Managed = false
	}
	if cmd.Bool("db-overwrite") {
		cfg.Database.OverwriteDataDir = true
		cfg.Database.DropBeforeImport = true
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
