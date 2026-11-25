//go:build !unix

package metrics

import "io/fs"

func volumeStat(info fs.FileInfo) (bytes, dev, ino, nlink uint64) {
	return uint64(max(info.Size(), 0)), 0, 0, 1
}
