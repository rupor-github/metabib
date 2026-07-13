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
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"metabib/config"
	"metabib/db"
	"metabib/fb2"
	"metabib/jsonl"
	"metabib/model"
)

const recordSchema = "metabib.record/1"

const progressInterval = 3000

func resultWindow(workers int) int {
	return max(workers*2, 1)
}

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

type archiveRepository interface {
	BookSourcesByIDs(ctx context.Context, ids []int64) (map[int64]model.DatabaseSource, error)
	BookIDByFilename(ctx context.Context, filename string) (int64, error)
	BookByID(ctx context.Context, id int64) (model.DatabaseSource, error)
}

type archiveBatch struct {
	Index   int
	Entries []archiveEntry
}

func ProcessArchives(
	ctx context.Context,
	repo *db.Repository,
	cfg *config.Config,
	archivePaths []string,
	out *jsonl.Writer,
	log *zap.Logger,
	verbose bool,
	plan []ArchiveManifestDecision,
) error {
	start := time.Now()
	if len(plan) == 0 {
		archives, err := expandArchives(archivePaths)
		if err != nil {
			return err
		}
		plan = make([]ArchiveManifestDecision, 0, len(archives))
		for _, archive := range archives {
			plan = append(plan, ArchiveManifestDecision{ArchivePath: archive})
		}
	}
	if log != nil {
		log.Info("Archive list prepared", zap.Int("archives", len(plan)), zap.Duration("elapsed", time.Since(start)))
	}
	var records int64
	for _, decision := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		if decision.Use {
			count, err := CopyManifestRecords(ctx, decision.ManifestPath, out, log)
			if err != nil {
				return err
			}
			records += count
			continue
		}
		count, err := processArchive(ctx, repo, cfg, out, decision, log, verbose)
		if err != nil {
			return err
		}
		records += count
	}
	if log != nil {
		log.Info("Archives processed", zap.Int("archives", len(plan)), zap.Int64("records", records), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func BuildArchiveManifests(ctx context.Context, cfg *config.Config, log *zap.Logger, verbose bool, plan []ArchiveManifestDecision) error {
	start := time.Now()
	var built int
	var records int64
	for _, decision := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !decision.Create {
			continue
		}
		count, err := processArchive(ctx, nil, cfg, nil, decision, log, verbose)
		if err != nil {
			return err
		}
		built++
		records += count
	}
	if log != nil {
		log.Info("Archive manifests built", zap.Int("manifests", built), zap.Int64("records", records), zap.Duration("elapsed", time.Since(start)))
	}
	return nil
}

func ProcessDatabase(ctx context.Context, repo *db.Repository, cfg *config.Config, out *jsonl.Writer, log *zap.Logger, verbose bool, manifest DatabaseManifestDecision) error {
	start := time.Now()
	var manifestOut *manifestWriter
	if manifest.Create {
		format, err := repo.DetectFormat(ctx)
		if err != nil {
			return fmt.Errorf("detect database format: %w", err)
		}
		manifest.Format = format
		provenance, err := repo.ImportProvenance(ctx)
		if err != nil {
			return fmt.Errorf("read database import provenance: %w", err)
		}
		manifest.DumpDir = provenance.DumpDir
		manifest.DumpDate = provenance.DumpDate
		manifest.Dumps = dumpManifestSourcesFromImportProvenance(provenance.Dumps)
		manifestOut, err = newManifestWriter(manifest.ManifestPath)
		if err != nil {
			return err
		}
		defer manifestOut.Abort()
		if verbose && log != nil {
			log.Info("Database manifest creation started", zap.String("manifest", manifest.ManifestPath))
		}
	}
	ids, err := repo.BookIDs(ctx)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("Database FB2 book list prepared", zap.Int("books", len(ids)), zap.Duration("elapsed", time.Since(start)))
	}
	workers := max(cfg.Processing.DatabaseWorkers, 1)
	if workers > len(ids) && len(ids) > 0 {
		workers = len(ids)
	}
	if verbose && log != nil {
		log.Info("Database processing started", zap.Int("workers", workers))
	}

	batchSize := cfg.Processing.DatabaseBatchSize
	if batchSize <= 0 {
		batchSize = 256
	}
	jobs := make(chan dbBatch, workers*2)
	results := make(chan dbBatchResult, workers*2)
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, workerCtx := errgroup.WithContext(processCtx)

	for worker := 0; worker < workers; worker++ {
		g.Go(func() error {
			for {
				var batch dbBatch
				select {
				case ready, ok := <-jobs:
					if !ok {
						return nil
					}
					batch = ready
				case <-workerCtx.Done():
					return workerCtx.Err()
				}
				batchStart := time.Now()
				sources, err := repo.BookSourcesByIDs(workerCtx, batch.IDs)
				if err != nil {
					return err
				}
				identities, err := repo.FileIdentitiesByIDs(workerCtx, batch.IDs)
				if err != nil {
					return err
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
				case <-workerCtx.Done():
					return workerCtx.Err()
				}
			}
		})
	}

	jobsClosed := false
	closeJobs := func() {
		if !jobsClosed {
			close(jobs)
			jobsClosed = true
		}
	}
	totalBatches := (len(ids) + batchSize - 1) / batchSize
	nextSubmit := 0
	submitBatches := func(nextWrite int) error {
		for nextSubmit < totalBatches && nextSubmit-nextWrite < resultWindow(workers) {
			start := nextSubmit * batchSize
			end := min(start+batchSize, len(ids))
			select {
			case jobs <- dbBatch{Index: nextSubmit, IDs: ids[start:end]}:
				nextSubmit++
			case <-workerCtx.Done():
				return workerCtx.Err()
			case <-processCtx.Done():
				return processCtx.Err()
			}
		}
		if nextSubmit == totalBatches {
			closeJobs()
		}
		return nil
	}
	resultsDone := make(chan error, 1)
	go func() {
		err := g.Wait()
		close(results)
		resultsDone <- err
	}()
	workerDone := false
	var workerErr error
	waitWorkers := func() error {
		if !workerDone {
			workerErr = <-resultsDone
			workerDone = true
		}
		return workerErr
	}
	defer func() {
		cancel()
		closeJobs()
		_ = waitWorkers()
	}()
	if err := submitBatches(0); err != nil {
		cancel()
		closeJobs()
		if workerErr := waitWorkers(); workerErr != nil {
			return workerErr
		}
		return err
	}

	var processed int64
	nextBatch := 0
	pending := make(map[int]dbBatchResult)
	var dbLoadElapsed, outputWaitElapsed, writeElapsed time.Duration
	progressStart := time.Now()
	writeBatch := func(batch dbBatchResult) error {
		outputWaitElapsed += time.Since(batch.ReadyAt)
		dbLoadElapsed += batch.DBLoadElapsed
		for _, rec := range batch.Records {
			if err := processCtx.Err(); err != nil {
				return err
			}
			writeStart := time.Now()
			if out != nil {
				if err := out.Write(rec); err != nil {
					return err
				}
			}
			if manifestOut != nil {
				if err := manifestOut.Write(rec); err != nil {
					return err
				}
			}
			writeElapsed += time.Since(writeStart)
			processed++
			if verbose && log != nil && processed%progressInterval == 0 {
				log.Info(
					"Database processing progress",
					zap.Int64("processed", processed),
					zap.Int("total", len(ids)),
					zap.Int64("book_id", rec.ID.BookID),
					zap.Duration("elapsed", time.Since(start)),
					zap.Duration("interval_elapsed", time.Since(progressStart)),
				)
				progressStart = time.Now()
			}
		}
		return nil
	}
	for nextBatch < totalBatches {
		var batch dbBatchResult
		select {
		case <-processCtx.Done():
			cancel()
			closeJobs()
			if workerErr := waitWorkers(); workerErr != nil {
				return workerErr
			}
			return processCtx.Err()
		case ready, ok := <-results:
			if !ok {
				if workerErr := waitWorkers(); workerErr != nil {
					return workerErr
				}
				return fmt.Errorf("workers stopped before all database results were processed")
			}
			batch = ready
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
		if err := submitBatches(nextBatch); err != nil {
			cancel()
			closeJobs()
			if workerErr := waitWorkers(); workerErr != nil {
				return workerErr
			}
			return err
		}
	}
	if err := waitWorkers(); err != nil {
		return err
	}
	if manifestOut != nil {
		manifestStart := time.Now()
		header := databaseManifestHeaderFor(cfg, manifest, processed)
		if err := manifestOut.Close(header); err != nil {
			return err
		}
		manifestWriteElapsed := time.Since(manifestStart)
		manifestOut = nil
		if log != nil {
			log.Info(
				"Database manifest created",
				zap.String("manifest", manifest.ManifestPath),
				zap.Int64("records", processed),
				zap.Duration("elapsed", time.Since(start)),
				zap.Duration("manifest_write_elapsed", manifestWriteElapsed),
			)
		}
	}
	if log != nil {
		log.Info("Database processed", zap.Int64("records", processed), zap.Duration("elapsed", time.Since(start)), zap.Duration("db_load_elapsed", dbLoadElapsed), zap.Duration("output_wait_elapsed", outputWaitElapsed), zap.Duration("jsonl_write_elapsed", writeElapsed))
	}
	return nil
}

func dumpManifestSourcesFromImportProvenance(dumps []db.ImportDumpProvenance) []DumpManifestSource {
	sources := make([]DumpManifestSource, 0, len(dumps))
	for _, dump := range dumps {
		sources = append(sources, DumpManifestSource{
			Path:          dump.Path,
			Name:          dump.Name,
			DumpDate:      dump.DumpDate,
			DumpCompleted: dump.DumpCompleted,
			Modified:      dump.Modified,
			MD5:           dump.MD5,
		})
	}
	return sources
}

func processArchive(
	ctx context.Context,
	repo *db.Repository,
	cfg *config.Config,
	out *jsonl.Writer,
	decision ArchiveManifestDecision,
	log *zap.Logger,
	verbose bool,
) (int64, error) {
	start := time.Now()
	path := decision.ArchivePath
	var manifestOut *manifestWriter
	if decision.Create {
		var err error
		manifestOut, err = newManifestWriter(decision.ManifestPath)
		if err != nil {
			return 0, err
		}
		defer manifestOut.Abort()
		if verbose && log != nil {
			log.Info("Archive manifest creation started", zap.String("archive", path), zap.String("manifest", decision.ManifestPath))
		}
		if decision.ArchiveMD5 == "" {
			md5Start := time.Now()
			decision.ArchiveMD5, err = fileMD5(ctx, path)
			if err != nil {
				return 0, err
			}
			if verbose && log != nil {
				log.Info("Archive checksum calculated", zap.String("archive", path), zap.Duration("elapsed", time.Since(md5Start)))
			}
		}
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("open archive %q: %w", path, err)
	}
	defer zr.Close()
	openElapsed := time.Since(start)

	entryListStart := time.Now()
	entries := make([]archiveEntry, 0, len(zr.File))
	for idx, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if file.FileInfo().IsDir() || isBackup(file.Name) || !isFB2Entry(file.Name) {
			continue
		}
		bookID, ext := entryIdentity(file.Name)
		entries = append(entries, archiveEntry{Index: idx, File: file, BookID: bookID, Ext: ext})
	}
	entryListElapsed := time.Since(entryListStart)
	workers := max(cfg.Processing.ArchiveWorkers, 1)
	if workers > len(entries) && len(entries) > 0 {
		workers = len(entries)
	}
	batchSize := cfg.Processing.ArchiveBatchSize
	if batchSize <= 0 {
		batchSize = 256
	}
	if verbose && log != nil {
		log.Info(
			"Archive processing started",
			zap.String("archive", path),
			zap.Int("entries", len(zr.File)),
			zap.Int("records", len(entries)),
			zap.Int("workers", workers),
			zap.Int("batch_size", batchSize),
			zap.Duration("elapsed", time.Since(start)),
			zap.Duration("open_elapsed", openElapsed),
			zap.Duration("entry_list_elapsed", entryListElapsed),
		)
	}

	jobs := make(chan archiveBatch, workers*2)
	results := make(chan dbBatchResult, workers*2)
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, workerCtx := errgroup.WithContext(processCtx)

	var archiveRepo archiveRepository
	if repo != nil {
		archiveRepo = repo
	}
	for worker := 0; worker < workers; worker++ {
		g.Go(func() error {
			for {
				var batch archiveBatch
				select {
				case ready, ok := <-jobs:
					if !ok {
						return nil
					}
					batch = ready
				case <-workerCtx.Done():
					return workerCtx.Err()
				}
				records, timing, err := processArchiveBatch(workerCtx, archiveRepo, cfg, path, batch.Entries)
				if err != nil {
					return err
				}
				timing.Index = batch.Index
				timing.Records = records
				timing.ReadyAt = time.Now()
				select {
				case results <- timing:
				case <-workerCtx.Done():
					return workerCtx.Err()
				}
			}
		})
	}
	jobsClosed := false
	closeJobs := func() {
		if !jobsClosed {
			close(jobs)
			jobsClosed = true
		}
	}
	totalBatches := (len(entries) + batchSize - 1) / batchSize
	nextSubmit := 0
	submitBatches := func(nextWrite int) error {
		for nextSubmit < totalBatches && nextSubmit-nextWrite < resultWindow(workers) {
			start := nextSubmit * batchSize
			end := min(start+batchSize, len(entries))
			select {
			case jobs <- archiveBatch{Index: nextSubmit, Entries: entries[start:end]}:
				nextSubmit++
			case <-workerCtx.Done():
				return workerCtx.Err()
			case <-processCtx.Done():
				return processCtx.Err()
			}
		}
		if nextSubmit == totalBatches {
			closeJobs()
		}
		return nil
	}
	resultsDone := make(chan error, 1)
	go func() {
		err := g.Wait()
		close(results)
		resultsDone <- err
	}()
	workerDone := false
	var workerErr error
	waitWorkers := func() error {
		if !workerDone {
			workerErr = <-resultsDone
			workerDone = true
		}
		return workerErr
	}
	defer func() {
		cancel()
		closeJobs()
		_ = waitWorkers()
	}()
	var records int64
	if err := submitBatches(0); err != nil {
		cancel()
		closeJobs()
		if workerErr := waitWorkers(); workerErr != nil {
			return records, workerErr
		}
		return records, err
	}

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
			if err := processCtx.Err(); err != nil {
				return err
			}
			writeStart := time.Now()
			if out != nil {
				if err := out.Write(rec); err != nil {
					return err
				}
			}
			if manifestOut != nil {
				if err := manifestOut.Write(rec); err != nil {
					return err
				}
			}
			writeElapsed += time.Since(writeStart)
			records++
			if verbose && log != nil && records%progressInterval == 0 {
				log.Info(
					"Archive processing progress",
					zap.String("archive", path),
					zap.Int64("processed", records),
					zap.Int("records", len(entries)),
					zap.Int("entries", len(zr.File)),
					zap.Duration("elapsed", time.Since(start)),
					zap.Duration("interval_elapsed", time.Since(progressStart)),
				)
				progressStart = time.Now()
			}
		}
		return nil
	}
	for nextBatch < totalBatches {
		var batch dbBatchResult
		select {
		case <-processCtx.Done():
			cancel()
			closeJobs()
			if workerErr := waitWorkers(); workerErr != nil {
				return records, workerErr
			}
			return records, processCtx.Err()
		case ready, ok := <-results:
			if !ok {
				if workerErr := waitWorkers(); workerErr != nil {
					return records, workerErr
				}
				return records, fmt.Errorf("workers stopped before all archive results were processed")
			}
			batch = ready
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
		if err := submitBatches(nextBatch); err != nil {
			cancel()
			closeJobs()
			if workerErr := waitWorkers(); workerErr != nil {
				return records, workerErr
			}
			return records, err
		}
	}
	if err := waitWorkers(); err != nil {
		return records, err
	}
	if manifestOut != nil {
		manifestStart := time.Now()
		header, err := archiveManifestHeaderFor(cfg, decision, records)
		if err != nil {
			return records, err
		}
		if err := manifestOut.Close(header); err != nil {
			return records, err
		}
		manifestWriteElapsed := time.Since(manifestStart)
		manifestOut = nil
		if log != nil {
			log.Info(
				"Archive manifest created",
				zap.String("archive", path),
				zap.String("manifest", decision.ManifestPath),
				zap.Int64("records", records),
				zap.Duration("elapsed", time.Since(start)),
				zap.Duration("manifest_write_elapsed", manifestWriteElapsed),
			)
		}
	}
	if verbose && log != nil {
		log.Info("Archive processed", zap.String("archive", path), zap.Int64("records", records), zap.Int("entries", len(zr.File)), zap.Duration("elapsed", time.Since(start)), zap.Duration("db_load_elapsed", dbLoadElapsed), zap.Duration("fb2_parse_elapsed", fb2ParseElapsed), zap.Duration("md5_elapsed", md5Elapsed), zap.Duration("fallback_lookup_elapsed", fallbackLookupElapsed), zap.Duration("output_wait_elapsed", outputWaitElapsed), zap.Duration("jsonl_write_elapsed", writeElapsed))
	}
	return records, nil
}

func processArchiveBatch(ctx context.Context, repo archiveRepository, cfg *config.Config, archive string, entries []archiveEntry) ([]model.Record, dbBatchResult, error) {
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
	if repo != nil && len(ids) > 0 {
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
		rec, et, err := processEntryWithSource(ctx, repo, cfg, archive, entry.File, entry.Index, entry.BookID, entry.Ext, sources[entry.BookID])
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

func processEntryWithSource(ctx context.Context, repo archiveRepository, cfg *config.Config, archive string, file *zip.File, index int, bookID int64, ext string, dbSource model.DatabaseSource) (rec model.Record, timing entryTiming, err error) {
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
				Index:            index,
				CompressedSize:   file.CompressedSize64,
				UncompressedSize: file.UncompressedSize64,
				Modified:         file.Modified.Format("2006-01-02T15:04:05Z07:00"),
			},
		},
	}
	rec.Source.Database = dbSource
	if shouldLookupArchiveFilename(repo, rec.Source.Database, rec.ID.BookID, ext) {
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
			limited := &fb2LimitReader{reader: r, remaining: fb2.MaxDecompressedBytes}
			reader := bufferedContextReader(ctx, limited, cfg.Processing.ArchiveReadBuffer)
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

func shouldLookupArchiveFilename(repo archiveRepository, dbSource model.DatabaseSource, bookID int64, ext string) bool {
	return repo != nil && !dbSource.Present && (bookID <= 0 || !strings.EqualFold(ext, "fb2"))
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

type fb2LimitReader struct {
	reader    io.Reader
	remaining int64
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

func (r *fb2LimitReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var b [1]byte
		n, err := r.reader.Read(b[:])
		if n > 0 {
			return 0, fmt.Errorf("%w: decompressed FB2 entry exceeds %d bytes", fb2.ErrLimitExceeded, fb2.MaxDecompressedBytes)
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
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

func isFB2Entry(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".fb2")
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
