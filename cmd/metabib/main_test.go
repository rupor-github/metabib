package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/db"
	"metabib/jsonl"
	"metabib/library"
	"metabib/model"
	"metabib/state"
)

func TestRollupCommandHasNoKeepUpdatesFlag(t *testing.T) {
	t.Parallel()

	for _, flag := range rollupCommand().Flags {
		if slices.Contains(flag.Names(), "keep-updates") {
			t.Fatal("rollup command still exposes --keep-updates")
		}
	}
}

func TestMergeCommandHasNoOutputPartSizeFlag(t *testing.T) {
	t.Parallel()

	for _, flag := range mergeCommand().Flags {
		if slices.Contains(flag.Names(), "output-part-size") {
			t.Fatal("merge command still exposes --output-part-size")
		}
	}
}

func TestMergeCommandHasAllowMissingFlag(t *testing.T) {
	t.Parallel()

	for _, flag := range mergeCommand().Flags {
		if slices.Contains(flag.Names(), "allow-missing") {
			return
		}
	}
	t.Fatal("merge command does not expose --allow-missing")
}

func TestRecordFileKeys(t *testing.T) {
	t.Parallel()

	rec := model.Record{
		ID:     model.RecordID{FileName: "Book", Extension: "fb2"},
		Source: model.RecordSources{Database: model.DatabaseSource{Filenames: []string{"Other.FB2"}}},
	}
	keys := recordFileKeys(rec)
	if containsString(keys, "book") {
		t.Fatalf("recordFileKeys() = %#v, should skip nonnumeric extensionless stem", keys)
	}
	for _, want := range []string{"book.fb2", "other.fb2"} {
		if !containsString(keys, want) {
			t.Fatalf("recordFileKeys() = %#v, missing %q", keys, want)
		}
	}
}

func TestRecordFileKeysKeepsNumericStem(t *testing.T) {
	t.Parallel()

	rec := model.Record{ID: model.RecordID{BookID: 42, FileName: "42", Extension: "fb2"}}
	keys := recordFileKeys(rec)
	for _, want := range []string{"42", "42.fb2"} {
		if !containsString(keys, want) {
			t.Fatalf("recordFileKeys() = %#v, missing %q", keys, want)
		}
	}
}

func TestRecordFileKeysSkipsNumericStemForDifferentBookID(t *testing.T) {
	t.Parallel()

	rec := model.Record{ID: model.RecordID{BookID: 200130, FileName: "1968", Extension: "pdf"}}
	keys := recordFileKeys(rec)
	if containsString(keys, "1968") {
		t.Fatalf("recordFileKeys() = %#v, should skip numeric stem for different book ID", keys)
	}
	if !containsString(keys, "1968.pdf") {
		t.Fatalf("recordFileKeys() = %#v, missing exact filename", keys)
	}
}

func TestDatabaseIndexUsesExactAliasesForNonnumericStems(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "database.manifest.zst")
	stem := "Megan_Lindholm_The_Wizard_of_the_Pigeons"
	writeTestManifest(
		t,
		manifestPath,
		model.Record{
			Schema: "metabib.record/1",
			ID:     model.RecordID{Library: "flibusta", BookID: 135445, FileName: stem, Extension: "pdf"},
			Source: model.RecordSources{Database: model.DatabaseSource{
				Present:   true,
				Book:      &model.DBBook{BookID: 135445},
				Filenames: []string{stem + ".pdf"},
			}},
		},
		model.Record{
			Schema: "metabib.record/1",
			ID:     model.RecordID{Library: "flibusta", BookID: 135446, FileName: stem, Extension: "rar"},
			Source: model.RecordSources{Database: model.DatabaseSource{
				Present:   true,
				Book:      &model.DBBook{BookID: 135446},
				Filenames: []string{stem + ".rar"},
			}},
		},
	)
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	index, err := loadDatabaseIndex(ctx, manifestPath, logger)
	if err != nil {
		t.Fatalf("loadDatabaseIndex() error = %v", err)
	}
	key := strings.ToLower(stem)
	if _, ok := index.byFile[key]; ok {
		t.Fatalf("extensionless stem stayed indexed: %#v", index.byFile)
	}
	if _, ok := index.ambiguousFiles[key]; ok {
		t.Fatalf("extensionless stem marked ambiguous: %#v", index.ambiguousFiles)
	}
	if got := index.byFile[key+".pdf"]; got != 135445 {
		t.Fatalf("pdf alias book ID = %d, want 135445", got)
	}
	if got := index.byFile[key+".rar"]; got != 135446 {
		t.Fatalf("rar alias book ID = %d, want 135446", got)
	}
	if logs.FilterMessage("Ambiguous database filename ignored").Len() != 0 {
		t.Fatalf("logs = %#v, want no ambiguous filename debug log", logs.AllUntimed())
	}
}

func TestDatabaseIndexPrefersActiveFilenameCollision(t *testing.T) {
	t.Parallel()

	key := "eu-ukraine association agenda ( soglashenie ob associacii s ukrainoy).pdf"
	deleted := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 350843, Deleted: "1"}}
	active := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 350852, Deleted: "0"}}
	index := databaseIndex{
		byID: map[int64]model.DatabaseSource{
			350843: deleted,
			350852: active,
		},
		byFile:         make(map[string]int64),
		ambiguousFiles: make(map[string]struct{}),
	}
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	index.addDatabaseFile(key, deleted, logger)
	index.addDatabaseFile(key, active, logger)

	if got := index.byFile[key]; got != 350852 {
		t.Fatalf("indexed book ID = %d, want active 350852", got)
	}
	if _, ok := index.ambiguousFiles[key]; ok {
		t.Fatalf("filename marked ambiguous: %#v", index.ambiguousFiles)
	}
	if logs.FilterMessage("Ambiguous database filename ignored").Len() != 0 {
		t.Fatalf("logs = %#v, want no ambiguous filename debug log", logs.AllUntimed())
	}
}

func TestDatabaseIndexPrefersJoinedOwnerFilenameCollision(t *testing.T) {
	t.Parallel()

	key := "serzhantiha.doc"
	olderDeleted := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 318346, Deleted: "1"}}
	joinedDeleted := model.DatabaseSource{
		Present:     true,
		Book:        &model.DBBook{BookID: 437115, Deleted: "1"},
		JoinedBooks: []model.DBJoinedBook{{BadID: 437115, GoodID: 438203, RealID: 481401}},
	}
	index := databaseIndex{
		byID: map[int64]model.DatabaseSource{
			318346: olderDeleted,
			437115: joinedDeleted,
		},
		byFile:         make(map[string]int64),
		ambiguousFiles: make(map[string]struct{}),
	}
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	index.addDatabaseFile(key, olderDeleted, logger)
	index.addDatabaseFile(key, joinedDeleted, logger)

	if got := index.byFile[key]; got != 437115 {
		t.Fatalf("indexed book ID = %d, want joined alias owner 437115", got)
	}
	if _, ok := index.ambiguousFiles[key]; ok {
		t.Fatalf("filename marked ambiguous: %#v", index.ambiguousFiles)
	}
	if logs.FilterMessage("Ambiguous database filename ignored").Len() != 0 {
		t.Fatalf("logs = %#v, want no ambiguous filename debug log", logs.AllUntimed())
	}
}

func TestDatabaseIndexStoresJoinedAliasOwner(t *testing.T) {
	t.Parallel()

	key := "a._gyul-_nazaryants_plenniki_barsova_uschelya.rar"
	joinedDeleted := model.DatabaseSource{
		Present:     true,
		Book:        &model.DBBook{BookID: 176497, Deleted: "1"},
		JoinedBooks: []model.DBJoinedBook{{BadID: 176497, GoodID: 184249, RealID: 184249}},
	}
	index := databaseIndex{
		byID:           map[int64]model.DatabaseSource{176497: joinedDeleted},
		byFile:         make(map[string]int64),
		ambiguousFiles: make(map[string]struct{}),
	}

	index.addDatabaseFile(key, joinedDeleted, nil)

	if got := index.byFile[key]; got != 176497 {
		t.Fatalf("indexed book ID = %d, want joined alias owner 176497", got)
	}
}

func TestDatabaseIndexPrefersJoinedRealIDFilenameCollisionTie(t *testing.T) {
	t.Parallel()

	key := "same.fb2"
	otherActive := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 42, Deleted: "0"}}
	realActive := model.DatabaseSource{
		Present:     true,
		Book:        &model.DBBook{BookID: 44, Deleted: "0"},
		JoinedBooks: []model.DBJoinedBook{{BadID: 43, GoodID: 44, RealID: 44}},
	}
	index := databaseIndex{
		byID: map[int64]model.DatabaseSource{
			42: otherActive,
			44: realActive,
		},
		byFile:         make(map[string]int64),
		ambiguousFiles: make(map[string]struct{}),
	}

	index.addDatabaseFile(key, otherActive, nil)
	index.addDatabaseFile(key, realActive, nil)

	if got := index.byFile[key]; got != 44 {
		t.Fatalf("indexed book ID = %d, want joined real 44", got)
	}
	if _, ok := index.ambiguousFiles[key]; ok {
		t.Fatalf("filename marked ambiguous: %#v", index.ambiguousFiles)
	}
}

func TestDatabaseIndexMarksDuplicateFilenamesAmbiguous(t *testing.T) {
	t.Parallel()

	index := databaseIndex{byFile: make(map[string]int64), ambiguousFiles: make(map[string]struct{})}
	first := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 42}}
	same := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 42}}
	other := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 43}}
	ignored := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 44}}
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	index.addDatabaseFile("book.fb2", first, logger)
	index.addDatabaseFile("book.fb2", same, logger)
	if got := index.byFile["book.fb2"]; got != 42 {
		t.Fatalf("indexed book ID = %d, want 42", got)
	}
	index.addDatabaseFile("book.fb2", other, logger)
	index.addDatabaseFile("book.fb2", ignored, logger)
	if _, ok := index.byFile["book.fb2"]; ok {
		t.Fatalf("ambiguous key stayed indexed: %#v", index.byFile)
	}
	if _, ok := index.ambiguousFiles["book.fb2"]; !ok {
		t.Fatalf("ambiguous files = %#v, want book.fb2", index.ambiguousFiles)
	}
	if logs.FilterMessage("Ambiguous database filename ignored").Len() != 1 {
		t.Fatalf("logs = %#v, want one ambiguous filename debug log", logs.AllUntimed())
	}
}

func TestDumpDirDatesDiffer(t *testing.T) {
	t.Parallel()

	if dumpDirDatesDiffer([]db.DumpFile{{DumpDate: "2026-06-20"}, {DumpDate: "2026-06-20"}}) {
		t.Fatal("dumpDirDatesDiffer() = true for same dates")
	}
	if !dumpDirDatesDiffer([]db.DumpFile{{DumpDate: "2026-06-20"}, {DumpDate: "2026-06-21"}}) {
		t.Fatal("dumpDirDatesDiffer() = false for different dates")
	}
}

func TestImportProvenanceFromDatabaseManifest(t *testing.T) {
	t.Parallel()

	manifest := library.DatabaseManifestDecision{
		DumpDir:  "/dumps",
		DumpDate: "2026-06-20",
		Dumps: []library.DumpManifestSource{{
			Path:          "/dumps/libbook.sql",
			Name:          "libbook.sql",
			DumpDate:      "2026-06-20",
			DumpCompleted: "2026-06-20T02:19:33",
			Modified:      "2026-06-20T02:19:34Z",
			MD5:           "abc123",
		}},
	}
	got := importProvenanceFromDatabaseManifest(manifest)
	if got.DumpDir != manifest.DumpDir || got.DumpDate != manifest.DumpDate || len(got.Dumps) != 1 || got.Dumps[0].MD5 != manifest.Dumps[0].MD5 {
		t.Fatalf("importProvenanceFromDatabaseManifest() = %#v", got)
	}
}

func TestWriteOutput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "out")
	err := writeOutput(context.Background(), path, "", nil, func(out *jsonl.Writer) error {
		return out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "out.jsonl.zst"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestWriteOutputNoCompression(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "out")
	err := writeOutput(context.Background(), path, "none", nil, func(out *jsonl.Writer) error {
		return out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "out.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestWriteOutputWritesDatasetHeaderFirst(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "all")
	err := writeOutput(context.Background(), path, "none", nil, func(out *jsonl.Writer) error {
		if err := out.WriteValue(model.Dataset{Schema: model.DatasetSchemaV1, RecordSchema: model.DatasetRecordSchemaV1, Records: 1}); err != nil {
			return err
		}
		bookID := int64(42)
		return out.WriteValue(model.DatasetRecord{
			Schema: model.DatasetRecordSchemaV1,
			Record: model.RecordDescriptor{
				Locator: model.RecordLocator{Kind: "database_book", Source: "database", BookID: &bookID},
			},
		})
	})
	if err != nil {
		t.Fatalf("writeOutput() error = %v", err)
	}
	data, err := os.ReadFile(path + ".jsonl")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %#v, want header and one record", lines)
	}
	if !strings.Contains(lines[0], `"schema":"metabib.dataset/1"`) || !strings.Contains(lines[1], `"schema":"metabib.dataset_record/1"`) {
		t.Fatalf("output lines = %#v", lines)
	}
}

func TestWriteOutputReturnsCloseError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	err := writeOutput(context.Background(), path, "none", nil, func(out *jsonl.Writer) error {
		if err := out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: 42}}); err != nil {
			return err
		}
		finalPath := filepath.Join(dir, "out.jsonl")
		if err := os.Mkdir(finalPath, 0o755); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		t.Fatal("writeOutput() error = nil, want close rename error")
	}
}

func TestWriteOutputAbortsOnWriteError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeErr := assertErr("write failed")
	err := writeOutput(context.Background(), filepath.Join(dir, "out"), "none", nil, func(out *jsonl.Writer) error {
		for _, id := range []int64{41, 42} {
			if err := out.Write(model.Record{Schema: "metabib.record/1", ID: model.RecordID{BookID: id}}); err != nil {
				return err
			}
		}
		return writeErr
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeOutput() error = %v, want write error", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "out*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches after failed writeOutput = %#v, want none", matches)
	}
}

func TestMergeArchiveManifestsRewritesArchivePath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	oldPath := filepath.Join(dir, "old", "books.zip")
	writeTestManifest(t, manifestPath, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			BookID:  1,
			Archive: &model.ArchiveInfo{Path: oldPath, Entry: "1.fb2"},
		},
	})

	currentPath := filepath.Join(dir, "new", "books.zip")
	out, err := jsonl.CreateCompressed(filepath.Join(dir, "out"), jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if _, err := mergeArchiveManifests(
		ctx,
		[]library.ArchiveManifestDecision{{ArchivePath: currentPath, ManifestPath: manifestPath}},
		databaseIndex{},
		map[string]string{currentPath: "archive-0001"},
		false,
		out,
		nil,
	); err != nil {
		t.Fatalf("mergeArchiveManifests() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close output error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "out.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want one output", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"schema":"metabib.dataset_record/1"`) || !strings.Contains(string(data), `"source":"archive-0001"`) {
		t.Fatalf("merged output = %s, want v2 archive source", data)
	}
	if strings.Contains(string(data), oldPath) || strings.Contains(string(data), currentPath) {
		t.Fatalf("merged output = %s, should not contain source archive paths", data)
	}
}

func TestMergeArchiveManifestsRecordsFilenameMatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	archivePath := filepath.Join(dir, "books.zip")
	writeTestManifest(t, manifestPath, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:  "flibusta",
			BookID:   7,
			FileName: "7",
			Archive:  &model.ArchiveInfo{Path: archivePath, Entry: "7.fb2"},
		},
	})
	dbSource := model.DatabaseSource{
		Present:   true,
		Book:      &model.DBBook{BookID: 42},
		Filenames: []string{"7.fb2"},
	}
	out, err := jsonl.CreateCompressed(filepath.Join(dir, "out"), jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if _, err := mergeArchiveManifests(
		ctx,
		[]library.ArchiveManifestDecision{{ArchivePath: archivePath, ManifestPath: manifestPath}},
		databaseIndex{
			byID:   map[int64]model.DatabaseSource{42: dbSource},
			byFile: map[string]int64{"7.fb2": 42},
		},
		map[string]string{archivePath: "archive-0001"},
		false,
		out,
		nil,
	); err != nil {
		t.Fatalf("mergeArchiveManifests() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close output error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, want := range []string{
		`"method":"filename_alias"`,
		`"code":"catalog_id_conflict"`,
		`"book_id":42`,
		`"observation":"archive","basis":"numeric_entry_stem"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("merged output = %s, missing %s", data, want)
		}
	}
}

func TestMergeArchiveManifestsRecordsJoinedFilenameAliasOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	archivePath := filepath.Join(dir, "books.zip")
	entry := "A._Gyul-_Nazaryants_Plenniki_Barsova_uschelya.rar"
	writeTestManifest(t, manifestPath, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			FileName:  "A._Gyul-_Nazaryants_Plenniki_Barsova_uschelya",
			Extension: "rar",
			Archive:   &model.ArchiveInfo{Path: archivePath, Entry: entry},
		},
	})
	joinedAliasOwner := model.DatabaseSource{
		Present: true,
		Book: &model.DBBook{
			BookID:   176497,
			Title:    "Пленники Барсова ущелья",
			FileType: "rtf",
			Deleted:  "1",
		},
		JoinedBooks: []model.DBJoinedBook{{BadID: 176497, GoodID: 184249, RealID: 184249}},
	}
	realFB2 := model.DatabaseSource{
		Present: true,
		Book: &model.DBBook{
			BookID:   184249,
			Title:    "Пленники Барсова ущелья",
			FileType: "fb2",
		},
	}
	out, err := jsonl.CreateCompressed(filepath.Join(dir, "out"), jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if _, err := mergeArchiveManifests(
		ctx,
		[]library.ArchiveManifestDecision{{ArchivePath: archivePath, ManifestPath: manifestPath}},
		databaseIndex{
			byID: map[int64]model.DatabaseSource{
				176497: joinedAliasOwner,
				184249: realFB2,
			},
			byFile: map[string]int64{strings.ToLower(entry): 176497},
		},
		map[string]string{archivePath: "archive-0001"},
		false,
		out,
		nil,
	); err != nil {
		t.Fatalf("mergeArchiveManifests() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close output error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, want := range []string{
		`"method":"filename_alias"`,
		`"locator":{"book_id":176497}`,
		`"file_type":"rtf"`,
		`"bad":"176497"`,
		`"real":"184249"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("merged output = %s, missing %s", data, want)
		}
	}
	if strings.Contains(string(data), `"locator":{"book_id":184249}`) ||
		strings.Contains(string(data), `"file_type":"fb2"`) {
		t.Fatalf("merged output = %s, should not use joined real book as filename match", data)
	}
}

func TestMergeArchiveManifestsRecordsConflictingFilenameEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	archivePath := filepath.Join(dir, "books.zip")
	writeTestManifest(t, manifestPath, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:  "flibusta",
			BookID:   7,
			FileName: "7",
			Archive:  &model.ArchiveInfo{Path: archivePath, Entry: "7.fb2"},
		},
	})
	numericSource := model.DatabaseSource{Present: true, Book: &model.DBBook{BookID: 7}}
	out, err := jsonl.CreateCompressed(filepath.Join(dir, "out"), jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if _, err := mergeArchiveManifests(
		ctx,
		[]library.ArchiveManifestDecision{{ArchivePath: archivePath, ManifestPath: manifestPath}},
		databaseIndex{
			byID:   map[int64]model.DatabaseSource{7: numericSource},
			byFile: map[string]int64{"7.fb2": 42},
		},
		map[string]string{archivePath: "archive-0001"},
		false,
		out,
		nil,
	); err != nil {
		t.Fatalf("mergeArchiveManifests() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close output error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, want := range []string{
		`"method":"numeric_entry_stem"`,
		`"code":"conflicting_match_evidence"`,
		`"book_id":7`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("merged output = %s, missing %s", data, want)
		}
	}
}

func TestMergeArchiveManifestsPrefersConflictingFilenameAliasOverNumericStem(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "books.manifest.zst")
	archivePath := filepath.Join(dir, "f.usr-198702-200863.zip")
	writeTestManifest(t, manifestPath, model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			BookID:    1968,
			FileName:  "1968",
			Extension: "pdf",
			Archive:   &model.ArchiveInfo{Path: archivePath, Entry: "1968.pdf"},
		},
	})
	numericSource := model.DatabaseSource{
		Present: true,
		Book:    &model.DBBook{BookID: 1968, Title: "Numeric FB2", FileType: "fb2"},
	}
	aliasSource := model.DatabaseSource{
		Present: true,
		Book:    &model.DBBook{BookID: 200130, Title: "Exact PDF", FileType: "pdf"},
		Filenames: []string{
			"1968.pdf",
		},
	}
	out, err := jsonl.CreateCompressed(filepath.Join(dir, "out"), jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if _, err := mergeArchiveManifests(
		ctx,
		[]library.ArchiveManifestDecision{{ArchivePath: archivePath, ManifestPath: manifestPath}},
		databaseIndex{
			byID: map[int64]model.DatabaseSource{
				1968:   numericSource,
				200130: aliasSource,
			},
			byFile: map[string]int64{
				"1968":     1968,
				"1968.pdf": 200130,
			},
		},
		map[string]string{archivePath: "archive-0001"},
		false,
		out,
		nil,
	); err != nil {
		t.Fatalf("mergeArchiveManifests() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close output error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, want := range []string{
		`"method":"filename_alias"`,
		`"input":"1968.pdf"`,
		`"book_id":200130`,
		`"file_type":"pdf"`,
		`"code":"catalog_id_conflict"`,
		`"observation":"archive","basis":"numeric_entry_stem"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("merged output = %s, missing %s", data, want)
		}
	}
	for _, unwanted := range []string{
		`"method":"numeric_entry_stem"`,
		`"locator":{"book_id":1968}`,
		`"title":[{"observation":"db","value":"Numeric FB2"}]`,
	} {
		if strings.Contains(string(data), unwanted) {
			t.Fatalf("merged output = %s, should not contain %s", data, unwanted)
		}
	}
}

func writeTestManifest(t *testing.T, path string, records ...model.Record) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", path, err)
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		f.Close()
		t.Fatalf("NewWriter() error = %v", err)
	}
	if err := jsonv2.MarshalWrite(enc, map[string]any{"schema": "metabib.archive_manifest/1", "records": len(records)}); err != nil {
		enc.Close()
		f.Close()
		t.Fatalf("MarshalWrite(header) error = %v", err)
	}
	if _, err := enc.Write([]byte{'\n'}); err != nil {
		enc.Close()
		f.Close()
		t.Fatalf("Write(header newline) error = %v", err)
	}
	for _, rec := range records {
		if err := jsonv2.MarshalWrite(enc, rec); err != nil {
			enc.Close()
			f.Close()
			t.Fatalf("MarshalWrite(record) error = %v", err)
		}
		if _, err := enc.Write([]byte{'\n'}); err != nil {
			enc.Close()
			f.Close()
			t.Fatalf("Write(record newline) error = %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		f.Close()
		t.Fatalf("Close encoder error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close file error = %v", err)
	}
}

func TestFailIfReportsNotReady(t *testing.T) {
	t.Parallel()

	if err := failIfReportsNotReady(nil, false); err != nil {
		t.Fatalf("failIfReportsNotReady(nil) error = %v", err)
	}
	if err := failIfReportsNotReady([]library.ManifestReport{{Valid: true, Fresh: true}}, false); err != nil {
		t.Fatalf("failIfReportsNotReady(ready) error = %v", err)
	}
	if err := failIfReportsNotReady([]library.ManifestReport{{Valid: true, Fresh: false}}, false); err == nil {
		t.Fatal("failIfReportsNotReady(stale) error = nil")
	}
	if err := failIfReportsNotReady([]library.ManifestReport{{Valid: true, Fresh: false}}, true); err != nil {
		t.Fatalf("failIfReportsNotReady(stale allowed) error = %v", err)
	}
}

func TestFilterMissingArchiveManifests(t *testing.T) {
	t.Parallel()

	plan := []library.ArchiveManifestDecision{
		{ArchivePath: "missing.zip", ManifestPath: "missing.manifest.zst"},
		{ArchivePath: "ready.zip", ManifestPath: "ready.manifest.zst"},
	}
	reports := []library.ManifestReport{
		{Kind: "archive", SourcePath: "missing.zip", Missing: true},
		{Kind: "archive", SourcePath: "ready.zip", Valid: true, Fresh: true},
	}

	filteredPlan, filteredReports := filterMissingArchiveManifests(plan, reports, nil)
	if len(filteredPlan) != 1 || filteredPlan[0].ArchivePath != "ready.zip" {
		t.Fatalf("filteredPlan = %#v", filteredPlan)
	}
	if len(filteredReports) != 1 || filteredReports[0].SourcePath != "ready.zip" {
		t.Fatalf("filteredReports = %#v", filteredReports)
	}
}

func TestExitErrHandlerNoLogger(t *testing.T) {
	t.Parallel()

	errWasHandled = false
	ctx := state.ContextWithEnv(context.Background())
	exitErrHandler(ctx, nil, assertErr("boom"))
	if errWasHandled {
		t.Fatal("errWasHandled = true without logger")
	}

	errWasHandled = false
	ctx = state.ContextWithEnv(context.Background())
	state.EnvFromContext(ctx).Log = zap.NewNop()
	exitErrHandler(ctx, nil, assertErr("boom"))
	if !errWasHandled {
		t.Fatal("errWasHandled = false with logger")
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
