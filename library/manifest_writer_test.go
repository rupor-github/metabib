package library

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"metabib/model"
)

func TestManifestWriterFrameCompatibility(t *testing.T) {
	t.Parallel()
	for _, schema := range []string{archiveManifestSchema, databaseManifestSchema} {
		for _, count := range []int{0, 1, 2000} {
			t.Run(fmt.Sprintf("%s/%d", schema, count), func(t *testing.T) {
				t.Parallel()
				header := manifestTestHeader(schema, int64(count))
				records := make([]model.Record, count)
				var expected bytes.Buffer
				if err := writeJSONLValue(&expected, header); err != nil {
					t.Fatal(err)
				}
				for idx := range records {
					records[idx] = manifestTestRecord(int64(idx + 1))
					if err := writeJSONLValue(&expected, records[idx]); err != nil {
						t.Fatal(err)
					}
				}
				for _, format := range []string{"single frame", "concatenated frames"} {
					t.Run(format, func(t *testing.T) {
						path := filepath.Join(t.TempDir(), "records.manifest.zst")
						if format == "single frame" {
							encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
							if err != nil {
								t.Fatal(err)
							}
							data := encoder.EncodeAll(expected.Bytes(), nil)
							if err := encoder.Close(); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(path, data, 0o600); err != nil {
								t.Fatal(err)
							}
						} else {
							w, err := newManifestWriter(path)
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { _ = w.Abort() })
							for _, rec := range records {
								if err := w.Write(rec); err != nil {
									t.Fatal(err)
								}
							}
							if err := w.Close(header); err != nil {
								t.Fatal(err)
							}
						}
						reader, err := openManifestReader(path)
						if err != nil {
							t.Fatal(err)
						}
						data, err := io.ReadAll(reader)
						closeErr := reader.Close()
						if err != nil || closeErr != nil {
							t.Fatal(errors.Join(err, closeErr))
						}
						if !bytes.Equal(data, expected.Bytes()) {
							t.Fatal("decompressed JSONL changed")
						}
						if schema == archiveManifestSchema {
							if _, err := readArchiveManifestHeader(path); err != nil {
								t.Fatal(err)
							}
						} else if _, err := readDatabaseManifestHeader(path); err != nil {
							t.Fatal(err)
						}
						var ids []int64
						n, err := ForEachManifestRecord(t.Context(), path, func(rec model.Record) error {
							ids = append(ids, rec.ID.BookID)
							if rec.Source.Database.Book.Title != "Название книги — Unicode" {
								t.Errorf("record title = %q", rec.Source.Database.Book.Title)
							}
							return nil
						})
						if err != nil || n != int64(count) {
							t.Fatalf("ForEachManifestRecord() count=%d error=%v, want %d", n, err, count)
						}
						wantIDs := make([]int64, count)
						for idx := range wantIDs {
							wantIDs[idx] = int64(idx + 1)
						}
						if !slices.Equal(ids, wantIDs) {
							t.Fatal("record order changed")
						}
					})
				}
			})
		}
	}
}

func TestManifestWriterCompressesSpoolDuringWrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	w, err := newManifestWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Abort() })
	rec := manifestTestRecord(1)
	rec.Source.Database.Annotations[0].Body = strings.Repeat("Long annotation text. ", 4000)
	plain, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := w.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.recordsBuf.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.recordsEncoder.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := w.recordsFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 || info.Size() >= int64(len(plain)*20/4) {
		t.Fatalf("spool size=%d, uncompressed size=%d", info.Size(), len(plain)*20)
	}
	var magic [4]byte
	if _, err := w.recordsFile.ReadAt(magic[:], 0); err != nil {
		t.Fatal(err)
	}
	if magic != [4]byte{0x28, 0xb5, 0x2f, 0xfd} {
		t.Fatalf("spool does not contain a Zstandard frame: %x", magic)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("manifest published before Close(): %v", err)
	}
	if err := w.Close(manifestTestHeader(databaseManifestSchema, 20)); err != nil {
		t.Fatal(err)
	}
	if count, err := ForEachManifestRecord(t.Context(), path, nil); err != nil || count != 20 {
		t.Fatalf("record count=%d error=%v", count, err)
	}
}

func TestManifestWriterFailureCleanup(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"abort", "header", "spool write", "missing spool", "rename"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "records.manifest.zst")
			existingPath := path
			if failure == "rename" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				existingPath = filepath.Join(path, "keep")
			}
			previous := []byte("previous manifest")
			if err := os.WriteFile(existingPath, previous, 0o600); err != nil {
				t.Fatal(err)
			}
			w, err := newManifestWriter(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Abort() })
			if err := w.Write(manifestTestRecord(1)); err != nil {
				t.Fatal(err)
			}
			header := manifestTestHeader(databaseManifestSchema, 1)
			switch failure {
			case "header":
				header = make(chan int)
			case "spool write":
				if err := w.recordsFile.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing spool":
				if err := os.Remove(w.tmpRecords); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "abort" {
				if err := w.Abort(); err != nil {
					t.Fatal(err)
				}
			} else if err := w.Close(header); err == nil {
				t.Fatal("Close() succeeded, want injected failure")
			}
			if w.recordsFile != nil || w.recordsBuf != nil || w.recordsEncoder != nil || w.tmpRecords != "" {
				t.Fatal("manifest writer retained temporary resources")
			}
			if err := w.Abort(); err != nil {
				t.Fatalf("second Abort(): %v", err)
			}
			if err := w.Write(manifestTestRecord(2)); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("Write() after cleanup error=%v, want closed writer", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
				t.Fatalf("files after cleanup=%v error=%v", entries, err)
			}
			data, err := os.ReadFile(existingPath)
			if err != nil || !bytes.Equal(data, previous) {
				t.Fatalf("existing manifest changed: data=%q error=%v", data, err)
			}
		})
	}
}

func TestManifestWriterRecordChecksum(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	w, err := newManifestWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Abort() })
	if err := w.Write(manifestTestRecord(1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(manifestTestHeader(databaseManifestSchema, 1)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ForEachManifestRecord(t.Context(), path, nil); err == nil {
		t.Fatal("corrupt record frame checksum was accepted")
	}
}

func TestManifestWriterZstdCLICompatibility(t *testing.T) {
	t.Parallel()
	cli, err := exec.LookPath("zstd")
	if err != nil {
		t.Skip("zstd CLI is not installed")
	}
	path := filepath.Join(t.TempDir(), "records.manifest.zst")
	w, err := newManifestWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Abort() })
	rec := manifestTestRecord(1)
	header := manifestTestHeader(databaseManifestSchema, 1)
	if err := w.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(header); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), cli, "-dc", "--", path)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	if err := writeJSONLValue(&expected, header); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONLValue(&expected, rec); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, expected.Bytes()) {
		t.Fatal("zstd CLI decompressed different JSONL")
	}
}

func manifestTestHeader(schema string, records int64) any {
	if schema == archiveManifestSchema {
		return archiveManifestHeader{Schema: schema, Records: records}
	}
	return databaseManifestHeader{Schema: schema, Records: records}
}

func manifestTestRecord(id int64) model.Record {
	return model.Record{
		Schema: recordSchema,
		ID:     model.RecordID{Library: "flibusta", BookID: id},
		Source: model.RecordSources{Database: model.DatabaseSource{
			Present: true,
			Book:    &model.DBBook{BookID: id, Title: "Название книги — Unicode", FileType: "fb2"},
			Annotations: []model.DBAnnotation{{
				NID: id, Title: "Annotation", Body: "Описание книги\nwith a second line.",
			}},
		}},
	}
}
