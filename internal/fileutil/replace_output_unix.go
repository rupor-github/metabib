//go:build !windows

package fileutil

import "os"

func ReplaceOutputFile(tmpOutputName string, outputName string) error {
	return os.Rename(tmpOutputName, outputName)
}
