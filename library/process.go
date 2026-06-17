package library

import (
	"archive/zip"
	"bufio"
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
	Index                 int
	Records               []model.Record
	ReadyAt               time.Time
	DBLoadElapsed         time.Duration
	FB2ParseElapsed       time.Duration
	MD5Elapsed            time.Duration
	FallbackLookupElapsed time.Duration
}

type entryTiming struct {
	FB2ParseElapsed       time.Duration
	MD5Elapsed            time.Duration
	FallbackLookupElapsed time.Duration
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
				batchStart := time.Now()
				sources, err := repo.BookSourcesByIDs(ctx, batch.IDs)
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				identities, err := repo.FileIdentitiesByIDs(ctx, batch.IDs)
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				dbLoadElapsed := time.Since(batchStart)
				records := make([]model.Record, 0, len(batch.IDs))
				for _, id := range batch.IDs {
					identity := identities[id]
					records = append(records, model.Record{
						Schema: recordSchema,
						ID:     model.RecordID{Library: cfg.Database.Name, BookID: id, FileName: identity.FileName, Extension: identity.Extension},
						Source: model.RecordSources{Database: sources[id], FB2: model.FB2Source{}},
					})
				}
				select {
				case results <- dbBatchResult{Index: batch.Index, Records: records, ReadyAt: time.Now(), DBLoadElapsed: dbLoadElapsed}:
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
	var dbLoadElapsed, outputWaitElapsed, writeElapsed time.Duration
	progressStart := time.Now()
	writeBatch := func(batch dbBatchResult) error {
		outputWaitElapsed += time.Since(batch.ReadyAt)
		dbLoadElapsed += batch.DBLoadElapsed
		for _, rec := range batch.Records {
			if err := ctx.Err(); err != nil {
				return err
			}
			writeStart := time.Now()
			if err := out.Write(rec); err != nil {
				return err
			}
			writeElapsed += time.Since(writeStart)
			processed++
			if cfg.Processing.Progress && log != nil && processed%progressInterval == 0 {
				log.Info("Database processing progress", zap.Int64("processed", processed), zap.Int("total", len(ids)), zap.Int64("book_id", rec.ID.BookID), zap.Duration("elapsed", time.Since(progressStart)), zap.Duration("total_elapsed", time.Since(start)))
				progressStart = time.Now()
			}
		}
		return nil
	}
	for batch := range results {
		if err := ctx.Err(); err != nil {
			cancel()
			return err
		}
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
		log.Info("Database processed", zap.Int64("records", processed), zap.Duration("elapsed", time.Since(start)), zap.Duration("db_load_elapsed", dbLoadElapsed), zap.Duration("output_wait_elapsed", outputWaitElapsed), zap.Duration("jsonl_write_elapsed", writeElapsed))
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
	openElapsed := time.Since(start)

	entryListStart := time.Now()
	entries := make([]archiveEntry, 0, len(zr.File))
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if file.FileInfo().IsDir() || isBackup(file.Name) || !wantEntry(file.Name, cfg.Processing.Process) {
			continue
		}
		bookID, ext := entryIdentity(file.Name)
		entries = append(entries, archiveEntry{Index: len(entries), File: file, BookID: bookID, Ext: ext})
	}
	entryListElapsed := time.Since(entryListStart)
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
		log.Info("Archive processing started", zap.String("archive", path), zap.Int("entries", len(zr.File)), zap.Int("records", len(entries)), zap.Int("workers", workers), zap.Int("batch_size", batchSize), zap.Duration("open_elapsed", openElapsed), zap.Duration("entry_list_elapsed", entryListElapsed), zap.Duration("elapsed", time.Since(entryListStart)))
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
				records, timing, err := processArchiveBatch(ctx, repo, cfg, path, batch)
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				timing.Index = batch[0].Index / batchSize
				timing.Records = records
				timing.ReadyAt = time.Now()
				select {
				case results <- timing:
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
	var dbLoadElapsed, fb2ParseElapsed, md5Elapsed, fallbackLookupElapsed, outputWaitElapsed, writeElapsed time.Duration
	progressStart := time.Now()
	writeBatch := func(batch dbBatchResult) error {
		outputWaitElapsed += time.Since(batch.ReadyAt)
		dbLoadElapsed += batch.DBLoadElapsed
		fb2ParseElapsed += batch.FB2ParseElapsed
		md5Elapsed += batch.MD5Elapsed
		fallbackLookupElapsed += batch.FallbackLookupElapsed
		for _, rec := range batch.Records {
			if err := ctx.Err(); err != nil {
				return err
			}
			writeStart := time.Now()
			if err := out.Write(rec); err != nil {
				return err
			}
			writeElapsed += time.Since(writeStart)
			records++
			if cfg.Processing.Progress && log != nil && records%progressInterval == 0 {
				log.Info("Archive processing progress", zap.String("archive", path), zap.Int64("processed", records), zap.Int("records", len(entries)), zap.Int("entries", len(zr.File)), zap.Duration("elapsed", time.Since(progressStart)), zap.Duration("total_elapsed", time.Since(start)))
				progressStart = time.Now()
			}
		}
		return nil
	}
	for batch := range results {
		if err := ctx.Err(); err != nil {
			cancel()
			return records, err
		}
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
		log.Info("Archive processed", zap.String("archive", path), zap.Int64("records", records), zap.Int("entries", len(zr.File)), zap.Duration("elapsed", time.Since(start)), zap.Duration("db_load_elapsed", dbLoadElapsed), zap.Duration("fb2_parse_elapsed", fb2ParseElapsed), zap.Duration("md5_elapsed", md5Elapsed), zap.Duration("fallback_lookup_elapsed", fallbackLookupElapsed), zap.Duration("output_wait_elapsed", outputWaitElapsed), zap.Duration("jsonl_write_elapsed", writeElapsed))
	}
	return records, nil
}

func processArchiveBatch(ctx context.Context, repo *db.Repository, cfg *config.Config, archive string, entries []archiveEntry) ([]model.Record, dbBatchResult, error) {
	var timing dbBatchResult
	if err := ctx.Err(); err != nil {
		return nil, timing, err
	}
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry.BookID > 0 {
			ids = append(ids, entry.BookID)
		}
	}
	sources := map[int64]model.DatabaseSource{}
	if len(ids) > 0 {
		var err error
		dbStart := time.Now()
		sources, err = repo.BookSourcesByIDs(ctx, ids)
		timing.DBLoadElapsed += time.Since(dbStart)
		if err != nil {
			return nil, timing, err
		}
	}
	records := make([]model.Record, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, timing, err
		}
		rec, et, err := processEntryWithSource(ctx, repo, cfg, archive, entry.File, entry.BookID, entry.Ext, sources[entry.BookID])
		if err != nil {
			return nil, timing, err
		}
		timing.FB2ParseElapsed += et.FB2ParseElapsed
		timing.MD5Elapsed += et.MD5Elapsed
		timing.FallbackLookupElapsed += et.FallbackLookupElapsed
		records = append(records, rec)
	}
	return records, timing, nil
}

func processEntryWithSource(ctx context.Context, repo *db.Repository, cfg *config.Config, archive string, file *zip.File, bookID int64, ext string, dbSource model.DatabaseSource) (rec model.Record, timing entryTiming, err error) {
	if err := ctx.Err(); err != nil {
		return model.Record{}, timing, err
	}
	rec = model.Record{
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
		lookupStart := time.Now()
		id, err := repo.BookIDByFilename(ctx, filepath.Base(file.Name))
		timing.FallbackLookupElapsed += time.Since(lookupStart)
		if err != nil {
			return rec, timing, err
		}
		if id > 0 {
			rec.ID.BookID = id
			lookupStart = time.Now()
			dbSource, err := repo.BookByID(ctx, id)
			timing.FallbackLookupElapsed += time.Since(lookupStart)
			if err != nil {
				return rec, timing, err
			}
			rec.Source.Database = dbSource
		}
	}
	if cfg.Processing.ParseFB2 && strings.EqualFold(ext, "fb2") {
		r, err := file.Open()
		if err != nil {
			rec.Errors = append(rec.Errors, fmt.Sprintf("open FB2 entry: %v", err))
		} else {
			reader := bufferedContextReader(ctx, r, cfg.Processing.ArchiveReadBuffer)
			var parser io.Reader = reader
			var hash hashWriter
			if cfg.Processing.ArchiveContentMD5 {
				hash = md5.New()
				parser = io.TeeReader(reader, hash)
			}
			parseStart := time.Now()
			fb2Source, err := fb2.Parse(parser, cfg.Processing.FB2DescriptionTree)
			timing.FB2ParseElapsed += time.Since(parseStart)
			if cfg.Processing.ArchiveContentMD5 {
				md5Start := time.Now()
				if _, copyErr := copyWithBuffer(hash, reader, cfg.Processing.ArchiveReadBuffer); copyErr != nil {
					rec.Errors = append(rec.Errors, fmt.Sprintf("hash FB2 entry: %v", copyErr))
				} else {
					rec.ID.Archive.ContentMD5 = hex.EncodeToString(hash.Sum(nil))
				}
				timing.MD5Elapsed += time.Since(md5Start)
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
		md5Start := time.Now()
		md5sum, err := archiveEntryMD5(ctx, file, cfg.Processing.ArchiveReadBuffer)
		timing.MD5Elapsed += time.Since(md5Start)
		if err != nil {
			rec.Errors = append(rec.Errors, err.Error())
		} else {
			rec.ID.Archive.ContentMD5 = md5sum
		}
	}
	return rec, timing, nil
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func bufferedContextReader(ctx context.Context, reader io.Reader, bufferSize int) io.Reader {
	cr := &contextReader{ctx: ctx, reader: reader}
	if bufferSize <= 0 {
		return cr
	}
	return bufio.NewReaderSize(cr, bufferSize)
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil {
		return n, err
	}
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, nil
}

func archiveEntryMD5(ctx context.Context, file *zip.File, bufferSize int) (string, error) {
	r, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open archive entry for hashing: %w", err)
	}
	defer r.Close()
	hash := md5.New()
	if _, err := copyWithBuffer(hash, &contextReader{ctx: ctx, reader: r}, bufferSize); err != nil {
		return "", fmt.Errorf("hash archive entry: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyWithBuffer(dst io.Writer, src io.Reader, bufferSize int) (int64, error) {
	if bufferSize <= 0 {
		return io.Copy(dst, src)
	}
	return io.CopyBuffer(dst, src, make([]byte, bufferSize))
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
