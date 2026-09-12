package main

import (
	"io"
	"os"
	"strings"

	"metabib/config"
)

type terminalStyle struct {
	enabled bool
}

func terminalOutputStyle(out io.Writer) terminalStyle {
	file, ok := out.(*os.File)
	return terminalStyle{enabled: ok && config.EnableColorOutput(file) && os.Getenv("NO_COLOR") == ""}
}

func (s terminalStyle) header(value string) string {
	if !s.enabled {
		return value
	}
	return "\x1b[1;36m" + value + "\x1b[0m"
}

func (s terminalStyle) status(value string) string {
	if !s.enabled {
		return value
	}
	switch value {
	case traceStatusFound, "ok":
		return "\x1b[32m" + value + "\x1b[0m"
	case traceStatusMissing, traceStatusAmbiguous, traceStatusNotApplicable:
		return "\x1b[33m" + value + "\x1b[0m"
	case traceStatusError:
		return "\x1b[31m" + value + "\x1b[0m"
	default:
		return value
	}
}

func (s terminalStyle) path(value string) string {
	if !s.enabled {
		return value
	}
	return "\x1b[2m" + value + "\x1b[0m"
}

func (s terminalStyle) label(value string) string {
	if !s.enabled {
		return value
	}
	return "\x1b[36m" + value + "\x1b[0m"
}

func (s terminalStyle) json(data []byte) string {
	if !s.enabled {
		return string(data)
	}
	var b strings.Builder
	lines := strings.Split(string(data), "\n")
	for lineIdx, line := range lines {
		if lineIdx > 0 {
			b.WriteByte('\n')
		}
		colon := strings.Index(line, "\":")
		quote := strings.IndexByte(line, '"')
		if quote < 0 || colon < quote {
			b.WriteString(line)
			continue
		}
		b.WriteString(line[:quote])
		b.WriteString("\x1b[36m")
		b.WriteString(line[quote : colon+1])
		b.WriteString("\x1b[0m")
		b.WriteString(line[colon+1:])
	}
	return b.String()
}
