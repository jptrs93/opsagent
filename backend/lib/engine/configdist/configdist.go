package configdist

import (
	"context"
)

type Resolver interface {
	ResolveConfigs(ids []int32) (map[int32]string, error)
}

// PrimaryProvider resolves batches from primary storage.
type PrimaryProvider struct {
	resolver Resolver
}

func NewPrimaryProvider(resolver Resolver) *PrimaryProvider {
	return &PrimaryProvider{resolver: resolver}
}

func (p *PrimaryProvider) FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error) {
	return p.resolver.ResolveConfigs(ids)
}
