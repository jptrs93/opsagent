package backup

import (
	"sync"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

// StatusPublisher owns replication observations independently of the database
// being replicated and the core store's writer mutex.
type StatusPublisher struct {
	mu      sync.Mutex
	updates pubsubu.PubSub[apigen.BackupStatus]
}

func (s *StatusPublisher) Publish(status apigen.BackupStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.updates.ValueOK(); ok && previous == status {
		return
	}
	s.updates.Notify(status)
}

func (s *StatusPublisher) Snapshot() apigen.BackupStatus { return s.updates.Value() }

func (s *StatusPublisher) SnapshotAndSubscribe() *pubsubu.Sub[apigen.BackupStatus] {
	return s.updates.Subscribe(nil)
}
