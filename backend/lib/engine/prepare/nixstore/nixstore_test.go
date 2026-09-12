package nixstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/network"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

type fakeRunner struct {
	mu       sync.Mutex
	root     string
	imageNix string
	digest   string
	pulled   bool
	specs    []ctrd.ContainerSpec
	failGC   bool
}

func (f *fakeRunner) EnsureImage(context.Context, string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pulled := !f.pulled
	f.pulled = true
	return f.digest, pulled, nil
}

var imageNixRE = regexp.MustCompile(`cp -a /nix `)

func (f *fakeRunner) RunBuild(_ context.Context, spec ctrd.ContainerSpec, stdout, stderr io.Writer) (ctrd.BuildResult, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if len(spec.Args) == 0 {
		return ctrd.BuildResult{}, errors.New("no args")
	}
	if spec.Args[0] == "nix" {
		if f.failGC {
			return ctrd.BuildResult{Code: 1}, nil
		}
		return ctrd.BuildResult{}, nil
	}
	script := strings.ReplaceAll(spec.Args[2], containerMountRoot, f.root)
	script = imageNixRE.ReplaceAllString(script, "cp -a "+f.imageNix+" ")
	cmd := exec.Command("sh", "-ec", script)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ctrd.BuildResult{Code: uint32(exitErr.ExitCode())}, nil
		}
		return ctrd.BuildResult{}, err
	}
	return ctrd.BuildResult{}, nil
}

func (f *fakeRunner) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ops []string
	for _, spec := range f.specs {
		parts := strings.Split(spec.ID, "-")
		ops = append(ops, parts[2])
	}
	return ops
}

func newTestManager(t *testing.T, cfg Config) (*Manager, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	imageNix := filepath.Join(root, "image-nix")
	for _, dir := range []string{"store/aaaa-hello/bin", "var/nix/db", "var/nix/profiles"} {
		if err := os.MkdirAll(filepath.Join(imageNix, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(imageNix, "store/aaaa-hello/bin/hello"), []byte("#!/bin/sh\necho hello\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageNix, "var/nix/db/db.sqlite"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{root: filepath.Join(root, "nix"), imageNix: imageNix, digest: "sha256:aaaa"}
	cfg.Root = runner.root
	if cfg.Image == "" {
		cfg.Image = "example.test/nix:1"
	}
	return New(cfg, runner), runner
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestKeyMatchesGitManager(t *testing.T) {
	if Key("github.com/acme/app") != repogit.RepoKey("github.com/acme/app") {
		t.Fatal("store key must match the git manager's repository key")
	}
	if Key(" github.com/acme/app ") != Key("github.com/acme/app") {
		t.Fatal("store key must trim the URL")
	}
}

func TestAcquireSerialisesRepositoryAndCapsNode(t *testing.T) {
	m, _ := newTestManager(t, Config{MaxConcurrent: 2})
	ctx := context.Background()
	releaseA, err := m.Acquire(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	go func() {
		release, err := m.Acquire(ctx, "a")
		if err == nil {
			release()
		}
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("second build of one repository must wait for the first")
	case <-time.After(50 * time.Millisecond):
	}
	releaseB, err := m.Acquire(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	capped, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := m.Acquire(capped, "c"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third concurrent build must wait on the node cap, got %v", err)
	}
	releaseA()
	releaseA()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting build did not proceed after release")
	}
	if _, ok := m.TryLock("b"); ok {
		t.Fatal("TryLock must fail while a build holds the repository")
	}
	releaseB()
	release, ok := m.TryLock("b")
	if !ok {
		t.Fatal("TryLock must succeed on an idle repository")
	}
	release()
}

func TestEnsureSeedsTemplateAndStoreThenReuses(t *testing.T) {
	m, runner := newTestManager(t, Config{})
	sink := &logSink{}
	key := Key("github.com/acme/app")
	store, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, sink.log)
	if err != nil {
		t.Fatal(err)
	}
	if store.Root != filepath.Join(m.cfg.Root, "stores", key, "nix") {
		t.Fatalf("store root = %s", store.Root)
	}
	if got := runner.ops(); !slices.Equal(got, []string{"template", "seed"}) {
		t.Fatalf("maintenance ops = %v", got)
	}
	for _, spec := range runner.specs {
		if !spec.NoNetwork || !spec.DefaultSeccomp || len(spec.Mounts) != 1 || spec.Mounts[0].Source != m.cfg.Root || spec.Mounts[0].Dest != containerMountRoot {
			t.Fatalf("maintenance spec = %+v", spec)
		}
	}
	templateHello := filepath.Join(m.templateDir("sha256:aaaa"), "nix/store/aaaa-hello/bin/hello")
	storeHello := filepath.Join(store.Root, "store/aaaa-hello/bin/hello")
	if inode(t, templateHello) != inode(t, storeHello) {
		t.Fatal("store paths must be hardlinked from the template")
	}
	if inode(t, filepath.Join(m.templateDir("sha256:aaaa"), "nix/var/nix/db/db.sqlite")) == inode(t, filepath.Join(store.Root, "var/nix/db/db.sqlite")) {
		t.Fatal("store database must be copied, not hardlinked")
	}
	for _, want := range []string{"pulled build image", "seeding store template", "creating store for repository github.com/acme/app"} {
		if !sink.contains(want) {
			t.Fatalf("log missing %q: %v", want, sink.lines)
		}
	}
	meta, err := m.readMeta(key)
	if err != nil || meta.Repo != "github.com/acme/app" || meta.ImageDigest != "sha256:aaaa" || meta.SeededAt.IsZero() {
		t.Fatalf("meta = %+v err=%v", meta, err)
	}
	if _, err := os.ReadFile(m.NixConfPath()); err != nil {
		t.Fatal(err)
	}

	again, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, sink.log)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.ops()) != 2 || again.Meta.SeededAt != meta.SeededAt {
		t.Fatalf("second ensure must reuse the store: ops=%v", runner.ops())
	}
}

func TestEnsureReseedsOnImageChangeAndReset(t *testing.T) {
	m, runner := newTestManager(t, Config{})
	key := Key("github.com/acme/app")
	ctx := context.Background()
	if _, err := m.Ensure(ctx, key, "github.com/acme/app", io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.StoreRoot(key), "store", "poison"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.digest = "sha256:bbbb"
	sink := &logSink{}
	store, err := m.Ensure(ctx, key, "github.com/acme/app", io.Discard, sink.log)
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.ops(); !slices.Equal(got, []string{"template", "seed", "delete", "template", "seed"}) {
		t.Fatalf("ops = %v", got)
	}
	if !sink.contains("was seeded from build image sha256:aaaa; reseeding") {
		t.Fatalf("log = %v", sink.lines)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "store", "poison")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reseeded store still holds the old contents")
	}
	if _, err := os.Stat(m.templateDir("sha256:aaaa")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("template for the previous image must be removed")
	}
	if store.Meta.ImageDigest != "sha256:bbbb" {
		t.Fatalf("meta digest = %s", store.Meta.ImageDigest)
	}

	m.RequestReset("github.com/acme/app", store.Meta.SeededAt.Add(-time.Second))
	if _, err := m.Ensure(ctx, key, "github.com/acme/app", io.Discard, nil); err != nil || len(runner.ops()) != 5 {
		t.Fatalf("stale reset request must not reseed: ops=%v err=%v", runner.ops(), err)
	}
	m.RequestReset("github.com/acme/app", store.Meta.SeededAt.Add(time.Second))
	reseeded, err := m.Ensure(ctx, key, "github.com/acme/app", io.Discard, sink.log)
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.ops(); !slices.Equal(got[5:], []string{"delete", "seed"}) {
		t.Fatalf("ops after reset = %v", got)
	}
	if !sink.contains("operator reset outstanding") || !reseeded.Meta.SeededAt.After(store.Meta.SeededAt) {
		t.Fatalf("reset not applied: %v", sink.lines)
	}
	if m.resetPending(key, reseeded.Meta) {
		t.Fatal("satisfied reset request must be cleared")
	}
}

func TestNewBuildScratchAndContainerSpec(t *testing.T) {
	m, _ := newTestManager(t, Config{CABundle: "/etc/ssl/certs/ca.crt", Resources: ctrd.Resources{MemoryBytes: 1 << 30, CPUs: 2, Pids: 100}})
	key := Key("github.com/acme/app")
	store, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	build, err := m.NewBuild(store, "fd00::53")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(build.Scratch, "home")); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(build.ResolvConf); err != nil || string(raw) != "nameserver fd00::53\noptions ndots:1\n" {
		t.Fatalf("resolv.conf = %q err=%v", raw, err)
	}
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	net := &network.ContainerNet{NetnsPath: "/run/netns/" + build.ContainerID()}
	spec := build.ContainerSpec(checkout, "services/api", ".#apiImage", net)
	if spec.ID != build.ContainerID() || !strings.HasPrefix(spec.ID, "opendeploy-16777215-") {
		t.Fatalf("container id = %s", spec.ID)
	}
	wantMounts := []ctrd.Mount{
		{Source: store.Root, Dest: "/nix"},
		{Source: checkout, Dest: "/build/src", ReadOnly: true},
		{Source: filepath.Join(m.cfg.Root, "etc", "empty"), Dest: "/build/src/.git", ReadOnly: true},
		{Source: m.NixConfPath(), Dest: "/etc/nix/nix.conf", ReadOnly: true},
		{Source: build.Scratch, Dest: "/build/tmp"},
		{Source: "/etc/ssl/certs/ca.crt", Dest: containerCAPath, ReadOnly: true},
	}
	if !slices.Equal(spec.Mounts, wantMounts) {
		t.Fatalf("mounts = %+v, want %+v", spec.Mounts, wantMounts)
	}
	wantArgs := []string{"nix", "build", "--no-update-lock-file", "--no-link", "--print-out-paths", "-L", "path:/build/src?dir=services/api#apiImage"}
	if !slices.Equal(spec.Args, wantArgs) {
		t.Fatalf("args = %q", spec.Args)
	}
	if spec.Cwd != "/build/src/services/api" || spec.NetnsPath != net.NetnsPath || spec.ResolvConfPath != build.ResolvConf || spec.NoNetwork {
		t.Fatalf("spec = %+v", spec)
	}
	wantEnv := []string{"TMPDIR=/build/tmp", "HOME=/build/tmp/home", "SSL_CERT_FILE=" + containerCAPath, "NIX_SSL_CERT_FILE=" + containerCAPath, "GIT_SSL_CAINFO=" + containerCAPath}
	if !slices.Equal(spec.Env, wantEnv) {
		t.Fatalf("env = %q", spec.Env)
	}
	if spec.Resources == nil || *spec.Resources != (ctrd.Resources{MemoryBytes: 1 << 30, CPUs: 2, Pids: 100}) || !spec.DefaultSeccomp {
		t.Fatalf("resources = %+v seccomp=%v", spec.Resources, spec.DefaultSeccomp)
	}
	if err := os.WriteFile(filepath.Join(build.Scratch, "junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	build.Cleanup()
	if _, err := os.Stat(build.Scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scratch dir must be removed")
	}
	if _, err := os.Stat(build.ResolvConf); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resolv.conf must be removed")
	}
}

func TestBuildArgsRootFlakeAndTargets(t *testing.T) {
	if got := BuildArgs(".", ""); got[len(got)-1] != "path:/build/src" {
		t.Fatalf("root flake ref = %q", got[len(got)-1])
	}
	if got := BuildArgs("", "image"); got[len(got)-1] != "path:/build/src#image" {
		t.Fatalf("root flake target ref = %q", got[len(got)-1])
	}
	if got := BuildArgs("app", ".#image"); got[len(got)-1] != "path:/build/src?dir=app#image" {
		t.Fatalf("subdir target ref = %q", got[len(got)-1])
	}
}

func TestGitMaskFileCheckout(t *testing.T) {
	m, _ := newTestManager(t, Config{})
	checkout := t.TempDir()
	if m.gitMask(checkout) != "" {
		t.Fatal("no .git means no mask")
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureDirs(); err != nil {
		t.Fatal(err)
	}
	mask := m.gitMask(checkout)
	info, err := os.Stat(mask)
	if err != nil || info.IsDir() || info.Size() != 0 {
		t.Fatalf("gitfile mask = %s err=%v", mask, err)
	}
}

func TestLifecyclePolicies(t *testing.T) {
	if gcTarget(100, 200) != 0 {
		t.Fatal("within cap must not collect")
	}
	if got := gcTarget(250, 200); got != 70 {
		t.Fatalf("gc target = %d, want 70", got)
	}
	now := time.Now()
	if needsScheduledReset(Meta{}, time.Hour, now) {
		t.Fatal("unknown seed time must not reset")
	}
	if needsScheduledReset(Meta{SeededAt: now.Add(-30 * time.Minute)}, time.Hour, now) {
		t.Fatal("fresh store must not reset")
	}
	if !needsScheduledReset(Meta{SeededAt: now.Add(-2 * time.Hour)}, time.Hour, now) {
		t.Fatal("old store must reset")
	}
	cfg := (Config{}).withDefaults()
	if cfg.Image != BuildImage || cfg.SizeCap != DefaultSizeCap || cfg.ResetInterval != DefaultResetInterval || cfg.MaxConcurrent != network.MaxBuildAttachments || cfg.Resources.Pids != DefaultPids || cfg.Resources.CPUs < 1 || cfg.Resources.MemoryBytes < 512<<20 {
		t.Fatalf("defaults = %+v", cfg)
	}
	if got := (Config{MaxConcurrent: 99}).withDefaults().MaxConcurrent; got != network.MaxBuildAttachments {
		t.Fatalf("cap above the attachment limit must clamp, got %d", got)
	}
}

func TestSizeCountsEachInodeOnce(t *testing.T) {
	m, _ := newTestManager(t, Config{})
	key := Key("github.com/acme/app")
	store, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(store.Root, "store/aaaa-hello/bin/hello"), filepath.Join(store.Root, "store/hello-link")); err != nil {
		t.Fatal(err)
	}
	size, err := m.Size(key)
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := os.Stat(filepath.Join(store.Root, "store/aaaa-hello/bin/hello"))
	db, _ := os.Stat(filepath.Join(store.Root, "var/nix/db/db.sqlite"))
	if size != hello.Size()+db.Size() {
		t.Fatalf("size = %d, want %d", size, hello.Size()+db.Size())
	}
	if size, err := m.Size("missing"); err != nil || size != 0 {
		t.Fatalf("missing store size = %d err=%v", size, err)
	}
}

func TestAfterBuildCollectsOverCap(t *testing.T) {
	m, runner := newTestManager(t, Config{SizeCap: 1})
	key := Key("github.com/acme/app")
	store, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	sink := &logSink{}
	m.AfterBuild(context.Background(), store, io.Discard, sink.log)
	ops := runner.ops()
	if ops[len(ops)-1] != "gc" {
		t.Fatalf("ops = %v", ops)
	}
	gc := runner.specs[len(runner.specs)-1]
	if gc.Args[0] != "nix" || gc.Args[2] != "gc" || gc.Mounts[0].Source != store.Root || gc.Mounts[0].Dest != "/nix" || !gc.NoNetwork {
		t.Fatalf("gc spec = %+v", gc)
	}
	if !sink.contains("store exceeds 1 B") {
		t.Fatalf("log = %v", sink.lines)
	}
	meta, err := m.readMeta(key)
	if err != nil || meta.LastBuildAt.IsZero() || meta.LastSize == 0 {
		t.Fatalf("meta = %+v err=%v", meta, err)
	}
}

func TestMaintainOnceResetsDueStoresOnlyWhenIdle(t *testing.T) {
	m, runner := newTestManager(t, Config{ResetInterval: time.Millisecond})
	key := Key("github.com/acme/app")
	if _, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	release, err := m.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	m.maintainOnce(context.Background())
	if !m.storeSeeded(key) {
		t.Fatal("store held by a build must not be reset")
	}
	release()
	m.maintainOnce(context.Background())
	if m.storeSeeded(key) {
		t.Fatal("due store must be reset when idle")
	}
	if ops := runner.ops(); ops[len(ops)-1] != "delete" {
		t.Fatalf("ops = %v", ops)
	}
	if _, err := os.Stat(m.metaPath(key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reset must drop the store metadata")
	}
	m.maintainOnce(context.Background())
	if ops := runner.ops(); ops[len(ops)-1] != "delete" || len(ops) != 3 {
		t.Fatalf("unseeded store must not be reset again: %v", ops)
	}
}

func TestSweepScratchRemovesLeftovers(t *testing.T) {
	m, _ := newTestManager(t, Config{})
	key := Key("github.com/acme/app")
	store, err := m.Ensure(context.Background(), key, "github.com/acme/app", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	build, err := m.NewBuild(store, "")
	if err != nil {
		t.Fatal(err)
	}
	if build.ResolvConf != "" {
		t.Fatal("no DNS means no resolv.conf")
	}
	m.sweepScratch()
	if _, err := os.Stat(build.Scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sweep must remove leftover scratch directories")
	}
}

func TestNixConfRendered(t *testing.T) {
	m, _ := newTestManager(t, Config{})
	if err := m.ensureDirs(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(m.NixConfPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"experimental-features = nix-command flakes", "sandbox = false", "build-users-group = nixbld", "substituters = https://cache.nixos.org/", "max-jobs = auto"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("nix.conf missing %q: %s", want, raw)
		}
	}
}
