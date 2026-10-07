package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Bare integers retain the original --chunksize interpretation in MiB.
func parseChunkSize(value string) (int64, error) {
	number := strings.TrimSpace(value)
	multiplier := int64(1 << 20)
	if unitStart := strings.IndexFunc(number, unicode.IsLetter); unitStart >= 0 {
		unit := strings.ToLower(strings.TrimSpace(number[unitStart:]))
		number = strings.TrimSpace(number[:unitStart])
		switch unit {
		case "b":
			multiplier = 1
		case "kib":
			multiplier = 1 << 10
		case "mib":
			multiplier = 1 << 20
		case "gib":
			multiplier = 1 << 30
		default:
			return 0, fmt.Errorf("invalid chunk size %q: supported units are B, KiB, MiB, and GiB; bare integers are MiB", value)
		}
	}
	amount, err := strconv.ParseInt(number, 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("invalid chunk size %q: size must be a positive integer with optional B, KiB, MiB, or GiB units", value)
	}
	if amount > (1<<63-1)/multiplier {
		return 0, fmt.Errorf("chunk size %q is too large", value)
	}
	return amount * multiplier, nil
}
