package flibinpx

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	"metabib/internal/inpxutil"
	"metabib/jsonl"
	"metabib/model"
)

func TestGenerateFLibraryINPX(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "fb2-0000000001-0000000002.zip")
	prefix := filepath.Join(dir, "all")
	writeFLibMetadata(t, prefix, model.MergeMetadata{
		Schema:  "metabib.merge_metadata/1",
		Library: "flibusta",
		Database: model.MergeDatabaseMetadata{
			DumpDate:    "20260603",
			DumpDateISO: "2026-06-03",
		},
		Archives: []model.MergeArchiveMetadata{{
			Path:    archivePath,
			Name:    filepath.Base(archivePath),
			Entries: 2,
			Ignored: []model.IndexRange{{Start: 1, End: 1}},
		}},
		Parts: []string{"all.jsonl"},
	})
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(flibRecord(archivePath, 0, "fb2")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Write(flibRecord(archivePath, 1, "fb2")); err != nil {
		t.Fatalf("Write() ignored error = %v", err)
	}
	if err := w.Write(flibOnlineRecord(2)); err != nil {
		t.Fatalf("Write() online-only error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	fixedTmp := filepath.Join(dir, "flibusta_20260603.inpx.tmp")
	if err := os.WriteFile(fixedTmp, []byte("stale fixed tmp"), 0o644); err != nil {
		t.Fatalf("write fixed temp file: %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:      prefix,
		OutputPrefix:     filepath.Join(dir, "flibusta"),
		SequenceMode:     SequenceAll,
		FB2Preference:    PreferMerge,
		FlattenMode:      FlattenPathLeaf,
		DedupMode:        DedupCaseInsensitive,
		FB2PathSeparator: " / ",
		CommentTemplate:  "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate:  "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if filepath.Base(stats.OutputPath) != "flibusta_20260603.inpx" {
		t.Fatalf("output = %q", stats.OutputPath)
	}
	if data, err := os.ReadFile(fixedTmp); err != nil || string(data) != "stale fixed tmp" {
		t.Fatalf("fixed temp file = %q, %v", data, err)
	}
	if stats.Archives != 1 || stats.Files != 1 || stats.Records != 3 ||
		stats.DBRecords != 3 || stats.FB2Records != 0 || stats.Dummy != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	entries := readZipEntries(t, stats.OutputPath)
	if entries["structure.info"] != structureInfo {
		t.Fatalf("structure.info = %q", entries["structure.info"])
	}
	if _, ok := entries["online.inp"]; ok {
		t.Fatalf("unexpected online.inp for mixed archive input: %#v", entries)
	}
	if strings.Contains(entries["fb2-0000000001-0000000002.inp"], "dummy record") {
		t.Fatalf("inp contains dummy row: %q", entries["fb2-0000000001-0000000002.inp"])
	}
	lines := strings.Split(strings.TrimSuffix(entries["fb2-0000000001-0000000002.inp"], "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d inp=%q", len(lines), entries["fb2-0000000001-0000000002.inp"])
	}
	first := strings.Split(lines[0], inpxutil.FieldSep)
	third := strings.Split(lines[2], inpxutil.FieldSep)
	if len(first) != 19 || len(third) != 19 {
		t.Fatalf("field counts = %d, %d first=%#v third=%#v", len(first), len(third), first, third)
	}
	if first[3] != "Cycle" || first[4] != "1" || first[11] != "1" || first[12] != filepath.Base(archivePath) {
		t.Fatalf("first fields = %#v", first)
	}
	if third[3] != "Universe / Cycle" || third[4] != "7" || third[11] != "3" {
		t.Fatalf("third fields = %#v", third)
	}
	if first[15] != "one:two:three:" || first[16] != "2025" || first[17] != "flibusta" {
		t.Fatalf("extended fields = %#v", first[15:18])
	}
}

func TestGenerateFLibraryDatabaseOnlyWritesOnlineINP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "online")
	writeFLibMetadata(t, prefix, model.MergeMetadata{
		Schema:  "metabib.merge_metadata/1",
		Library: "librusec",
		Database: model.MergeDatabaseMetadata{
			DumpDate:    "20260713",
			DumpDateISO: "2026-07-13",
		},
		Parts: []string{"online.jsonl"},
	})
	w, err := jsonl.CreateCompressed(prefix, jsonl.CompressionNone)
	if err != nil {
		t.Fatalf("CreateCompressed() error = %v", err)
	}
	if err := w.Write(flibOnlineRecord(1)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	stats, err := Generate(context.Background(), Options{
		InputPrefix:     prefix,
		OutputPrefix:    filepath.Join(dir, "librusec"),
		SequenceMode:    SequenceAll,
		FB2Preference:   PreferComplement,
		FlattenMode:     FlattenAll,
		DedupMode:       DedupCaseInsensitive,
		CommentTemplate: "{{ .DatabaseName }} {{ .DisplayDate }}",
		VersionTemplate: "{{ .DumpDate }}\r\n",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if stats.Archives != 1 || stats.Files != 1 || stats.Records != 1 || stats.DBRecords != 1 || stats.FB2Records != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	entries := readZipEntries(t, stats.OutputPath)
	if !strings.Contains(entries["online.inp"], "Last,First,") || !strings.Contains(entries["online.inp"], "Online Title") {
		t.Fatalf("online.inp = %q", entries["online.inp"])
	}
}

func TestFlattenFB2Sequences(t *testing.T) {
	t.Parallel()

	sequences := []model.FB2Sequence{{
		Name: "Universe",
		Nested: []model.FB2Sequence{{
			Name:   "Cycle",
			Number: "2.5",
		}},
	}}
	tests := []struct {
		name string
		mode FlattenMode
		want []sequence
	}{
		{
			name: "all",
			mode: FlattenAll,
			want: []sequence{{Name: "Universe", Source: "fb2"}, {Name: "Cycle", Number: "2", Source: "fb2"}},
		},
		{name: "leaf", mode: FlattenLeaf, want: []sequence{{Name: "Cycle", Number: "2", Source: "fb2"}}},
		{name: "path", mode: FlattenPath, want: []sequence{{Name: "Universe > Cycle", Number: "2", Source: "fb2"}}},
		{
			name: "path-leaf",
			mode: FlattenPathLeaf,
			want: []sequence{{Name: "Universe > Cycle", Number: "2", Source: "fb2"}, {Name: "Cycle", Number: "2", Source: "fb2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := flattenFB2Sequences(sequences, tt.mode, " > ")
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d got=%#v", len(got), got)
			}
			for idx := range got {
				if got[idx] != tt.want[idx] {
					t.Fatalf("sequence[%d] = %#v, want %#v", idx, got[idx], tt.want[idx])
				}
			}
		})
	}
}

func flibRecord(archivePath string, index int, ext string) model.Record {
	return model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "flibusta",
			BookID:    int64(index + 1),
			FileName:  strconv.Itoa(index + 1),
			Extension: ext,
			Archive: &model.ArchiveInfo{
				Path: archivePath, Entry: "1.fb2", Index: index, UncompressedSize: 123, Modified: "2026-06-03T00:00:00Z",
			},
		},
		Source: model.RecordSources{
			Database: model.DatabaseSource{
				Present: true,
				Book: &model.DBBook{
					BookID:   int64(index + 1),
					FileSize: 123,
					Title:    "Title",
					FileType: "fb2",
					Time:     "2026-06-03T00:00:00Z",
					Lang:     "ru",
					Keywords: "one,two;three",
					Year:     2025,
				},
				Authors: []model.Contributor{{FirstName: "First", LastName: "Last"}},
				Genres:  []model.DBGenre{{Code: "sf"}},
				Sequences: []model.DBSequence{
					{Name: "Cycle", Number: 1, Level: 1, Type: 0},
					{Name: "Publisher Series", Number: 10, Level: 101, Type: 1},
				},
			},
			FB2: model.FB2Source{Present: true, Description: &model.FB2Description{
				TitleInfo: &model.FB2TitleInfo{
					Title:    "FB2 Title",
					Language: "ru",
					Sequences: []model.FB2Sequence{{
						Name:   "Universe",
						Nested: []model.FB2Sequence{{Name: "Cycle", Number: "7"}},
					}},
				},
			}},
		},
	}
}

func flibOnlineRecord(bookID int64) model.Record {
	return model.Record{
		Schema: "metabib.record/1",
		ID: model.RecordID{
			Library:   "librusec",
			BookID:    bookID,
			FileName:  strconv.FormatInt(bookID, 10),
			Extension: "fb2",
		},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book: &model.DBBook{
				BookID:   bookID,
				FileSize: 123,
				Title:    "Online Title",
				FileType: "fb2",
				Time:     "2026-07-13T00:00:00Z",
				Lang:     "ru",
			},
			Authors: []model.Contributor{{FirstName: "First", LastName: "Last"}},
			Genres:  []model.DBGenre{{Code: "sf"}},
		}},
	}
}

func writeFLibMetadata(t *testing.T, prefix string, meta model.MergeMetadata) {
	t.Helper()
	data, err := jsonv2.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(prefix+".meta.json", append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readZipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	entries := map[string]string{}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatalf("Open(%q) error = %v", file.Name, err)
		}
		buf := new(strings.Builder)
		if _, err := io.Copy(buf, r); err != nil {
			r.Close()
			t.Fatalf("ReadFrom(%q) error = %v", file.Name, err)
		}
		r.Close()
		entries[file.Name] = buf.String()
	}
	return entries
}
