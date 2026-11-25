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
	block := uint64(st.Frsize)
	if block == 0 {
		block = uint64(st.Bsize)
	}
	return FSUsage{
		TotalBytes:  st.Blocks * block,
		FreeBytes:   st.Bfree * block,
		AvailBytes:  st.Bavail * block,
		InodesTotal: st.Files,
		InodesFree:  st.Ffree,
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
