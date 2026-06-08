package handler

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type enrollmentRequestIPKey struct{}

type enrollmentSession struct {
	id                  int32
	requestingMachineID string
	accepted            chan *apigen.EnrollmentAccepted
}

var EnrollmentMachineIDRequiredErr = apigen.NewApiErr("Requesting machine ID is required", "enrollment_machine_id_required", http.StatusBadRequest)
var EnrollmentWorkerNameRequiredErr = apigen.NewApiErr("Worker name is required", "enrollment_worker_name_required", http.StatusBadRequest)
var EnrollmentNotConnectedErr = apigen.NewApiErr("Worker is not connected", "enrollment_not_connected", http.StatusConflict)
var EnrollmentSigningNotConfiguredErr = apigen.NewApiErr("Cluster CA signing key is not configured", "enrollment_signing_not_configured", http.StatusServiceUnavailable)
var EnrollmentNotFoundErr = apigen.NewApiErr("Enrollment request not found", "enrollment_not_found", http.StatusNotFound)

func (h *Handler) VerifyEnrollmentRequest(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
	return apigen.Context{Ctx: context.WithValue(ctx, enrollmentRequestIPKey{}, remoteIP(r))}, nil
}

func (h *Handler) PostV1EnrollmentRequest(ctx apigen.Context, reqs iter.Seq2[*apigen.EnrollmentWorkerMsg, error]) iter.Seq2[*apigen.EnrollmentPrimaryMsg, error] {
	return func(yield func(*apigen.EnrollmentPrimaryMsg, error) bool) {
		hello, err := readEnrollmentHello(reqs)
		if err != nil {
			yield(nil, err)
			return
		}
		requestingMachineID := strings.TrimSpace(hello.RequestingMachineID)
		if requestingMachineID == "" {
			yield(nil, EnrollmentMachineIDRequiredErr)
			return
		}

		status := h.Store.MustUpsertEnrollmentRequest(enrollmentRequestIP(ctx), requestingMachineID)
		sess := &enrollmentSession{
			id:                  status.ID,
			requestingMachineID: requestingMachineID,
			accepted:            make(chan *apigen.EnrollmentAccepted, 1),
		}
		h.registerEnrollmentSession(sess)
		defer func() {
			if h.unregisterEnrollmentSession(sess) {
				h.Store.MustMarkEnrollmentDisconnected(status.ID, requestingMachineID)
			}
		}()

		if !yield(&apigen.EnrollmentPrimaryMsg{RequestStatus: status}, nil) {
			return
		}

		disconnected := drainEnrollmentStream(reqs)
		select {
		case accepted := <-sess.accepted:
			yield(&apigen.EnrollmentPrimaryMsg{Accepted: accepted}, nil)
		case err := <-disconnected:
			if err != nil {
				yield(nil, err)
			}
		case <-ctx.Done():
		}
	}
}

func (h *Handler) PostV1EnrollmentAccept(ctx apigen.Context, req *apigen.EnrollmentAcceptRequest) (*apigen.EnrollmentRequestStatus, error) {
	if req == nil || req.ID == 0 {
		return nil, EnrollmentNotFoundErr
	}
	workerName := strings.TrimSpace(req.WorkerName)
	if workerName == "" {
		return nil, EnrollmentWorkerNameRequiredErr
	}
	sess := h.enrollmentSession(req.ID)
	if sess == nil {
		return nil, EnrollmentNotConnectedErr
	}
	caCert, workerCert, workerKey, err := cluster.GenerateWorkerCertificate(h.Secrets, workerName)
	if errors.Is(err, secrets.ErrLocked) || errors.Is(err, secrets.ErrNotFound) {
		return nil, EnrollmentSigningNotConfiguredErr
	}
	if err != nil {
		return nil, fmt.Errorf("signing worker cert: %w", err)
	}
	status, err := h.Store.AcceptEnrollmentRequest(req.ID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return nil, EnrollmentNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	accepted := &apigen.EnrollmentAccepted{
		ID:                req.ID,
		WorkerName:        workerName,
		CaCertificate:     caCert,
		WorkerCertificate: workerCert,
		WorkerPrivateKey:  workerKey,
	}
	select {
	case sess.accepted <- accepted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return status, nil
}

func (h *Handler) registerEnrollmentSession(sess *enrollmentSession) {
	h.enrollmentMu.Lock()
	defer h.enrollmentMu.Unlock()
	h.enrollmentSessions[sess.id] = sess
}

func (h *Handler) unregisterEnrollmentSession(sess *enrollmentSession) bool {
	h.enrollmentMu.Lock()
	defer h.enrollmentMu.Unlock()
	if h.enrollmentSessions[sess.id] == sess {
		delete(h.enrollmentSessions, sess.id)
		return true
	}
	return false
}

func (h *Handler) enrollmentSession(id int32) *enrollmentSession {
	h.enrollmentMu.Lock()
	defer h.enrollmentMu.Unlock()
	return h.enrollmentSessions[id]
}

func readEnrollmentHello(reqs iter.Seq2[*apigen.EnrollmentWorkerMsg, error]) (*apigen.EnrollmentHello, error) {
	for msg, err := range reqs {
		if err != nil {
			return nil, err
		}
		if msg != nil && msg.Hello != nil {
			return msg.Hello, nil
		}
		return nil, EnrollmentMachineIDRequiredErr
	}
	return nil, EnrollmentMachineIDRequiredErr
}

func drainEnrollmentStream(reqs iter.Seq2[*apigen.EnrollmentWorkerMsg, error]) <-chan error {
	done := make(chan error, 1)
	go func() {
		for _, err := range reqs {
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return done
}

func enrollmentRequestIP(ctx apigen.Context) string {
	ip, _ := ctx.Value(enrollmentRequestIPKey{}).(string)
	return ip
}

func remoteIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
