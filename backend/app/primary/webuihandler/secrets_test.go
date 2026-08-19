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

func generateSecret(t *testing.T, h *Handler, user *apigen.InternalUser, req *apigen.SecretGenerateRequest) (*apigen.Secret, error) {
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
	if meta.Fs.Name != "db-password" || meta.ID == 0 || len(meta.Versions) != 1 {
		t.Fatalf("secret = %+v, want a named secret with an id and one version", meta)
	}
	if meta.Versions[0].Author != user.ID {
		t.Fatalf("Author = %d, want the approving operator %d", meta.Versions[0].Author, user.ID)
	}

	// The response type has no value field at all, so the only way to confirm
	// something real was stored is to go and reveal it.
	revealed, err := h.PostV1SecretsReveal(apigen.Context{Ctx: context.Background(), User: user},
		&apigen.SecretRevealRequest{ID: meta.Versions[0].ID})
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
		&apigen.SecretRevealRequest{ID: meta.Versions[0].ID})
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
	generated, err := generateSecret(t, h, user, req)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	// Without this guard a caller could append a version over an operator's
	// credential and neither of them could read the result back.
	if _, err := generateSecret(t, h, user, req); !errors.Is(err, SecretAlreadyExistsErr) {
		t.Fatalf("second generate err = %v, want SecretAlreadyExistsErr", err)
	}

	// The same secret set explicitly still rotates, so the guard is on this
	// route and not on the store.
	if _, err := h.PostV1SecretsSet(apigen.Context{Ctx: context.Background(), User: user},
		&apigen.SecretSetRequest{SecretID: generated.ID, Value: []byte("manual")}); err != nil {
		t.Fatalf("PostV1SecretsSet over a generated secret: %v", err)
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
			if _, exists := h.Store.GetSecretInRootByName(1, strings.TrimSpace(tc.req.Name)); exists {
				t.Fatalf("a rejected request still stored %q", tc.req.Name)
			}
		})
	}
}

// The point of the whole route: what it mints never comes back over the wire,
// only its metadata. Driven through the real mux so the response body is the
// encoded one a caller actually receives, not the handler's return value.
// Whether a given caller may reach the route at all is an authz question and is
// covered in TestEnforcementDelegated.
func TestGenerateSecretNeverEchoesTheValue(t *testing.T) {
	h, user := newAuthTestHandler(t)
	server := httptest.NewServer(apigen.CreateApiServerMux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 1 << 20,
	}))
	t.Cleanup(server.Close)

	token := h.mustToken(t, user.ID, []string{ScopeDefault}, time.Hour)

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

	status, generated := post(t, "/v1/secrets/generate", token,
		`{"name": "db-password", "password": {"length": 32}}`)
	if status != http.StatusOK {
		t.Fatalf("generate: status %d, want 200", status)
	}
	id, _ := generated["id"].(float64)
	if id == 0 {
		t.Fatalf("generate returned no id: %#v", generated)
	}
	if _, ok := generated["value"]; ok {
		t.Fatalf("generate response carried a value: %#v", generated)
	}
	refs, _ := generated["versions"].([]any)
	if len(refs) != 1 {
		t.Fatalf("generate returned no versions: %#v", generated)
	}
	versionID, _ := refs[0].(map[string]any)["id"].(float64)
	if versionID == 0 {
		t.Fatalf("generate returned no version id: %#v", generated)
	}
	revealBody := fmt.Sprintf(`{"id": %d}`, int(versionID))

	// The value exists and is intact — it was simply never returned by generate.
	status, revealed := post(t, "/v1/secrets/reveal", token, revealBody)
	if status != http.StatusOK {
		t.Fatalf("reveal: status %d, want 200", status)
	}
	value, err := base64.StdEncoding.DecodeString(revealed["value"].(string))
	if err != nil {
		t.Fatalf("decoding revealed value: %v", err)
	}
	if len(value) != 32 {
		t.Fatalf("revealed %d bytes, want the 32 that were requested", len(value))
	}
}
