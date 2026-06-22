//go:build windows

package fileutil

import "golang.org/x/sys/windows"

func ReplaceOutputFile(tmpOutputName string, outputName string) error {
	from, err := windows.UTF16PtrFromString(tmpOutputName)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(outputName)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
