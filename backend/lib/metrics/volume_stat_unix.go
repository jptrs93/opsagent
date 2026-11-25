//go:build unix

package metrics

import (
	"io/fs"
	"syscall"
)

func volumeStat(info fs.FileInfo) (bytes, dev, ino, nlink uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return uint64(max(info.Size(), 0)), 0, 0, 1
	}
	return uint64(st.Blocks) * 512, uint64(st.Dev), uint64(st.Ino), uint64(st.Nlink)
}
