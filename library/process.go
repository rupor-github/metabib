package library

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"metabib/config"
	"metabib/db"
	"metabib/fb2"
	"metabib/jsonl"
	"metabib/model"
)

const recordSchema = "metabib.record/1"

const progressInterval = 3000

type dbBatch struct {
	Index int
	IDs   []int64
}

type dbBatchResult struct {
	Index   int
	Records []model.Record
}

type archiveEntry struct {
	Index  int
	File   *zip.File
	BookID int64
	Ext    string
}

func ProcessArchives(ctx context.Context, repo *db.Repository, cfg *config.Config, out *jsonl.Writer, log *zap.Logger) error {
	start := time.Now()
	archives, err := expandArchives(cfg.Processing.Archives)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("Archive list prepared", zap.Int("archives", len(archives)), zap.Duration("elapsed", time.Since(start)))
	}
	var records int64
	for _, archive := range archives {
		count, err := processArchive(ctx, repo, cfg, out, archive, log)
		if err != nil {
			return err
		}
		records += count
	}
	if log != nil {
		log.Info("Archives processed", zap.Int("archives", len(archives)), zap.Int64("records", records), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func ProcessDatabase(ctx context.Context, repo *db.Repository, cfg *config.Config, out *jsonl.Writer, log *zap.Logger) error {
	start := time.Now()
	ids, err := repo.BookIDs(ctx, cfg.Processing.Process)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("Database book list prepared", zap.Int("books", len(ids)), zap.String("process", cfg.Processing.Process), zap.Duration("elapsed", time.Since(start)))
	}
	workers := cfg.Processing.DatabaseWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(ids) && len(ids) > 0 {
		workers = len(ids)
	}
	if log != nil {
		log.Info("Database processing started", zap.Int("workers", workers))
	}

	batchSize := cfg.Processing.DatabaseBatchSize
	if batchSize <= 0 {
		batchSize = 256
	}
	jobs := make(chan dbBatch, workers*2)
	results := make(chan dbBatchResult, workers*2)
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobs {
				sources, err := repo.BookSourcesByIDs(ctx, batch.IDs)
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				records := make([]model.Record, 0, len(batch.IDs))
				for _, id := range batch.IDs {
					records = append(records, model.Record{
						Schema: recordSchema,
						ID:     model.RecordID{Library: cfg.Database.Name, BookID: id},
						Source: model.RecordSources{Database: sources[id], FB2: model.FB2Source{}},
					})
				}
				select {
				case results <- dbBatchResult{Index: batch.Index, Records: records}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for idx, start := 0, 0; start < len(ids); idx, start = idx+1, start+batchSize {
			end := start + batchSize
			if end > len(ids) {
				end = len(ids)
			}
			select {
			case jobs <- dbBatch{Index: idx, IDs: ids[start:end]}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var processed int64
	nextBatch := 0
	pending := make(map[int]dbBatchResult)
	progressStart := time.Now()
	writeBatch := func(batch dbBatchResult) error {
		for _, rec := range batch.Records {
			if err := out.Write(rec); err != nil {
				return err
			}
			processed++
			if log != nil && processed%progressInterval == 0 {
				log.Info("Database processing progress", zap.Int64("processed", processed), zap.Int("total", len(ids)), zap.Int64("book_id", rec.ID.BookID), zap.Duration("elapsed", time.Since(progressStart)), zap.Duration("total_elapsed", time.Since(start)))
				progressStart = time.Now()
			}
		}
		return nil
	}
	for batch := range results {
		pending[batch.Index] = batch
		for {
			ready, ok := pending[nextBatch]
			if !ok {
				break
			}
			if err := writeBatch(ready); err != nil {
				cancel()
				return err
			}
			delete(pending, nextBatch)
			nextBatch++
		}
	}
	select {
	case err := <-errs:
		return err
	default:
	}
	if log != nil {
		log.Info("Database processed", zap.Int64("records", processed), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func processArchive(ctx context.Context, repo *db.Repository, cfg *config.Config, out *jsonl.Writer, path string, log *zap.Logger) (int64, error) {
	start := time.Now()
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("open archive %q: %w", path, err)
	}
	defer zr.Close()

	entries := make([]archiveEntry, 0, len(zr.File))
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || isBackup(file.Name) || !wantEntry(file.Name, cfg.Processing.Process) {
			continue
		}
		bookID, ext := entryIdentity(file.Name)
		entries = append(entries, archiveEntry{Index: len(entries), File: file, BookID: bookID, Ext: ext})
	}
	workers := cfg.Processing.ArchiveWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(entries) && len(entries) > 0 {
		workers = len(entries)
	}
	batchSize := cfg.Processing.ArchiveBatchSize
	if batchSize <= 0 {
		batchSize = 256
	}
	if log != nil {
		log.Info("Archive processing started", zap.String("archive", path), zap.Int("entries", len(zr.File)), zap.Int("records", len(entries)), zap.Int("workers", workers), zap.Int("batch_size", batchSize), zap.Duration("elapsed", time.Since(start)))
	}

	jobs := make(chan []archiveEntry, workers*2)
	results := make(chan dbBatchResult, workers*2)
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobs {
				records, err := processArchiveBatch(ctx, repo, cfg, path, batch)
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				select {
				case results <- dbBatchResult{Index: batch[0].Index / batchSize, Records: records}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for start := 0; start < len(entries); start += batchSize {
			end := start + batchSize
			if end > len(entries) {
				end = len(entries)
			}
			select {
			case jobs <- entries[start:end]:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var records int64
	nextBatch := 0
	pending := make(map[int]dbBatchResult)
	progressStart := time.Now()
	writeBatch := func(batch dbBatchResult) error {
		for _, rec := range batch.Records {
			if err := out.Write(rec); err != nil {
				return err
			}
			records++
			if log != nil && records%progressInterval == 0 {
				log.Info("Archive processing progress", zap.String("archive", path), zap.Int64("processed", records), zap.Int("records", len(entries)), zap.Int("entries", len(zr.File)), zap.Duration("elapsed", time.Since(progressStart)), zap.Duration("total_elapsed", time.Since(start)))
				progressStart = time.Now()
			}
		}
		return nil
	}
	for batch := range results {
		pending[batch.Index] = batch
		for {
			ready, ok := pending[nextBatch]
			if !ok {
				break
			}
			if err := writeBatch(ready); err != nil {
				cancel()
				return records, err
			}
			delete(pending, nextBatch)
			nextBatch++
		}
	}
	select {
	case err := <-errs:
		return records, err
	default:
	}
	if log != nil {
		log.Info("Archive processed", zap.String("archive", path), zap.Int64("records", records), zap.Int("entries", len(zr.File)), zap.Duration("elapsed", time.Since(start)))
	}
	return records, nil
}

func processArchiveBatch(ctx context.Context, repo *db.Repository, cfg *config.Config, archive string, entries []archiveEntry) ([]model.Record, error) {
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry.BookID > 0 {
			ids = append(ids, entry.BookID)
		}
	}
	sources := map[int64]model.DatabaseSource{}
	if len(ids) > 0 {
		var err error
		sources, err = repo.BookSourcesByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
	}
	records := make([]model.Record, 0, len(entries))
	for _, entry := range entries {
		rec, err := processEntryWithSource(ctx, repo, cfg, archive, entry.File, entry.BookID, entry.Ext, sources[entry.BookID])
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

func processEntryWithSource(ctx context.Context, repo *db.Repository, cfg *config.Config, archive string, file *zip.File, bookID int64, ext string, dbSource model.DatabaseSource) (model.Record, error) {
	rec := model.Record{
		Schema: recordSchema,
		ID: model.RecordID{
			Library:   cfg.Database.Name,
			BookID:    bookID,
			FileName:  strings.TrimSuffix(filepath.Base(file.Name), filepath.Ext(file.Name)),
			Extension: ext,
			Archive: &model.ArchiveInfo{
				Path:             archive,
				Entry:            file.Name,
				CompressedSize:   file.CompressedSize64,
				UncompressedSize: file.UncompressedSize64,
				Modified:         file.Modified.Format("2006-01-02T15:04:05Z07:00"),
			},
		},
	}
	rec.Source.Database = dbSource
	if !rec.Source.Database.Present && !strings.EqualFold(ext, "fb2") {
		id, err := repo.BookIDByFilename(ctx, filepath.Base(file.Name))
		if err != nil {
			return rec, err
		}
		if id > 0 {
			rec.ID.BookID = id
			dbSource, err := repo.BookByID(ctx, id)
			if err != nil {
				return rec, err
			}
			rec.Source.Database = dbSource
		}
	}
	if cfg.Processing.ParseFB2 && strings.EqualFold(ext, "fb2") {
		r, err := file.Open()
		if err != nil {
			rec.Errors = append(rec.Errors, fmt.Sprintf("open FB2 entry: %v", err))
		} else {
			var parser io.Reader = r
			var hash hashWriter
			if cfg.Processing.ArchiveContentMD5 {
				hash = md5.New()
				parser = io.TeeReader(r, hash)
			}
			fb2Source, err := fb2.Parse(parser, cfg.Processing.FB2DescriptionTree)
			if cfg.Processing.ArchiveContentMD5 {
				if _, copyErr := io.Copy(hash, r); copyErr != nil {
					rec.Errors = append(rec.Errors, fmt.Sprintf("hash FB2 entry: %v", copyErr))
				} else {
					rec.ID.Archive.ContentMD5 = hex.EncodeToString(hash.Sum(nil))
				}
			}
			if closeErr := r.Close(); closeErr != nil {
				rec.Errors = append(rec.Errors, fmt.Sprintf("close FB2 entry: %v", closeErr))
			}
			if err != nil {
				rec.Errors = append(rec.Errors, err.Error())
			} else {
				rec.Source.FB2 = fb2Source
			}
		}
	} else if cfg.Processing.ArchiveContentMD5 {
		md5sum, err := archiveEntryMD5(file)
		if err != nil {
			rec.Errors = append(rec.Errors, err.Error())
		} else {
			rec.ID.Archive.ContentMD5 = md5sum
		}
	}
	return rec, nil
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

func archiveEntryMD5(file *zip.File) (string, error) {
	r, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open archive entry for hashing: %w", err)
	}
	defer r.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, r); err != nil {
		return "", fmt.Errorf("hash archive entry: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func expandArchives(paths []string) ([]string, error) {
	var archives []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat archive path %q: %w", path, err)
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(path), ".zip") {
				archives = append(archives, path)
			}
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("read archive directory %q: %w", path, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".zip") {
				archives = append(archives, filepath.Join(path, entry.Name()))
			}
		}
	}
	sort.Strings(archives)
	if len(archives) == 0 {
		return nil, fmt.Errorf("no zip archives found in %v", paths)
	}
	return archives, nil
}

func wantEntry(name string, process string) bool {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	isFB2 := strings.EqualFold(ext, "fb2")
	switch process {
	case "fb2":
		return isFB2
	case "usr":
		return !isFB2
	case "all":
		return true
	default:
		return isFB2
	}
}

func isBackup(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".org")
}

func entryIdentity(name string) (int64, string) {
	base := filepath.Base(name)
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	id, _ := strconv.ParseInt(stem, 10, 64)
	return id, ext
}
