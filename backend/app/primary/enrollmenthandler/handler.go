package enrollmenthandler

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/util/version"
)

var _ apigen.EnrollmentV1Handler = (*Handler)(nil)

type enrollmentRequestIPKey struct{}

type enrollmentSession struct {
	id                  int32
	requestingMachineID string
	opendeployVersion   string
	csrPEM              []byte
	underlayAddress     string
	requestUpdatedAt    time.Time
	accepted            chan *apigen.EnrollmentAccepted
}

// Handler owns worker enrollment streams and the operator actions that accept
// them. It implements apigen.EnrollmentV1Handler.
type Handler struct {
	store          *sqlite.PrimaryStorage
	secrets        *secrets.Manager
	configService  *config.Service
	tlsFingerprint string
	networkMaps    networkMapProvider

	mu       sync.Mutex
	sessions map[int32]*enrollmentSession
}

type networkMapProvider interface {
	Refresh() error
	SnapshotForNode(nodeID int32) *apigen.ClusterNetMap
}

func New(store *sqlite.PrimaryStorage, secretsMgr *secrets.Manager, configService *config.Service, tlsFingerprint string, networkMaps networkMapProvider) *Handler {
	return &Handler{
		store:          store,
		secrets:        secretsMgr,
		configService:  configService,
		tlsFingerprint: tlsFingerprint,
		networkMaps:    networkMaps,
		sessions:       make(map[int32]*enrollmentSession),
	}
}

var EnrollmentMachineIDRequiredErr = apigen.NewApiErr("Requesting machine ID is required", "enrollment_machine_id_required", http.StatusBadRequest)
var EnrollmentCSRRequiredErr = apigen.NewApiErr("Worker certificate request is required", "enrollment_csr_required", http.StatusBadRequest)
var EnrollmentWorkerNameRequiredErr = apigen.NewApiErr("Worker name is required", "enrollment_worker_name_required", http.StatusBadRequest)
var EnrollmentNotConnectedErr = apigen.NewApiErr("Worker is not connected", "enrollment_not_connected", http.StatusConflict)
var EnrollmentSigningNotConfiguredErr = apigen.NewApiErr("Cluster CA signing key is not configured", "enrollment_signing_not_configured", http.StatusServiceUnavailable)
var EnrollmentNotFoundErr = apigen.NewApiErr("Enrollment request not found", "enrollment_not_found", http.StatusNotFound)
var EnrollmentFingerprintNotConfiguredErr = apigen.NewApiErr("Enrollment TLS fingerprint is not configured", "enrollment_fingerprint_not_configured", http.StatusServiceUnavailable)

func (h *Handler) VerifyEnrollmentRequest(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
	return apigen.Context{Ctx: context.WithValue(ctx, enrollmentRequestIPKey{}, remoteIP(r))}, nil
}

func (h *Handler) GetV1EnrollmentInfo(ctx apigen.Context) (*apigen.EnrollmentInfo, error) {
	fingerprint := strings.TrimSpace(h.tlsFingerprint)
	if fingerprint == "" {
		return nil, EnrollmentFingerprintNotConfiguredErr
	}
	return &apigen.EnrollmentInfo{EnrollmentTlsSpkiSha256: fingerprint}, nil
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
		if len(hello.WorkerCertificateRequest) == 0 {
			yield(nil, EnrollmentCSRRequiredErr)
			return
		}
		opendeployVersion := strings.TrimSpace(hello.OpendeployVersion)
		underlayAddress, err := h.store.NormalizeNodeUnderlay(requestingMachineID, hello.UnderlayAddress)
		if err != nil {
			yield(nil, err)
			return
		}

		status := h.store.MustUpsertEnrollmentRequest(enrollmentRequestIP(ctx), requestingMachineID, opendeployVersion, underlayAddress)
		sess := &enrollmentSession{
			id:                  status.ID,
			requestingMachineID: requestingMachineID,
			opendeployVersion:   opendeployVersion,
			csrPEM:              hello.WorkerCertificateRequest,
			underlayAddress:     underlayAddress,
			requestUpdatedAt:    status.UpdatedAt,
			accepted:            make(chan *apigen.EnrollmentAccepted, 1),
		}
		h.registerEnrollmentSession(sess)
		defer func() {
			if h.unregisterEnrollmentSession(sess) {
				h.store.MustMarkEnrollmentDisconnected(status.ID, requestingMachineID)
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

func (h *Handler) PostV1EnrollmentList(ctx apigen.Context) (*apigen.EnrollmentRequestList, error) {
	items, err := h.store.ListEnrollmentRequests()
	if err != nil {
		return nil, err
	}
	return &apigen.EnrollmentRequestList{Items: items}, nil
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
	if _, err := h.store.NormalizeNodeUnderlay(sess.requestingMachineID, sess.underlayAddress); err != nil {
		return nil, err
	}
	caCert, workerCert, err := certu.SignWorkerCertificateRequest(h.secrets, sess.csrPEM, sess.requestingMachineID)
	if errors.Is(err, secrets.ErrLocked) || errors.Is(err, secrets.ErrNotFound) {
		return nil, EnrollmentSigningNotConfiguredErr
	}
	if err != nil {
		return nil, fmt.Errorf("signing worker CSR: %w", err)
	}
	status, err := h.store.AcceptEnrollmentRequest(req.ID, workerName, sess.requestingMachineID, sess.underlayAddress, sess.requestUpdatedAt)
	if errors.Is(err, sqlite.ErrEnrollmentRequestChanged) {
		return nil, EnrollmentNotConnectedErr
	}
	if err != nil {
		return nil, err
	}
	nodeID, err := h.store.NodeIDByIdentifier(sess.requestingMachineID)
	if err != nil {
		return nil, fmt.Errorf("resolve enrolled worker %q: %w", sess.requestingMachineID, err)
	}
	h.store.EnsureSystemDeployment(nodeID, version.Version)
	h.store.EnsureNetproxyDeployment(nodeID, version.Version)
	predicate := storage.DeploymentPredicate(func(cfg apigen.DeploymentConfig) bool {
		return cfg.NodeID == nodeID
	})
	nodeDeployment, nodeNetDeployment := enrollmentBootstrapDeployments(h.store.FetchDeploymentSnapshot(predicate))
	if nodeDeployment == nil || nodeNetDeployment == nil {
		return nil, fmt.Errorf("enrollment bootstrap deployments missing for worker %q", sess.requestingMachineID)
	}
	var netMap *apigen.ClusterNetMap
	if h.networkMaps != nil {
		if err := h.networkMaps.Refresh(); err != nil {
			slog.Error("refreshing enrollment network map failed", "node_id", nodeID, "err", err)
		} else {
			netMap = h.networkMaps.SnapshotForNode(nodeID)
		}
	}
	accepted := &apigen.EnrollmentAccepted{
		ID:                req.ID,
		WorkerName:        workerName,
		CaCertificate:     caCert,
		WorkerCertificate: workerCert,
		ClusterNetwork:    &apigen.ClusterNetworkInfo{UlaPrefix: h.configService.NetworkPrefix().Bytes()},
		NodeDeployment:    nodeDeployment,
		NodeNetDeployment: nodeNetDeployment,
		ClusterNetMap:     netMap,
	}
	select {
	case sess.accepted <- accepted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return status, nil
}

func enrollmentBootstrapDeployments(snapshot []apigen.DeploymentWithStatus) (*apigen.DeploymentWithStatus, *apigen.DeploymentWithStatus) {
	var nodeDeployment *apigen.DeploymentWithStatus
	var nodeNetDeployment *apigen.DeploymentWithStatus
	for i := range snapshot {
		item := &snapshot[i]
		if sqlite.IsSystemDeploymentConfig(&item.Config) {
			nodeDeployment = item
		}
		if sqlite.IsNetproxyDeploymentConfig(&item.Config) {
			nodeNetDeployment = item
		}
	}
	return nodeDeployment, nodeNetDeployment
}

func (h *Handler) registerEnrollmentSession(sess *enrollmentSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sess.id] = sess
}

func (h *Handler) unregisterEnrollmentSession(sess *enrollmentSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[sess.id] == sess {
		delete(h.sessions, sess.id)
		return true
	}
	return false
}

func (h *Handler) enrollmentSession(id int32) *enrollmentSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
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
