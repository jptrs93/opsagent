// Package nixstores records operator requests to reseed a repository's Nix
// build store and fans them out to every node's cluster session.
package nixstores

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const maxRepoLength = 512

type Service struct {
	store      *state.Service
	applyLocal func(repo string, requestedAt time.Time)

	mu          sync.Mutex
	resets      *apigen.NixStoreResets
	subscribers map[chan *apigen.NixStoreResets]struct{}
}

// New loads the persisted requests and replays them into the primary's own
// store manager through applyLocal.
func New(store *state.Service, applyLocal func(repo string, requestedAt time.Time)) (*Service, error) {
	items, err := listItems(context.Background(), store.Queries())
	if err != nil {
		return nil, err
	}
	s := &Service{
		store:       store,
		applyLocal:  applyLocal,
		resets:      &apigen.NixStoreResets{Items: items},
		subscribers: make(map[chan *apigen.NixStoreResets]struct{}),
	}
	if applyLocal != nil {
		for _, item := range items {
			applyLocal(item.Repo, item.RequestedAt)
		}
	}
	return s, nil
}

func (s *Service) RequestReset(ctx context.Context, repo string, now time.Time, author int64) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return errors.New("repository is required")
	}
	if len(repo) > maxRepoLength {
		return errors.New("repository is too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now.UnixMilli(), Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		var id uint64
		switch previous, err := q.GetNixStoreReset(ctx, repo); {
		case errors.Is(err, sql.ErrNoRows):
			meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
			if id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET); err != nil {
				return nil, err
			}
		case err != nil:
			return nil, err
		default:
			id = previous.ID
		}
		return pq.NewUpdate(pq.NixStoreResetMutation(meta, id, &apigen.NixStoreReset{Repo: repo})), nil
	})
	if err != nil {
		return err
	}
	items, err := listItems(ctx, s.store.Queries())
	if err != nil {
		return err
	}
	s.resets = &apigen.NixStoreResets{Items: items}
	if s.applyLocal != nil {
		s.applyLocal(repo, now)
	}
	for updates := range s.subscribers {
		select {
		case updates <- s.resets:
		default:
			select {
			case <-updates:
			default:
			}
			updates <- s.resets
		}
	}
	return nil
}

func (s *Service) Snapshot() *apigen.NixStoreResets {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resets
}

func (s *Service) SnapshotAndSubscribe() (*apigen.NixStoreResets, <-chan *apigen.NixStoreResets, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	updates := make(chan *apigen.NixStoreResets, 1)
	s.subscribers[updates] = struct{}{}
	var once sync.Once
	return s.resets, updates, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.subscribers, updates)
		})
	}
}

func listItems(ctx context.Context, q *pq.Queries) ([]apigen.NixStoreResetItem, error) {
	rows, err := q.ListNixStoreResetRows(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.NixStoreResetItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, apigen.NixStoreResetItem{Repo: r.Repo, RequestedAt: time.UnixMilli(r.RequestedAt)})
	}
	return items, nil
}
