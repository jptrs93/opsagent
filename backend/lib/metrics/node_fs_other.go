//go:build unix && !linux

package metrics

import (
	"fmt"
	"os"
	"syscall"
)

func statFS(path string) (FSUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return FSUsage{}, err
	}
	block := uint64(st.Bsize)
	return FSUsage{
		TotalBytes:  uint64(st.Blocks) * block,
		FreeBytes:   uint64(st.Bfree) * block,
		AvailBytes:  uint64(st.Bavail) * block,
		InodesTotal: uint64(st.Files),
		InodesFree:  uint64(st.Ffree),
	}, nil
}

func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("no device id for %s", path)
	}
	return uint64(st.Dev), nil
}
