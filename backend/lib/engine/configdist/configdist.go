package configdist

import (
	"context"
)

type Resolver func(ids []int32) (map[int32]string, error)

type PrimaryProvider struct {
	resolver Resolver
}

func NewPrimaryProvider(resolver Resolver) *PrimaryProvider {
	return &PrimaryProvider{resolver: resolver}
}

func (p *PrimaryProvider) FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error) {
	return p.resolver(ids)
}
