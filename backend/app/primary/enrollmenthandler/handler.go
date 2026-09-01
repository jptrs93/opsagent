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

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/lib/wgkey"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
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
	wgPublicKey         string
	expectedVersion     int64
	accepted            chan *apigen.EnrollmentAccepted
}

// Handler owns secondary enrollment streams and the operator actions that accept
// them. It implements apigen.EnrollmentV1Handler.
type Handler struct {
	store          *state.Service
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

func New(store *state.Service, secretsMgr *secrets.Manager, configService *config.Service, tlsFingerprint string, networkMaps networkMapProvider) *Handler {
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
var EnrollmentCSRRequiredErr = apigen.NewApiErr("Secondary certificate request is required", "enrollment_csr_required", http.StatusBadRequest)
var EnrollmentNodeNameRequiredErr = apigen.NewApiErr("Node name is required", "enrollment_node_name_required", http.StatusBadRequest)
var EnrollmentNotConnectedErr = apigen.NewApiErr("Secondary is not connected", "enrollment_not_connected", http.StatusConflict)
var EnrollmentSigningNotConfiguredErr = apigen.NewApiErr("Cluster CA signing key is not configured", "enrollment_signing_not_configured", http.StatusServiceUnavailable)
var EnrollmentNotFoundErr = apigen.NewApiErr("Enrollment request not found", "enrollment_not_found", http.StatusNotFound)
var EnrollmentFingerprintNotConfiguredErr = apigen.NewApiErr("Enrollment TLS fingerprint is not configured", "enrollment_fingerprint_not_configured", http.StatusServiceUnavailable)
var EnrollmentInvalidWGKeyErr = apigen.NewApiErr("Invalid WireGuard public key", "enrollment_invalid_wg_key", http.StatusBadRequest)

func (h *Handler) VerifyEnrollmentRequest(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
	ctx = logu.AddTag(ctx, "Enrollment")
	return apigen.Context{Ctx: context.WithValue(ctx, enrollmentRequestIPKey{}, remoteIP(r))}, nil
}

func (h *Handler) GetV1NodesEnrollmentsInfo(ctx apigen.Context) (*apigen.NodeEnrollmentInfo, error) {
	fingerprint := strings.TrimSpace(h.tlsFingerprint)
	if fingerprint == "" {
		return nil, EnrollmentFingerprintNotConfiguredErr
	}
	return &apigen.NodeEnrollmentInfo{EnrollmentTlsSpkiSha256: fingerprint}, nil
}

func (h *Handler) PostV1EnrollmentRequest(ctx apigen.Context, reqs iter.Seq2[*apigen.EnrollmentSecondaryMsg, error]) iter.Seq2[*apigen.EnrollmentPrimaryMsg, error] {
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
		if len(hello.SecondaryCertificateRequest) == 0 {
			yield(nil, EnrollmentCSRRequiredErr)
			return
		}
		opendeployVersion := strings.TrimSpace(hello.OpendeployVersion)
		underlayAddress, err := h.store.NormalizeNodeUnderlay(requestingMachineID, hello.UnderlayAddress)
		if err != nil {
			yield(nil, err)
			return
		}
		wgPublicKey, err := wgkey.ValidatePublic(hello.WgPublicKey)
		if err != nil {
			yield(nil, EnrollmentInvalidWGKeyErr)
			return
		}

		status, expectedVersion := h.store.MustUpsertEnrollmentRequest(enrollmentRequestIP(ctx), requestingMachineID, opendeployVersion, underlayAddress, wgPublicKey)
		sess := &enrollmentSession{
			id:                  status.ID,
			requestingMachineID: requestingMachineID,
			opendeployVersion:   opendeployVersion,
			csrPEM:              hello.SecondaryCertificateRequest,
			underlayAddress:     underlayAddress,
			wgPublicKey:         wgPublicKey,
			expectedVersion:     expectedVersion,
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

func (h *Handler) PostV1NodesEnrollmentsList(ctx apigen.Context) (*apigen.EnrollmentRequestList, error) {
	items, err := h.store.ListEnrollmentRequests()
	if err != nil {
		return nil, err
	}
	return &apigen.EnrollmentRequestList{Items: items}, nil
}

func (h *Handler) PostV1NodesEnrollmentsAccept(ctx apigen.Context, req *apigen.EnrollmentAcceptRequest) (*apigen.EnrollmentRequestStatus, error) {
	if req == nil || req.ID == 0 {
		return nil, EnrollmentNotFoundErr
	}
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		return nil, EnrollmentNodeNameRequiredErr
	}
	sess := h.enrollmentSession(req.ID)
	if sess == nil {
		return nil, EnrollmentNotConnectedErr
	}
	if _, err := h.store.NormalizeNodeUnderlay(sess.requestingMachineID, sess.underlayAddress); err != nil {
		return nil, err
	}
	caCert, secondaryCert, err := certu.SignSecondaryCertificateRequest(h.secrets, sess.csrPEM, sess.requestingMachineID)
	if errors.Is(err, secrets.ErrLocked) || errors.Is(err, secrets.ErrNotFound) {
		return nil, EnrollmentSigningNotConfiguredErr
	}
	if err != nil {
		return nil, fmt.Errorf("signing secondary CSR: %w", err)
	}
	status, err := h.store.AcceptEnrollmentRequest(req.ID, nodeName, sess.requestingMachineID, sess.underlayAddress, sess.wgPublicKey, sess.expectedVersion)
	if errors.Is(err, state.ErrEnrollmentRequestChanged) {
		return nil, EnrollmentNotConnectedErr
	}
	if err != nil {
		return nil, err
	}
	nodeID, err := h.store.NodeIDByIdentifier(sess.requestingMachineID)
	if err != nil {
		return nil, fmt.Errorf("resolve enrolled secondary %q: %w", sess.requestingMachineID, err)
	}
	h.store.EnsureSystemDeployment(nodeID, version.Version)
	h.store.EnsureNetproxyDeployment(nodeID, version.Version)
	nodeDeployment, nodeNetDeployment := h.ensureEnrollmentBootstrapInstances(nodeID)
	if nodeDeployment == nil || nodeNetDeployment == nil {
		return nil, fmt.Errorf("enrollment bootstrap deployments missing for secondary %q", sess.requestingMachineID)
	}
	var netMap *apigen.ClusterNetMap
	if h.networkMaps != nil {
		if err := h.networkMaps.Refresh(); err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("refreshing enrollment network map for node %d failed", nodeID), "err", err)
		} else {
			netMap = h.networkMaps.SnapshotForNode(nodeID)
		}
	}
	accepted := &apigen.EnrollmentAccepted{
		ID:                   req.ID,
		NodeName:             nodeName,
		CaCertificate:        caCert,
		SecondaryCertificate: secondaryCert,
		ClusterNetwork:       &apigen.ClusterNetworkInfo{UlaPrefix: h.configService.NetworkPrefix().Bytes()},
		NodeDeployment:       nodeDeployment,
		NodeNetDeployment:    nodeNetDeployment,
		ClusterNetMap:        netMap,
	}
	select {
	case sess.accepted <- accepted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return status, nil
}

func (h *Handler) ensureEnrollmentBootstrapInstances(nodeID int32) (*apigen.ScheduledInstanceState, *apigen.ScheduledInstanceState) {
	predicate := storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	})
	for _, cfg := range h.store.FetchDeploymentSnapshot(func(c apigen.Deployment) bool { return c.Def.NodeID == nodeID }) {
		if !internaldeploy.IsSelfConfig(&cfg) && !internaldeploy.IsNetproxyConfig(&cfg) {
			continue
		}
		// A node being enrolled has no placements yet, so its system deployments
		// start out serving rather than warming up behind something.
		h.store.EnsureRunScheduledInstance(cfg.ID, cfg.Version, cfg.Def.NodeID, 0,
			apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	}
	return enrollmentBootstrapInstances(h.store.FetchScheduledSnapshot(predicate))
}

func enrollmentBootstrapInstances(snapshot []apigen.ScheduledInstanceState) (*apigen.ScheduledInstanceState, *apigen.ScheduledInstanceState) {
	var nodeDeployment *apigen.ScheduledInstanceState
	var nodeNetDeployment *apigen.ScheduledInstanceState
	for i := range snapshot {
		item := &snapshot[i]
		if internaldeploy.IsSelfConfig(&item.Config) && (nodeDeployment == nil || item.Instance.ID > nodeDeployment.Instance.ID) {
			nodeDeployment = item
		}
		if internaldeploy.IsNetproxyConfig(&item.Config) && (nodeNetDeployment == nil || item.Instance.ID > nodeNetDeployment.Instance.ID) {
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

func readEnrollmentHello(reqs iter.Seq2[*apigen.EnrollmentSecondaryMsg, error]) (*apigen.EnrollmentHello, error) {
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

func drainEnrollmentStream(reqs iter.Seq2[*apigen.EnrollmentSecondaryMsg, error]) <-chan error {
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
