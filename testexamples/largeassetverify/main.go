package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jptrs93/goutil/logu"
)

const defaultAssetPath = "/tmp/opendeploy-e2e-large-asset.bin"

func main() {
	slog.SetDefault(logu.NewJSONLogger(os.Stdout, slog.LevelInfo))
	path := strings.TrimSpace(os.Getenv("OPENDEPLOY_E2E_ASSET_PATH"))
	if path == "" {
		path = defaultAssetPath
	}
	expected := strings.TrimSpace(os.Getenv("OPENDEPLOY_E2E_ASSET_SHA256"))

	actual, size, err := hashFile(path)
	if err != nil {
		fatalf("largeassetverify asset read error path=%s err=%v", path, err)
	}

	logf("largeassetverify asset read path=%s bytes=%d", path, size)
	logf("largeassetverify asset sha256=%s", actual)
	if expected != "" && actual != expected {
		fatalf("largeassetverify asset verified false expected=%s actual=%s", expected, actual)
	}
	logf("largeassetverify asset verified true")

	for count := 1; ; count++ {
		logf("largeassetverify count=%d time=%s", count, time.Now().Format(time.RFC3339))
		time.Sleep(10 * time.Second)
	}
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
