//go:build !windows

package config

import (
	"os"

	"golang.org/x/term"
)

func EnableColorOutput(stream *os.File) bool {
	return term.IsTerminal(int(stream.Fd()))
}
