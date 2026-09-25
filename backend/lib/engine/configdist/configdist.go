package configdist

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type Resolver func(refs []apigen.ValueRef) (map[apigen.ValueRef]string, error)

type PrimaryProvider struct {
	resolver Resolver
}

func NewPrimaryProvider(resolver Resolver) *PrimaryProvider {
	return &PrimaryProvider{resolver: resolver}
}

func (p *PrimaryProvider) FetchConfigs(ctx context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	return p.resolver(refs)
}
