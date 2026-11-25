//go:build !unix

package metrics

import "errors"

var errNoFilesystemStats = errors.New("filesystem usage is not available on this platform")

func statFS(path string) (FSUsage, error) {
	return FSUsage{}, errNoFilesystemStats
}

func deviceOf(path string) (uint64, error) {
	return 0, errNoFilesystemStats
}
