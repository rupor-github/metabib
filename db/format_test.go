package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectDumpFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    Format
	}{
		{name: "flibusta", content: "CREATE TABLE `libbook` (`BookId` int);", want: FormatFlibustaCurrent},
		{name: "librusec", content: "CREATE TABLE `libbook` (`bid` int);", want: FormatLibrusecCurrent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "libbook.sql")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write test dump: %v", err)
			}
			got, err := DetectDumpFormat([]DumpFile{{Path: path, Name: "libbook.sql"}})
			if err != nil {
				t.Fatalf("DetectDumpFormat() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DetectDumpFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectDumpFormatMissingLibbook(t *testing.T) {
	t.Parallel()

	_, err := DetectDumpFormat(nil)
	if !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("DetectDumpFormat() error = %v, want ErrUnknownFormat", err)
	}
}

func TestRepositoryDetectFormatFromCachedSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tables map[string]bool
		cols   map[string]map[string]bool
		want   Format
	}{
		{
			name:   "flibusta",
			tables: map[string]bool{"libavtors": false, "libgenres": false, "libseqs": false},
			cols:   map[string]map[string]bool{"libbook": {"BookId": true, "bid": false}},
			want:   FormatFlibustaCurrent,
		},
		{
			name:   "librusec",
			tables: map[string]bool{"libavtors": true, "libgenres": true, "libseqs": true},
			cols:   map[string]map[string]bool{"libbook": {"BookId": false, "bid": true}},
			want:   FormatLibrusecCurrent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &Repository{tables: tt.tables, cols: tt.cols}
			got, err := repo.DetectFormat(t.Context())
			if err != nil {
				t.Fatalf("DetectFormat() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DetectFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}
