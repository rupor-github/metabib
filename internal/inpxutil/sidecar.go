package inpxutil

import (
	"path/filepath"
	"strings"
)

func AnnotationsOutputPath(outputPath string) string {
	return strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + "-annotations.zip"
}

func CompilationsOutputPath(outputPath string) string {
	return strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + "-compilations.zip"
}
