//go:build !windows

package jsonl

import "os"

func replaceOutputFile(tmpOutputName, outputName string) error {
	return os.Rename(tmpOutputName, outputName)
}

func ReplaceOutputFile(tmpOutputName, outputName string) error {
	return replaceOutputFile(tmpOutputName, outputName)
}
