package webuihandler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func bg() apigen.Context { return apigen.Context{Ctx: context.Background()} }

func (h *Handler) operatorCtx(t *testing.T, user *apigen.InternalUser, scopes ...string) apigen.Context {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{"default"}
	}
	return apigen.Context{
		Ctx:   context.Background(),
		User:  user,
		Token: h.mustToken(t, user.ID, scopes, 48*time.Hour),
	}
}

func (h *Handler) mustRequestStart(t *testing.T, userID int32) *apigen.AgentSessionRequest {
	t.Helper()
	req, err := h.PostV1AgentSessionsRequestStart(bg(), &apigen.AgentSessionRequestStartRequest{UserID: userID})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsRequestStart: %v", err)
	}
	return req
}

// The whole point of the flow: an agent with no credential ends up holding a
// working one, but only after a real user approved it.
func TestAgentSessionRequestApprovePickup(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)

	if req.Status != apigen.AgentSessionStatus_AGENT_SESSION_PENDING {
		t.Fatalf("status = %v, want PENDING", req.Status)
	}
	if req.ApprovalCode == "" || !strings.Contains(req.ApprovalCode, "-") {
		t.Fatalf("approval code = %q, want a grouped code", req.ApprovalCode)
	}

	before, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsGetSession: %v", err)
	}
	if before.Status != apigen.AgentSessionStatus_AGENT_SESSION_PENDING || before.Token != "" {
		t.Fatalf("pickup before approval = %#v, want PENDING with no token", before)
	}

	if _, err := h.PostV1AgentSessionsApprove(h.operatorCtx(t, user), &apigen.AgentSessionApproveRequest{ID: req.ID}); err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}

	got, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsGetSession after approval: %v", err)
	}
	if got.Status != apigen.AgentSessionStatus_AGENT_SESSION_APPROVED || got.Token == "" {
		t.Fatalf("pickup = %#v, want APPROVED with a token", got)
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+got.Token)
	policy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"default"}}
	if _, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy); err != nil {
		t.Fatalf("collected token failed VerifyAuth: %v", err)
	}
}

// The token is delivered once. A second pickup must not hand out another one,
// or a leaked request id would keep minting credentials.
func TestAgentSessionTokenIsDeliveredOnce(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)
	if _, err := h.PostV1AgentSessionsApprove(h.operatorCtx(t, user), &apigen.AgentSessionApproveRequest{ID: req.ID}); err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}

	first, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
	if err != nil || first.Token == "" {
		t.Fatalf("first pickup = %#v, err = %v", first, err)
	}
	second, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
	if err != nil {
		t.Fatalf("second pickup: %v", err)
	}
	if second.Token != "" {
		t.Fatal("second pickup returned a token")
	}
	if second.Status != apigen.AgentSessionStatus_AGENT_SESSION_APPROVED {
		t.Fatalf("second pickup status = %v, want APPROVED", second.Status)
	}
}

// Two agents polling at once must not both walk away with a working token.
func TestConcurrentPickupMintsExactlyOneToken(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)
	if _, err := h.PostV1AgentSessionsApprove(h.operatorCtx(t, user), &apigen.AgentSessionApproveRequest{ID: req.ID}); err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}

	const callers = 8
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
			if err == nil {
				tokens[i] = res.Token
			}
		}()
	}
	wg.Wait()

	issued := 0
	for _, token := range tokens {
		if token != "" {
			issued++
		}
	}
	if issued != 1 {
		t.Fatalf("issued %d tokens, want exactly 1", issued)
	}
}

// Only one request may be open per user, so an operator is never asked to
// choose between two identical-looking approvals.
func TestSecondRequestWhileOnePendingIsRejected(t *testing.T) {
	h, user := newAuthTestHandler(t)
	h.mustRequestStart(t, user.ID)

	_, err := h.PostV1AgentSessionsRequestStart(bg(), &apigen.AgentSessionRequestStartRequest{UserID: user.ID})
	if err == nil {
		t.Fatal("expected a second request to be rejected")
	}
	var apiErr apigen.ApiErr
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusConflict {
		t.Fatalf("err = %v, want a 409", err)
	}
}

// A stale request must not hold the slot: request-start is unauthenticated, so
// otherwise anyone could block an operator's own agent indefinitely.
func TestStaleRequestIsSupersededRatherThanBlocking(t *testing.T) {
	h, user := newAuthTestHandler(t)
	stale := h.mustRequestStart(t, user.ID)
	shrinkTTL(t, &agentSessionPendingTTL)

	fresh, err := h.PostV1AgentSessionsRequestStart(bg(), &apigen.AgentSessionRequestStartRequest{UserID: user.ID})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsRequestStart after a stale request: %v", err)
	}
	if fresh.ID == stale.ID {
		t.Fatal("expected a new request id")
	}
	rec, err := h.Store.FetchAgentSession(stale.ID)
	if err != nil {
		t.Fatalf("FetchAgentSession: %v", err)
	}
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_REJECTED {
		t.Fatalf("stale request status = %v, want REJECTED", rec.Status)
	}
}

// An approval nobody collects must expire rather than sitting open forever.
func TestApprovedSessionExpiresUncollected(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)
	if _, err := h.PostV1AgentSessionsApprove(h.operatorCtx(t, user), &apigen.AgentSessionApproveRequest{ID: req.ID}); err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}
	shrinkTTL(t, &agentSessionPickupTTL)

	got, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsGetSession: %v", err)
	}
	if got.Status != apigen.AgentSessionStatus_AGENT_SESSION_REJECTED || got.Token != "" {
		t.Fatalf("pickup = %#v, want REJECTED with no token", got)
	}
}

// Approval is the step that turns an anonymous request into access, so it must
// be the request's own user doing it.
func TestApproveIsScopedToTheRequestedUser(t *testing.T) {
	h, user := newAuthTestHandler(t)
	other := &apigen.InternalUser{ID: 2, WebAuthNID: user.WebAuthNID, Name: "other"}
	h.Store.WriteUser(other)
	req := h.mustRequestStart(t, user.ID)

	if _, err := h.PostV1AgentSessionsApprove(h.operatorCtx(t, other), &apigen.AgentSessionApproveRequest{ID: req.ID}); err == nil {
		t.Fatal("expected another user's approval to fail")
	}
	rec, err := h.Store.FetchAgentSession(req.ID)
	if err != nil {
		t.Fatalf("FetchAgentSession: %v", err)
	}
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_PENDING {
		t.Fatalf("status = %v, want the request still PENDING", rec.Status)
	}
}

// Approving twice must not re-issue or re-scope a session.
func TestApproveTwiceIsRejected(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)
	ctx := h.operatorCtx(t, user)
	if _, err := h.PostV1AgentSessionsApprove(ctx, &apigen.AgentSessionApproveRequest{ID: req.ID}); err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}
	if _, err := h.PostV1AgentSessionsApprove(ctx, &apigen.AgentSessionApproveRequest{ID: req.ID}); err == nil {
		t.Fatal("expected a second approval to fail")
	}
}

// An approved session carries the approver's scopes and no more. Nothing is
// withheld at this layer any more: what an agent token may do is decided by the
// authz rules that carry delegation_allowed.
func TestApprovedSessionCarriesApproverScopes(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)
	session, err := h.PostV1AgentSessionsApprove(
		h.operatorCtx(t, user, ScopeDefault),
		&apigen.AgentSessionApproveRequest{ID: req.ID},
	)
	if err != nil {
		t.Fatalf("PostV1AgentSessionsApprove: %v", err)
	}
	if !reflect.DeepEqual(session.Scopes, []string{ScopeDefault}) {
		t.Fatalf("scopes = %v, want %v", session.Scopes, []string{ScopeDefault})
	}
}

// Rejecting a pending request must close it, not revoke a session that never
// existed.
func TestRevokeOnAPendingRequestRejectsIt(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)

	if err := h.PostV1AgentSessionsRevoke(h.operatorCtx(t, user), &apigen.AgentSessionRevokeRequest{ID: req.ID}); err != nil {
		t.Fatalf("PostV1AgentSessionsRevoke: %v", err)
	}
	rec, err := h.Store.FetchAgentSession(req.ID)
	if err != nil {
		t.Fatalf("FetchAgentSession: %v", err)
	}
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_REJECTED {
		t.Fatalf("status = %v, want REJECTED", rec.Status)
	}
	got, err := h.PostV1AgentSessionsGetSession(bg(), &apigen.AgentSessionGetRequest{ID: req.ID})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsGetSession: %v", err)
	}
	if got.Token != "" {
		t.Fatal("a rejected request handed out a token")
	}
}

// A pending row exists in the table but backs no credential, so nothing that
// carries its id as a jti may authenticate.
func TestPendingSessionIDDoesNotAuthenticate(t *testing.T) {
	h, user := newAuthTestHandler(t)
	req := h.mustRequestStart(t, user.ID)

	token, err := h.signAgentToken(user.ID, req.ID, []string{"default"}, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("signAgentToken: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	policy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"default"}}
	if _, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy); err == nil {
		t.Fatal("a token minted against a pending request authenticated")
	}
}

// The instructions are the one unauthenticated page an operator hands out, so
// they must render for a real user and refuse a made-up one.
func TestAgentInstructionsRender(t *testing.T) {
	h, user := newAuthTestHandler(t)
	mux := apigen.CreateApiServerMux(h, &apigen.MuxConfig{VerifyAuth: h.VerifyAuth})

	url := fmt.Sprintf("/v1/agent-sessions/instructions?user_id=%d", user.ID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "/v1/agent-sessions/request-start") {
		t.Fatal("instructions omit the request-start endpoint")
	}
	if !strings.Contains(body, fmt.Sprintf(`"user_id": %d`, user.ID)) {
		t.Fatalf("instructions did not carry the user id: %s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content type = %q, want markdown", ct)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/agent-sessions/instructions?user_id=999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown user status = %d, want 404", w.Code)
	}
}

// A browser following the link should get something readable rather than a
// download prompt.
func TestAgentInstructionsServeHTMLToBrowsers(t *testing.T) {
	h, _ := newAuthTestHandler(t)
	mux := apigen.CreateApiServerMux(h, &apigen.MuxConfig{VerifyAuth: h.VerifyAuth})

	r := httptest.NewRequest(http.MethodGet, "/v1/agent-sessions/instructions?user_id=1", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q, want html", ct)
	}
	if !strings.Contains(w.Body.String(), "<pre>") {
		t.Fatal("html wrapper missing")
	}
}

// Approval codes exist to be read aloud, so they must avoid the glyphs that get
// misread between the agent's output and the operator's screen.
func TestApprovalCodesAvoidAmbiguousCharacters(t *testing.T) {
	for range 200 {
		code, err := generateApprovalCode()
		if err != nil {
			t.Fatalf("generateApprovalCode: %v", err)
		}
		if len(code) != 8 || code[3] != '-' {
			t.Fatalf("code = %q, want XXX-XXXX", code)
		}
		if strings.ContainsAny(code, "ILOU") {
			t.Fatalf("code %q contains an ambiguous character", code)
		}
	}
}

// shrinkTTL makes an already-created session count as expired, which is how the
// TTL branches get exercised without sleeping or rewriting stored timestamps.
func shrinkTTL(t *testing.T, ttl *time.Duration) {
	t.Helper()
	original := *ttl
	*ttl = time.Nanosecond
	t.Cleanup(func() { *ttl = original })
}
