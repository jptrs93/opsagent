package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/runtimebin"
)

// dryRun, when set via --dry-run, makes every mutating helper log the action it
// *would* take and return without performing it. Read-only probes (unitActive,
// readlink, userExists, downloads-for-inspection) still run so the planned
// actions are realistic.
var dryRun bool

func itoa(i int) string { return strconv.Itoa(i) }

func step(format string, a ...any) { fmt.Printf("\n==> "+format+"\n", a...) }
func info(format string, a ...any) { fmt.Printf("    "+format+"\n", a...) }

func planned(format string, a ...any) {
	fmt.Printf("    [dry-run] would %s\n", fmt.Sprintf(format, a...))
}

func isRoot() bool { return os.Geteuid() == 0 }

func run(name string, args ...string) error {
	if dryRun {
		planned("run: %s %s", name, strings.Join(args, " "))
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// probe runs a command for its exit status only, discarding output and never
// mutating. Used for is-active/is-enabled style checks, so it ignores dryRun.
func probe(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	return cmd.Run() == nil
}

type owner struct{ uid, gid int }

// noChown is used before the opendeploy user exists (or for root-owned files).
var noChown = owner{uid: -1, gid: -1}

func (o owner) apply(path string) error {
	if o.uid < 0 && o.gid < 0 {
		return nil
	}
	if dryRun {
		planned("chown %d:%d %s", o.uid, o.gid, path)
		return nil
	}
	return os.Chown(path, o.uid, o.gid)
}

func ensureDir(path string, mode os.FileMode, own owner) error {
	if dryRun {
		planned("mkdir -m %o %s", mode, path)
		return own.apply(path)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return own.apply(path)
}

// writeFile writes content to path with mode + ownership. If onlyIfAbsent is
// set and the file already exists, it is left untouched (mirrors the env-file
// "don't clobber operator edits" behavior).
func writeFile(path string, content []byte, mode os.FileMode, own owner, onlyIfAbsent bool) (wrote bool, err error) {
	if onlyIfAbsent {
		if _, statErr := os.Stat(path); statErr == nil {
			return false, nil
		}
	}
	if dryRun {
		planned("write %s (%d bytes, mode %o)", path, len(content), mode)
		return true, own.apply(path)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err = os.WriteFile(path, content, mode); err != nil {
		return false, err
	}
	if err = os.Chmod(path, mode); err != nil {
		return false, err
	}
	return true, own.apply(path)
}

func installBinary(src, dst string, mode os.FileMode, own owner) error {
	if dryRun {
		planned("install -m %o %s -> %s", mode, src, dst)
		return own.apply(dst)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err = os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err = os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err = own.apply(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func fileBytesEqual(a, b string) bool {
	da, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false
	}
	return bytes.Equal(da, db)
}

// atomicSymlink points link at target, replacing any existing link atomically
// (create temp + rename), equivalent to `ln -sfn`.
func atomicSymlink(target, link string) error {
	if dryRun {
		planned("symlink %s -> %s", link, target)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func readlink(link string) string {
	t, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	return t
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removeAll(path string) error {
	if dryRun {
		planned("rm -rf %s", path)
		return nil
	}
	return os.RemoveAll(path)
}

func removeFile(path string) error {
	if dryRun {
		planned("rm -f %s", path)
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func download(url, dest string) error {
	info("downloading %s", url)
	return runtimebin.Download(context.Background(), url, dest)
}

func verifySHA256(path, want string) error {
	if err := runtimebin.VerifySHA256(path, want); err != nil {
		return err
	}
	info("checksum ok: %s", filepath.Base(path))
	return nil
}

func systemctl(args ...string) error { return run("systemctl", args...) }

func daemonReload() error { return systemctl("daemon-reload") }

func unitInstalled(unit string) bool {
	return probe("systemctl", "cat", unit)
}

func unitActive(unit string) bool {
	return probe("systemctl", "is-active", "--quiet", unit)
}

func unitEnabled(unit string) bool {
	return probe("systemctl", "is-enabled", "--quiet", unit)
}

func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

func ensureSystemUser() error {
	if userExists(osUser) {
		return nil
	}
	info("creating system user: %s", osUser)
	return run("useradd", "--system", "--shell", "/usr/sbin/nologin",
		"--home-dir", dataDir, "--create-home", osUser)
}

// lookupOwner resolves the opendeploy uid/gid. In dry-run before the user is
// created it may not exist yet; callers fall back to noChown.
func lookupOwner() (owner, error) {
	u, err := user.Lookup(osUser)
	if err != nil {
		return noChown, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return noChown, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return noChown, err
	}
	return owner{uid: uid, gid: gid}, nil
}

func resolveLatestTag() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "opendeploy-installer")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve latest release: %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("no tag_name in latest release response")
	}
	return body.TagName, nil
}
