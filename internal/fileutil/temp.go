package fileutil

import (
	"os"
	"path/filepath"
	"strings"
)

func HiddenTempPattern(name string) string {
	base := filepath.Base(name)
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "tmp"
	}
	if !strings.HasPrefix(base, ".") {
		base = "." + base
	}
	return base + "-*.tmp"
}

func CreateHiddenTemp(dir string, name string) (*os.File, error) {
	return os.CreateTemp(dir, HiddenTempPattern(name))
}
