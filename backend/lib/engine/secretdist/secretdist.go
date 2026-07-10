package secretdist

import (
	"context"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

// PrimaryProvider resolves batches from the primary encrypted secrets manager.
type PrimaryProvider struct {
	manager *secrets.Manager
}

func NewPrimaryProvider(manager *secrets.Manager) *PrimaryProvider {
	return &PrimaryProvider{manager: manager}
}

func (p *PrimaryProvider) FetchSecrets(ctx context.Context, ids []int32) (map[int32]string, error) {
	return p.manager.ResolveMany(ids)
}
