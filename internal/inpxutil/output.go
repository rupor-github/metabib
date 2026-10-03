package inpxutil

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"metabib/internal/fileutil"
	"metabib/model"
)

// PrepareOutput reserves a temporary file beside the final output for atomic publication.
func PrepareOutput(outputPath string, label string, log *zap.Logger) (string, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", fmt.Errorf("create %s output directory: %w", label, err)
	}
	tmpFile, err := fileutil.CreateHiddenTemp(filepath.Dir(outputPath), filepath.Base(outputPath))
	if err != nil {
		return "", fmt.Errorf("create temporary %s output: %w", label, err)
	}
	path := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close temporary %s output %q: %w", label, path, err)
	}
	if _, err := os.Stat(outputPath); err == nil && log != nil {
		log.Warn("Overwriting existing "+label+" output", zap.String("file", outputPath))
	} else if err != nil && !os.IsNotExist(err) {
		_ = os.Remove(path)
		return "", fmt.Errorf("stat %s output %q: %w", label, outputPath, err)
	}
	return path, nil
}

func DatasetArchiveIndex(dataset model.Dataset) ([]*DatasetArchiveRows, map[string]int) {
	archives := DatasetArchiveRowsList(dataset)
	byID := make(map[string]int, len(archives))
	for idx, archive := range archives {
		byID[archive.Meta.ID] = idx
	}
	return archives, byID
}

func WriteInfoEntries(zw *zip.Writer, meta Metadata, structure string, opts TemplateOptions) error {
	if structure != "" {
		if err := WriteZipText(zw, "structure.info", structure); err != nil {
			return err
		}
	}
	collection, err := CollectionInfo(meta, opts)
	if err != nil {
		return err
	}
	if err := WriteZipText(zw, "collection.info", collection); err != nil {
		return err
	}
	version, err := VersionInfo(meta, opts)
	if err != nil {
		return err
	}
	return WriteZipText(zw, "version.info", version)
}

func JoinINPFields(fields []string) string {
	return strings.Join(fields, FieldSep) + FieldSep + "\r\n"
}
