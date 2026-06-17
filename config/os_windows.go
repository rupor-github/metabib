//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/term"
)

func EnableColorOutput(stream *os.File) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	v, _, err := k.GetIntegerValue("CurrentMajorVersionNumber")
	if err != nil || v < 10 || !term.IsTerminal(int(stream.Fd())) {
		return false
	}

	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(stream.Fd()), &mode); err != nil {
		return false
	}
	const enableVirtualTerminalProcessing uint32 = 0x4
	return windows.SetConsoleMode(windows.Handle(stream.Fd()), mode|enableVirtualTerminalProcessing) == nil
}
