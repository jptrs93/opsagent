package secretdist

import (
	"context"
	"fmt"
	"sync"

	"github.com/jptrs93/opsagent/backend/secrets"
)

// Cache stores plaintext deployment secrets in memory only. It is populated by
// the prepare step and read by runners when expanding ${s:name} references.
type Cache struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewCache() *Cache {
	return &Cache{values: make(map[string]string)}
}

func (c *Cache) Resolve(name string) (string, bool) {
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

// PrimaryProvider resolves batches from the primary encrypted secrets manager
// and mirrors them into the in-memory runner cache.
type PrimaryProvider struct {
	*Cache
	manager *secrets.Manager
}

func NewPrimaryProvider(manager *secrets.Manager) *PrimaryProvider {
	return &PrimaryProvider{Cache: NewCache(), manager: manager}
}

func (p *PrimaryProvider) FetchSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	if p.manager == nil {
		return nil, fmt.Errorf("secrets manager is not configured")
	}
	values, err := p.manager.ResolveMany(keys)
	if err != nil {
		return nil, err
	}
	p.Store(values)
	return values, nil
}
