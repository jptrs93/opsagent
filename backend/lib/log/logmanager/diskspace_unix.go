//go:build unix

package logmanager

import (
	"fmt"
	"os"
	"syscall"
)

func ensureFreeSpace(dir string, need int64) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < need {
		return fmt.Errorf("insufficient free space under %s: %d bytes free, %d needed", dir, free, need)
	}
	return nil
}
