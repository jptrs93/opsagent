// Package nixstore owns the per-repository Nix stores that build containers
// mount at /nix: seeding from the pinned build image, per-repository
// serialisation, the node-wide build cap, scratch directories, and the store
// lifecycle (size cap, scheduled and operator resets). Every write inside a
// store happens in a one-shot maintenance container; the agent only reads.
package nixstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/network"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

const (
	// BuildImage is the official nixos/nix image, pinned by digest. Upgrading
	// it is a digest bump here; every store is reseeded from the new template
	// on its next build because the digest is part of the store's identity.
	BuildImage        = "docker.io/nixos/nix@sha256:7a007c766426c1877758ddc5cb87a965ac131fc78c582ce0083d922d51ae945c"
	BuildImageVersion = "2.35.2"

	DefaultSizeCap       = 6 << 30
	DefaultResetInterval = 7 * 24 * time.Hour
	DefaultPids          = 4096
	DefaultMemoryBytes   = 2 << 30

	MaintenanceTimeout = 30 * time.Minute

	containerNixDir      = "/nix"
	containerMountRoot   = "/mnt/opendeploy-nix"
	containerSrcDir      = "/build/src"
	containerScratchDir  = "/build/tmp"
	containerNixConfPath = "/etc/nix/nix.conf"
	containerCAPath      = "/etc/ssl/certs/opendeploy-build-ca.crt"
)

// Runner is the containerd surface the store manager needs; ctrd.Default in
// production, a local fake in tests.
type Runner interface {
	EnsureImage(ctx context.Context, ref string) (digest string, pulled bool, err error)
	RunBuild(ctx context.Context, spec ctrd.ContainerSpec, stdout, stderr io.Writer) (ctrd.BuildResult, error)
}

type Logger func(format string, args ...any)

type Config struct {
	Root          string
	Image         string
	CABundle      string
	SizeCap       int64
	ResetInterval time.Duration
	MaxConcurrent int
	Resources     ctrd.Resources
}

// ConfigFromEnv derives the node's configuration from ainit, with the
// defaults described in the design.
func ConfigFromEnv() Config {
	sc := ainit.StaticConfig
	cfg := Config{
		Root:          sc.NixStoresDir,
		Image:         sc.NixBuildImage,
		CABundle:      sc.NixBuildCABundle,
		SizeCap:       sc.NixStoreSizeCapMB << 20,
		ResetInterval: time.Duration(sc.NixStoreResetHours) * time.Hour,
		MaxConcurrent: network.MaxBuildAttachments,
		Resources: ctrd.Resources{
			MemoryBytes: sc.NixBuildMemoryMB << 20,
			CPUs:        sc.NixBuildCPUs,
			Pids:        sc.NixBuildPids,
		},
	}
	return cfg.withDefaults()
}

func (cfg Config) withDefaults() Config {
	if cfg.Image == "" {
		cfg.Image = BuildImage
	}
	if cfg.SizeCap <= 0 {
		cfg.SizeCap = DefaultSizeCap
	}
	if cfg.ResetInterval <= 0 {
		cfg.ResetInterval = DefaultResetInterval
	}
	if cfg.MaxConcurrent <= 0 || cfg.MaxConcurrent > network.MaxBuildAttachments {
		cfg.MaxConcurrent = network.MaxBuildAttachments
	}
	if cfg.Resources.MemoryBytes <= 0 {
		cfg.Resources.MemoryBytes = defaultMemoryLimit()
	}
	if cfg.Resources.CPUs <= 0 {
		cfg.Resources.CPUs = defaultCPUs()
	}
	if cfg.Resources.Pids <= 0 {
		cfg.Resources.Pids = DefaultPids
	}
	return cfg
}

type Manager struct {
	cfg    Config
	runner Runner
	ctx    context.Context

	locks sync.Map
	slots chan struct{}

	mu            sync.Mutex
	imageDigest   string
	pendingResets map[string]time.Time
}

// Default is the process-wide manager, created lazily from the environment
// so tests and processes without containerd never touch it.
var (
	defaultOnce sync.Once
	defaultMgr  *Manager
)

func Default() *Manager {
	defaultOnce.Do(func() {
		defaultMgr = New(ConfigFromEnv(), ctrd.Default)
	})
	return defaultMgr
}

func New(cfg Config, runner Runner) *Manager {
	cfg = cfg.withDefaults()
	return &Manager{
		cfg:           cfg,
		runner:        runner,
		ctx:           logu.AddTag(context.Background(), "NixStore"),
		slots:         make(chan struct{}, cfg.MaxConcurrent),
		pendingResets: map[string]time.Time{},
	}
}

func (m *Manager) Config() Config { return m.cfg }

// Key is the store identity of a repository URL, shared with the Git manager's
// checkout cache.
func Key(repoURL string) string { return repogit.RepoKey(repoURL) }

func (m *Manager) StoreDir(key string) string  { return filepath.Join(m.cfg.Root, "stores", key) }
func (m *Manager) StoreRoot(key string) string { return filepath.Join(m.StoreDir(key), "nix") }
func (m *Manager) RecordPath(key string) string {
	return filepath.Join(m.StoreDir(key), "verified-layers.json")
}
func (m *Manager) metaPath(key string) string { return filepath.Join(m.StoreDir(key), "store.json") }
func (m *Manager) scratchParent(key string) string {
	return filepath.Join(m.StoreDir(key), "tmp")
}
func (m *Manager) templatesDir() string { return filepath.Join(m.cfg.Root, "template") }
func (m *Manager) templateDir(digest string) string {
	return filepath.Join(m.templatesDir(), digestHex(digest))
}
func (m *Manager) etcDir() string { return filepath.Join(m.cfg.Root, "etc") }

// NixConfPath is the node's rendered nix.conf, mounted read-only into every
// build and maintenance container.
func (m *Manager) NixConfPath() string { return filepath.Join(m.etcDir(), "nix.conf") }

func digestHex(digest string) string {
	if _, hex, ok := strings.Cut(digest, ":"); ok {
		return hex
	}
	return digest
}

// containerPath maps a host path under the store root to its path inside a
// maintenance container, which mounts the whole root at one mount point so
// hardlinks between template and store stay on one mount.
func (m *Manager) containerPath(hostPath string) string {
	rel, err := filepath.Rel(m.cfg.Root, hostPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return hostPath
	}
	return filepath.Join(containerMountRoot, rel)
}

type storeLock struct {
	ch chan struct{}
}

func (m *Manager) lockFor(key string) *storeLock {
	l, _ := m.locks.LoadOrStore(key, &storeLock{ch: make(chan struct{}, 1)})
	return l.(*storeLock)
}

// Acquire serialises builds of one repository and bounds concurrent builds
// across repositories. The returned function releases both.
func (m *Manager) Acquire(ctx context.Context, key string) (func(), error) {
	lock := m.lockFor(key)
	select {
	case lock.ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case m.slots <- struct{}{}:
	case <-ctx.Done():
		<-lock.ch
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-m.slots
			<-lock.ch
		})
	}, nil
}

// TryLock takes the repository lock only when no build holds it, for
// maintenance that must never delay a build.
func (m *Manager) TryLock(key string) (func(), bool) {
	lock := m.lockFor(key)
	select {
	case lock.ch <- struct{}{}:
	default:
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { <-lock.ch }) }, true
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// EnsureImage makes the build image available locally and returns its digest.
func (m *Manager) EnsureImage(ctx context.Context, log Logger) (string, error) {
	digest, pulled, err := m.runner.EnsureImage(ctx, m.cfg.Image)
	if err != nil {
		return "", err
	}
	if pulled && log != nil {
		log("pulled build image %s (%s)", m.cfg.Image, digest)
	}
	m.mu.Lock()
	m.imageDigest = digest
	m.mu.Unlock()
	return digest, nil
}

func (m *Manager) ensureDirs() error {
	for _, dir := range []string{m.cfg.Root, m.templatesDir(), filepath.Join(m.cfg.Root, "stores"), m.etcDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return m.writeNixConf()
}

func (m *Manager) maintenanceSpec(op string, args ...string) ctrd.ContainerSpec {
	return ctrd.ContainerSpec{
		ID:             "opendeploy-nixstore-" + op + "-" + newID(),
		Image:          m.cfg.Image,
		Args:           args,
		Env:            []string{"TMPDIR=/tmp"},
		Mounts:         []ctrd.Mount{{Source: m.cfg.Root, Dest: containerMountRoot}},
		NoNetwork:      true,
		DefaultSeccomp: true,
	}
}

// runMaintenance executes one shell script in the build image with the store
// root mounted and no network.
func (m *Manager) runMaintenance(ctx context.Context, op string, script string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	ctx, cancel := context.WithTimeout(ctx, MaintenanceTimeout)
	defer cancel()
	spec := m.maintenanceSpec(op, "sh", "-ec", script)
	result, err := m.runner.RunBuild(ctx, spec, out, out)
	if err != nil {
		return fmt.Errorf("running store maintenance %s: %w", op, err)
	}
	if result.Code != 0 {
		return fmt.Errorf("store maintenance %s exited with status %d", op, result.Code)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
