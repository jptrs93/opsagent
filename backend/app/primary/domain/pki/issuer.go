package pki

import (
	"context"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/issuedtls"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

type Issuer struct {
	Secrets *secrets.Manager
}

func (i *Issuer) Issue(cfg *apigen.DeploymentEvent) (*apigen.ClusterIssuedTLSResponse, error) {
	mount := runtimeinputs.IssuedTLSMountOf(cfg)
	if mount == nil {
		return nil, fmt.Errorf("deployment %d has no issued TLS mount", cfg.DeploymentID)
	}
	caCert, caKey, err := BootstrapWorkloadCA(i.Secrets)
	if err != nil {
		return nil, fmt.Errorf("loading workload CA: %w", err)
	}
	if mount.CaOnly {
		notAfter, err := certu.CertificateNotAfter(caCert)
		if err != nil {
			return nil, fmt.Errorf("reading workload CA expiry: %w", err)
		}
		return &apigen.ClusterIssuedTLSResponse{
			CaCertPem: caCert,
			IssuedAt:  time.Now().UnixMilli(),
			NotAfter:  notAfter.UnixMilli(),
		}, nil
	}
	dnsName := network.DeploymentDNSName(cfg.Value.Name, cfg.Value.SpaceID)
	names := []string{dnsName}
	if cfg.Value.Spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		if prefix, ok := network.Default.PrefixValue(); ok {
			if addr, addrErr := prefix.InboundAddr(cfg.Value.SpaceID, cfg.DeploymentID, 0); addrErr == nil {
				names = append(names, addr.String())
			}
		}
	}
	names = append(names, mount.ExtraNames...)
	now := time.Now()
	certPEM, keyPEM, err := certu.SignWorkloadCertificate(caCert, caKey, dnsName, names, issuedtls.Validity)
	if err != nil {
		return nil, err
	}
	return &apigen.ClusterIssuedTLSResponse{
		CertPem:   certPEM,
		KeyPem:    keyPEM,
		CaCertPem: caCert,
		IssuedAt:  now.UnixMilli(),
		NotAfter:  now.Add(issuedtls.Validity).UnixMilli(),
	}, nil
}

type IssuedTLSProvider struct {
	Issuer   *Issuer
	Snapshot func() []apigen.DeploymentEvent
}

func (p *IssuedTLSProvider) FetchIssuedTLS(_ context.Context, deploymentID, _ int32) (*runtimeinputs.IssuedTLSValue, error) {
	for _, cfg := range p.Snapshot() {
		if cfg.DeploymentID != deploymentID {
			continue
		}
		res, err := p.Issuer.Issue(&cfg)
		if err != nil {
			return nil, err
		}
		return issuedtls.ValueFromResponse(res, cfg.SpecVersion), nil
	}
	return nil, fmt.Errorf("deployment %d not found", deploymentID)
}
