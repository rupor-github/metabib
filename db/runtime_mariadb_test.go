package db

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"metabib/config"
)

func TestManagedServerArgsMariaDB(t *testing.T) {
	if os.Getenv("METABIB_TEST_MARIADB") != "1" {
		t.Skip("set METABIB_TEST_MARIADB=1 to verify managed-server options against MariaDB")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	runtime, err := PrepareRuntime(ctx, config.DatabaseConfig{
		Managed: true, Temporary: true, Protocol: "unix",
		ServerArgs: []string{
			"--key-buffer-size=16M",
			"--max-connections=17",
			"--datadir=" + filepath.Join(t.TempDir(), "overridden"),
		},
	}, false, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	dsn, err := DSN(runtime.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var bufferSize, connections int64
	var socket, dataDir string
	if err := database.QueryRowContext(ctx,
		"SELECT @@global.key_buffer_size, @@global.max_connections, @@global.socket, @@global.datadir",
	).Scan(&bufferSize, &connections, &socket, &dataDir); err != nil {
		t.Fatal(err)
	}
	if bufferSize != 16*1024*1024 || connections != 17 {
		t.Fatalf("server options not applied: key_buffer_size=%d max_connections=%d", bufferSize, connections)
	}
	if filepath.Clean(socket) != filepath.Clean(runtime.Config.Socket) ||
		filepath.Clean(dataDir) != filepath.Clean(runtime.Config.DataDir) {
		t.Fatalf("managed locations changed: socket=%q datadir=%q", socket, dataDir)
	}
}
