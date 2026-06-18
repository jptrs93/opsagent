package configdist

import (
	"context"
	"fmt"
	"sync"
)

// Cache stores deployment user configs in memory for runner config ref
// expansion. Primary and secondary use the same cache shape.
type Cache struct {
	mu     sync.RWMutex
	values map[int32]string
}

func NewCache() *Cache {
	return &Cache{values: make(map[int32]string)}
}

func (c *Cache) ResolveConfig(id int32) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, ok := c.values[id]
	return value, ok
}

func (c *Cache) Store(values map[int32]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = make(map[int32]string, len(values))
	}
	for key, value := range values {
		c.values[key] = value
	}
}

type Resolver interface {
	ResolveConfigs(ids []int32) (map[int32]string, error)
}

// PrimaryProvider resolves batches from primary storage and mirrors them into
// the in-memory runner cache.
type PrimaryProvider struct {
	*Cache
	resolver Resolver
}

func NewPrimaryProvider(resolver Resolver) *PrimaryProvider {
	return &PrimaryProvider{Cache: NewCache(), resolver: resolver}
}

func (p *PrimaryProvider) FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error) {
	if p.resolver == nil {
		return nil, fmt.Errorf("config resolver is not configured")
	}
	values, err := p.resolver.ResolveConfigs(ids)
	if err != nil {
		return nil, err
	}
	p.Store(values)
	return values, nil
}
