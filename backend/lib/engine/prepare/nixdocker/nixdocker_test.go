package nixdocker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
)

const testCommit = "0123456789ABCDEF0123456789ABCDEF01234567"

func TestCheckedOutFlakePathRequiresLocalFile(t *testing.T) {
	repoDir := t.TempDir()
	flakePath := filepath.Join(repoDir, "nix", "app", "flake.nix")
	if err := os.MkdirAll(filepath.Dir(flakePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flakePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkedOutFlakePath(repoDir, "nix/app/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if got != flakePath {
		t.Fatalf("path = %q, want %q", got, flakePath)
	}
}

func TestCheckedOutFlakePathRejectsEscapingPath(t *testing.T) {
	if _, err := checkedOutFlakePath(t.TempDir(), "../flake.nix"); err == nil {
		t.Fatal("expected escaping path error")
	}
}

func TestCheckedOutFlakePathRejectsMissingFile(t *testing.T) {
	if _, err := checkedOutFlakePath(t.TempDir(), "flake.nix"); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestCheckedOutFlakePathRejectsNonRegularAndWrongBasename(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "real.nix"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.nix", filepath.Join(repoDir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedOutFlakePath(repoDir, "flake.nix"); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if _, err := checkedOutFlakePath(repoDir, "real.nix"); err == nil {
		t.Fatal("expected non-flake.nix basename rejection")
	}
}

func TestNixBuildArgs(t *testing.T) {
	base := []string{
		"--extra-experimental-features", "nix-command flakes",
		"build", "--no-update-lock-file", "--no-link", "--print-out-paths", "-L",
	}
	if got := nixBuildArgs(""); !reflect.DeepEqual(got, base) {
		t.Fatalf("default args = %q, want %q", got, base)
	}

	want := append(append([]string(nil), base...), ".#radkitRpaClientImage")
	if got := nixBuildArgs(".#radkitRpaClientImage"); !reflect.DeepEqual(got, want) {
		t.Fatalf("target args = %q, want %q", got, want)
	}
}

func TestImageRefUsesBuildInputsAndCommit(t *testing.T) {
	nix := &apigen.NixDockerBuild2{
		Repo:   "github.com/acme/platform",
		Flake:  "services/api/flake.nix",
		Target: ".#apiImage",
	}
	ref := imageRef(nix, testCommit)
	if !strings.HasPrefix(ref, "opendeploy.local/nix-docker-build/v1/") {
		t.Fatalf("image ref = %q, want v1 cache namespace", ref)
	}
	if !strings.HasSuffix(ref, ":"+strings.ToLower(testCommit)) {
		t.Fatalf("image ref = %q, want lowercase commit tag", ref)
	}

	same := imageRef(&apigen.NixDockerBuild2{
		Repo:   nix.Repo,
		Flake:  nix.Flake,
		Target: nix.Target,
	}, strings.ToLower(testCommit))
	if same != ref {
		t.Fatalf("equivalent build ref = %q, want %q", same, ref)
	}

	changes := []*apigen.NixDockerBuild2{
		{Repo: "github.com/acme/other", Flake: nix.Flake, Target: nix.Target},
		{Repo: nix.Repo, Flake: "services/worker/flake.nix", Target: nix.Target},
		{Repo: nix.Repo, Flake: nix.Flake, Target: ".#workerImage"},
	}
	for _, changed := range changes {
		if got := imageRef(changed, testCommit); got == ref {
			t.Errorf("changed build inputs reused image ref %q", got)
		}
	}
	if imageSourceKey(nix, "linux", "amd64") == imageSourceKey(nix, "linux", "arm64") {
		t.Fatal("platform change did not change image source key")
	}
}

func TestPrepareReusesReadyImageBeforeCheckout(t *testing.T) {
	dep := testNixDeployment()
	log, _, err := preparerlog.New(context.Background(), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	p := New(nil)
	var checkedRef string
	p.imageReady = func(_ context.Context, ref string) error {
		checkedRef = ref
		return nil
	}
	artifact, status := p.Prepare(context.Background(), dep, log)
	wantRef := imageRef(dep.Spec.Container().Source.NixDockerBuild, dep.WorkloadVersion())
	if status != apigen.PreparationStatus_READY {
		t.Fatalf("status = %v, want READY", status)
	}
	if artifact != wantRef || checkedRef != wantRef {
		t.Fatalf("artifact/checked ref = %q/%q, want %q", artifact, checkedRef, wantRef)
	}
}

func TestPrepareFailsOnContainerdCacheCheckError(t *testing.T) {
	dep := testNixDeployment()
	log, _, err := preparerlog.New(context.Background(), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	p := New(nil)
	p.imageReady = func(context.Context, string) error { return errors.New("containerd unavailable") }
	artifact, status := p.Prepare(context.Background(), dep, log)
	if status != apigen.PreparationStatus_FAILED || artifact != "" {
		t.Fatalf("artifact/status = %q/%v, want empty/FAILED", artifact, status)
	}
}

func testNixDeployment() *apigen.DeploymentConfig2 {
	return &apigen.DeploymentConfig2{
		ID:      987654,
		Version: 3,
		Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
			Source: apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild2{
				Repo:   "github.com/acme/platform",
				Flake:  "services/api/flake.nix",
				Target: ".#apiImage",
			}},
			Version: testCommit,
			Running: true,
		}},
	}
}

func TestFormatImageSize(t *testing.T) {
	tests := map[int64]string{
		0:                  "0 B",
		1023:               "1023 B",
		1024:               "1.0 KiB",
		5 * 1024 * 1024:    "5.0 MiB",
		1536 * 1024 * 1024: "1.5 GiB",
	}
	for size, want := range tests {
		if got := formatImageSize(size); got != want {
			t.Errorf("formatImageSize(%d) = %q, want %q", size, got, want)
		}
	}
}
