package webuihandler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

const generatorSymbols = "!@#$%^&*()-_=+[]{}"

func generateSecret(t *testing.T, h *Handler, user *apigen.InternalUser, req *apigen.SecretGenerateRequest) (*apigen.SecretMeta, error) {
	t.Helper()
	return h.PostV1SecretsGenerate(apigen.Context{Ctx: context.Background(), User: user}, req)
}

func TestGenerateSecretStoresAValueTheCallerNeverSees(t *testing.T) {
	h, user := newAuthTestHandler(t)

	meta, err := generateSecret(t, h, user, &apigen.SecretGenerateRequest{
		Name:     "db-password",
		Password: &apigen.SecretPasswordSpec{},
	})
	if err != nil {
		t.Fatalf("PostV1SecretsGenerate: %v", err)
	}
	if meta.Name != "db-password" || meta.ID == 0 {
		t.Fatalf("meta = %+v, want a named secret with an id", meta)
	}
	if meta.UpdatedBy != user.ID {
		t.Fatalf("UpdatedBy = %d, want the approving operator %d", meta.UpdatedBy, user.ID)
	}

	// The response type has no value field at all, so the only way to confirm
	// something real was stored is to go and reveal it.
	revealed, err := h.PostV1SecretsReveal(apigen.Context{Ctx: context.Background(), User: user},
		&apigen.SecretRevealRequest{ID: meta.ID})
	if err != nil {
		t.Fatalf("PostV1SecretsReveal: %v", err)
	}
	if len(revealed.Value) != secrets.DefaultPasswordLength {
		t.Fatalf("stored value is %d bytes, want the %d-byte default", len(revealed.Value), secrets.DefaultPasswordLength)
	}
	if strings.ContainsAny(string(revealed.Value), generatorSymbols) {
		t.Fatalf("value %q contains symbols, which were not requested", revealed.Value)
	}
}

func TestGenerateSecretHonoursTheSpecification(t *testing.T) {
	h, user := newAuthTestHandler(t)

	meta, err := generateSecret(t, h, user, &apigen.SecretGenerateRequest{
		Name:     "api-key",
		SpaceID:  1,
		Password: &apigen.SecretPasswordSpec{Length: 64, IncludeSymbols: true},
	})
	if err != nil {
		t.Fatalf("PostV1SecretsGenerate: %v", err)
	}
	revealed, err := h.PostV1SecretsReveal(apigen.Context{Ctx: context.Background(), User: user},
		&apigen.SecretRevealRequest{ID: meta.ID})
	if err != nil {
		t.Fatalf("PostV1SecretsReveal: %v", err)
	}
	if len(revealed.Value) != 64 {
		t.Fatalf("value is %d bytes, want the 64 requested", len(revealed.Value))
	}
}

func TestGenerateSecretIsCreateOnly(t *testing.T) {
	h, user := newAuthTestHandler(t)

	req := &apigen.SecretGenerateRequest{Name: "db-password", Password: &apigen.SecretPasswordSpec{}}
	if _, err := generateSecret(t, h, user, req); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	// Without this guard a caller could append a version over an operator's
	// credential and neither of them could read the result back.
	_, err := generateSecret(t, h, user, req)
	if !errors.Is(err, SecretAlreadyExistsErr) {
		t.Fatalf("second generate err = %v, want SecretAlreadyExistsErr", err)
	}

	// The same name set explicitly still rotates, so the guard is on this route
	// and not on the store.
	if _, err := h.PostV1SecretsSet(apigen.Context{Ctx: context.Background(), User: user},
		&apigen.SecretSetRequest{Name: "db-password", Value: []byte("manual")}); err != nil {
		t.Fatalf("PostV1SecretsSet over a generated name: %v", err)
	}
}

func TestGenerateSecretRejectsBadRequests(t *testing.T) {
	h, user := newAuthTestHandler(t)

	cases := []struct {
		name string
		req  *apigen.SecretGenerateRequest
		want error
	}{
		{"no name", &apigen.SecretGenerateRequest{Password: &apigen.SecretPasswordSpec{}}, SecretNameRequiredErr},
		{"blank name", &apigen.SecretGenerateRequest{Name: "   ", Password: &apigen.SecretPasswordSpec{}}, SecretNameRequiredErr},
		{"no specification", &apigen.SecretGenerateRequest{Name: "a"}, SecretGeneratorRequiredErr},
		{"too short", &apigen.SecretGenerateRequest{Name: "a", Password: &apigen.SecretPasswordSpec{Length: 15}}, SecretPasswordLengthErr},
		{"too long", &apigen.SecretGenerateRequest{Name: "a", Password: &apigen.SecretPasswordSpec{Length: 4097}}, SecretPasswordLengthErr},
		{"negative", &apigen.SecretGenerateRequest{Name: "a", Password: &apigen.SecretPasswordSpec{Length: -1}}, SecretPasswordLengthErr},
		{"reserved name", &apigen.SecretGenerateRequest{Name: "opendeploy.internal", Password: &apigen.SecretPasswordSpec{}}, SecretReservedNameErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := generateSecret(t, h, user, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(h.Secrets.MetasByName(strings.TrimSpace(tc.req.Name))) > 0 {
				t.Fatalf("a rejected request still stored %q", tc.req.Name)
			}
		})
	}
}

// The point of the whole route: a token that cannot read secret values can
// still mint one. Driven through the real mux because the scope split lives in
// the generated policy, not in the handler.
func TestGenerateSecretIsReachableWithoutSecretsAccess(t *testing.T) {
	h, user := newAuthTestHandler(t)
	server := httptest.NewServer(apigen.CreateApiServerMux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 1 << 20,
	}))
	t.Cleanup(server.Close)

	agentToken := h.mustToken(t, user.ID, agentSessionScopes([]string{ScopeDefault, ScopeSecretsAccess}), time.Hour)
	browserToken := h.mustToken(t, user.ID, []string{ScopeDefault, ScopeSecretsAccess}, time.Hour)

	post := func(t *testing.T, path, token, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	status, generated := post(t, "/v1/secrets/generate", agentToken,
		`{"name": "db-password", "password": {"length": 32}}`)
	if status != http.StatusOK {
		t.Fatalf("generate with an agent token: status %d, want 200", status)
	}
	id, _ := generated["id"].(float64)
	if id == 0 {
		t.Fatalf("generate returned no id: %#v", generated)
	}
	if _, ok := generated["value"]; ok {
		t.Fatalf("generate response carried a value: %#v", generated)
	}
	revealBody := fmt.Sprintf(`{"id": %d}`, int(id))

	// ...but reading it back is still refused.
	if status, _ := post(t, "/v1/secrets/reveal", agentToken, revealBody); status != http.StatusForbidden {
		t.Fatalf("reveal with an agent token: status %d, want 403", status)
	}

	status, revealed := post(t, "/v1/secrets/reveal", browserToken, revealBody)
	if status != http.StatusOK {
		t.Fatalf("reveal with a browser token: status %d, want 200", status)
	}
	value, err := base64.StdEncoding.DecodeString(revealed["value"].(string))
	if err != nil {
		t.Fatalf("decoding revealed value: %v", err)
	}
	if len(value) != 32 {
		t.Fatalf("operator revealed %d bytes, want the 32 the agent asked for", len(value))
	}
}
