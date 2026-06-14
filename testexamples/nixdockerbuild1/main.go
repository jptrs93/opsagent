package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_MESSAGE=%s\n", os.Getenv("OPENDEPLOY_E2E_MESSAGE"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_COLOR=%s\n", os.Getenv("OPENDEPLOY_E2E_COLOR"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_CONFIG=%s\n", os.Getenv("OPENDEPLOY_E2E_CONFIG"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_SECRET=%s\n", os.Getenv("OPENDEPLOY_E2E_SECRET"))
	printAssetMount("/tmp")

	for count := 1; ; count++ {
		fmt.Printf("nixdockerbuild1 count=%d time=%s\n", count, time.Now().Format(time.RFC3339))
		time.Sleep(10 * time.Second)
	}
}

func printAssetMount(root string) {
	info, err := os.Stat(root)
	if err != nil {
		fmt.Printf("nixdockerbuild1 asset root %s error=%v\n", root, err)
		return
	}
	if !info.IsDir() {
		fmt.Printf("nixdockerbuild1 asset root %s is not a directory\n", root)
		return
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("nixdockerbuild1 asset walk %s error=%v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fmt.Printf("nixdockerbuild1 asset file %s\n", rel)
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			fmt.Printf("nixdockerbuild1 asset read %s error=%v\n", rel, readErr)
			return nil
		}
		fmt.Printf("nixdockerbuild1 asset content %s=%s\n", rel, strings.TrimSpace(string(content)))
		return nil
	})
	if err != nil {
		fmt.Printf("nixdockerbuild1 asset walk root error=%v\n", err)
	}
}
