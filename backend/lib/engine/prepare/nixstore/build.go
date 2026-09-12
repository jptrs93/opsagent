package nixstore

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

// Build is the per-build scratch state around one store.
type Build struct {
	ID         string
	Store      Store
	Scratch    string
	ResolvConf string
	manager    *Manager
}

// NewBuild creates the scratch directory and DNS configuration for one build.
func (m *Manager) NewBuild(store Store, dns string) (*Build, error) {
	id := newID()
	scratch := filepath.Join(m.scratchParent(store.Key), id)
	if err := os.MkdirAll(filepath.Join(scratch, "home"), 0o755); err != nil {
		return nil, fmt.Errorf("creating build scratch dir: %w", err)
	}
	b := &Build{ID: id, Store: store, Scratch: scratch, manager: m}
	if dns != "" {
		b.ResolvConf = filepath.Join(m.scratchParent(store.Key), id+".resolv.conf")
		if err := os.WriteFile(b.ResolvConf, []byte("nameserver "+dns+"\noptions ndots:1\n"), 0o644); err != nil {
			b.Cleanup()
			return nil, fmt.Errorf("writing build resolv.conf: %w", err)
		}
	}
	return b, nil
}

// ContainerID is the build container's id and the name of its network
// namespace.
func (b *Build) ContainerID() string { return network.BuildContainerID(b.ID) }

// Cleanup removes the scratch directory. Files the container created belong
// to root; the agent's DAC override covers them, and anything it still cannot
// remove is left for the maintenance sweep.
func (b *Build) Cleanup() {
	_ = os.RemoveAll(b.Scratch)
	if b.ResolvConf != "" {
		_ = os.Remove(b.ResolvConf)
	}
}

// BuildArgs is the nix invocation; the flake ref is a path: reference so the
// container never runs git against the mounted checkout.
func BuildArgs(flakeDir string, target string) []string {
	ref := "path:" + containerSrcDir
	if flakeDir != "" && flakeDir != "." {
		ref += "?dir=" + flakeDir
	}
	if target != "" {
		ref += "#" + strings.TrimPrefix(target, ".#")
	}
	return []string{"nix", "build", "--no-update-lock-file", "--no-link", "--print-out-paths", "-L", ref}
}

// ContainerSpec assembles the build container: store read-write at /nix, the
// checkout read-only at /build/src with its .git masked, the node nix.conf,
// the scratch directory, no host environment, and the node's resource limits.
func (b *Build) ContainerSpec(checkoutDir string, flakeDir string, target string, net *network.ContainerNet) ctrd.ContainerSpec {
	m := b.manager
	mounts := []ctrd.Mount{
		{Source: b.Store.Root, Dest: containerNixDir},
		{Source: checkoutDir, Dest: containerSrcDir, ReadOnly: true},
	}
	if mask := m.gitMask(checkoutDir); mask != "" {
		mounts = append(mounts, ctrd.Mount{Source: mask, Dest: path.Join(containerSrcDir, ".git"), ReadOnly: true})
	}
	mounts = append(mounts,
		ctrd.Mount{Source: m.NixConfPath(), Dest: containerNixConfPath, ReadOnly: true},
		ctrd.Mount{Source: b.Scratch, Dest: containerScratchDir},
	)
	env := []string{"TMPDIR=" + containerScratchDir, "HOME=" + containerScratchDir + "/home"}
	if m.cfg.CABundle != "" {
		mounts = append(mounts, ctrd.Mount{Source: m.cfg.CABundle, Dest: containerCAPath, ReadOnly: true})
		env = append(env, "SSL_CERT_FILE="+containerCAPath, "NIX_SSL_CERT_FILE="+containerCAPath, "GIT_SSL_CAINFO="+containerCAPath)
	}
	resources := m.cfg.Resources
	spec := ctrd.ContainerSpec{
		ID:             b.ContainerID(),
		Image:          m.cfg.Image,
		Env:            env,
		Args:           BuildArgs(flakeDir, target),
		Cwd:            path.Join(containerSrcDir, flakeDir),
		Mounts:         mounts,
		Resources:      &resources,
		DefaultSeccomp: true,
		NoNetwork:      true,
	}
	if net != nil {
		spec.NetnsPath = net.NetnsPath
		spec.ResolvConfPath = b.ResolvConf
		spec.NoNetwork = false
	}
	return spec
}

// gitMask returns an empty directory or file to mount over the checkout's
// .git so the path: fetcher never copies the object database into the store.
func (m *Manager) gitMask(checkoutDir string) string {
	info, err := os.Lstat(filepath.Join(checkoutDir, ".git"))
	if err != nil {
		return ""
	}
	if info.IsDir() {
		dir := filepath.Join(m.etcDir(), "empty")
		if err := os.MkdirAll(dir, 0o555); err != nil {
			return ""
		}
		return dir
	}
	file := filepath.Join(m.etcDir(), "empty-file")
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(file, nil, 0o444); err != nil {
			return ""
		}
	}
	return file
}

func defaultCPUs() int {
	return max(1, runtime.NumCPU())
}

func defaultMemoryLimit() int64 {
	total := totalMemory()
	if total <= 0 {
		return DefaultMemoryBytes
	}
	return max(total/2, 512<<20)
}

func totalMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}
