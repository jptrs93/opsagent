package logstore

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"

	"github.com/jptrs93/goutil/syncu"
)

type Encodable interface {
	Encode() []byte
}

type KeyLogStore[T Encodable, K syncu.Lockable] struct {
	StorageFilePath string
	KeyGetter       func(T) K
	Decode          func([]byte) (T, error)
	Data            *AppendOnlyLogFileWrapper
	Latest          map[K]T
	latestMu        sync.RWMutex
	Subs            *Subs[T]
	locks           *syncu.StripedLock[K]
}

var ErrNotFound = fmt.Errorf("record not found")

func NewKey[T Encodable, K syncu.Lockable](
	path string,
	keyGetter func(T) K,
	decode func([]byte) (T, error),
) *KeyLogStore[T, K] {
	store := &KeyLogStore[T, K]{
		StorageFilePath: path,
		KeyGetter:       keyGetter,
		Decode:          decode,
		Data: &AppendOnlyLogFileWrapper{
			Path:       path,
			OpenFile:   nil,
			Count:      atomic.Int32{},
			LastRecord: nil,
			Mu:         sync.RWMutex{},
		},
		Latest: make(map[K]T),
		Subs:   &Subs[T]{},
		locks:  syncu.NewStripedLock[K](25),
	}
	return store
}

func NewKeyAndInit[T Encodable, K syncu.Lockable](
	ctx context.Context,
	path string,
	keyGetter func(T) K,
	decode func([]byte) (T, error),
) (*KeyLogStore[T, K], error) {
	store := NewKey(path, keyGetter, decode)
	if err := store.Init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *KeyLogStore[T, K]) Init(ctx context.Context) error {
	s.latestMu.Lock()
	s.Latest = make(map[K]T)
	s.latestMu.Unlock()

	var indexErr error
	err := s.Data.InitFile(ctx, func(record []byte) {
		if indexErr != nil {
			return
		}
		item, err := s.Decode(record)
		if err != nil {
			indexErr = fmt.Errorf("decoding record while indexing: %w", err)
			return
		}
		s.latestMu.Lock()
		s.Latest[s.KeyGetter(item)] = item
		s.latestMu.Unlock()
	})
	if err != nil {
		return err
	}
	if indexErr != nil {
		s.Data.CloseFile(ctx)
		return indexErr
	}
	return nil
}

func (s *KeyLogStore[T, K]) Write(ctx context.Context, item T) error {
	record := encodeItemRecord(item)

	if err := s.Data.Append(ctx, record); err != nil {
		return err
	}

	s.latestMu.Lock()
	if s.Latest == nil {
		s.Latest = make(map[K]T)
	}
	s.Latest[s.KeyGetter(item)] = item
	s.latestMu.Unlock()

	if s.Subs != nil {
		s.Subs.Notify(item)
	}
	return nil
}
func (s *KeyLogStore[T, K]) applyUpdate(ctx context.Context, item T, f func(d T)) (T, error) {
	f(item)
	if err := s.Write(ctx, item); err != nil {
		return item, err
	}
	return item, nil
}

func (s *KeyLogStore[T, K]) WriteUpdateByKey(ctx context.Context, key K, f func(d T)) (T, error) {
	s.locks.Lock(key)
	defer s.locks.Unlock(key)
	item, err := s.FetchLatestForKey(key)
	if err != nil {
		return item, err
	}
	return s.applyUpdate(ctx, item, f)
}

func (s *KeyLogStore[T, K]) WriteUpdateByOneMatching(ctx context.Context, predicate func(T) bool, f func(d T)) (T, error) {
	item, err := s.FetchAnyMatching(predicate)
	if err != nil {
		return item, err
	}
	key := s.KeyGetter(item)
	s.locks.Lock(key)
	defer s.locks.Unlock(key)
	return s.applyUpdate(ctx, item, f)
}

func (s *KeyLogStore[T, K]) Seq() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for raw, err := range s.Data.Seq() {
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}

			item, decodeErr := s.Decode(raw)
			if decodeErr != nil {
				var zero T
				yield(zero, fmt.Errorf("decoding record: %w", decodeErr))
				return
			}

			if !yield(item, nil) {
				return
			}
		}
	}
}

func (s *KeyLogStore[T, K]) FetchAnyMatching(predicate func(T) bool) (T, error) {
	s.latestMu.RLock()
	defer s.latestMu.RUnlock()

	for _, item := range s.Latest {
		if predicate(item) {
			return item, nil
		}
	}
	var zero T
	return zero, ErrNotFound
}

func (s *KeyLogStore[T, K]) FetchMatching(predicate func(T) bool) ([]T, error) {
	s.latestMu.RLock()
	defer s.latestMu.RUnlock()

	res := make([]T, 0)
	for _, item := range s.Latest {
		if predicate(item) {
			res = append(res, item)
		}
	}
	return res, nil
}

func (s *KeyLogStore[T, K]) FetchLatestForKey(key K) (T, error) {
	s.latestMu.RLock()
	defer s.latestMu.RUnlock()

	item, ok := s.Latest[key]
	if !ok {
		var zero T
		return zero, ErrNotFound
	}
	return item, nil
}

func (s *KeyLogStore[T, K]) Snapshot() map[K]T {
	s.latestMu.RLock()
	defer s.latestMu.RUnlock()

	snapshot := make(map[K]T, len(s.Latest))
	for key, item := range s.Latest {
		snapshot[key] = item
	}
	return snapshot
}

func (s *KeyLogStore[T, K]) SnapshotAndSubscribe() (map[K]T, <-chan T, func()) {
	if s.Subs == nil {
		snapshot := s.Snapshot()
		closed := make(chan T)
		close(closed)
		return snapshot, closed, func() {}
	}

	sub, unsub := s.Subs.Subscribe(nil)
	snapshot := s.Snapshot()
	return snapshot, sub.Ch, unsub
}

func (s *KeyLogStore[T, K]) FetchForKey(key K) ([]T, error) {
	res := make([]T, 0)
	for item, err := range s.Seq() {
		if err != nil {
			return nil, err
		}
		if s.KeyGetter(item) == key {
			res = append(res, item)
		}
	}
	if len(res) == 0 {
		return nil, ErrNotFound
	}
	return res, nil
}

func MustNewKeyAndInit[T Encodable, K syncu.Lockable](
	ctx context.Context,
	path string,
	keyGetter func(T) K,
	decode func([]byte) (T, error),
) *KeyLogStore[T, K] {
	store, err := NewKeyAndInit(ctx, path, keyGetter, decode)
	if err != nil {
		panic(err)
	}
	return store
}

func (s *KeyLogStore[T, K]) MustInit(ctx context.Context) {
	if err := s.Init(ctx); err != nil {
		panic(err)
	}
}

func (s *KeyLogStore[T, K]) MustWrite(ctx context.Context, item T) {
	if err := s.Write(ctx, item); err != nil {
		panic(err)
	}
}

func (s *KeyLogStore[T, K]) MustWriteUpdateByKey(ctx context.Context, key K, f func(d T)) T {
	item, err := s.WriteUpdateByKey(ctx, key, f)
	if err != nil {
		panic(err)
	}
	return item
}

func (s *KeyLogStore[T, K]) MustWriteUpdateByOneMatching(ctx context.Context, predicate func(T) bool, f func(d T)) T {
	item, err := s.WriteUpdateByOneMatching(ctx, predicate, f)
	if err != nil {
		panic(err)
	}
	return item
}

func (s *KeyLogStore[T, K]) MustSeq() iter.Seq[T] {
	return func(yield func(T) bool) {
		for item, err := range s.Seq() {
			if err != nil {
				panic(err)
			}
			if !yield(item) {
				return
			}
		}
	}
}

func (s *KeyLogStore[T, K]) MustFetchAnyMatching(predicate func(T) bool) T {
	item, err := s.FetchAnyMatching(predicate)
	if err != nil {
		panic(err)
	}
	return item
}

func (s *KeyLogStore[T, K]) MustFetchMatching(predicate func(T) bool) []T {
	items, err := s.FetchMatching(predicate)
	if err != nil {
		panic(err)
	}
	return items
}

func (s *KeyLogStore[T, K]) MustFetchLatestForKey(key K) T {
	item, err := s.FetchLatestForKey(key)
	if err != nil {
		panic(err)
	}
	return item
}

func (s *KeyLogStore[T, K]) MustFetchForKey(key K) []T {
	items, err := s.FetchForKey(key)
	if err != nil {
		panic(err)
	}
	return items
}
