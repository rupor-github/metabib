package library

import (
	"archive/zip"
	"context"
	"fmt"
	"path/filepath"
	"time"

	"metabib/model"
)

const mergeMetadataSchema = "metabib.merge_metadata/1"

func MergeMetadataFor(ctx context.Context, libraryName string, database DatabaseManifestDecision, archives []ArchiveManifestDecision, compression string) (model.MergeMetadata, error) {
	meta := model.MergeMetadata{
		Schema:      mergeMetadataSchema,
		Library:     libraryName,
		Compression: compression,
		Created:     time.Now().UTC().Format(time.RFC3339),
	}
	if database.ManifestPath != "" || database.DumpDate != "" || database.DumpDir != "" {
		meta.Database = model.MergeDatabaseMetadata{
			DumpDir:     database.DumpDir,
			DumpDate:    compactDate(database.DumpDate),
			DumpDateISO: database.DumpDate,
		}
	}
	for _, decision := range archives {
		if err := ctx.Err(); err != nil {
			return meta, err
		}
		archiveMeta, err := archiveMetadata(decision.ArchivePath, decision.ManifestPath)
		if err != nil {
			return meta, err
		}
		meta.Archives = append(meta.Archives, archiveMeta)
	}
	return meta, nil
}

func archiveMetadata(path string, manifestPath string) (model.MergeArchiveMetadata, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return model.MergeArchiveMetadata{}, fmt.Errorf("open archive %q for metadata: %w", path, err)
	}
	defer zr.Close()
	meta := model.MergeArchiveMetadata{Path: path, Name: filepath.Base(path), Entries: len(zr.File), ManifestPath: manifestPath}
	var dummy []int
	for idx, file := range zr.File {
		if file.FileInfo().IsDir() || isBackup(file.Name) {
			meta.Ignored = appendIndex(meta.Ignored, idx)
			continue
		}
		if isFB2Entry(file.Name) {
			meta.FB2Entries++
			continue
		}
		dummy = append(dummy, idx)
	}
	for _, idx := range dummy {
		meta.Dummy = appendIndex(meta.Dummy, idx)
	}
	return meta, nil
}

func appendIndex(ranges []model.IndexRange, idx int) []model.IndexRange {
	if len(ranges) == 0 || ranges[len(ranges)-1].End+1 != idx {
		return append(ranges, model.IndexRange{Start: idx, End: idx})
	}
	ranges[len(ranges)-1].End = idx
	return ranges
}

func compactDate(value string) string {
	if len(value) >= 10 && value[4] == '-' && value[7] == '-' {
		return value[:4] + value[5:7] + value[8:10]
	}
	return value
}
