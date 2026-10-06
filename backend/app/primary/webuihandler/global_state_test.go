package webuihandler

import (
	"bytes"
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

func TestPostV1GlobalEventsReturnsEachSection(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	space, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := values.CreateConfig(h.Store, "log_level", space.ID, 0, 0, "debug"); err != nil {
		t.Fatalf("CreateConfigWithVersion: %v", err)
	}
	cfg := createTestDeployment(h.Store, "node-a", space.ID, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))

	res, err := h.PostV1GlobalEvents(apigen.Context{Ctx: context.Background()}, &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatalf("PostV1GlobalEvents: %v", err)
	}
	if res.Seq == 0 || !res.Snapshot.Present || !res.Synced || len(res.Snapshot.Value.Entities) == 0 || res.Snapshot.Value.Seq != res.Seq {
		t.Fatalf("expected a synced snapshot with entries, got %+v", res)
	}
	fold := foldOpening(res)
	if len(fold[apigen.CoreEntityType_CORE_ENTITY_SPACE]) == 0 {
		t.Error("expected at least the created space")
	}
	var foundConfig bool
	for _, c := range fold[apigen.CoreEntityType_CORE_ENTITY_CONFIG] {
		if c.Value.Config.Fs.Key == "log_level" && c.Value.Config.Value == "debug" {
			foundConfig = true
		}
	}
	if !foundConfig {
		t.Errorf("expected log_level config, got %+v", fold[apigen.CoreEntityType_CORE_ENTITY_CONFIG])
	}
	if fold[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT][cfg.Deployment.ID] == nil {
		t.Errorf("expected deployment %d in the bootstrap", cfg.Deployment.ID)
	}
}

func TestPostV1GlobalEventsExcludesDeletedDeployments(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", 0, "gone", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	markDeleted(t, h, cfg)

	res, err := h.PostV1GlobalEvents(apigen.Context{Ctx: context.Background()}, &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatalf("PostV1GlobalEvents: %v", err)
	}
	if foldOpening(res)[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT][cfg.Deployment.ID] != nil {
		t.Fatalf("deleted deployment %d must not appear", cfg.Deployment.ID)
	}
}

func TestPostV1DeploymentsGetReturnsConfigAndInstances(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", 0, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	seedDeploymentRunnerStatus(h.Store, cfg, apigen.RunningStatus_RUNNING_STATUS_RUNNING)

	res, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: cfg.Deployment.ID})
	if err != nil {
		t.Fatalf("PostV1DeploymentsGet: %v", err)
	}
	if res.Deployment.Deployment.ID != cfg.Deployment.ID {
		t.Fatalf("deployment = %+v, want id %d", res.Deployment, cfg.Deployment.ID)
	}
	if meta := res.Deployment.Meta; meta.Version != cfg.Meta.Version || meta.SpecVersion != cfg.Meta.SpecVersion || meta.UpdatedSeq != cfg.Meta.UpdatedSeq || meta.CreatedTime != cfg.Meta.CreatedTime || meta.Deleted {
		t.Fatalf("deployment meta = %+v, want version %d spec %d seq %d", res.Deployment.Meta, cfg.Meta.Version, cfg.Meta.SpecVersion, cfg.Meta.UpdatedSeq)
	}
	if res.ScheduledInstances == nil || len(res.ScheduledInstances) == 0 {
		t.Fatalf("expected at least one instance, got %+v", res.ScheduledInstances)
	}
	for _, inst := range res.ScheduledInstances {
		if inst.Deployment.DeploymentID != cfg.Deployment.ID {
			t.Errorf("instance belongs to deployment %d, want %d", inst.Deployment.DeploymentID, cfg.Deployment.ID)
		}
	}
}

func TestPostV1DeploymentsGetOnlyReturnsRequestedDeployment(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	wanted := createTestDeployment(h.Store, "node-a", 0, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	other := createTestDeployment(h.Store, "node-b", 0, "web", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	seedDeploymentRunnerStatus(h.Store, wanted, apigen.RunningStatus_RUNNING_STATUS_RUNNING)
	seedDeploymentRunnerStatus(h.Store, other, apigen.RunningStatus_RUNNING_STATUS_RUNNING)

	res, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: wanted.Deployment.ID})
	if err != nil {
		t.Fatalf("PostV1DeploymentsGet: %v", err)
	}
	for _, inst := range res.ScheduledInstances {
		if inst.Deployment.DeploymentID == other.Deployment.ID {
			t.Fatalf("leaked instance from deployment %d", other.Deployment.ID)
		}
	}
}

func TestPostV1DeploymentsGetRejectsBadID(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	cfg := createTestDeployment(h.Store, "node-a", 0, "gone", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	markDeleted(t, h, cfg)

	for name, id := range map[string]uint64{"zero": 0, "unknown": 99999, "deleted": cfg.Deployment.ID} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.PostV1DeploymentsGet(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentGetRequest{ID: id}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestGlobalStateRoutesSpeakJSON(t *testing.T) {
	h := newGlobalStateTestHandler(t)
	if _, err := nodes.CreateSpace(h.Store, "prod", 0); err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	cfg := createTestDeployment(h.Store, "node-a", 0, "api", ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	mux := apigen.CreateApiServerMux(h, &apigen.MuxConfig{
		VerifyAuth: func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
			return apigen.Context{Ctx: ctx}, nil
		},
	})

	t.Run("global-events", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/global/events", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
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
		for _, key := range []string{"snapshot", "seq", "synced"} {
			if _, ok := body[key]; !ok {
				t.Errorf("missing %q in JSON body, got keys %v", key, keysOf(body))
			}
		}
	})

	t.Run("deployment-state accepts a JSON request body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/deployments/get", strings.NewReader(`{"id":`+strconv.Itoa(int(cfg.Deployment.ID))+`}`))
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
		record, ok := body["deployment"].(map[string]any)
		if !ok {
			t.Fatalf("missing deployment record, got keys %v", keysOf(body))
		}
		deployment, _ := record["deployment"].(map[string]any)
		meta, _ := record["meta"].(map[string]any)
		if deployment == nil || meta == nil || uint64(deployment["id"].(float64)) != cfg.Deployment.ID || uint32(meta["version"].(float64)) != cfg.Meta.Version {
			t.Errorf("record = %v, want deployment %d at version %d with meta", record, cfg.Deployment.ID, cfg.Meta.Version)
		}
	})

	t.Run("protobuf stays the default", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/global/events", bytes.NewReader((&apigen.EventStreamRequest{}).Encode()))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if ct := w.Header().Get("Content-Type"); ct != "application/protobuf" {
			t.Fatalf("Content-Type = %q, want application/protobuf", ct)
		}
		if _, err := apigen.DecodeEventStreamMsg(w.Body.Bytes()); err != nil {
			t.Fatalf("body did not decode as protobuf: %v", err)
		}
	})
}

func markDeleted(t *testing.T, h *Handler, cfg *apigen.DeploymentRecord) {
	t.Helper()
	statetest.DeleteDeployment(h.Store, apigen.Context{}, cfg.Deployment.ID)
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
