package state

import (
	"slices"
	"sync"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type Service struct {
	Mu sync.Mutex

	q *pq.Queries

	updateTriggers []UpdateTrigger
	onUpdate       []*updateHandler
	closed         bool
}

func Open(dbPath string) *Service {
	return &Service{q: pq.Open(dbPath)}
}

func (s *Service) Close() error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.closed = true
	for _, handler := range slices.Clone(s.onUpdate) {
		handler.drop()
	}
	return s.q.Close()
}

func (s *Service) Queries() *pq.Queries { return s.q }
