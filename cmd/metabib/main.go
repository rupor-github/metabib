package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	cli "github.com/urfave/cli/v3"
	"go.uber.org/zap"

	"metabib/config"
	"metabib/db"
	"metabib/jsonl"
	"metabib/library"
	"metabib/misc"
	"metabib/state"
)

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
	env.Log.Debug("Program started", zap.Strings("args", os.Args), zap.String("version", misc.GetVersion()), zap.String("runtime", runtime.Version()), zap.String("git", misc.GetGitHash()))
	return ctx, nil
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
		Before:          initializeAppContext,
		After:           destroyAppContext,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, DefaultText: "", Usage: "load configuration from `FILE` (YAML)"},
		},
		Commands: []*cli.Command{
			buildCommand(),
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
			fmt.Fprintf(os.Stderr, "Program ended with error: %v\n", err)
			os.Exit(1)
		}
	}()
	err = app.Run(ctx, os.Args)
}

func buildCommand() *cli.Command {
	return &cli.Command{
		Name:      "build",
		Usage:     "Imports SQL dumps, reads archives, and writes JSONL metadata",
		ArgsUsage: "DUMP_DIR",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dump-dir", Usage: "directory containing SQL dumps"},
			&cli.StringSliceFlag{Name: "archive", Aliases: []string{"a"}, Usage: "archive file or directory with archives; can be repeated"},
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "write JSONL to `FILE`"},
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
		Action: runBuild,
	}
}

func runBuild(ctx context.Context, cmd *cli.Command) error {
	cfg := state.EnvFromContext(ctx).Cfg
	applyBuildOverrides(cfg, cmd)

	dumpDir := cmd.String("dump-dir")
	if dumpDir == "" {
		dumpDir = cmd.Args().Get(0)
	}
	if cfg.Database.Import && dumpDir != "" {
		discoverStart := time.Now()
		_, dumpDate, err := db.DiscoverDumps(dumpDir)
		if err != nil {
			return err
		}
		if log := state.EnvFromContext(ctx).Log; log != nil {
			log.Info("SQL dump date detected", zap.String("dump_date", dumpDate), zap.String("directory", dumpDir), zap.Duration("elapsed", time.Since(discoverStart)))
		}
	}

	env := state.EnvFromContext(ctx)
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

	if cfg.Database.Import && dumpDir != "" {
		dumps, _, err := db.DiscoverDumps(dumpDir)
		if err != nil {
			return err
		}
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

	out, err := jsonl.Create(cfg.Output.JSONL)
	if err != nil {
		return err
	}
	defer out.Close()

	if len(cfg.Processing.Archives) > 0 {
		return library.ProcessArchives(ctx, repo, cfg, out, env.Log)
	}
	if cfg.Processing.OnlineWhenNoArchives {
		return library.ProcessDatabase(ctx, repo, cfg, out, env.Log)
	}
	return nil
}

func applyBuildOverrides(cfg *config.Config, cmd *cli.Command) {
	if v := cmd.String("output"); v != "" {
		cfg.Output.JSONL = v
	}
	if archives := cmd.StringSlice("archive"); len(archives) > 0 {
		cfg.Processing.Archives = archives
	}
	if cmd.Bool("no-import") {
		cfg.Database.Import = false
	}
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
