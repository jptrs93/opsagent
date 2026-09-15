package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/ainit"
)

func newStoreID() string {
	return uuid.Must(uuid.NewV7()).String()
}

func localPath(storeID string) string {
	return filepath.Join(ainit.StaticConfig.LargeAssetsDir, storeID)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func objectKey(prefix, storeID string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return storeID
	}
	return prefix + "/" + storeID
}

func hashBlob(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
