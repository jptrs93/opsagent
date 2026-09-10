//go:build !unix

package logmanager

import "os"

func ensureFreeSpace(dir string, need int64) error {
	return os.MkdirAll(dir, 0o750)
}
