package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
)

const (
	DefaultInterval = 10 * time.Second

	cgroupRoot = "/sys/fs/cgroup"
)

type TargetKey struct {
	DeploymentID        int32
	ScheduledInstanceID int32
	Ordinal             int32
	SpecVersion         int32
	Run                 int32
}

type TargetSpec struct {
	Key         TargetKey
	PID         uint32
	CgroupsPath string
	HostNetwork bool
}

type Sample struct {
	Key      TargetKey
	Time     time.Time
	Terminal bool
	Cgroup   CgroupMetrics
	Net      *NetMetrics
	OpenFDs  *uint64
}

type Consumer interface {
	Consume(ctx context.Context, samples []Sample)
}

type Sampler struct {
	mu      sync.Mutex
	targets map[TargetKey]*target

	sampleMu sync.Mutex
	ctx      context.Context
	consumer Consumer
}

type target struct {
	spec      TargetSpec
	cgroupDir string
	failing   bool
}

type Registration struct {
	s    *Sampler
	t    *target
	once sync.Once
}

var Default = &Sampler{}

func (s *Sampler) Register(ctx context.Context, spec TargetSpec) *Registration {
	t := &target{spec: spec}
	if spec.CgroupsPath != "" {
		t.cgroupDir = filepath.Join(cgroupRoot, spec.CgroupsPath)
	}
	if spec.PID != 0 {
		resolved, err := procCgroupPath(spec.PID)
		switch {
		case err != nil:
			slog.WarnContext(ctx, fmt.Sprintf("reading cgroup of pid %d failed", spec.PID), "err", err)
		case t.cgroupDir == "":
			t.cgroupDir = filepath.Join(cgroupRoot, resolved)
		case resolved != spec.CgroupsPath:
			slog.WarnContext(ctx, fmt.Sprintf("pid %d is in cgroup %s, expected %s; sampling the assigned path", spec.PID, resolved, spec.CgroupsPath))
		}
	}
	s.mu.Lock()
	if s.targets == nil {
		s.targets = make(map[TargetKey]*target)
	}
	s.targets[spec.Key] = t
	s.mu.Unlock()
	return &Registration{s: s, t: t}
}

func (r *Registration) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		s := r.s
		s.mu.Lock()
		if cur, ok := s.targets[r.t.spec.Key]; ok && cur == r.t {
			delete(s.targets, r.t.spec.Key)
		}
		s.mu.Unlock()

		s.sampleMu.Lock()
		defer s.sampleMu.Unlock()
		if s.consumer == nil {
			return
		}
		sample := s.read(s.ctx, r.t, time.Now())
		sample.Terminal = true
		s.consumer.Consume(s.ctx, []Sample{sample})
	})
}

func (s *Sampler) Run(ctx context.Context, interval time.Duration, consumer Consumer) {
	ctx = logu.AddTag(ctx, "Metrics")
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("cgroup v2 hierarchy not found at %s; container metrics disabled", cgroupRoot), "err", err)
		return
	}
	s.sampleMu.Lock()
	s.ctx = ctx
	s.consumer = consumer
	s.sampleMu.Unlock()

	timer := time.NewTimer(time.Until(nextTick(time.Now(), interval)))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.sampleMu.Lock()
			if samples := s.sampleAll(ctx, time.Now()); len(samples) > 0 {
				consumer.Consume(ctx, samples)
			}
			s.sampleMu.Unlock()
			timer.Reset(time.Until(nextTick(time.Now(), interval)))
		}
	}
}

func nextTick(now time.Time, interval time.Duration) time.Time {
	return now.Truncate(interval).Add(interval)
}

func (s *Sampler) sampleAll(ctx context.Context, now time.Time) []Sample {
	s.mu.Lock()
	targets := make([]*target, 0, len(s.targets))
	for _, t := range s.targets {
		targets = append(targets, t)
	}
	s.mu.Unlock()

	samples := make([]Sample, 0, len(targets))
	for _, t := range targets {
		samples = append(samples, s.read(ctx, t, now))
	}
	return samples
}

func (s *Sampler) read(ctx context.Context, t *target, now time.Time) Sample {
	sample := Sample{Key: t.spec.Key, Time: now}
	var err error
	if t.cgroupDir != "" {
		sample.Cgroup, err = readCgroup(t.cgroupDir)
	}
	if t.spec.PID != 0 {
		sample.OpenFDs = readOpenFDs(t.spec.PID)
		if !t.spec.HostNetwork {
			var netErr error
			sample.Net, netErr = readNet(t.spec.PID)
			if err == nil {
				err = netErr
			}
		}
	}
	if err != nil && !t.failing {
		t.failing = true
		slog.DebugContext(ctx, fmt.Sprintf("sampling dep=%d run=%d failed", t.spec.Key.DeploymentID, t.spec.Key.Run), "err", err)
	} else if err == nil {
		t.failing = false
	}
	return sample
}
