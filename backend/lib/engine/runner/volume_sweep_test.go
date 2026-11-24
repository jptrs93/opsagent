package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func volumeTree(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, name, "default", "nested")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte("x"), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func volumeNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func placedOn(ids ...uint64) func() map[uint64]struct{} {
	return func() map[uint64]struct{} {
		out := make(map[uint64]struct{}, len(ids))
		for _, id := range ids {
			out[id] = struct{}{}
		}
		return out
	}
}

func TestVolumeSweepKeepsEverythingUntilTheLiveSetIsKnown(t *testing.T) {
	root := volumeTree(t, "7", "8", "9.deleting")
	s := newVolumeSweeper(root, placedOn())
	s.sweep(context.Background())
	if got := volumeNames(t, root); !slices.Equal(got, []string{"7", "8"}) {
		t.Fatalf("volumes before the live set is known = %v, want [7 8]", got)
	}
}

func TestVolumeSweepRemovesOnlyDeletedDeployments(t *testing.T) {
	root := volumeTree(t, "7", "8", "9", "10", "07", "0", "lost+found", "11.deleting")
	outside := volumeTree(t, "kept")
	if err := os.WriteFile(filepath.Join(root, "12"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "kept"), filepath.Join(root, "13")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "kept"), filepath.Join(root, "9", "default", "link")); err != nil {
		t.Fatal(err)
	}
	s := newVolumeSweeper(root, placedOn(8))
	s.SetLive([]uint64{7})
	s.sweep(context.Background())
	want := []string{"0", "07", "12", "13", "7", "8", "lost+found"}
	if got := volumeNames(t, root); !slices.Equal(got, want) {
		t.Fatalf("volumes after the sweep = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(outside, "kept", "default", "nested", "data")); err != nil {
		t.Fatalf("the sweep followed a symlink out of the volume root: %v", err)
	}

	s = newVolumeSweeper(root, placedOn())
	s.SetLive([]uint64{7})
	s.sweep(context.Background())
	want = []string{"0", "07", "12", "13", "7", "lost+found"}
	if got := volumeNames(t, root); !slices.Equal(got, want) {
		t.Fatalf("volumes once deployment 8 has no placement = %v, want %v", got, want)
	}
}

func TestVolumeSweepRunsWhenTheLiveSetChanges(t *testing.T) {
	root := volumeTree(t, "7", "8")
	s := newVolumeSweeper(root, placedOn())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	s.SetLive([]uint64{7, 8})
	s.SetLive([]uint64{7})
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := volumeNames(t, root)
		if slices.Equal(got, []string{"7"}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("volumes after deployment 8 left the live set = %v, want [7]", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
