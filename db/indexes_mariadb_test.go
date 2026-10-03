package db

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"metabib/config"
	"metabib/model"
)

// Opt in with METABIB_TEST_ANNOTATION_DUMP=/path/to/lib.b.annotations.sql.
// The dump is imported into a private temporary MariaDB instance and removed on shutdown.
func TestAnnotationReadIndexMariaDB(t *testing.T) {
	dump := os.Getenv("METABIB_TEST_ANNOTATION_DUMP")
	if dump == "" {
		t.Skip("set METABIB_TEST_ANNOTATION_DUMP to test annotation lookups against an actual SQL dump")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	runtime, err := PrepareRuntime(ctx, config.DatabaseConfig{
		Managed: true, Temporary: true, Protocol: "unix", Name: "metabib_index_test",
	}, true, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close MariaDB runtime: %v", err)
		}
	})
	importer := NewImporter(runtime.Config, runtime.Client, nil, io.Discard, false, true)
	if err := importer.PrepareDatabase(ctx); err != nil {
		t.Fatal(err)
	}
	if err := importer.importDump(ctx, runtime.Client, DumpFile{Path: dump, Name: filepath.Base(dump)}); err != nil {
		t.Fatal(err)
	}
	dsn, err := DSN(runtime.Config, true)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo := &Repository{
		db: database, format: FormatFlibustaCurrent,
		tables: map[string]bool{"libbannotations": true},
	}
	query, args := inQuery(
		"SELECT BookId, nid, Title, Body FROM libbannotations WHERE BookId IN (",
		makeAnnotationBatch(100000),
		") ORDER BY BookId, nid",
	)
	explain := func() string {
		t.Helper()
		var plan string
		if err := database.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query, args...).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		return plan
	}
	beforePlan := explain()
	readBatches := func() ([]map[int64]model.DatabaseSource, time.Duration) {
		t.Helper()
		start := time.Now()
		results := make([]map[int64]model.DatabaseSource, 10)
		for batch := range results {
			ids := makeAnnotationBatch(100000 + int64(batch)*50000)
			out := make(map[int64]model.DatabaseSource, len(ids))
			for _, id := range ids {
				out[id] = model.DatabaseSource{}
			}
			if err := repo.attachAnnotations(ctx, ids, out); err != nil {
				t.Fatal(err)
			}
			results[batch] = out
		}
		return results, time.Since(start)
	}
	before, beforeElapsed := readBatches()
	indexStart := time.Now()
	if err := repo.EnsureReadIndexes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	indexElapsed := time.Since(indexStart)
	afterPlan := explain()
	after, afterElapsed := readBatches()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("annotation results changed after creating the read index")
	}
	if !strings.Contains(afterPlan, `"access_type": "range"`) && !strings.Contains(afterPlan, `"access_type": "ref"`) {
		t.Fatalf("annotation lookup did not use an indexed lookup: %s", afterPlan)
	}
	if err := repo.EnsureReadIndexes(ctx, nil); err != nil {
		t.Fatalf("second index preparation: %v", err)
	}
	t.Logf("before plan: %s", beforePlan)
	t.Logf("after plan: %s", afterPlan)
	t.Logf("10 annotation batches: before=%s after=%s speedup=%.1fx index_creation=%s",
		beforeElapsed, afterElapsed, float64(beforeElapsed)/float64(afterElapsed), indexElapsed)
}

func makeAnnotationBatch(start int64) []int64 {
	ids := make([]int64, 256)
	for idx := range ids {
		ids[idx] = start + int64(idx)
	}
	return ids
}
