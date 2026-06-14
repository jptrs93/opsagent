package configdist

import (
	"context"
	"fmt"
	"sync"
)

// Cache stores deployment user configs in memory for runner ${c:name}
// expansion. Primary and secondary use the same cache shape.
type Cache struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewCache() *Cache {
	return &Cache{values: make(map[string]string)}
}

func (c *Cache) ResolveConfig(name string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, ok := c.values[name]
	return value, ok
}

func (c *Cache) Store(values map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = make(map[string]string, len(values))
	}
	for key, value := range values {
		c.values[key] = value
	}
}

type Resolver interface {
	ResolveConfigs(names []string) (map[string]string, error)
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

func (p *PrimaryProvider) FetchConfigs(ctx context.Context, keys []string) (map[string]string, error) {
	if p.resolver == nil {
		return nil, fmt.Errorf("config resolver is not configured")
	}
	values, err := p.resolver.ResolveConfigs(keys)
	if err != nil {
		return nil, err
	}
	p.Store(values)
	return values, nil
}
