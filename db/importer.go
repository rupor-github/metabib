package db

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"

	"metabib/config"
)

type DumpFile struct {
	Path          string
	Name          string
	DumpDate      string
	DumpCompleted string
}

type Importer struct {
	cfg     config.DatabaseConfig
	client  string
	log     *zap.Logger
	logOut  io.Writer
	verbose bool
	create  bool
}

func NewImporter(
	cfg config.DatabaseConfig,
	client string,
	log *zap.Logger,
	logOut io.Writer,
	verbose bool,
	create bool,
) *Importer {
	if logOut == nil {
		logOut = io.Discard
	}
	return &Importer{
		cfg:     cfg,
		client:  client,
		log:     log,
		logOut:  logOut,
		verbose: verbose,
		create:  create,
	}
}

func DiscoverDumps(dir string, allowDateMismatch bool) ([]DumpFile, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", fmt.Errorf("read dump directory %q: %w", dir, err)
	}
	dumps := make([]DumpFile, 0, len(entries))
	dumpDate := ""
	firstDumpDate := ""
	dateMismatch := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".sql") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		date, completed, err := readDumpCompleted(path)
		if err != nil {
			return nil, "", err
		}
		if date != "" {
			if firstDumpDate == "" {
				firstDumpDate = date
			}
			if dumpDate != "" && dumpDate != date {
				if !allowDateMismatch {
					return nil, "", fmt.Errorf("dump files have different dates: %s and %s", dumpDate, date)
				}
				dateMismatch = true
			}
			dumpDate = date
		}
		dumps = append(dumps, DumpFile{Path: path, Name: entry.Name(), DumpDate: date, DumpCompleted: completed})
	}
	if dateMismatch {
		dumpDate = ""
	} else {
		dumpDate = firstDumpDate
	}
	sort.Slice(dumps, func(i, j int) bool { return dumps[i].Name < dumps[j].Name })
	return dumps, dumpDate, nil
}

func (i *Importer) PrepareDatabase(ctx context.Context) error {
	start := time.Now()
	if !i.create {
		return nil
	}
	defer func() {
		if i.log != nil {
			i.log.Info(
				"Database prepared",
				zap.String("database", i.cfg.Name),
				zap.Bool("create", i.create),
				zap.Duration("elapsed", time.Since(start)),
			)
		}
	}()
	dsn, err := DSN(i.cfg, false)
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open MariaDB admin connection: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping MariaDB admin connection: %w", err)
	}
	if i.create {
		if _, err := db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+quoteIdentifier(i.cfg.Name)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			return fmt.Errorf("create database %q: %w", i.cfg.Name, err)
		}
	}
	return nil
}

func (i *Importer) ImportDumps(ctx context.Context, dumps []DumpFile) error {
	start := time.Now()
	if len(dumps) == 0 {
		return errors.New("no SQL dumps found")
	}
	format, err := DetectDumpFormat(dumps)
	if err != nil {
		return err
	}
	dumps = filterImportDumps(dumps, format)
	if len(dumps) == 0 {
		return fmt.Errorf("no SQL dumps selected for import format %q", format)
	}
	client := i.client
	if client == "" {
		var err error
		client, err = findBinary(i.cfg.ClientPath, "mariadb", "mysql")
		if err != nil {
			return err
		}
	}
	for idx, dump := range dumps {
		dumpStart := time.Now()
		if err := i.importDump(ctx, client, dump); err != nil {
			return err
		}
		if i.log != nil {
			if i.verbose {
				i.log.Info(
					"SQL dump import progress",
					zap.String("file", dump.Path),
					zap.Int("file_index", idx+1),
					zap.Int("files", len(dumps)),
					zap.Duration("elapsed", time.Since(start)),
					zap.Duration("dump_elapsed", time.Since(dumpStart)),
				)
			} else {
				i.log.Debug("SQL dump imported", zap.String("file", dump.Path), zap.Duration("dump_elapsed", time.Since(dumpStart)))
			}
		}
	}
	if i.log != nil {
		i.log.Info("SQL import completed", zap.Int("files", len(dumps)), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func filterImportDumps(dumps []DumpFile, format Format) []DumpFile {
	if format != FormatLibrusecCurrent {
		return dumps
	}
	out := make([]DumpFile, 0, len(dumps))
	for _, dump := range dumps {
		if librusecImportDump(dump.Name) {
			out = append(out, dump)
		}
	}
	return out
}

func librusecImportDump(name string) bool {
	switch strings.ToLower(filepath.Base(name)) {
	case "libbook.sql",
		"libavtor.sql",
		"libavtors.sql",
		"libgenre.sql",
		"libgenres.sql",
		"libseq.sql",
		"libseqs.sql",
		"librate.sql":
		return true
	default:
		return false
	}
}

func (i *Importer) importDump(ctx context.Context, client string, dump DumpFile) error {
	f, err := os.Open(dump.Path)
	if err != nil {
		return fmt.Errorf("open dump %q: %w", dump.Path, err)
	}
	defer f.Close()
	stdin := importFixupReader(f, dump.Name)

	args, err := i.clientArgs()
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, client, args...)
	cmd.Stdin = stdin
	cmd.Stdout = i.logOut
	cmd.Stderr = i.logOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("import dump %q with %q: %w", dump.Path, client, err)
	}
	return nil
}

func isAuthorAliasDump(name string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(name)), "libavtoraliase")
}

func importFixupReader(r io.Reader, dumpName string) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		_ = pw.CloseWithError(writeImportFixup(pw, r, isAuthorAliasDump(dumpName)))
	}()
	return pr
}

func writeImportFixup(w io.Writer, r io.Reader, fixAuthorAliases bool) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			fixed := fixImportSQLLine(line, fixAuthorAliases)
			if fixed != "" {
				if _, writeErr := io.WriteString(w, fixed); writeErr != nil {
					return writeErr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func writeAuthorAliasFixup(w io.Writer, r io.Reader) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if _, writeErr := io.WriteString(w, fixImportSQLLine(line, true)); writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func fixImportSQLLine(line string, fixAuthorAliases bool) string {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(strings.ToUpper(trimmed), "ALTER DATABASE ") {
		return ""
	}
	if fixAuthorAliases {
		return fixAuthorAliasSQLLine(line)
	}
	return line
}

func fixAuthorAliasSQLLine(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]
	if strings.HasPrefix(trimmed, "`AliaseId` int(11) NOT NULL auto_increment,") ||
		strings.HasPrefix(trimmed, "`AliaseId` int(11) NOT NULL AUTO_INCREMENT,") {
		return line + indent + "`dummyId` int(11) NOT NULL default '0',\n"
	}
	const insertPrefix = "INSERT INTO `libavtoraliase`"
	if rest, ok := strings.CutPrefix(trimmed, insertPrefix); ok {
		return indent + "INSERT INTO `libavtoraliase` (dummyId, BadId, GoodId)" + rest
	}
	return line
}

func (i *Importer) clientArgs() ([]string, error) {
	args := []string{"--default-character-set=utf8"}
	dbName := i.cfg.Name
	if i.cfg.DSN != "" {
		mc, err := mysql.ParseDSN(i.cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("parse database DSN for client import: %w", err)
		}
		if mc.DBName != "" {
			dbName = mc.DBName
		}
		switch mc.Net {
		case "unix":
			args = append(args, "--socket", mc.Addr)
		default:
			host, port := splitHostPortDefault(mc.Addr, "127.0.0.1", "3306")
			args = append(args, "--protocol", "tcp", "--host", host, "--port", port)
		}
		if mc.User != "" {
			args = append(args, "--user", mc.User)
		}
		if mc.Passwd != "" {
			args = append(args, "--password="+mc.Passwd)
		} else {
			args = append(args, "--skip-ssl")
		}
		return append(args, dbName), nil
	}

	if i.cfg.Protocol == "unix" {
		args = append(args, "--socket", i.cfg.Host)
	} else {
		args = append(args, "--protocol", "tcp", "--host", i.cfg.Host, "--port", fmt.Sprintf("%d", i.cfg.Port))
	}
	args = append(args, "--user", i.cfg.User)
	if i.cfg.Password != "" {
		args = append(args, "--password="+i.cfg.Password)
	} else {
		args = append(args, "--skip-ssl")
	}
	return append(args, dbName), nil
}

func splitHostPortDefault(addr string, defaultHost string, defaultPort string) (string, string) {
	if addr == "" {
		return defaultHost, defaultPort
	}
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" {
			host = defaultHost
		}
		if port == "" {
			port = defaultPort
		}
		return host, port
	}
	parts := strings.Split(addr, ":")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return addr, defaultPort
}

func readDumpCompleted(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("open dump %q: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", "", fmt.Errorf("stat dump %q: %w", path, err)
	}
	start := max(st.Size()-4096, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("seek dump %q: %w", path, err)
	}

	re := regexp.MustCompile(`--\s*Dump\s+completed\s+on\s+(\d{4}-\d{2}-\d{2})(?:\s+(\d{1,2}:\d{2}:\d{2}))?`)
	s := bufio.NewScanner(f)
	for s.Scan() {
		if m := re.FindStringSubmatch(s.Text()); m != nil {
			completed := m[1]
			if m[2] != "" {
				completed += "T" + zeroPadHour(m[2])
			}
			return m[1], completed, nil
		}
	}
	if err := s.Err(); err != nil {
		return "", "", fmt.Errorf("read dump %q: %w", path, err)
	}
	return "", "", nil
}

func zeroPadHour(value string) string {
	if len(value) == len("1:00:00") {
		return "0" + value
	}
	return value
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
