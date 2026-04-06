package logstore

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
)

type TupleLogStore[T Encodable] struct {
	StorageFilePath string
	Decode          func([]byte) (T, error)
	Data            *AppendOnlyLogFileWrapper
	Latest          T
	Subs            *Subs[T]
}

func New[T Encodable](
	path string,
	decode func([]byte) (T, error)) *TupleLogStore[T] {
	store := &TupleLogStore[T]{
		StorageFilePath: path,
		Decode:          decode,
		Data: &AppendOnlyLogFileWrapper{
			Path:       path,
			OpenFile:   nil,
			Count:      atomic.Int32{},
			LastRecord: nil,
			Mu:         sync.RWMutex{},
		},
		Subs: &Subs[T]{},
	}
	return store
}

func NewAndInit[T Encodable](
	ctx context.Context,
	path string,
	decode func([]byte) (T, error),
) (*TupleLogStore[T], error) {
	store := New(path, decode)
	if err := store.Init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (t *TupleLogStore[T]) Init(ctx context.Context) error {
	if err := t.Data.InitFile(ctx, nil); err != nil {
		return err
	}
	if len(t.Data.LastRecord) == 0 {
		var zero T
		t.Latest = zero
		return nil
	}

	latest, err := t.Decode(t.Data.LastRecord)
	if err != nil {
		return fmt.Errorf("decoding latest record: %w", err)
	}
	t.Latest = latest
	return nil
}

func (t *TupleLogStore[T]) Write(ctx context.Context, item T) error {
	record := encodeItemRecord(item)

	if err := t.Data.Append(ctx, record); err != nil {
		return err
	}
	t.Latest = item

	if t.Subs != nil {
		t.Subs.Notify(item)
	}
	return nil
}

func (t *TupleLogStore[T]) Seq() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for raw, err := range t.Data.Seq() {
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}

			item, decodeErr := t.Decode(raw)
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

func (t *TupleLogStore[T]) FetchAnyMatching(predicate func(T) bool) (T, error) {
	for item, err := range t.Seq() {
		if err != nil {
			var zero T
			return zero, err
		}
		if predicate(item) {
			return item, nil
		}
	}
	var zero T
	return zero, ErrNotFound
}

func (t *TupleLogStore[T]) FetchMatching(predicate func(T) bool) ([]T, error) {
	res := make([]T, 0)
	for item, err := range t.Seq() {
		if err != nil {
			return nil, err
		}
		if predicate(item) {
			res = append(res, item)
		}
	}
	return res, nil
}

func (t *TupleLogStore[T]) LatestAndSubscribe() (T, <-chan T, func()) {
	if t.Subs == nil {
		closed := make(chan T)
		close(closed)
		return t.Latest, closed, func() {}
	}

	sub, unsub := t.Subs.Subscribe(nil)
	return t.Latest, sub.Ch, unsub
}

func MustNewAndInit[T Encodable](
	ctx context.Context,
	path string,
	decode func([]byte) (T, error),
) *TupleLogStore[T] {
	store, err := NewAndInit(ctx, path, decode)
	if err != nil {
		panic(err)
	}
	return store
}

func (t *TupleLogStore[T]) MustInit(ctx context.Context) {
	if err := t.Init(ctx); err != nil {
		panic(err)
	}
}

func (t *TupleLogStore[T]) MustWrite(ctx context.Context, item T) {
	if err := t.Write(ctx, item); err != nil {
		panic(err)
	}
}

func (t *TupleLogStore[T]) MustSeq() iter.Seq[T] {
	return func(yield func(T) bool) {
		for item, err := range t.Seq() {
			if err != nil {
				panic(err)
			}
			if !yield(item) {
				return
			}
		}
	}
}

func (t *TupleLogStore[T]) MustFetchAnyMatching(predicate func(T) bool) T {
	item, err := t.FetchAnyMatching(predicate)
	if err != nil {
		panic(err)
	}
	return item
}

func (t *TupleLogStore[T]) MustFetchMatching(predicate func(T) bool) []T {
	items, err := t.FetchMatching(predicate)
	if err != nil {
		panic(err)
	}
	return items
}
