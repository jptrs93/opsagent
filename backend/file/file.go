package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jptrs93/goutil/syncu"
)

var locks = syncu.NewStripedLock[string](25)

func SafeWrite(path string, data []byte) error {
	locks.Lock(path)
	defer locks.Unlock(path)
	return writeAtomically(path, data)
}

func SafeAppend(path string, data []byte) error {
	locks.Lock(path)
	defer locks.Unlock(path)
	current, err := readUnlocked(path)
	if err != nil {
		return err
	}
	updated := append(current, data...)
	return writeAtomically(path, updated)
}

func SafeInsert(path string, start int, data []byte, overwrite bool) error {
	locks.Lock(path)
	defer locks.Unlock(path)
	current, err := readUnlocked(path)
	if err != nil {
		return err
	}
	if start < 0 {
		return fmt.Errorf("negative start: %d", start)
	}
	if start > len(current) {
		return fmt.Errorf("start %d exceeds file length %d", start, len(current))
	}
	updated := append([]byte{}, current[:start]...)
	updated = append(updated, data...)
	if overwrite {
		overwriteEnd := start + len(data)
		if overwriteEnd < len(current) {
			updated = append(updated, current[overwriteEnd:]...)
		}
	} else {
		updated = append(updated, current[start:]...)
	}
	return writeAtomically(path, updated)
}

func Read(path string) ([]byte, error) {
	locks.Lock(path)
	defer locks.Unlock(path)
	return readUnlocked(path)
}

func readUnlocked(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	return b, nil
}

func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %q: %w", path, err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temp file %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %q with %q: %w", path, tmpPath, err)
	}
	return nil
}
