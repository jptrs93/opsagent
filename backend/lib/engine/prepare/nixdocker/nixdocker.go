// Package nixdocker builds nix2container images in a one-shot build container
// and ingests them into OpenDeploy's containerd image store.
package nixdocker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nix2container"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixstore"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	"github.com/jptrs93/opsagent/backend/lib/network"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

// Preparer builds a Nix flake whose selected output is a nix2container image
// JSON, inside a container with the repository's own store, and imports the
// image into containerd without executing anything the build produced.
type Preparer struct {
	gitManager *repogit.Manager
	stores     *nixstore.Manager
	imageReady func(context.Context, string) error
}

const imageCacheSchemaVersion = "v2"

func New(gitManager *repogit.Manager) *Preparer {
	return &Preparer{
		gitManager: gitManager,
		stores:     nixstore.Default(),
		imageReady: ctrd.Default.ImageReady,
	}
}

// Stores exposes the store manager for operator resets.
func (p *Preparer) Stores() *nixstore.Manager { return p.stores }

// RunMaintenance removes build containers and attachments a previous agent
// process left behind, then runs the store lifecycle loop until ctx ends.
func (p *Preparer) RunMaintenance(ctx context.Context) {
	for _, prefix := range []string{network.BuildContainerID(""), "opendeploy-nixstore-"} {
		ids, err := ctrd.Default.ListContainerIDs(ctx, prefix)
		if err != nil {
			slog.WarnContext(ctx, "listing leftover build containers failed", "err", err)
			break
		}
		for _, id := range ids {
			slog.InfoContext(ctx, "removing leftover build container "+id)
			_ = ctrd.Default.Remove(ctx, id)
		}
	}
	p.stores.RunMaintenance(ctx)
}

func (p *Preparer) Prepare(ctx context.Context, dep *apigen.DeploymentEvent, log *preparerlog.Log) (string, apigen.ImageStatus) {
	version := dep.WorkloadVersion()
	nix := dep.Value.Spec.Container().Source.NixDockerBuild
	localImageRef := imageRef(nix, version)
	log.Write("checking for reusable image %s", localImageRef)
	if err := p.imageReady(ctx, localImageRef); err == nil {
		log.Write("reusing existing image %s", localImageRef)
		return localImageRef, apigen.ImageStatus_IMAGE_READY
	} else if !errors.Is(err, ctrd.ErrImageUnavailable) {
		log.Error("checking reusable image: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("reusable image not found; building %s", localImageRef)

	key := nixstore.Key(nix.Repo)
	log.Write("waiting for the build slot of repository %s", nix.Repo)
	release, err := p.stores.Acquire(ctx, key)
	if err != nil {
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	defer release()

	slog.InfoContext(ctx, fmt.Sprintf("nix docker build starting, logging to %s", dep.PrepareOutputPath()))
	log.Write("checking out repository %s at version %s", nix.Repo, version)
	checkoutStarted := time.Now()
	repoDir, err := p.gitManager.EnsureCheckout(ctx, nix.Repo, version, log.Output())
	if err != nil {
		log.Error("checking out repository: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("checkout complete in %s: %s", time.Since(checkoutStarted).Round(time.Millisecond), repoDir)

	flakePath, err := checkedOutFlakePath(repoDir, nix.Flake)
	if err != nil {
		log.Error("validating flake path: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	flakeDir, err := filepath.Rel(repoDir, filepath.Dir(flakePath))
	if err != nil {
		log.Error("resolving flake directory: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}

	store, err := p.stores.Ensure(ctx, key, nix.Repo, log.Output(), log.Write)
	if err != nil {
		if errors.Is(err, ctrd.ErrImagePull) {
			log.Error("build image unavailable: %v", err)
		} else {
			log.Error("preparing nix store: %v", err)
		}
		return "", apigen.ImageStatus_IMAGE_FAILED
	}

	dns, ok := network.Default.DNSAddr()
	if !ok {
		log.Error("setting up build network: netproxy DNS address is not known")
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	build, err := p.stores.NewBuild(store, dns.String())
	if err != nil {
		log.Error("preparing build scratch directory: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	defer build.Cleanup()

	netSpec, err := network.Default.BuildNetSpec(build.ID)
	if err != nil {
		log.Error("setting up build network: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	buildNet, err := network.Default.SetupContainerNet(netSpec)
	if err != nil {
		log.Error("setting up build network: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	defer network.Default.TeardownContainerNet(buildNet)

	spec := build.ContainerSpec(repoDir, flakeDir, nix.Target, buildNet)
	cfg := p.stores.Config()
	log.Write("running Nix build in container %s: image %s, memory limit %s, %d cpus, %d pids", spec.ID, cfg.Image, formatImageSize(cfg.Resources.MemoryBytes), cfg.Resources.CPUs, cfg.Resources.Pids)
	log.Write("running command: %s", strings.Join(spec.Args, " "))
	buildStarted := time.Now()
	stdout := newLineCapture(log.Output(), 16)
	stderr := newLineCapture(log.Output(), 40)
	result, err := ctrd.Default.RunBuild(ctx, spec, stdout, stderr)
	if err != nil {
		if isContextDone(ctx.Err()) {
			log.Write("build cancelled")
			return "", apigen.ImageStatus_IMAGE_FAILED
		}
		log.Error("running build container: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	switch {
	case result.OOMKilled:
		log.Error("build exceeded its memory limit of %s (exit status %d)", formatImageSize(cfg.Resources.MemoryBytes), result.Code)
		return "", apigen.ImageStatus_IMAGE_FAILED
	case result.Code != 0 && needsCredentials(stderr.Lines()):
		log.Error("Nix build failed with exit status %d: a flake input requires credentials, which builds do not receive", result.Code)
		return "", apigen.ImageStatus_IMAGE_FAILED
	case result.Code != 0:
		log.Error("Nix build failed with exit status %d", result.Code)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	artifactPath := stdout.LastNonEmpty()
	log.Write("build complete in %s, image description: %s", time.Since(buildStarted).Round(time.Millisecond), artifactPath)
	if artifactPath == "" {
		log.Error("Nix build returned an empty output path")
		return "", apigen.ImageStatus_IMAGE_FAILED
	}

	n2cStore := nix2container.Store{Root: store.Root}
	image, err := nix2container.Load(n2cStore, artifactPath)
	if err != nil {
		if errors.Is(err, nix2container.ErrNotImage) {
			log.Error("output is not a nix2container image: %v", err)
		} else {
			log.Error("reading image description: %v", err)
		}
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("importing %d layers as %s", len(image.Layers), localImageRef)
	importStarted := time.Now()
	if err := p.ingest(ctx, n2cStore, image, key, localImageRef, log); err != nil {
		if errors.Is(err, nix2container.ErrLayerMismatch) {
			log.Error("image layer failed verification: %v", err)
		} else {
			log.Error("importing image: %v", err)
		}
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("image import complete in %s", time.Since(importStarted).Round(time.Millisecond))
	imageSize, err := ctrd.Default.ImageSize(ctx, localImageRef)
	if err != nil {
		log.Write("image import complete: %s (size unavailable: %v)", localImageRef, err)
	} else {
		log.Write("image import complete: %s (size: %s)", localImageRef, formatImageSize(imageSize))
	}
	p.stores.AfterBuild(ctx, store, log.Output(), log.Write)
	return localImageRef, apigen.ImageStatus_IMAGE_READY
}

func (p *Preparer) ingest(ctx context.Context, store nix2container.Store, image *nix2container.Image, key string, ref string, log *preparerlog.Log) error {
	record, err := nix2container.OpenRecord(p.stores.RecordPath(key))
	if err != nil {
		return fmt.Errorf("opening verified layer record: %w", err)
	}
	session, err := ctrd.Default.OpenContentSession(ctx)
	if err != nil {
		return err
	}
	defer session.Done()
	result, err := nix2container.Ingest(session.Ctx, session.Store, store, image, record, log.Write)
	if err != nil {
		return err
	}
	return ctrd.Default.TagManifest(session.Ctx, ref, result.Manifest)
}

// lineCapture forwards output to a writer while keeping the last lines for
// the artifact path and error classification.
type lineCapture struct {
	mu      sync.Mutex
	w       io.Writer
	partial bytes.Buffer
	lines   []string
	keep    int
}

func newLineCapture(w io.Writer, keep int) *lineCapture {
	return &lineCapture{w: w, keep: keep}
}

func (c *lineCapture) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.partial.Write(p)
	for {
		raw := c.partial.Bytes()
		idx := bytes.IndexByte(raw, '\n')
		if idx < 0 {
			break
		}
		c.push(string(raw[:idx]))
		c.partial.Next(idx + 1)
	}
	return n, err
}

func (c *lineCapture) push(line string) {
	c.lines = append(c.lines, line)
	if len(c.lines) > c.keep {
		c.lines = c.lines[len(c.lines)-c.keep:]
	}
}

func (c *lineCapture) Lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	lines := append([]string(nil), c.lines...)
	if c.partial.Len() > 0 {
		lines = append(lines, c.partial.String())
	}
	return lines
}

func (c *lineCapture) LastNonEmpty() string {
	return lastNonEmptyLine(c.Lines())
}

// needsCredentials recognises a fetch that was refused because the build
// has no credentials.
func needsCredentials(lines []string) bool {
	markers := []string{"HTTP error 401", "HTTP error 403", "Authentication failed", "could not read Username", "Permission denied (publickey", "Repository not found"}
	for _, line := range lines {
		for _, marker := range markers {
			if strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}

func isContextDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func checkedOutFlakePath(repoDir string, flake string) (string, error) {
	clean, err := repogit.CleanFlakePath(flake)
	if err != nil {
		return "", err
	}
	path := filepath.Join(repoDir, clean)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("flake file not found at %s: %w", clean, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("flake path is not a regular file: %s", clean)
	}
	return path, nil
}

func lastNonEmptyLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func formatImageSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	for _, suffix := range units {
		value /= unit
		if value < unit || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", size)
}

func imageRef(nix *apigen.NixDockerBuild, version string) string {
	return fmt.Sprintf(
		"opendeploy.local/nix-docker-build/%s/%s:%s",
		imageCacheSchemaVersion,
		imageSourceKey(nix, runtime.GOOS, runtime.GOARCH),
		sanitizeImageTag(strings.ToLower(version)),
	)
}

func imageSourceKey(nix *apigen.NixDockerBuild, goos, goarch string) string {
	h := sha256.New()
	for _, value := range []string{nix.Repo, nix.Flake, nix.Target, goos, goarch} {
		_, _ = fmt.Fprintf(h, "%d:", len(value))
		_, _ = io.WriteString(h, value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sanitizeImageTag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if isASCIITagChar(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 128 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	if !isASCIITagStart(rune(out[0])) {
		out = "v" + out
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

func isASCIITagChar(r rune) bool {
	return isASCIITagStart(r) || r == '.' || r == '-'
}

func isASCIITagStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}
