package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"

	"metabib/config"
)

type Runtime struct {
	Config  config.DatabaseConfig
	Client  string
	LogOut  io.Writer
	Log     *zap.Logger
	managed bool
	cmd     *exec.Cmd
	tmpDir  string
}

func PrepareRuntime(
	ctx context.Context,
	cfg config.DatabaseConfig,
	needClient bool,
	overwriteDataDir bool,
	log *zap.Logger,
	logOut io.Writer,
) (*Runtime, error) {
	start := time.Now()
	if logOut == nil {
		logOut = io.Discard
	}
	defer func() {
		if log != nil {
			log.Info("Database runtime preparation completed", zap.Bool("managed", cfg.Managed), zap.Duration("elapsed", time.Since(start)))
		}
	}()
	if cfg.DSN != "" || !cfg.Managed {
		if name, err := dsnDatabaseName(cfg.DSN); err != nil {
			return nil, err
		} else if name != "" {
			cfg.Name = name
		}
		client, err := findBinary(cfg.ClientPath, "mariadb", "mysql")
		if err != nil && needClient {
			return nil, err
		}
		return &Runtime{Config: cfg, Client: client, LogOut: logOut, Log: log}, nil
	}

	server, err := findBinary(cfg.ServerPath, "mariadbd", "mysqld")
	if err != nil {
		return nil, err
	}
	installDB, err := findBinary(cfg.InstallDBPath, "mariadb-install-db", "mysql_install_db")
	if err != nil {
		return nil, err
	}
	client, err := findBinary(cfg.ClientPath, "mariadb", "mysql")
	if err != nil {
		return nil, err
	}

	rt := &Runtime{Config: cfg, Client: client, LogOut: logOut, Log: log, managed: true}
	if err := rt.prepareManagedPaths(); err != nil {
		return nil, err
	}
	if err := rt.initializeDataDir(ctx, installDB, overwriteDataDir); err != nil {
		rt.Close()
		return nil, err
	}
	if err := rt.startServer(ctx, server); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

func (r *Runtime) Close() error {
	start := time.Now()
	var errs []error
	defer func() {
		if r != nil && r.Log != nil {
			r.Log.Info("Database runtime closed", zap.Bool("managed", r.managed), zap.Duration("elapsed", time.Since(start)))
		}
	}()
	if r != nil && r.managed && r.cmd != nil && !r.Config.KeepRunning {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		admin, err := findBinary("", "mariadb-admin", "mysqladmin")
		if err == nil {
			args := []string{"--user", r.Config.User}
			if r.Config.Protocol == "unix" {
				args = append(args, "--protocol", "socket", "--socket", r.Config.Socket)
			} else {
				args = append(args, "--protocol", "tcp", "--host", r.Config.Host, "--port", fmt.Sprintf("%d", r.Config.Port))
			}
			if r.Config.Password != "" {
				args = append(args, "--password="+r.Config.Password)
			} else {
				args = append(args, "--skip-ssl")
			}
			args = append(args, "shutdown")
			cmd := exec.CommandContext(ctx, admin, args...)
			cmd.Stdout = r.LogOut
			cmd.Stderr = r.LogOut
			if err := cmd.Run(); err != nil {
				errs = append(errs, fmt.Errorf("shutdown managed MariaDB: %w", err))
			}
		} else if r.cmd.Process != nil {
			if err := r.cmd.Process.Signal(os.Interrupt); err != nil {
				errs = append(errs, fmt.Errorf("signal managed MariaDB: %w", err))
			}
		}
		if r.cmd.Process != nil {
			done := make(chan error, 1)
			go func() {
				if r.cmd.ProcessState != nil {
					done <- nil
					return
				}
				done <- r.cmd.Wait()
			}()
			select {
			case err := <-done:
				if err != nil && !strings.Contains(err.Error(), "signal") {
					errs = append(errs, fmt.Errorf("wait managed MariaDB: %w", err))
				}
			case <-ctx.Done():
				if err := r.cmd.Process.Kill(); err != nil {
					errs = append(errs, fmt.Errorf("kill managed MariaDB: %w", err))
				}
			}
		}
	}
	if r != nil && r.tmpDir != "" && !r.Config.KeepRunning {
		if err := os.RemoveAll(r.tmpDir); err != nil {
			errs = append(errs, fmt.Errorf("remove temporary MariaDB directory: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) prepareManagedPaths() error {
	if r.Config.Temporary {
		tmp, err := os.MkdirTemp("", "metabib-mariadb-*")
		if err != nil {
			return fmt.Errorf("create temporary MariaDB directory: %w", err)
		}
		r.tmpDir = tmp
		r.Config.DataDir = filepath.Join(tmp, "data")
	}
	if r.Config.DataDir == "" {
		r.Config.DataDir = filepath.Join("data", "mariadb")
	}
	dataDir, err := filepath.Abs(r.Config.DataDir)
	if err != nil {
		return fmt.Errorf("resolve MariaDB data directory: %w", err)
	}
	r.Config.DataDir = dataDir
	base := filepath.Dir(dataDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return fmt.Errorf("create MariaDB base directory %q: %w", base, err)
	}
	if r.Config.PIDFile == "" {
		r.Config.PIDFile = filepath.Join(base, "metabib.pid")
	}
	if r.Config.LogFile == "" {
		r.Config.LogFile = filepath.Join(base, "metabib-mariadb.log")
	}
	if r.Config.Protocol == "unix" {
		if r.Config.Socket == "" {
			r.Config.Socket = filepath.Join(base, "metabib.sock")
		}
		r.Config.Host = r.Config.Socket
		r.Config.Port = 0
	} else {
		r.Config.Protocol = "tcp"
		r.Config.Host = defaultString(r.Config.Host, "127.0.0.1")
		if r.Config.Port == 0 {
			return errors.New("managed TCP MariaDB requires database.port to be set")
		}
	}
	r.Config.User = defaultString(r.Config.User, "root")
	return nil
}

func (r *Runtime) initializeDataDir(ctx context.Context, installDB string, overwrite bool) error {
	start := time.Now()
	if overwrite {
		if err := os.RemoveAll(r.Config.DataDir); err != nil {
			return fmt.Errorf("remove MariaDB data directory %q: %w", r.Config.DataDir, err)
		}
	}
	initialized, err := dataDirInitialized(r.Config.DataDir)
	if err != nil {
		return err
	}
	if initialized {
		if r.Log != nil {
			r.Log.Info("MariaDB data directory reused", zap.String("dir", r.Config.DataDir), zap.Duration("elapsed", time.Since(start)))
		}
		return nil
	}
	if err := os.MkdirAll(r.Config.DataDir, 0o755); err != nil {
		return fmt.Errorf("create MariaDB data directory %q: %w", r.Config.DataDir, err)
	}
	args := installDBArgs(r.Config.DataDir)
	cmd := exec.CommandContext(ctx, installDB, args...)
	cmd.Stdout = r.LogOut
	cmd.Stderr = r.LogOut
	if err := cmd.Run(); err != nil {
		fallbackArgs := []string{"--datadir=" + r.Config.DataDir}
		cmd = exec.CommandContext(ctx, installDB, fallbackArgs...)
		cmd.Stdout = r.LogOut
		cmd.Stderr = r.LogOut
		if fallbackErr := cmd.Run(); fallbackErr != nil {
			return fmt.Errorf("initialize MariaDB data directory with %q: %w", installDB, err)
		}
	}
	if r.Log != nil {
		r.Log.Info("MariaDB data directory initialized", zap.String("dir", r.Config.DataDir), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func installDBArgs(dataDir string) []string {
	args := []string{"--datadir=" + dataDir}
	if runtime.GOOS != "windows" {
		args = append(args, "--auth-root-authentication-method=normal", "--skip-test-db")
	}
	return args
}

func (r *Runtime) startServer(ctx context.Context, server string) error {
	start := time.Now()
	args := []string{
		"--datadir=" + r.Config.DataDir,
		"--pid-file=" + r.Config.PIDFile,
		"--log-error=" + r.Config.LogFile,
		"--skip-grant-tables",
		"--character-set-server=utf8mb4",
		"--collation-server=utf8mb4_unicode_ci",
	}
	if r.Config.Protocol == "unix" {
		if err := os.Remove(r.Config.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale MariaDB socket: %w", err)
		}
		args = append(args, "--socket="+r.Config.Socket, "--skip-networking")
	} else {
		args = append(args, "--bind-address="+r.Config.Host, fmt.Sprintf("--port=%d", r.Config.Port))
	}
	r.cmd = exec.CommandContext(ctx, server, args...)
	r.cmd.Stdout = r.LogOut
	r.cmd.Stderr = r.LogOut
	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start managed MariaDB with %q: %w", server, err)
	}
	if err := r.waitReady(ctx); err != nil {
		return err
	}
	if r.Log != nil {
		fields := []zap.Field{zap.String("protocol", r.Config.Protocol), zap.Duration("elapsed", time.Since(start))}
		if r.Config.Protocol == "unix" {
			fields = append(fields, zap.String("socket", r.Config.Socket))
		} else {
			fields = append(fields, zap.String("host", r.Config.Host), zap.Int("port", r.Config.Port))
		}
		r.Log.Info("Managed MariaDB started", fields...)
	}
	return nil
}

func (r *Runtime) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if r.Config.Protocol == "unix" {
			if _, err := os.Stat(r.Config.Socket); err != nil {
				lastErr = err
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		dsn, err := DSN(r.Config, false)
		if err != nil {
			return err
		}
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			lastErr = db.PingContext(ctx)
			db.Close()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("managed MariaDB did not become ready: %w", lastErr)
	}
	return errors.New("managed MariaDB did not become ready")
}

func dataDirInitialized(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read MariaDB data directory %q: %w", path, err)
	}
	for _, entry := range entries {
		if entry.Name() == "mysql" && entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func findBinary(explicit string, names ...string) (string, error) {
	if explicit != "" {
		if usableBinary(explicit) {
			return explicit, nil
		}
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("binary %q is not usable: %w", explicit, err)
		}
		return "", fmt.Errorf("binary %q is not executable", explicit)
	}

	seen := make(map[string]bool)
	var candidates []string
	cwd, _ := os.Getwd()
	fileNames := binaryFileNames(names)
	if cwd != "" {
		candidates = append(candidates, walkBinaryCandidates(filepath.Join(cwd, "mariadb"), fileNames)...)
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if usableBinary(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unable to find executable %s in ./mariadb or PATH", strings.Join(names, "/"))
}

func walkBinaryCandidates(root string, names []string) []string {
	if root == "" {
		return nil
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil
	}
	var candidates []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".jj" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if binaryNameMatches(d.Name(), names) {
			candidates = append(candidates, path)
		}
		return nil
	})
	sort.Strings(candidates)
	return candidates
}

func binaryFileNames(names []string) []string {
	out := make([]string, 0, len(names)*2)
	seen := make(map[string]bool)
	for _, name := range names {
		if !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
		if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
			exe := name + ".exe"
			if !seen[exe] {
				out = append(out, exe)
				seen[exe] = true
			}
		}
	}
	return out
}

func binaryNameMatches(name string, candidates []string) bool {
	for _, candidate := range candidates {
		if runtime.GOOS == "windows" {
			if strings.EqualFold(name, candidate) {
				return true
			}
			continue
		}
		if name == candidate {
			return true
		}
	}
	return false
}

func usableBinary(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".exe", ".com", ".bat", ".cmd":
			return true
		default:
			return false
		}
	}
	return st.Mode()&0o111 != 0
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
