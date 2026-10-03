package db

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"metabib/config"
)

func TestImportWorkersMariaDB(t *testing.T) {
	if os.Getenv("METABIB_TEST_MARIADB") != "1" {
		t.Skip("set METABIB_TEST_MARIADB=1 to test imports against a private MariaDB instance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	runtime, err := PrepareRuntime(ctx, config.DatabaseConfig{
		Managed: true, Temporary: true, Protocol: "unix", Name: "metabib_import_test",
	}, true, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	dir := t.TempDir()
	inputs := map[string]string{
		"libbook.sql":  "DROP TABLE IF EXISTS libbook; CREATE TABLE libbook (`BookId` INT PRIMARY KEY); INSERT INTO libbook VALUES (1);",
		"libavtor.sql": "DROP TABLE IF EXISTS libavtor; CREATE TABLE libavtor (BookId INT); INSERT INTO libavtor VALUES (1);",
		"libgenre.sql": "DROP TABLE IF EXISTS libgenre; CREATE TABLE libgenre (BookId INT); INSERT INTO libgenre VALUES (1);",
	}
	for name, sql := range inputs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dumps, _, err := DiscoverDumps(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			cfg := runtime.Config
			cfg.ImportWorkers = workers
			importer := NewImporter(cfg, runtime.Client, nil, io.Discard, false, true)
			if err := importer.PrepareDatabase(ctx); err != nil {
				t.Fatal(err)
			}
			if err := importer.ImportDumps(ctx, dumps); err != nil {
				t.Fatal(err)
			}
			repo, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			for _, table := range []string{"libbook", "libavtor", "libgenre"} {
				var count int
				if err := repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("%s has %d records after import, want 1", table, count)
				}
			}
		})
	}
}
