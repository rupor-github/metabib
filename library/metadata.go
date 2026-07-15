package library

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"metabib/config"
	"metabib/model"
)

func DatasetFor(
	ctx context.Context,
	libraryName string,
	database DatabaseManifestDecision,
	archives []ArchiveManifestDecision,
	processing config.ProcessingConfig,
	generatorVersion string,
) (model.Dataset, error) {
	id, err := newDatasetID()
	if err != nil {
		return model.Dataset{}, err
	}
	dataset := model.Dataset{
		Schema:       model.DatasetSchemaV1,
		ID:           id,
		RecordSchema: model.DatasetRecordSchemaV1,
		Library:      libraryName,
		Created:      time.Now().UTC().Format(time.RFC3339),
		Generator: model.DatasetGenerator{
			Name:    "metabib",
			Version: generatorVersion,
		},
		Normalization: model.DatasetNormalization{Model: "metabib.claims/1"},
		Ordering:      datasetOrdering(archives),
		Processing: model.DatasetProcessing{
			ParseFB2:    processing.ParseFB2,
			FB2Coverage: datasetFB2Coverage(processing),
			ArchiveContentChecksum: model.DatasetChecksumOption{
				Enabled:   processing.ArchiveContentMD5,
				Algorithm: checksumAlgorithm(processing.ArchiveContentMD5),
			},
		},
	}
	if database.ManifestPath != "" || database.DumpDate != "" || database.DumpDir != "" {
		dataset.Database = datasetDatabase(database)
	}
	if len(archives) == 0 {
		dataset.Records = database.Records
		return dataset, nil
	}
	for ordinal, decision := range archives {
		if err := ctx.Err(); err != nil {
			return dataset, err
		}
		archive, err := datasetArchive(ordinal, decision)
		if err != nil {
			return dataset, err
		}
		dataset.Archives = append(dataset.Archives, archive)
		dataset.Records += decision.Records
	}
	return dataset, nil
}

func datasetDatabase(database DatabaseManifestDecision) *model.DatasetDatabase {
	out := &model.DatasetDatabase{
		ID:          "database",
		Format:      string(database.Format),
		DumpDirHint: database.DumpDir,
		DumpDate:    database.DumpDate,
	}
	for _, dump := range database.Dumps {
		out.Dumps = append(out.Dumps, model.DatasetDump{
			Name:          dump.Name,
			PathHint:      dump.Path,
			DumpCompleted: dump.DumpCompleted,
			Modified:      dump.Modified,
			Checksum:      checksum("md5", "", dump.MD5),
		})
	}
	return out
}

func datasetArchive(ordinal int, decision ArchiveManifestDecision) (model.DatasetArchive, error) {
	meta, err := archiveMetadata(decision.ArchivePath)
	if err != nil {
		return model.DatasetArchive{}, err
	}
	out := model.DatasetArchive{
		ID:         fmt.Sprintf("archive-%04d", ordinal+1),
		Ordinal:    ordinal,
		Name:       meta.Name,
		PathHint:   decision.ArchivePath,
		Checksum:   checksum("md5", "container", decision.ArchiveMD5),
		Entries:    meta.Entries,
		FB2Entries: meta.FB2Entries,
		Ignored:    meta.Ignored,
		Dummy:      meta.Dummy,
	}
	if info, err := os.Stat(decision.ArchivePath); err == nil {
		out.Modified = info.ModTime().UTC().Format(time.RFC3339)
	} else if !os.IsNotExist(err) {
		return model.DatasetArchive{}, fmt.Errorf("stat archive %q: %w", decision.ArchivePath, err)
	}
	return out, nil
}

func datasetOrdering(archives []ArchiveManifestDecision) model.DatasetOrdering {
	if len(archives) > 0 {
		return model.DatasetOrdering{
			Mode:       "archive_entry",
			ArchiveKey: "ordinal",
			EntryKey:   "index",
			Direction:  "ascending",
		}
	}
	return model.DatasetOrdering{Mode: "database_book_id", Source: "database", Direction: "ascending"}
}

func datasetFB2Coverage(processing config.ProcessingConfig) string {
	if !processing.ParseFB2 {
		return ""
	}
	if processing.FB2DescriptionTree {
		return "description"
	}
	return "title_info"
}

func checksumAlgorithm(enabled bool) string {
	if enabled {
		return "md5"
	}
	return ""
}

func checksum(algorithm string, scope string, value string) *model.Checksum {
	if value == "" {
		return nil
	}
	return &model.Checksum{Algorithm: algorithm, Scope: scope, Value: value}
}

func newDatasetID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate dataset ID: %w", err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	out := make([]byte, 36)
	hex.Encode(out[0:8], data[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], data[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], data[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], data[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], data[10:16])
	return "urn:uuid:" + string(out), nil
}

type archiveLayout struct {
	Name       string
	Entries    int
	FB2Entries int
	Ignored    []model.IndexRange
	Dummy      []model.IndexRange
}

func archiveMetadata(path string) (archiveLayout, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return archiveLayout{}, fmt.Errorf("open archive %q for metadata: %w", path, err)
	}
	defer zr.Close()
	meta := archiveLayout{
		Name:    filepath.Base(path),
		Entries: len(zr.File),
	}
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
