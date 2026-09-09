package issuedtls

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
)

const Validity = 10 * 365 * 24 * time.Hour

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
