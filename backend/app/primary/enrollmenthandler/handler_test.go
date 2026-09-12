package enrollmenthandler

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/enrollment"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const testWGKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="

func newTestHandler(t *testing.T) (*Handler, *state.Service) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { store.Close() })
	return New(store, nil, nil, "", nil), store
}

type testIdentity struct {
	identifier string
	csrPEM     []byte
}

func newTestIdentity(t *testing.T) testIdentity {
	t.Helper()
	keyPEM, err := certu.GenerateSecondaryKey()
	if err != nil {
		t.Fatal(err)
	}
	identifier, csrPEM, err := certu.SecondaryIdentity(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return testIdentity{identifier: identifier, csrPEM: csrPEM}
}

func helloFor(id testIdentity) *apigen.EnrollmentHello {
	return &apigen.EnrollmentHello{
		Reported:                    &apigen.NodeReported{Identifier: id.identifier, UnderlayAddress: "192.0.2.2", WgPublicKey: testWGKey},
		SecondaryCertificateRequest: id.csrPEM,
		OpendeployVersion:           "v1",
	}
}

func helloStream(ctx context.Context, hello *apigen.EnrollmentHello) iter.Seq2[*apigen.EnrollmentSecondaryMsg, error] {
	return func(yield func(*apigen.EnrollmentSecondaryMsg, error) bool) {
		if !yield(&apigen.EnrollmentSecondaryMsg{Hello: hello}, nil) {
			return
		}
		<-ctx.Done()
	}
}

type helloReply struct {
	status *apigen.EnrollmentRequestStatus
	err    error
}

type helloRun struct {
	first  chan helloReply
	done   chan error
	cancel context.CancelFunc
}

func runHello(t *testing.T, h *Handler, hello *apigen.EnrollmentHello) *helloRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &helloRun{first: make(chan helloReply, 1), done: make(chan error, 1), cancel: cancel}
	go func() {
		var final error
		sent := false
		for msg, err := range h.PostV1EnrollmentRequest(apigen.Context{Ctx: ctx}, helloStream(ctx, hello)) {
			if !sent {
				sent = true
				reply := helloReply{err: err}
				if msg != nil {
					reply.status = msg.RequestStatus
				}
				run.first <- reply
			}
			if err != nil {
				final = err
			}
		}
		if !sent {
			run.first <- helloReply{}
		}
		run.done <- final
	}()
	return run
}

func (r *helloRun) firstReply(t *testing.T) helloReply {
	t.Helper()
	select {
	case reply := <-r.first:
		return reply
	case <-time.After(5 * time.Second):
		t.Fatal("no reply from enrollment stream")
		return helloReply{}
	}
}

func (r *helloRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("enrollment stream did not end")
		return nil
	}
}

func (r *helloRun) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	return r.wait(t)
}

func mustPending(t *testing.T, run *helloRun) *apigen.EnrollmentRequestStatus {
	t.Helper()
	reply := run.firstReply(t)
	if reply.err != nil || reply.status == nil {
		t.Fatalf("hello reply = %+v, want a pending request", reply)
	}
	return reply.status
}

func TestHelloRejectsEnrolledIdentifier(t *testing.T) {
	h, store := newTestHandler(t)
	ctx := context.Background()
	id := newTestIdentity(t)
	member := nodes.EnsurePrimaryNode(store, "primary", id.identifier)
	before, err := store.Queries().GetNodeRowByIdentifier(ctx, member.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	run := runHello(t, h, helloFor(id))
	if reply := run.firstReply(t); !errors.Is(reply.err, enrollment.IdentifierEnrolledErr) {
		t.Fatalf("reply = %+v, want IdentifierEnrolledErr", reply)
	}
	if err := run.stop(t); !errors.Is(err, enrollment.IdentifierEnrolledErr) {
		t.Fatalf("stream ended with %v", err)
	}
	after, err := store.Queries().GetNodeRowByIdentifier(ctx, member.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if after.Event.Version != before.Event.Version || after.Status.IsConnected {
		t.Fatalf("rejected hello changed the member row: %+v", after)
	}
	if h.enrollmentSession(after.Event.NodeID) != nil {
		t.Fatal("rejected hello registered a session")
	}
}

func TestHelloRejectsCSRNotBoundToIdentifier(t *testing.T) {
	h, store := newTestHandler(t)
	id := newTestIdentity(t)
	foreignCSR, _, err := certu.GenerateSecondaryCertificateRequest(id.identifier)
	if err != nil {
		t.Fatal(err)
	}
	legacyCSR, _, err := certu.GenerateSecondaryCertificateRequest("8c3f0d7e-4d2c-4c1b-9a1e-0c9f6a2b7d10")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]*apigen.EnrollmentHello{
		"foreign key":       helloFor(testIdentity{identifier: id.identifier, csrPEM: foreignCSR}),
		"uuid identifier":   helloFor(testIdentity{identifier: "8c3f0d7e-4d2c-4c1b-9a1e-0c9f6a2b7d10", csrPEM: legacyCSR}),
		"other identifier":  helloFor(testIdentity{identifier: newTestIdentity(t).identifier, csrPEM: id.csrPEM}),
		"malformed request": helloFor(testIdentity{identifier: id.identifier, csrPEM: []byte("not a csr")}),
	}
	for name, hello := range cases {
		run := runHello(t, h, hello)
		if reply := run.firstReply(t); !errors.Is(reply.err, EnrollmentInvalidCSRErr) {
			t.Fatalf("%s: reply = %+v, want EnrollmentInvalidCSRErr", name, reply)
		}
		run.stop(t)
	}
	if n := len(nodes.ListNodes(store.Queries())); n != 0 {
		t.Fatalf("invalid CSRs created %d node rows", n)
	}
}

func TestHelloWithDifferentKeyIsASeparateRequest(t *testing.T) {
	h, _ := newTestHandler(t)
	a := runHello(t, h, helloFor(newTestIdentity(t)))
	first := mustPending(t, a)
	b := runHello(t, h, helloFor(newTestIdentity(t)))
	second := mustPending(t, b)
	if first.ID == second.ID || first.RequestingMachineID == second.RequestingMachineID {
		t.Fatalf("different keys shared a request: %+v vs %+v", first, second)
	}
	if h.enrollmentSession(first.ID) == nil || h.enrollmentSession(second.ID) == nil {
		t.Fatal("both requests should have live sessions")
	}
	if err := a.stop(t); err != nil {
		t.Fatalf("first stream ended with %v", err)
	}
	if err := b.stop(t); err != nil {
		t.Fatalf("second stream ended with %v", err)
	}
}

func TestHelloWithSameKeySupersedesLiveSession(t *testing.T) {
	h, store := newTestHandler(t)
	ctx := context.Background()
	id := newTestIdentity(t)
	a := runHello(t, h, helloFor(id))
	first := mustPending(t, a)
	sessA := h.enrollmentSession(first.ID)

	b := runHello(t, h, helloFor(id))
	second := mustPending(t, b)
	if second.ID != first.ID {
		t.Fatalf("same key reconnect got request %d, want %d", second.ID, first.ID)
	}
	if err := a.wait(t); err != nil {
		t.Fatalf("superseded stream ended with %v", err)
	}
	sessB := h.enrollmentSession(first.ID)
	if sessB == nil || sessB == sessA {
		t.Fatal("same-key hello did not replace the live session")
	}
	row, err := store.Queries().GetNodeRowByID(ctx, int64(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !row.Status.IsConnected {
		t.Fatal("superseded stream cleanup marked the live session disconnected")
	}

	if err := b.stop(t); err != nil {
		t.Fatalf("second stream ended with %v", err)
	}
	row, err = store.Queries().GetNodeRowByID(ctx, int64(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	if row.Status.IsConnected || h.enrollmentSession(first.ID) != nil {
		t.Fatal("closing the live stream did not clean up the session")
	}
}

func TestRemoteIPUsesPeerAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/enrollment/request", nil)
	r.RemoteAddr = "198.51.100.7:4242"
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := remoteIP(r); got != "198.51.100.7" {
		t.Fatalf("remoteIP = %q, want the peer address", got)
	}
}
