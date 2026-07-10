package preparerlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLogFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prepare.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	log := &Log{ctx: context.Background(), file: file}

	log.Write("pulling %s", "image")
	log.Error("pull failed: %v", "denied")
	if _, err := log.Output().Write([]byte("raw output\n")); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "==> pulling image\n==> ERROR: pull failed: denied\nraw output\n"
	if string(got) != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}
