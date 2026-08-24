package webuihandler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// The instructions document hand-writes curl calls against this API, so the
// JSON field names and status numbers it tells an agent to expect are verified
// here over real HTTP rather than through the Go handler signatures.
func TestAgentSessionHandshakeOverJSON(t *testing.T) {
	h, user := newAuthTestHandler(t)
	server := httptest.NewServer(apigen.CreateApiServerMux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 1 << 20,
	}))
	t.Cleanup(server.Close)

	post := func(t *testing.T, path, body string) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("POST %s: status %d", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("POST %s: content type %q, want JSON", path, ct)
		}
		var out map[string]any
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
		return out
	}

	// Step 1, exactly as the instructions spell it.
	started := post(t, "/v1/agent-sessions/request-start", `{"user_id": 1}`)
	id, _ := started["id"].(string)
	if id == "" {
		t.Fatalf("request-start returned no id: %#v", started)
	}
	if code, _ := started["approval_code"].(string); code == "" {
		t.Fatalf("request-start returned no approval_code: %#v", started)
	}
	if status, _ := started["status"].(float64); int(status) != 1 {
		t.Fatalf("status = %v, want 1 (PENDING) as documented", started["status"])
	}

	// Step 2 before approval: pending, and pointedly no token.
	pending := post(t, "/v1/agent-sessions/get-session", `{"id": "`+id+`"}`)
	if status, _ := pending["status"].(float64); int(status) != 1 {
		t.Fatalf("status = %v, want 1 (PENDING)", pending["status"])
	}
	if token, _ := pending["token"].(string); token != "" {
		t.Fatal("a pending get-session handed out a token")
	}

	if _, err := h.PostV1AgentSessionsApprove(h.operatorCtx(t, user), &apigen.AgentSessionApproveRequest{ID: id}); err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}

	collected := post(t, "/v1/agent-sessions/get-session", `{"id": "`+id+`"}`)
	if status, _ := collected["status"].(float64); int(status) != 2 {
		t.Fatalf("status = %v, want 2 (APPROVED) as documented", collected["status"])
	}
	token, _ := collected["token"].(string)
	if token == "" {
		t.Fatalf("approved get-session returned no token: %#v", collected)
	}

	// Step 3: the collected token authenticates a scoped endpoint, and stops
	// doing so the moment the operator revokes it.
	authed := func(t *testing.T) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/secrets/status", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("authenticated request: %v", err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	if got := authed(t); got != http.StatusOK {
		t.Fatalf("collected token status = %d, want 200", got)
	}
	if err := h.PostV1AgentSessionsRevoke(h.operatorCtx(t, user), &apigen.AgentSessionRevokeRequest{ID: id}); err != nil {
		t.Fatalf("PostV1AgentSessionsRevoke: %v", err)
	}
	if got := authed(t); got != http.StatusUnauthorized {
		t.Fatalf("status after revoke = %d, want 401", got)
	}
}
