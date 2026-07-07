package logfilter

import (
	"bytes"
	"strings"
)

var levels = []string{"DEBUG", "INFO", "WARN", "ERROR"}

// Match reports whether line satisfies the exact substring and minimum level filters.
func Match(line []byte, searchStr string, minLevel string) bool {
	if searchStr != "" && !bytes.Contains(line, []byte(searchStr)) {
		return false
	}

	minLevel = strings.TrimSpace(minLevel)
	if minLevel == "" {
		return true
	}

	for i, level := range levels {
		if level != minLevel {
			continue
		}
		for _, allowed := range levels[i:] {
			if bytes.Contains(line, []byte(allowed)) {
				return true
			}
		}
		return false
	}

	return true
}
