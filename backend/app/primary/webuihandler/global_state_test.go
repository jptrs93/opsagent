package webuihandler

import (
	"context"
	"encoding/json"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newGlobalStateTestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Handler{Store: store, Queries: store.Queries(), Secrets: secretManager}
}

func TestGetV1GlobalSnapshotReturnsEachSection(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	space, err := nodes.CreateSpace(h.Store, "prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := values.CreateConfig(h.Store, "log_level", space.ID, 0, 0, "debug"); err != nil {
		t.Fatalf("CreateConfigWithVersion: %v", err)
	}
	cfg := createTestDeployment(h.Store, "node-a", space.ID, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))

	res, err := h.GetV1GlobalSnapshot(apigen.Context{Ctx: context.Background()})
	if err != nil {
		t.Fatalf("GetV1GlobalSnapshot: %v", err)
	}
	if res.Seq == 0 {
		t.Fatalf("expected every section to be populated, got %+v", res)
	}
	if len(res.Spaces) == 0 {
		t.Error("expected at least the created space")
	}
	var foundConfig bool
	for _, c := range res.ConfigEvents {
		if c.Value.Fs.Name == "log_level" && len(statetest.ValueVersions(h.Store, c)) > 0 && statetest.ValueVersions(h.Store, c)[0].Value == "debug" {
			foundConfig = true
		}
	}
	if !foundConfig {
		t.Errorf("expected log_level config, got %+v", res.ConfigEvents)
	}
	var foundDeployment bool
	for _, d := range res.DeploymentEvents {
		if d.DeploymentID == cfg.DeploymentID {
			foundDeployment = true
		}
	}
	if !foundDeployment {
		t.Errorf("expected deployment %d in snapshot", cfg.DeploymentID)
	}
}

func TestGetV1GlobalSnapshotExcludesDeletedDeployments(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", 0, "gone", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	markDeleted(t, h, cfg)

	res, err := h.GetV1GlobalSnapshot(apigen.Context{Ctx: context.Background()})
	if err != nil {
		t.Fatalf("GetV1GlobalSnapshot: %v", err)
	}
	for _, d := range res.DeploymentEvents {
		if d.DeploymentID == cfg.DeploymentID {
			t.Fatalf("deleted deployment %d must not appear", cfg.DeploymentID)
		}
	}
}

func TestPostV1DeploymentsGetReturnsConfigAndInstances(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", 0, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	seedDeploymentRunnerStatus(h.Store, cfg, apigen.RunningStatus_RUNNING)

	res, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: cfg.DeploymentID})
	if err != nil {
		t.Fatalf("PostV1DeploymentsGet: %v", err)
	}
	if res.DeploymentEvent == nil || res.DeploymentEvent.DeploymentID != cfg.DeploymentID {
		t.Fatalf("config = %+v, want id %d", res.DeploymentEvent, cfg.DeploymentID)
	}
	if res.ScheduledInstanceEvents == nil || len(res.ScheduledInstanceEvents) == 0 {
		t.Fatalf("expected at least one instance, got %+v", res.ScheduledInstanceEvents)
	}
	for _, inst := range res.ScheduledInstanceEvents {
		if inst.Value.DeploymentID != cfg.DeploymentID {
			t.Errorf("instance belongs to deployment %d, want %d", inst.Value.DeploymentID, cfg.DeploymentID)
		}
	}
}

func TestPostV1DeploymentsGetOnlyReturnsRequestedDeployment(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	wanted := createTestDeployment(h.Store, "node-a", 0, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	other := createTestDeployment(h.Store, "node-b", 0, "web", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	seedDeploymentRunnerStatus(h.Store, wanted, apigen.RunningStatus_RUNNING)
	seedDeploymentRunnerStatus(h.Store, other, apigen.RunningStatus_RUNNING)

	res, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: wanted.DeploymentID})
	if err != nil {
		t.Fatalf("PostV1DeploymentsGet: %v", err)
	}
	for _, inst := range res.ScheduledInstanceEvents {
		if inst.Value.DeploymentID == other.DeploymentID {
			t.Fatalf("leaked instance from deployment %d", other.DeploymentID)
		}
	}
}

func TestPostV1DeploymentsGetRejectsBadID(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", 0, "gone", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	markDeleted(t, h, cfg)

	for name, id := range map[string]int32{"zero": 0, "negative": -1, "unknown": 99999, "deleted": cfg.DeploymentID} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: id}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestGlobalStateRoutesSpeakJSON(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	if _, err := nodes.CreateSpace(h.Store, "prod"); err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	cfg := createTestDeployment(h.Store, "node-a", 0, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	mux := apigen.CreateApiServerMux(h, &apigen.MuxConfig{
		VerifyAuth: func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
			return apigen.Context{Ctx: ctx}, nil
		},
	})

	t.Run("global-state", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/global/snapshot", nil)
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
		for _, key := range []string{"spaces", "deployment_events", "node_events", "seq"} {
			if _, ok := body[key]; !ok {
				t.Errorf("missing %q in JSON body, got keys %v", key, keysOf(body))
			}
		}
	})

	t.Run("deployment-state accepts a JSON request body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/deployments/get", strings.NewReader(`{"id":`+strconv.Itoa(int(cfg.DeploymentID))+`}`))
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
		config, ok := body["deployment_event"].(map[string]any)
		if !ok {
			t.Fatalf("missing config object, got keys %v", keysOf(body))
		}
		if int32(config["deployment_id"].(float64)) != cfg.DeploymentID {
			t.Errorf("config.id = %v, want %d", config["deployment_id"], cfg.DeploymentID)
		}
	})

	t.Run("protobuf stays the default", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/global/snapshot", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if ct := w.Header().Get("Content-Type"); ct != "application/protobuf" {
			t.Fatalf("Content-Type = %q, want application/protobuf", ct)
		}
		if _, err := apigen.DecodeSnapshot(w.Body.Bytes()); err != nil {
			t.Fatalf("body did not decode as protobuf: %v", err)
		}
	})
}

func markDeleted(t *testing.T, h *Handler, cfg *apigen.DeploymentEvent) {
	t.Helper()
	statetest.DeleteDeployment(h.Store, apigen.Context{}, cfg.DeploymentID)
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
