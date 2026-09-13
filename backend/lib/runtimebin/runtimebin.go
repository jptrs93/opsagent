package runtimebin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
)

var (
	Dir         = "/var/lib/opendeploy/runtime"
	BinDir      = "/var/lib/opendeploy/runtime/bin"
	VersionsDir = "/var/lib/opendeploy/runtime/versions"
)

const ContainerdUnit = "opendeploy-containerd.service"

type Component struct {
	Name     string
	Version  string
	Binaries []string
	tarball  bool
	url      func(arch string) string
	sha256   map[string]string
}

var Containerd = Component{
	Name:     "containerd",
	Version:  "2.3.5",
	Binaries: []string{"containerd", "containerd-shim-runc-v2", "ctr"},
	tarball:  true,
	url: func(arch string) string {
		return "https://github.com/containerd/containerd/releases/download/v2.3.5/containerd-2.3.5-linux-" + arch + ".tar.gz"
	},
	sha256: map[string]string{
		"amd64": "2f0a095a71e3262d0d91ff0e50e2e4ae73c3866c4d1ff6a15f341097fba3dd44",
		"arm64": "06f46cbc073872c5ad1fbc922a53543e9a26b798d62ff106d44639dcb9947942",
	},
}

var Runc = Component{
	Name:     "runc",
	Version:  "1.5.1",
	Binaries: []string{"runc"},
	url: func(arch string) string {
		return "https://github.com/opencontainers/runc/releases/download/v1.5.1/runc." + arch
	},
	sha256: map[string]string{
		"amd64": "177df879d50c913eb205e898d5c1c05a18f574053c0ce5524c471208eaf06f6f",
		"arm64": "ca70e7dbd6616ca782a59b5d3ac86909123fdaa9fa3f89dcf29051c70eee7ce9",
	},
}

func Components() []Component { return []Component{Containerd, Runc} }

func (c Component) VersionDir() string { return filepath.Join(VersionsDir, c.Name+"-"+c.Version) }

func (c Component) URL(arch string) string { return c.url(arch) }

func (c Component) Checksum(arch string) (string, bool) {
	sum, ok := c.sha256[arch]
	return sum, ok
}

type Staged struct {
	Component Component
	Files     map[string]string
}

type Reporter func(format string, args ...any)

func (r Reporter) printf(format string, args ...any) {
	if r != nil {
		r(format, args...)
	}
}

func HostArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("unsupported architecture %s (need amd64 or arm64)", runtime.GOARCH)
	}
}

func Fetch(ctx context.Context, c Component, arch, dir string, report Reporter) (Staged, error) {
	st := Staged{Component: c, Files: make(map[string]string, len(c.Binaries))}
	want, ok := c.Checksum(arch)
	if !ok {
		return st, fmt.Errorf("no %s checksum for arch %s", c.Name, arch)
	}
	dl := filepath.Join(dir, c.Name+"-download")
	report.printf("downloading %s", c.URL(arch))
	if err := Download(ctx, c.URL(arch), dl); err != nil {
		return st, err
	}
	if err := VerifySHA256(dl, want); err != nil {
		return st, err
	}
	report.printf("checksum ok: %s %s", c.Name, c.Version)
	if !c.tarball {
		st.Files[c.Binaries[0]] = dl
		return st, nil
	}
	stageDir := filepath.Join(dir, "stage-"+c.Name)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return st, err
	}
	if err := extractTarGzMembers(dl, stageDir, c.Binaries); err != nil {
		return st, err
	}
	for _, b := range c.Binaries {
		st.Files[b] = filepath.Join(stageDir, b)
	}
	return st, nil
}

func Download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "opendeploy")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func SHA256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func VerifySHA256(path, want string) error {
	got, err := SHA256OfFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s:\n  want %s\n  got  %s", filepath.Base(path), want, got)
	}
	return nil
}

func extractTarGzMembers(tarPath, destDir string, members []string) error {
	want := make(map[string]bool, len(members))
	for _, m := range members {
		want[m] = true
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		base := filepath.Base(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || !want[base] {
			continue
		}
		out, err := os.OpenFile(filepath.Join(destDir, base), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
		found++
	}
	if found < len(members) {
		return fmt.Errorf("expected %d members in %s, extracted %d", len(members), filepath.Base(tarPath), found)
	}
	return nil
}

func InstalledVersion(c Component) string {
	target, err := os.Readlink(filepath.Join(BinDir, c.Binaries[0]))
	if err != nil {
		return ""
	}
	version, ok := strings.CutPrefix(filepath.Base(filepath.Dir(target)), c.Name+"-")
	if !ok {
		return ""
	}
	return version
}

func InstalledSummary() string {
	var parts []string
	for _, c := range Components() {
		if v := InstalledVersion(c); v != "" {
			parts = append(parts, c.Name+" "+v)
		}
	}
	return strings.Join(parts, ", ")
}

type Links map[string]string

func Install(st Staged) (bool, Links, error) {
	versionDir := st.Component.VersionDir()
	for _, d := range []string{Dir, BinDir, VersionsDir, versionDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return false, nil, err
		}
	}
	for _, b := range st.Component.Binaries {
		if err := installFile(st.Files[b], filepath.Join(versionDir, b)); err != nil {
			return false, nil, err
		}
	}
	changed := false
	previous := Links{}
	for _, b := range st.Component.Binaries {
		target := filepath.Join(versionDir, b)
		link := filepath.Join(BinDir, b)
		prev, _ := os.Readlink(link)
		previous[link] = prev
		if prev == target {
			continue
		}
		if err := atomicSymlink(target, link); err != nil {
			return changed, previous, err
		}
		changed = true
	}
	return changed, previous, nil
}

func RestoreLinks(previous Links) error {
	var errs []error
	for link, target := range previous {
		if target == "" {
			if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		if err := atomicSymlink(target, link); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func installFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func atomicSymlink(target, link string) error {
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func Restart(ctx context.Context) error {
	args := []string{"systemctl", "restart", ContainerdUnit}
	if os.Geteuid() != 0 {
		args = []string{"sudo", "-n", "/usr/bin/systemctl", "restart", ContainerdUnit}
	}
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func DaemonVersion(ctx context.Context, socket string) (string, error) {
	cl, err := containerd.New(socket, containerd.WithTimeout(5*time.Second))
	if err != nil {
		return "", err
	}
	defer cl.Close()
	v, err := cl.Version(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(v.Version), "v"), nil
}

type Options struct {
	Arch          string
	Restart       func(context.Context) error
	DaemonVersion func(context.Context) (string, error)
	DaemonWait    time.Duration
	Report        Reporter
}

type Result struct {
	Updated []Component
	Skipped []Component
}

func Reconcile(ctx context.Context, opts Options) (Result, error) {
	var res Result
	var pending []Component
	for _, c := range Components() {
		installed := InstalledVersion(c)
		switch {
		case installed == c.Version:
		case installed != "" && newerVersion(installed, c.Version):
			res.Skipped = append(res.Skipped, c)
		default:
			pending = append(pending, c)
		}
	}
	if len(pending) == 0 {
		return res, nil
	}
	if err := os.MkdirAll(VersionsDir, 0o755); err != nil {
		return res, err
	}
	tmp, err := os.MkdirTemp(VersionsDir, ".staging-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	var staged []Staged
	for _, c := range pending {
		st, err := Fetch(ctx, c, opts.Arch, tmp, opts.Report)
		if err != nil {
			return res, err
		}
		staged = append(staged, st)
	}
	changed := false
	previous := Links{}
	for _, st := range staged {
		ch, prev, err := Install(st)
		for link, target := range prev {
			if _, seen := previous[link]; !seen {
				previous[link] = target
			}
		}
		if err != nil {
			return res, errors.Join(err, RestoreLinks(previous))
		}
		changed = changed || ch
	}
	res.Updated = pending
	if !changed {
		return res, nil
	}
	opts.Report.printf("restarting %s", ContainerdUnit)
	if err := opts.Restart(ctx); err != nil {
		return Result{Skipped: res.Skipped}, errors.Join(err, RestoreLinks(previous))
	}
	expected := InstalledVersion(Containerd)
	wait := opts.DaemonWait
	if wait == 0 {
		wait = 90 * time.Second
	}
	if err := waitForDaemon(ctx, opts.DaemonVersion, expected, wait); err != nil {
		rollback := RestoreLinks(previous)
		restart := opts.Restart(ctx)
		return Result{Skipped: res.Skipped}, errors.Join(fmt.Errorf("containerd did not report %s after restart; rolled back: %w", expected, err), rollback, restart)
	}
	return res, nil
}

func waitForDaemon(ctx context.Context, query func(context.Context) (string, error), want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		got, err := query(ctx)
		switch {
		case err == nil && got == want:
			return nil
		case err == nil:
			last = fmt.Errorf("daemon reports %s, want %s", got, want)
		default:
			last = err
		}
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return errors.Join(last, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func newerVersion(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var parts []int
	for _, s := range strings.Split(v, ".") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		parts = append(parts, n)
	}
	return parts
}

func ReconcileAtStartup(ctx context.Context) {
	ctx = logu.AddTag(ctx, "Runtime")
	if runtime.GOOS != "linux" {
		return
	}
	if _, err := os.Stat(BinDir); err != nil {
		slog.InfoContext(ctx, "container runtime is not provisioned; skipping runtime reconcile")
		return
	}
	arch, err := HostArch()
	if err != nil {
		slog.WarnContext(ctx, "skipping runtime reconcile", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	socket := ainit.StaticConfig.CtrdAddress
	res, err := Reconcile(ctx, Options{
		Arch:          arch,
		Restart:       Restart,
		DaemonVersion: func(ctx context.Context) (string, error) { return DaemonVersion(ctx, socket) },
		Report: func(format string, args ...any) {
			slog.InfoContext(ctx, fmt.Sprintf(format, args...))
		},
	})
	for _, c := range res.Skipped {
		slog.WarnContext(ctx, fmt.Sprintf("installed %s %s is newer than the pinned %s; leaving it in place", c.Name, InstalledVersion(c), c.Version))
	}
	if err != nil {
		slog.ErrorContext(ctx, "container runtime reconcile failed; continuing with the installed runtime", "err", err)
		return
	}
	if len(res.Updated) > 0 {
		slog.InfoContext(ctx, fmt.Sprintf("container runtime updated: %s", InstalledSummary()))
		return
	}
	slog.InfoContext(ctx, fmt.Sprintf("container runtime current: %s", InstalledSummary()))
}
