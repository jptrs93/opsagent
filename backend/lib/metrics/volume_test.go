package metrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkVolumeCountsEveryEntryAndAHardLinkOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "default", "a"), 100_000)
	writeFile(t, filepath.Join(root, "default", "sub", "b"), 50_000)
	before, err := walkVolume(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if before.Inodes != 5 {
		t.Fatalf("inodes = %d, want 5: the root, default, sub and two files", before.Inodes)
	}
	if before.Bytes < 150_000 {
		t.Fatalf("bytes = %d, want at least the 150000 written", before.Bytes)
	}

	if err := os.Link(filepath.Join(root, "default", "a"), filepath.Join(root, "default", "a-link")); err != nil {
		t.Fatal(err)
	}
	linked, err := walkVolume(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Inodes != before.Inodes {
		t.Fatalf("inodes after a hard link = %d, want %d: a second name is not a second file", linked.Inodes, before.Inodes)
	}
	if linked.Bytes >= before.Bytes+100_000 {
		t.Fatalf("bytes after a hard link = %d, was %d: the file was counted twice", linked.Bytes, before.Bytes)
	}
}

func TestWalkVolumeDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "big"), 1_000_000)
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	usage, err := walkVolume(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Inodes != 2 || usage.Bytes >= 1_000_000 {
		t.Fatalf("usage = %+v, want the root and the link only", usage)
	}
}

func TestWalkVolumeStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := walkVolume(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := walkVolume(context.Background(), filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not exist for a missing volume", err)
	}
}

func TestVolumeDirDeployment(t *testing.T) {
	for name, want := range map[string]int32{"7": 7, "120": 120, "0": 0, "07": 0, "7.deleting": 0, "abc": 0, "-3": 0, "99999999999": 0} {
		got, ok := volumeDirDeployment(name)
		if got != want || ok != (want != 0) {
			t.Fatalf("volumeDirDeployment(%q) = %d, %v", name, got, ok)
		}
	}
}

func TestVolumeSlotsAreSpreadAndRepeatEveryInterval(t *testing.T) {
	seen := map[time.Duration]int32{}
	for id := int32(1); id <= 40; id++ {
		off := volumeOffset(id, VolumeInterval)
		if off < 0 || off >= VolumeInterval {
			t.Fatalf("offset of %d = %s, outside the interval", id, off)
		}
		if other, dup := seen[off]; dup {
			t.Fatalf("deployments %d and %d share the offset %s", other, id, off)
		}
		seen[off] = id
	}
	after := time.Date(2026, 11, 25, 10, 7, 0, 0, time.UTC)
	slot := volumeSlot(9, after, VolumeInterval)
	if slot.Before(after) || slot.Sub(after) >= VolumeInterval {
		t.Fatalf("slot %s is not within one interval after %s", slot, after)
	}
	if next := volumeSlot(9, slot.Add(VolumeInterval/2), VolumeInterval); !next.Equal(slot.Add(VolumeInterval)) {
		t.Fatalf("next slot = %s, want %s", next, slot.Add(VolumeInterval))
	}
}

type fakeVolumes struct {
	*volumes
	clock time.Time
	walks []int32
	usage map[int32]VolumeUsage
	fail  map[int32]error
}

func newFakeVolumes(t *testing.T, ids ...int32) *fakeVolumes {
	t.Helper()
	f := &fakeVolumes{
		volumes: newVolumes(t.TempDir()),
		clock:   time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
		usage:   map[int32]VolumeUsage{},
		fail:    map[int32]error{},
	}
	f.now = func() time.Time { return f.clock }
	f.walk = func(_ context.Context, dir string) (VolumeUsage, error) {
		id64, _ := strconv.ParseInt(filepath.Base(dir), 10, 32)
		id := int32(id64)
		f.walks = append(f.walks, id)
		return f.usage[id], f.fail[id]
	}
	for _, id := range ids {
		f.addDir(t, id)
	}
	return f
}

func (f *fakeVolumes) addDir(t *testing.T, id int32) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, strconv.Itoa(int(id))), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestVolumePassMeasuresEachDirectoryOnceThenOnItsSlot(t *testing.T) {
	f := newFakeVolumes(t, 3, 12)
	if err := os.MkdirAll(filepath.Join(f.root, "12.deleting"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.usage[3] = VolumeUsage{Bytes: 300, Inodes: 3}
	f.usage[12] = VolumeUsage{Bytes: 1200, Inodes: 12}

	f.pass(context.Background())
	if len(f.walks) != 2 || f.walks[0] != 3 || f.walks[1] != 12 {
		t.Fatalf("walks = %v, want each volume once in id order", f.walks)
	}
	got := f.latest()
	if len(got) != 2 || got[0].DeploymentID != 3 || got[0].Usage.Bytes != 300 || got[1].Usage.Inodes != 12 {
		t.Fatalf("latest = %+v", got)
	}

	f.clock = f.clock.Add(volumeScanInterval)
	f.pass(context.Background())
	if len(f.walks) != 2 {
		t.Fatalf("walks = %v, want no second walk before the slot", f.walks)
	}

	slot3 := f.state[3].next
	slot12 := f.state[12].next
	if slot3.Equal(slot12) {
		t.Fatalf("both volumes share the slot %s", slot3)
	}
	first, second := int32(3), int32(12)
	if slot12.Before(slot3) {
		first, second = 12, 3
	}
	f.clock = f.state[first].next
	f.pass(context.Background())
	if len(f.walks) != 3 || f.walks[2] != first {
		t.Fatalf("walks = %v, want only %d measured at its slot", f.walks, first)
	}
	f.clock = f.state[second].next
	f.pass(context.Background())
	if len(f.walks) != 4 || f.walks[3] != second {
		t.Fatalf("walks = %v, want %d measured at its slot", f.walks, second)
	}
	if gap := f.state[second].next.Sub(f.clock); gap != VolumeInterval {
		t.Fatalf("next slot is %s away, want %s", gap, VolumeInterval)
	}
}

func TestVolumePassForgetsARemovedDirectoryAndKeepsTheLastReadingOnFailure(t *testing.T) {
	f := newFakeVolumes(t, 3, 12)
	f.usage[3] = VolumeUsage{Bytes: 300, Inodes: 3}
	f.usage[12] = VolumeUsage{Bytes: 1200, Inodes: 12}
	f.pass(context.Background())

	if err := os.RemoveAll(filepath.Join(f.root, "12")); err != nil {
		t.Fatal(err)
	}
	f.fail[3] = context.DeadlineExceeded
	f.clock = f.state[3].next
	f.pass(context.Background())
	got := f.latest()
	if len(got) != 1 || got[0].DeploymentID != 3 || got[0].Usage.Bytes != 300 {
		t.Fatalf("latest = %+v, want the earlier reading of 3 and nothing for the removed 12", got)
	}
	if f.state[3].seq != 1 || !f.state[3].failing {
		t.Fatalf("state of 3 = %+v, want one reading and a failing mark", f.state[3])
	}
}

func TestAReadingGoesOnOneRunOfTheDeploymentOnce(t *testing.T) {
	f := newFakeVolumes(t, 3)
	f.usage[3] = VolumeUsage{Bytes: 300, Inodes: 3}
	tick := func() []Sample {
		samples := []Sample{
			{Key: TargetKey{DeploymentID: 3, ScheduledInstanceID: 20, Run: 1}},
			{Key: TargetKey{DeploymentID: 3, ScheduledInstanceID: 10, Run: 4}},
			{Key: TargetKey{DeploymentID: 8, ScheduledInstanceID: 30, Run: 1}},
		}
		f.attach(samples)
		return samples
	}
	if s := tick(); s[0].Volume != nil || s[1].Volume != nil || s[2].Volume != nil {
		t.Fatalf("a sample carried a volume before any reading: %+v", s)
	}
	f.pass(context.Background())
	s := tick()
	if s[1].Volume == nil || *s[1].Volume != (VolumeUsage{Bytes: 300, Inodes: 3}) {
		t.Fatalf("the lowest run of deployment 3 has volume %+v", s[1].Volume)
	}
	if s[0].Volume != nil || s[2].Volume != nil {
		t.Fatalf("a second run or another deployment carried the reading: %+v %+v", s[0].Volume, s[2].Volume)
	}
	if s := tick(); s[0].Volume != nil || s[1].Volume != nil {
		t.Fatalf("the reading was written twice")
	}

	f.usage[3] = VolumeUsage{Bytes: 900, Inodes: 9}
	f.clock = f.state[3].next
	f.pass(context.Background())
	if s := tick(); s[1].Volume == nil || s[1].Volume.Bytes != 900 {
		t.Fatalf("the next reading was not written: %+v", s[1].Volume)
	}
}

func TestAReadingWaitsForARunOfItsDeployment(t *testing.T) {
	f := newFakeVolumes(t, 3)
	f.usage[3] = VolumeUsage{Bytes: 300, Inodes: 3}
	f.pass(context.Background())

	other := []Sample{{Key: TargetKey{DeploymentID: 8, Run: 1}}}
	f.attach(other)
	if other[0].Volume != nil {
		t.Fatalf("another deployment carried the reading")
	}
	started := []Sample{{Key: TargetKey{DeploymentID: 3, Run: 1}}}
	f.attach(started)
	if started[0].Volume == nil || started[0].Volume.Bytes != 300 {
		t.Fatalf("the reading was not written once a run of the deployment appeared: %+v", started[0].Volume)
	}
}
