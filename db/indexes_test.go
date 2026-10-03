package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestEnsureReadIndexes(t *testing.T) {
	t.Parallel()
	metadataErr := errors.New("metadata unavailable")
	createErr := errors.New("index creation failed")
	tests := []struct {
		name        string
		format      Format
		absent      bool
		existing    int64
		metadataErr error
		createErr   error
		wantCreates int
		wantErr     error
	}{
		{name: "missing index", wantCreates: 1},
		{name: "existing leading key", existing: 1},
		{name: "optional table absent", absent: true},
		{name: "Librusec", format: FormatLibrusecCurrent},
		{name: "metadata failure", metadataErr: metadataErr, wantErr: metadataErr},
		{name: "DDL failure", createErr: createErr, wantCreates: 1, wantErr: createErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			repo.format = tt.format
			repo.tables["libbannotations"] = !tt.absent
			existing, creates := tt.existing, 0
			testDriverMu.Lock()
			testDriverHandlers[t.Name()] = func(query string, _ []driver.NamedValue) (testRows, error) {
				switch {
				case strings.Contains(query, "FROM information_schema.statistics"):
					if tt.metadataErr != nil {
						return testRows{}, tt.metadataErr
					}
					return rows([]string{"count"}, []driver.Value{existing}), nil
				case query == "CREATE INDEX metabib_bookid_nid ON libbannotations (BookId, nid)":
					creates++
					if tt.createErr != nil {
						return testRows{}, tt.createErr
					}
					existing++
					return testRows{}, nil
				default:
					return testRows{}, fmt.Errorf("unexpected query: %s", query)
				}
			}
			testDriverMu.Unlock()
			if err := repo.EnsureReadIndexes(context.Background(), nil); !errors.Is(err, tt.wantErr) {
				t.Fatalf("EnsureReadIndexes() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil {
				if err := repo.EnsureReadIndexes(context.Background(), nil); err != nil {
					t.Fatalf("EnsureReadIndexes() second call error = %v", err)
				}
			}
			if creates != tt.wantCreates {
				t.Fatalf("index creation calls = %d, want %d", creates, tt.wantCreates)
			}
		})
	}
}
