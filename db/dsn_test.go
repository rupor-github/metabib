package db

import (
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"metabib/config"
)

func TestDSNFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cfg          config.DatabaseConfig
		withDatabase bool
		wantNet      string
		wantAddr     string
		wantDB       string
	}{
		{
			name:         "tcp with database",
			cfg:          config.DatabaseConfig{Protocol: "tcp", Host: "127.0.0.1", Port: 3307, User: "u", Password: "p", Name: "lib"},
			withDatabase: true,
			wantNet:      "tcp",
			wantAddr:     "127.0.0.1:3307",
			wantDB:       "lib",
		},
		{
			name:         "unix without database",
			cfg:          config.DatabaseConfig{Protocol: "unix", Host: "/tmp/metabib.sock", User: "root", Name: "lib"},
			withDatabase: false,
			wantNet:      "unix",
			wantAddr:     "/tmp/metabib.sock",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn, err := DSN(tt.cfg, tt.withDatabase)
			if err != nil {
				t.Fatalf("DSN() error = %v", err)
			}
			mc, err := mysql.ParseDSN(dsn)
			if err != nil {
				t.Fatalf("ParseDSN(%q) error = %v", dsn, err)
			}
			if mc.Net != tt.wantNet || mc.Addr != tt.wantAddr || mc.DBName != tt.wantDB {
				t.Fatalf("parsed DSN net=%q addr=%q db=%q", mc.Net, mc.Addr, mc.DBName)
			}
			if mc.Collation != "utf8mb4_unicode_ci" {
				t.Fatalf("collation = %q", mc.Collation)
			}
		})
	}
}

func TestDSNFromExplicitDSN(t *testing.T) {
	t.Parallel()

	cfg := config.DatabaseConfig{DSN: "user:pass@tcp(localhost:3306)/", Name: "fallback"}
	dsn, err := DSN(cfg, true)
	if err != nil {
		t.Fatalf("DSN() error = %v", err)
	}
	if !strings.Contains(dsn, "/fallback?") {
		t.Fatalf("DSN() = %q, want fallback database", dsn)
	}

	name, err := dsnDatabaseName("user:pass@tcp(localhost:3306)/library")
	if err != nil {
		t.Fatalf("dsnDatabaseName() error = %v", err)
	}
	if name != "library" {
		t.Fatalf("dsnDatabaseName() = %q", name)
	}
}
