package runtimebin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldDir, oldBin, oldVersions := Dir, BinDir, VersionsDir
	Dir = filepath.Join(root, "runtime")
	BinDir = filepath.Join(Dir, "bin")
	VersionsDir = filepath.Join(Dir, "versions")
	t.Cleanup(func() { Dir, BinDir, VersionsDir = oldDir, oldBin, oldVersions })
	return root
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type fixture struct {
	server     *httptest.Server
	containerd Component
	runc       Component
}

func newFixture(t *testing.T, containerdVersion, runcVersion string) fixture {
	t.Helper()
	tarball := tarGz(t, map[string]string{
		"bin/containerd":              "containerd " + containerdVersion,
		"bin/containerd-shim-runc-v2": "shim " + containerdVersion,
		"bin/ctr":                     "ctr " + containerdVersion,
		"bin/other":                   "ignored",
	})
	runcBin := []byte("runc " + runcVersion)
	mux := http.NewServeMux()
	mux.HandleFunc("/containerd.tar.gz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tarball) })
	mux.HandleFunc("/runc", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(runcBin) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return fixture{
		server: server,
		containerd: Component{
			Name: "containerd", Version: containerdVersion, Binaries: []string{"containerd", "containerd-shim-runc-v2", "ctr"}, tarball: true,
			url:    func(string) string { return server.URL + "/containerd.tar.gz" },
			sha256: map[string]string{"amd64": sum(tarball)},
		},
		runc: Component{
			Name: "runc", Version: runcVersion, Binaries: []string{"runc"},
			url:    func(string) string { return server.URL + "/runc" },
			sha256: map[string]string{"amd64": sum(runcBin)},
		},
	}
}

func usePins(t *testing.T, containerdPin, runcPin Component) {
	t.Helper()
	oldContainerd, oldRunc := Containerd, Runc
	Containerd, Runc = containerdPin, runcPin
	t.Cleanup(func() { Containerd, Runc = oldContainerd, oldRunc })
}

func readLink(t *testing.T, name string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(BinDir, name))
	if err != nil {
		return ""
	}
	return target
}

func TestFetchVerifiesChecksumAndExtractsMembers(t *testing.T) {
	fx := newFixture(t, "2.3.5", "1.5.1")
	dir := t.TempDir()
	st, err := Fetch(context.Background(), fx.containerd, "amd64", dir, nil)
	if err != nil {
		t.Fatalf("Fetch containerd: %v", err)
	}
	for _, b := range fx.containerd.Binaries {
		data, err := os.ReadFile(st.Files[b])
		if err != nil {
			t.Fatalf("staged %s missing: %v", b, err)
		}
		if !strings.Contains(string(data), "2.3.5") {
			t.Fatalf("staged %s content = %q", b, data)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "stage-containerd", "other")); err == nil {
		t.Fatal("non-member tar entry was extracted")
	}
	bad := fx.runc
	bad.sha256 = map[string]string{"amd64": strings.Repeat("0", 64)}
	if _, err := Fetch(context.Background(), bad, "amd64", dir, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Fetch with wrong checksum err = %v", err)
	}
	if _, err := Fetch(context.Background(), fx.runc, "s390x", dir, nil); err == nil || !strings.Contains(err.Error(), "no runc checksum") {
		t.Fatalf("Fetch with unknown arch err = %v", err)
	}
}

func TestInstallFlipsLinksAndReportsPrevious(t *testing.T) {
	useTempRoot(t)
	fx := newFixture(t, "2.3.5", "1.5.1")
	st, err := Fetch(context.Background(), fx.runc, "amd64", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, previous, err := Install(st)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !changed {
		t.Fatal("first install reported no change")
	}
	link := filepath.Join(BinDir, "runc")
	if previous[link] != "" {
		t.Fatalf("previous target = %q, want empty", previous[link])
	}
	if got := readLink(t, "runc"); got != filepath.Join(fx.runc.VersionDir(), "runc") {
		t.Fatalf("link target = %q", got)
	}
	if InstalledVersion(fx.runc) != "1.5.1" {
		t.Fatalf("InstalledVersion = %q", InstalledVersion(fx.runc))
	}
	changed, previous, err = Install(st)
	if err != nil || changed {
		t.Fatalf("second install changed=%v err=%v", changed, err)
	}
	if previous[link] != filepath.Join(fx.runc.VersionDir(), "runc") {
		t.Fatalf("previous target after reinstall = %q", previous[link])
	}
	if err := RestoreLinks(Links{link: ""}); err != nil {
		t.Fatal(err)
	}
	if readLink(t, "runc") != "" {
		t.Fatal("RestoreLinks did not remove a link that had no previous target")
	}
}

func TestReconcileIsNoopWhenCurrent(t *testing.T) {
	useTempRoot(t)
	fx := newFixture(t, "2.3.5", "1.5.1")
	usePins(t, fx.containerd, fx.runc)
	for _, c := range []Component{fx.containerd, fx.runc} {
		st, err := Fetch(context.Background(), c, "amd64", t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Install(st); err != nil {
			t.Fatal(err)
		}
	}
	restarts := 0
	res, err := Reconcile(context.Background(), Options{
		Arch:          "amd64",
		Restart:       func(context.Context) error { restarts++; return nil },
		DaemonVersion: func(context.Context) (string, error) { return "2.3.5", nil },
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Updated) != 0 || len(res.Skipped) != 0 || restarts != 0 {
		t.Fatalf("Reconcile on a current runtime updated=%v skipped=%v restarts=%d", res.Updated, res.Skipped, restarts)
	}
	if InstalledSummary() != "containerd 2.3.5, runc 1.5.1" {
		t.Fatalf("InstalledSummary = %q", InstalledSummary())
	}
}

func TestReconcileUpgradesAndVerifiesDaemon(t *testing.T) {
	useTempRoot(t)
	old := newFixture(t, "2.3.4", "1.5.0")
	for _, c := range []Component{old.containerd, old.runc} {
		st, err := Fetch(context.Background(), c, "amd64", t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Install(st); err != nil {
			t.Fatal(err)
		}
	}
	fx := newFixture(t, "2.3.5", "1.5.1")
	usePins(t, fx.containerd, fx.runc)
	daemon := "2.3.4"
	restarts := 0
	res, err := Reconcile(context.Background(), Options{
		Arch: "amd64",
		Restart: func(context.Context) error {
			restarts++
			daemon = strings.TrimPrefix(filepath.Base(filepath.Dir(readLink(t, "containerd"))), "containerd-")
			return nil
		},
		DaemonVersion: func(context.Context) (string, error) { return daemon, nil },
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Updated) != 2 || restarts != 1 {
		t.Fatalf("updated=%v restarts=%d", res.Updated, restarts)
	}
	if InstalledSummary() != "containerd 2.3.5, runc 1.5.1" {
		t.Fatalf("InstalledSummary = %q", InstalledSummary())
	}
	if _, err := os.Stat(old.containerd.VersionDir()); err != nil {
		t.Fatal("previous version directory was removed")
	}
	entries, _ := os.ReadDir(VersionsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatalf("staging directory %s left behind", e.Name())
		}
	}
}

func TestReconcileRollsBackWhenDaemonDoesNotComeUp(t *testing.T) {
	useTempRoot(t)
	old := newFixture(t, "2.3.4", "1.5.1")
	for _, c := range []Component{old.containerd, old.runc} {
		st, err := Fetch(context.Background(), c, "amd64", t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Install(st); err != nil {
			t.Fatal(err)
		}
	}
	fx := newFixture(t, "2.3.5", "1.5.1")
	usePins(t, fx.containerd, old.runc)
	restarts := 0
	_, err := Reconcile(context.Background(), Options{
		Arch:          "amd64",
		Restart:       func(context.Context) error { restarts++; return nil },
		DaemonVersion: func(context.Context) (string, error) { return "", errors.New("socket refused") },
		DaemonWait:    1500 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("Reconcile err = %v, want rollback", err)
	}
	if restarts != 2 {
		t.Fatalf("restarts = %d, want restart plus rollback restart", restarts)
	}
	if InstalledVersion(old.containerd) != "2.3.4" {
		t.Fatalf("containerd link after rollback = %q", readLink(t, "containerd"))
	}
}

func TestReconcileSkipsNewerInstalledVersion(t *testing.T) {
	useTempRoot(t)
	newer := newFixture(t, "2.4.0", "1.5.1")
	for _, c := range []Component{newer.containerd, newer.runc} {
		st, err := Fetch(context.Background(), c, "amd64", t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Install(st); err != nil {
			t.Fatal(err)
		}
	}
	fx := newFixture(t, "2.3.5", "1.5.1")
	usePins(t, fx.containerd, fx.runc)
	res, err := Reconcile(context.Background(), Options{
		Arch:          "amd64",
		Restart:       func(context.Context) error { t.Fatal("restart on skip"); return nil },
		DaemonVersion: func(context.Context) (string, error) { return "2.4.0", nil },
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "containerd" || len(res.Updated) != 0 {
		t.Fatalf("skipped=%v updated=%v", res.Skipped, res.Updated)
	}
	if InstalledVersion(newer.containerd) != "2.4.0" {
		t.Fatalf("containerd was downgraded to %q", InstalledVersion(newer.containerd))
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.4.0", "2.3.5", true},
		{"2.3.5", "2.3.5", false},
		{"2.3.4", "2.3.5", false},
		{"v2.10.0", "2.9.9", true},
		{"1.5.1-rc1", "1.5.0", true},
		{"garbage", "1.0.0", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Fatalf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
