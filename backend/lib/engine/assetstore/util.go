package assetstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func localPath(assetID int32) string {
	return filepath.Join(ainit.StaticConfig.LargeAssetsDir, strconv.Itoa(int(assetID)))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func localLocation(assetID int32) string {
	return "local://" + strconv.Itoa(int(assetID))
}

func parseLocalLocation(location string) (int32, error) {
	const scheme = "local://"
	if !strings.HasPrefix(location, scheme) {
		return 0, fmt.Errorf("unsupported asset location %q", location)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(location, scheme), 10, 32)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid asset location %q", location)
	}
	return int32(id), nil
}

func pendingLocation(name string) string {
	return "pending://" + name
}

func parsePendingLocation(location string) (string, error) {
	const scheme = "pending://"
	name := strings.TrimPrefix(location, scheme)
	if !strings.HasPrefix(location, scheme) || name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid pending asset location %q", location)
	}
	return name, nil
}

func objectKey(prefix string, assetID int32) string {
	base := strconv.Itoa(int(assetID))
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return base
	}
	return prefix + "/" + base
}

func parseS3Location(location string) (string, string, error) {
	const scheme = "s3://"
	if !strings.HasPrefix(location, scheme) {
		return "", "", fmt.Errorf("unsupported asset location %q", location)
	}
	parts := strings.SplitN(strings.TrimPrefix(location, scheme), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid asset location %q", location)
	}
	return parts[0], parts[1], nil
}
