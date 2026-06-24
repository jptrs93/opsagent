package handler

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	secretspkg "github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestValidateDeploymentSpecNixDockerBuild(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			NixDockerBuild: &apigen.NixDockerBuildConfig{
				Repo:  "github.com/acme/web",
				Flake: "nix/web/flake.nix",
			},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{User: "1000"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Prepare.NixDockerBuild == nil {
		t.Fatal("nixDockerBuild is nil")
	}
	if spec.Prepare.NixDockerBuild.Repo != "github.com/acme/web" {
		t.Fatalf("repo = %q", spec.Prepare.NixDockerBuild.Repo)
	}
	if spec.Prepare.NixDockerBuild.Flake != "nix/web/flake.nix" {
		t.Fatalf("flake = %q", spec.Prepare.NixDockerBuild.Flake)
	}
	if spec.Runner.Container.User != "1000" {
		t.Fatalf("container user = %q", spec.Runner.Container.User)
	}
}

type fakeAssetResolver map[string]*apigen.Asset

func (r fakeAssetResolver) GetAsset(key string, version int32) (*apigen.Asset, bool) {
	asset, ok := r[key]
	if !ok || (version > 0 && asset.Version != version) {
		return nil, false
	}
	return asset, true
}

type fakeSecretResolver map[int32]string

func (r fakeSecretResolver) List() []secretspkg.Meta {
	out := make([]secretspkg.Meta, 0, len(r))
	for id := range r {
		out = append(out, secretspkg.Meta{ID: id})
	}
	return out
}

type fakeConfigResolver map[int32]string

func (r fakeConfigResolver) ResolveConfig(id int32) (string, bool) {
	v, ok := r[id]
	return v, ok
}

func TestValidateDeploymentSpecResolvesAssetMounts(t *testing.T) {
	assets := fakeAssetResolver{
		"nginx.conf": {
			ID:        42,
			Key:       "nginx.conf",
			CreatedAt: time.UnixMilli(1000),
			Version:   3,
			Format:    "nginx",
		},
	}
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				AssetMounts: []*apigen.ContainerAssetMount{{
					Asset:      "nginx.conf",
					Path:       "/etc/nginx/nginx.conf",
					Executable: true,
				}},
			},
		},
	}, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mounts := spec.Runner.Container.AssetMounts
	if len(mounts) != 1 {
		t.Fatalf("asset mounts len = %d", len(mounts))
	}
	if mounts[0].AssetID != 42 || mounts[0].Asset != "nginx.conf" || mounts[0].Version != 3 || mounts[0].Path != "/etc/nginx/nginx.conf" || mounts[0].Format != "nginx" || !mounts[0].Executable {
		t.Fatalf("asset mount not resolved: %+v", mounts[0])
	}
}

func TestValidateDeploymentSpecResolvesEnvAssetRefs(t *testing.T) {
	assets := fakeAssetResolver{
		"app.conf": {
			ID:      51,
			Key:     "app.conf",
			Version: 7,
		},
	}
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				EnvVars: map[string]*apigen.EnvVarValue{
					"APP_CONFIG": {Asset: " app.conf "},
				},
			},
		},
	}, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	value := spec.Runner.Container.EnvVars["APP_CONFIG"]
	if value.Asset != "app.conf" || value.AssetID != 51 || value.Version != 7 {
		t.Fatalf("env asset ref not resolved: %+v", value)
	}
}

func TestValidateDeploymentSpecRejectsUnknownEnvAssetRef(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				EnvVars: map[string]*apigen.EnvVarValue{
					"APP_CONFIG": {Asset: "missing.conf"},
				},
			},
		},
	}, fakeAssetResolver{})
	if err == nil || !strings.Contains(err.Error(), `asset "missing.conf" not found`) {
		t.Fatalf("err = %v, want unknown asset", err)
	}
}

func TestValidateDeploymentSpecAcceptsHostMounts(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				Mounts: []*apigen.ContainerMount{{
					Host:      " /home/ubuntu/coflip-server/data ",
					Container: " /data ",
					Readonly:  false,
				}},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mount := spec.Runner.Container.Mounts[0]
	if mount.Host != "/home/ubuntu/coflip-server/data" || mount.Container != "/data" || mount.Readonly {
		t.Fatalf("mount not normalized: %+v", mount)
	}
}

func TestValidateDeploymentSpecNormalizesContainerCommand(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				Command: []string{" /app/server ", "", " --listen ", " :8080 ", "   "},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	want := []string{"/app/server", "--listen", ":8080"}
	if got := spec.Runner.Container.Command; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestValidateDeploymentSpecAcceptsDevShmSizeKb(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DevShmSizeKb: 65536,
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Runner.Container.DevShmSizeKb != 65536 {
		t.Fatalf("devShmSizeKb = %d, want 65536", spec.Runner.Container.DevShmSizeKb)
	}
}

func TestValidateDeploymentSpecRejectsInvalidDevShmSizeKb(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DevShmSizeKb: -1,
			},
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "devShmSizeKb") {
		t.Fatalf("err = %v, want invalid devShmSizeKb", err)
	}
}

func TestValidateDeploymentSpecRejectsInvalidHostMounts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		host      string
		container string
	}{
		{name: "relative host", host: "data", container: "/data"},
		{name: "relative container", host: "/srv/data", container: "data"},
		{name: "root host", host: "/", container: "/data"},
		{name: "root container", host: "/srv/data", container: "/"},
		{name: "unclean host", host: "/srv/../data", container: "/data"},
		{name: "unclean container", host: "/srv/data", container: "/var/../data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{
					ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
				},
				Runner: apigen.RunnerConfig{
					Container: apigen.ContainerRunnerConfig{
						Mounts: []*apigen.ContainerMount{{Host: tc.host, Container: tc.container}},
					},
				},
			}, nil)
			if err == nil {
				t.Fatal("expected invalid host mount")
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsSystemdRunner(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			NixDockerBuild: &apigen.NixDockerBuildConfig{
				Repo:  "github.com/acme/web",
				Flake: "nix/web/flake.nix",
			},
		},
		Runner: apigen.RunnerConfig{
			Systemd: apigen.SystemdRunnerConfig{Name: "opendeploy", BinPath: "/var/lib/opendeploy/bin/opendeploy"},
		},
	}, nil)
	if err == nil {
		t.Fatal("expected validateDeploymentSpecWithAssets to reject systemd runner")
	}
}

func TestDeploymentUpdateRejectsSystemDeploymentSpecUpdate(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	store.EnsureSystemDeployment("primary", "v0.0.194")
	var system *apigen.DeploymentConfig
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if sqlite.IsSystemDeploymentConfig(cfg) {
			system = cfg
			break
		}
	}
	if system == nil {
		t.Fatal("system deployment not found")
	}

	h := &Handler{Store: store}
	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: system.ID,
		Version:      system.Version + 1,
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{UpgradeStrategy: apigen.ContainerUpgradeStrategy_RECREATE}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecAcceptsKnownEnvRefs(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"PGUSER":     {SecretID: ptrInt32(6)},
				"PGDATABASE": {ConfigID: ptrInt32(18)},
			}},
		},
	}, nil, fakeSecretResolver{6: "postgres"}, fakeConfigResolver{18: "postgres"})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownSecretRef(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"PGPASSWORD": {SecretID: ptrInt32(99)},
			}},
		},
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown secret id 99") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownConfigRef(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"PGDATABASE": {ConfigID: ptrInt32(99)},
			}},
		},
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown config id 99") {
		t.Fatalf("err = %v, want unknown config", err)
	}
}

func TestValidateDeploymentSpecAcceptsLiteralEnvValues(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"LITERAL": {Value: ptrString("${s:not.real} and ${c:not.real}")},
			}},
		},
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestDeploymentCreatePersistsInitialStoppedDesiredState(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	h := &Handler{Store: store}

	cfg, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"},
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		},
		DesiredState: apigen.DesiredState{Version: "1.25", Running: false},
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentCreate failed: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want initial config version 1", cfg.Version)
	}
	if cfg.DesiredState.Version != "1.25" || cfg.DesiredState.Running {
		t.Fatalf("desired state = %+v, want stopped 1.25", cfg.DesiredState)
	}
	if history := store.MustFetchDeploymentHistory(cfg.ID); len(history) != 1 {
		t.Fatalf("history len = %d, want create only", len(history))
	} else if history[0].DesiredState.Version != "1.25" || history[0].DesiredState.Running {
		t.Fatalf("history desired state = %+v, want stopped 1.25", history[0].DesiredState)
	}
}

func TestDeploymentDeleteRequiresStoppedDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{Version: "1.25", Running: false})
	store.MustWriteDeploymentStatus(created.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_RUNNING
		return true
	})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version + 1,
		TargetVersion: "1.26",
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate setup failed: %v", err)
	}
	cfg := h.findConfigByID(created.ID)
	if cfg == nil {
		t.Fatal("deployment not found")
	}
	err = h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: cfg.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped-only rejection", err)
	}
}

func TestDeploymentDeleteAllowsRunningMissingMachineDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "worker-a", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{Version: "1.25", Running: true})
	store.MustWriteDeploymentStatus(created.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_RUNNING
		return true
	})
	h := &Handler{Store: store, MachineName: "primary"}

	err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteSoftDeletesStoppedDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{Version: "1.25", Running: false})
	store.MustWriteDeploymentStatus(created.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_STOPPED
		return true
	})
	h := &Handler{Store: store}

	err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
	history := store.MustFetchDeploymentHistory(created.ID)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	deleted := history[len(history)-1]
	if !deleted.Deleted || deleted.DesiredState.Running || deleted.DesiredState.Version != "1.25" {
		t.Fatalf("deleted history entry = %+v", deleted)
	}
}

func TestDeploymentUpdateCombinesSpaceAndDesiredStateInSingleConfigVersion(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	spaceID := int32(2)
	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version + 1,
		SpaceID:       &spaceID,
		TargetVersion: "1.25",
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate failed: %v", err)
	}

	cfg := h.findConfigByID(created.ID)
	if cfg == nil {
		t.Fatal("updated deployment not found")
	}
	if cfg.Version != created.Version+1 {
		t.Fatalf("version = %d, want %d", cfg.Version, created.Version+1)
	}
	if cfg.ConfigID.SpaceID != spaceID {
		t.Fatalf("space = %d, want %d", cfg.ConfigID.SpaceID, spaceID)
	}
	if cfg.DesiredState.Version != "1.25" || !cfg.DesiredState.Running {
		t.Fatalf("desired state = %+v, want running 1.25", cfg.DesiredState)
	}
	if history := store.MustFetchDeploymentHistory(created.ID); len(history) != 2 {
		t.Fatalf("history len = %d, want create + one combined update", len(history))
	}
}

func TestDeploymentUpdateRejectsStaleExpectedVersion(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version,
		TargetVersion: "1.25",
	})
	apiErr, ok := err.(apigen.ApiErr)
	if !ok || apiErr.Code != http.StatusBadRequest || !strings.Contains(apiErr.Error(), "version mismatch") {
		t.Fatalf("err = %#v, want 400 version mismatch", err)
	}
}

func TestDeploymentUpdateRequiresExpectedVersion(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		TargetVersion: "1.25",
	})
	apiErr, ok := err.(apigen.ApiErr)
	if !ok || apiErr.Code != http.StatusBadRequest || !strings.Contains(apiErr.Error(), "version mismatch") {
		t.Fatalf("err = %#v, want 400 version mismatch", err)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
