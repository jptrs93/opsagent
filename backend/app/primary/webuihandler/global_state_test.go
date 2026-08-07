package webuihandler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func newGlobalStateTestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	store := sqlite.NewPrimaryStorage(filepath.Join(dir, "primary.db"))
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	return &Handler{Store: store, Secrets: secretManager}
}

func TestGetV1GlobalStateReturnsEachSection(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	space, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	h.Store.SetUserConfig("log_level", "debug", 0, space.ID)
	cfg := createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "api", SpaceID: space.ID}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))

	res, err := h.GetV1GlobalState(apigen.Context{Ctx: context.Background()})
	if err != nil {
		t.Fatalf("GetV1GlobalState: %v", err)
	}
	if res.Spaces == nil || res.Assets == nil || res.Configs == nil || res.Secrets == nil || res.DeploymentConfigs == nil {
		t.Fatalf("expected every section to be populated, got %+v", res)
	}
	if len(res.Spaces.Items) == 0 {
		t.Error("expected at least the created space")
	}
	var foundConfig bool
	for _, c := range res.Configs.Items {
		if c.Name == "log_level" && c.Value == "debug" {
			foundConfig = true
		}
	}
	if !foundConfig {
		t.Errorf("expected log_level config, got %+v", res.Configs.Items)
	}
	var foundDeployment bool
	for _, d := range res.DeploymentConfigs.Items {
		if d.ID == cfg.ID {
			foundDeployment = true
		}
	}
	if !foundDeployment {
		t.Errorf("expected deployment %d in snapshot", cfg.ID)
	}
}

func TestGetV1GlobalStateRedactsSystemdRuntime(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	spec := apigen.DeploymentSpec{
		SystemdSpec:    &apigen.SystemdSpec{Runtime: &apigen.SystemdRuntime{Name: "svc", BinPath: "/usr/local/bin/svc"}},
		Networking:     hostNetworking(),
		Container1Spec: nil,
	}
	createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "svc"}, &spec)

	res, err := h.GetV1GlobalState(apigen.Context{Ctx: context.Background()})
	if err != nil {
		t.Fatalf("GetV1GlobalState: %v", err)
	}
	for _, d := range res.DeploymentConfigs.Items {
		if d.Spec.SystemdSpec != nil && d.Spec.SystemdSpec.Runtime != nil {
			t.Fatalf("systemd runtime must be redacted, got %+v", d.Spec.SystemdSpec.Runtime)
		}
	}
}

func TestGetV1GlobalStateExcludesDeletedDeployments(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "gone"}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	markDeleted(t, h, cfg)

	res, err := h.GetV1GlobalState(apigen.Context{Ctx: context.Background()})
	if err != nil {
		t.Fatalf("GetV1GlobalState: %v", err)
	}
	for _, d := range res.DeploymentConfigs.Items {
		if d.ID == cfg.ID {
			t.Fatalf("deleted deployment %d must not appear", cfg.ID)
		}
	}
}

func TestPostV1DeploymentsGetReturnsConfigAndInstances(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "api"}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	seedDeploymentRunnerStatus(h.Store, cfg, apigen.RunningStatus_RUNNING)

	res, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: cfg.ID})
	if err != nil {
		t.Fatalf("PostV1DeploymentsGet: %v", err)
	}
	if res.Config == nil || res.Config.ID != cfg.ID {
		t.Fatalf("config = %+v, want id %d", res.Config, cfg.ID)
	}
	if res.Instances == nil || len(res.Instances.Items) == 0 {
		t.Fatalf("expected at least one instance, got %+v", res.Instances)
	}
	for _, inst := range res.Instances.Items {
		if inst.Instance.DeploymentID != cfg.ID {
			t.Errorf("instance belongs to deployment %d, want %d", inst.Instance.DeploymentID, cfg.ID)
		}
	}
}

func TestPostV1DeploymentsGetOnlyReturnsRequestedDeployment(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	wanted := createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "api"}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	other := createTestDeployment(h.Store, "node-b", apigen.DeploymentIdentity{Name: "web"}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	seedDeploymentRunnerStatus(h.Store, wanted, apigen.RunningStatus_RUNNING)
	seedDeploymentRunnerStatus(h.Store, other, apigen.RunningStatus_RUNNING)

	res, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: wanted.ID})
	if err != nil {
		t.Fatalf("PostV1DeploymentsGet: %v", err)
	}
	for _, inst := range res.Instances.Items {
		if inst.Instance.DeploymentID == other.ID {
			t.Fatalf("leaked instance from deployment %d", other.ID)
		}
	}
}

func TestPostV1DeploymentsGetRejectsBadID(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "gone"}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	markDeleted(t, h, cfg)

	for name, id := range map[string]int32{"zero": 0, "negative": -1, "unknown": 99999, "deleted": cfg.ID} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: id}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestGlobalStateRoutesSpeakJSON(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	if _, err := h.Store.CreateSpace("prod"); err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	cfg := createTestDeployment(h.Store, "node-a", apigen.DeploymentIdentity{Name: "api"}, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	mux := apigen.CreateApiServerMux(h, &apigen.MuxConfig{
		VerifyAuth: func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
			return apigen.Context{Ctx: ctx}, nil
		},
	})

	t.Run("global-state", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/global/state", nil)
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q", ct)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
		}
		for _, key := range []string{"spaces", "assets", "configs", "secrets", "deployment_configs"} {
			if _, ok := body[key]; !ok {
				t.Errorf("missing %q in JSON body, got keys %v", key, keysOf(body))
			}
		}
	})

	t.Run("deployment-state accepts a JSON request body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/deployments/get", strings.NewReader(`{"id":`+strconv.Itoa(int(cfg.ID))+`}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
		}
		config, ok := body["config"].(map[string]any)
		if !ok {
			t.Fatalf("missing config object, got keys %v", keysOf(body))
		}
		if int32(config["id"].(float64)) != cfg.ID {
			t.Errorf("config.id = %v, want %d", config["id"], cfg.ID)
		}
	})

	t.Run("protobuf stays the default", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/global/state", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if ct := w.Header().Get("Content-Type"); ct != "application/protobuf" {
			t.Fatalf("Content-Type = %q, want application/protobuf", ct)
		}
		if _, err := apigen.DecodeGlobalState(w.Body.Bytes()); err != nil {
			t.Fatalf("body did not decode as protobuf: %v", err)
		}
	})
}

func markDeleted(t *testing.T, h *Handler, cfg *apigen.DeploymentConfig) {
	t.Helper()
	deleted := true
	spec := cfg.Spec
	_, _, versionOK := h.Store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, sqlite.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            &spec,
		Deleted:         &deleted,
	})
	if !versionOK {
		t.Fatalf("marking deployment %d deleted: version conflict", cfg.ID)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func ptr[T any](v T) *T {
	return &v
}
