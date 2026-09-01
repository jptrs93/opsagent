package issuedtls

import (
	"context"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const Validity = 10 * 365 * 24 * time.Hour

type Issuer struct {
	Secrets *secrets.Manager
}

func (i *Issuer) Issue(cfg *apigen.Deployment) (*apigen.ClusterIssuedTLSResponse, error) {
	mount := runtimeinputs.IssuedTLSMountOf(cfg)
	if mount == nil {
		return nil, fmt.Errorf("deployment %d has no issued TLS mount", cfg.ID)
	}
	caCert, caKey, err := certu.BootstrapWorkloadCA(i.Secrets)
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
	dnsName := network.DeploymentDNSName(cfg.Def.Name, cfg.Def.SpaceID)
	names := []string{dnsName}
	if cfg.Def.Spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		if prefix, ok := network.Default.PrefixValue(); ok {
			if addr, addrErr := prefix.InboundAddr(cfg.Def.SpaceID, cfg.ID, 0); addrErr == nil {
				names = append(names, addr.String())
			}
		}
	}
	names = append(names, mount.ExtraNames...)
	now := time.Now()
	certPEM, keyPEM, err := certu.SignWorkloadCertificate(caCert, caKey, dnsName, names, Validity)
	if err != nil {
		return nil, err
	}
	return &apigen.ClusterIssuedTLSResponse{
		CertPem:   certPEM,
		KeyPem:    keyPEM,
		CaCertPem: caCert,
		IssuedAt:  now.UnixMilli(),
		NotAfter:  now.Add(Validity).UnixMilli(),
	}, nil
}

type PrimaryProvider struct {
	Issuer   *Issuer
	Snapshot func() []apigen.Deployment
}

func (p *PrimaryProvider) FetchIssuedTLS(_ context.Context, deploymentID, _ int32) (*runtimeinputs.IssuedTLSValue, error) {
	for _, cfg := range p.Snapshot() {
		if cfg.ID != deploymentID {
			continue
		}
		res, err := p.Issuer.Issue(&cfg)
		if err != nil {
			return nil, err
		}
		return ValueFromResponse(res, cfg.SpecVersion), nil
	}
	return nil, fmt.Errorf("deployment %d not found", deploymentID)
}

func ValueFromResponse(res *apigen.ClusterIssuedTLSResponse, specVersion int32) *runtimeinputs.IssuedTLSValue {
	return &runtimeinputs.IssuedTLSValue{
		CertPEM:     res.CertPem,
		KeyPEM:      res.KeyPem,
		CACertPEM:   res.CaCertPem,
		IssuedAt:    time.UnixMilli(res.IssuedAt),
		NotAfter:    time.UnixMilli(res.NotAfter),
		SpecVersion: specVersion,
	}
}
