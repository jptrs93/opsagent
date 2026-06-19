package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const defaultAssetPath = "/tmp/opendeploy-e2e-large-asset.bin"

func main() {
	path := strings.TrimSpace(os.Getenv("OPENDEPLOY_E2E_ASSET_PATH"))
	if path == "" {
		path = defaultAssetPath
	}
	expected := strings.TrimSpace(os.Getenv("OPENDEPLOY_E2E_ASSET_SHA256"))

	actual, size, err := hashFile(path)
	if err != nil {
		fmt.Printf("largeassetverify asset read error path=%s err=%v\n", path, err)
		os.Exit(1)
	}

	fmt.Printf("largeassetverify asset read path=%s bytes=%d\n", path, size)
	fmt.Printf("largeassetverify asset sha256=%s\n", actual)
	if expected != "" && actual != expected {
		fmt.Printf("largeassetverify asset verified false expected=%s actual=%s\n", expected, actual)
		os.Exit(1)
	}
	fmt.Println("largeassetverify asset verified true")

	for count := 1; ; count++ {
		fmt.Printf("largeassetverify count=%d time=%s\n", count, time.Now().Format(time.RFC3339))
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
