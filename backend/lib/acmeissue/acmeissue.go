// Package acmeissue obtains and renews HTTP-01 ACME certificates for claimed
// HTTPS ingress hostnames on the primary. Issued certificates are stored as
// versioned secrets; hostname bindings and pending challenge tokens are
// published to every node through the acmestate holder.
package acmeissue

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"golang.org/x/crypto/acme"
)

const (
	accountKeySecretName = "acme_account_key"
	certSecretPrefix     = "acme.cert."
	renewBefore          = 30 * 24 * time.Hour
	reconcileInterval    = 12 * time.Hour
	issueTimeout         = 4 * time.Minute
	challengeSettleDelay = 2 * time.Second
)

type Manager struct {
	Secrets      *secrets.Manager
	Snapshot     func() []apigen.Deployment
	Subscribe    func() ([]apigen.Deployment, chan apigen.Deployment, func())
	Holder       *acmestate.Holder
	DirectoryURL string

	challenges map[string]string
}

func New(secretsMgr *secrets.Manager, snapshot func() []apigen.Deployment, subscribe func() ([]apigen.Deployment, chan apigen.Deployment, func()), holder *acmestate.Holder) *Manager {
	directory := os.Getenv("OPENDEPLOY_ACME_DIRECTORY")
	if directory == "" {
		directory = acme.LetsEncryptURL
	}
	return &Manager{
		Secrets:      secretsMgr,
		Snapshot:     snapshot,
		Subscribe:    subscribe,
		Holder:       holder,
		DirectoryURL: directory,
		challenges:   map[string]string{},
	}
}

func (m *Manager) Run(ctx context.Context) {
	ctx = logu.AddTag(ctx, "AcmeIssue")
	snapshot, events, unsubscribe := m.Subscribe()
	defer unsubscribe()
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	m.reconcile(ctx, snapshot)
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			drainDeploymentEvents(events)
			m.reconcile(ctx, m.Snapshot())
		case <-ticker.C:
			m.reconcile(ctx, m.Snapshot())
		}
	}
}

func drainDeploymentEvents(events chan apigen.Deployment) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func CertSecretName(hostname string) string {
	return certSecretPrefix + hostname
}

func acmeHostnames(configs []apigen.Deployment) []string {
	seen := map[string]bool{}
	for _, cfg := range configs {
		if cfg.Deleted() || cfg.Def.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		for _, route := range cfg.Def.Spec.Networking.Ingress {
			if route == nil || route.Kind != apigen.IngressKind_INGRESS_KIND_HTTPS || route.HttpsConfig == nil {
				continue
			}
			source := route.HttpsConfig.CertSource
			if source != nil && source.Secret != nil {
				continue
			}
			hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.Hostname)), ".")
			if hostname != "" {
				seen[hostname] = true
			}
		}
	}
	hostnames := make([]string, 0, len(seen))
	for hostname := range seen {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	return hostnames
}

func (m *Manager) reconcile(ctx context.Context, configs []apigen.Deployment) {
	hostnames := acmeHostnames(configs)
	claimed := map[string]bool{}
	for _, hostname := range hostnames {
		claimed[hostname] = true
	}
	bindings := acmestate.Bindings(m.Holder.Get())
	if bindings == nil {
		bindings = map[string]int32{}
	}
	for hostname := range bindings {
		if !claimed[hostname] {
			delete(bindings, hostname)
		}
	}
	for _, hostname := range hostnames {
		if id, ok := m.currentCert(hostname); ok {
			bindings[hostname] = id
			continue
		}
		if err := m.issue(ctx, hostname); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, fmt.Sprintf("ACME issuance failed for %s", hostname), "err", err)
			continue
		}
		if meta, ok := m.Secrets.LatestMetaByName(CertSecretName(hostname)); ok {
			bindings[hostname] = meta.ID
			slog.InfoContext(ctx, fmt.Sprintf("ACME certificate issued for %s secret_version_id=%d", hostname, meta.ID))
		}
	}
	clear(m.challenges)
	m.publish(bindings)
}

func (m *Manager) currentCert(hostname string) (int32, bool) {
	meta, ok := m.Secrets.LatestMetaByName(CertSecretName(hostname))
	if !ok {
		return 0, false
	}
	value, err := m.Secrets.RevealByID(meta.ID)
	if err != nil {
		return 0, false
	}
	pair, err := tls.X509KeyPair(value, value)
	if err != nil {
		return 0, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return 0, false
	}
	if err := leaf.VerifyHostname(hostname); err != nil {
		return 0, false
	}
	if time.Until(leaf.NotAfter) < renewBefore {
		return 0, false
	}
	return meta.ID, true
}

func (m *Manager) publish(bindings map[string]int32) {
	state := &apigen.AcmeState{Seq: time.Now().UnixNano()}
	hostnames := make([]string, 0, len(bindings))
	for hostname := range bindings {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	for _, hostname := range hostnames {
		state.CertBindings = append(state.CertBindings, &apigen.AcmeCertBinding{Hostname: hostname, SecretVersionID: bindings[hostname]})
	}
	tokens := make([]string, 0, len(m.challenges))
	for token := range m.challenges {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		state.Challenges = append(state.Challenges, &apigen.AcmeHttpChallenge{Token: token, KeyAuthorization: m.challenges[token]})
	}
	m.Holder.Set(state)
}

func (m *Manager) issue(ctx context.Context, hostname string) error {
	ctx, cancel := context.WithTimeout(ctx, issueTimeout)
	defer cancel()
	client, err := m.client(ctx)
	if err != nil {
		return err
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(hostname))
	if err != nil {
		return fmt.Errorf("authorizing order: %w", err)
	}
	for _, authzURL := range order.AuthzURLs {
		if err := m.completeAuthorization(ctx, client, authzURL); err != nil {
			return err
		}
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		DNSNames: []string{hostname},
	}, certKey)
	if err != nil {
		return err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fmt.Errorf("finalizing order: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return err
	}
	var combined []byte
	for _, der := range chain {
		combined = append(combined, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	combined = append(combined, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	if _, err := m.Secrets.SetByName(CertSecretName(hostname), combined, 0); err != nil {
		return fmt.Errorf("storing issued certificate: %w", err)
	}
	return nil
}

func (m *Manager) completeAuthorization(ctx context.Context, client *acme.Client, authzURL string) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("fetching authorization: %w", err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}
	var challenge *acme.Challenge
	for _, c := range authz.Challenges {
		if c.Type == "http-01" {
			challenge = c
			break
		}
	}
	if challenge == nil {
		return errors.New("authorization offers no http-01 challenge")
	}
	auth, err := client.HTTP01ChallengeResponse(challenge.Token)
	if err != nil {
		return err
	}
	m.challenges[challenge.Token] = auth
	m.publishCurrent()
	defer func() {
		delete(m.challenges, challenge.Token)
		m.publishCurrent()
	}()
	select {
	case <-time.After(challengeSettleDelay):
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := client.Accept(ctx, challenge); err != nil {
		return fmt.Errorf("accepting challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
		return fmt.Errorf("waiting for authorization: %w", err)
	}
	return nil
}

func (m *Manager) publishCurrent() {
	m.publish(acmestate.Bindings(m.Holder.Get()))
}

func (m *Manager) client(ctx context.Context) (*acme.Client, error) {
	key, err := m.accountKey()
	if err != nil {
		return nil, err
	}
	client := &acme.Client{Key: key, DirectoryURL: m.DirectoryURL}
	_, err = client.Register(ctx, &acme.Account{}, acme.AcceptTOS)
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("registering ACME account: %w", err)
	}
	return client, nil
}

func (m *Manager) accountKey() (*ecdsa.PrivateKey, error) {
	existing, err := m.Secrets.RevealInternal(accountKeySecretName)
	if err == nil && len(existing) > 0 {
		block, _ := pem.Decode(existing)
		if block == nil {
			return nil, errors.New("stored ACME account key is not PEM")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := m.Secrets.SetInternal(accountKeySecretName, encoded); err != nil {
		return nil, fmt.Errorf("storing ACME account key: %w", err)
	}
	return key, nil
}
