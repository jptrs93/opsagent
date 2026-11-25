package metrics

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
)

const (
	VolumeInterval = 30 * time.Minute

	volumeStartDelay   = time.Minute
	volumeScanInterval = 30 * time.Second
	volumeWalkTimeout  = 10 * time.Minute
	volumeYieldEvery   = 2048
	volumeYieldMax     = 200 * time.Millisecond
)

type VolumeUsage struct {
	Bytes  uint64
	Inodes uint64
}

type VolumeReading struct {
	DeploymentID int32
	Time         time.Time
	Usage        VolumeUsage
	Walk         time.Duration
}

type volumeState struct {
	reading VolumeReading
	seq     uint64
	emitted uint64
	next    time.Time
	failing bool
}

type volumes struct {
	root     string
	interval time.Duration
	now      func() time.Time
	walk     func(ctx context.Context, dir string) (VolumeUsage, error)

	mu    sync.Mutex
	state map[int32]*volumeState
}

func newVolumes(root string) *volumes {
	return &volumes{
		root:     root,
		interval: VolumeInterval,
		now:      time.Now,
		walk:     walkVolume,
		state:    make(map[int32]*volumeState),
	}
}

func (s *Sampler) RunVolumes(ctx context.Context, root string) {
	ctx = logu.AddTag(ctx, "Metrics")
	v := newVolumes(root)
	s.mu.Lock()
	s.volumes = v
	s.mu.Unlock()

	timer := time.NewTimer(volumeStartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			v.pass(ctx)
			timer.Reset(volumeScanInterval)
		}
	}
}

func (s *Sampler) Volumes() []VolumeReading {
	s.mu.Lock()
	v := s.volumes
	s.mu.Unlock()
	if v == nil {
		return nil
	}
	return v.latest()
}

func (v *volumes) pass(ctx context.Context) {
	entries, err := os.ReadDir(v.root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.DebugContext(ctx, fmt.Sprintf("listing volume root %s failed", v.root), "err", err)
		return
	}
	present := make(map[int32]struct{}, len(entries))
	for _, e := range entries {
		if id, ok := volumeDirDeployment(e.Name()); ok && e.IsDir() {
			present[id] = struct{}{}
		}
	}
	now := v.now()
	var due []int32
	v.mu.Lock()
	for id := range v.state {
		if _, ok := present[id]; !ok {
			delete(v.state, id)
		}
	}
	for id := range present {
		st := v.state[id]
		if st == nil {
			st = &volumeState{}
			v.state[id] = st
		}
		if !now.Before(st.next) {
			due = append(due, id)
		}
	}
	v.mu.Unlock()
	slices.Sort(due)
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		v.measure(ctx, id)
	}
}

func (v *volumes) measure(ctx context.Context, id int32) {
	walkCtx, cancel := context.WithTimeout(ctx, volumeWalkTimeout)
	start := v.now()
	usage, err := v.walk(walkCtx, filepath.Join(v.root, strconv.FormatInt(int64(id), 10)))
	cancel()
	end := v.now()
	if ctx.Err() != nil {
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.state[id]
	if st == nil {
		return
	}
	st.next = volumeSlot(id, end.Add(v.interval/2), v.interval)
	switch {
	case err == nil:
		st.failing = false
		st.seq++
		st.reading = VolumeReading{DeploymentID: id, Time: end, Usage: usage, Walk: end.Sub(start)}
	case errors.Is(err, os.ErrNotExist):
	case !st.failing:
		st.failing = true
		if errors.Is(err, context.DeadlineExceeded) {
			slog.WarnContext(ctx, fmt.Sprintf("measuring the volume of deployment %d took longer than %s; it has no reading", id, volumeWalkTimeout))
		} else {
			slog.WarnContext(ctx, fmt.Sprintf("measuring the volume of deployment %d failed", id), "err", err)
		}
	}
}

func (v *volumes) latest() []VolumeReading {
	v.mu.Lock()
	out := make([]VolumeReading, 0, len(v.state))
	for _, st := range v.state {
		if st.seq > 0 {
			out = append(out, st.reading)
		}
	}
	v.mu.Unlock()
	slices.SortFunc(out, func(a, b VolumeReading) int { return int(a.DeploymentID) - int(b.DeploymentID) })
	return out
}

func (v *volumes) attach(samples []Sample) {
	v.mu.Lock()
	defer v.mu.Unlock()
	chosen := map[int32]int{}
	for i := range samples {
		id := samples[i].Key.DeploymentID
		st := v.state[id]
		if st == nil || st.seq == st.emitted {
			continue
		}
		if j, ok := chosen[id]; !ok || keyLess(samples[i].Key, samples[j].Key) {
			chosen[id] = i
		}
	}
	for id, i := range chosen {
		st := v.state[id]
		usage := st.reading.Usage
		samples[i].Volume = &usage
		st.emitted = st.seq
	}
}

func keyLess(a, b TargetKey) bool {
	if a.ScheduledInstanceID != b.ScheduledInstanceID {
		return a.ScheduledInstanceID < b.ScheduledInstanceID
	}
	if a.Ordinal != b.Ordinal {
		return a.Ordinal < b.Ordinal
	}
	if a.DeploymentVersion != b.DeploymentVersion {
		return a.DeploymentVersion < b.DeploymentVersion
	}
	return a.Run < b.Run
}

func volumeDirDeployment(name string) (int32, bool) {
	id, err := strconv.ParseUint(name, 10, 64)
	if err != nil || id == 0 || id > math.MaxInt32 || strconv.FormatUint(id, 10) != name {
		return 0, false
	}
	return int32(id), true
}

func volumeOffset(id int32, interval time.Duration) time.Duration {
	secs := uint64(interval / time.Second)
	if secs == 0 {
		return 0
	}
	spread := (uint64(uint32(id)) * 0x9E3779B97F4A7C15) >> 32
	return time.Duration(spread*secs>>32) * time.Second
}

func volumeSlot(id int32, after time.Time, interval time.Duration) time.Time {
	slot := after.Truncate(interval).Add(volumeOffset(id, interval))
	if slot.Before(after) {
		slot = slot.Add(interval)
	}
	return slot
}

type fileID struct {
	dev, ino uint64
}

func walkVolume(ctx context.Context, root string) (VolumeUsage, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return VolumeUsage{}, err
	}
	_, rootDev, _, _ := volumeStat(rootInfo)
	var usage VolumeUsage
	linked := map[fileID]struct{}{}
	entries := 0
	batchStart := time.Now()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		bytes, dev, ino, nlink := volumeStat(info)
		if d.IsDir() {
			if dev != rootDev {
				return fs.SkipDir
			}
		} else if nlink > 1 {
			id := fileID{dev, ino}
			if _, ok := linked[id]; ok {
				return nil
			}
			linked[id] = struct{}{}
		}
		usage.Bytes += bytes
		usage.Inodes++
		entries++
		if entries%volumeYieldEvery == 0 {
			if err := volumeYield(ctx, &batchStart); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return VolumeUsage{}, err
	}
	if err := ctx.Err(); err != nil {
		return VolumeUsage{}, err
	}
	return usage, nil
}

func volumeYield(ctx context.Context, batchStart *time.Time) error {
	pause := min(time.Since(*batchStart), volumeYieldMax)
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	*batchStart = time.Now()
	return nil
}
