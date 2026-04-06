package logstore

import "sync"

type Sub[T any] struct {
	Filter func(T) bool
	Ch     chan T
}

type Subs[T any] struct {
	Subs []*Sub[T]
	Mu   sync.Mutex
}

func (s *Subs[T]) SubscribePerm() chan T {
	sub, _ := s.Subscribe(nil)
	return sub.Ch
}

func (s *Subs[T]) Subscribe(f func(T) bool) (*Sub[T], func()) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	sub := &Sub[T]{Filter: f, Ch: make(chan T, 1_000)}
	s.Subs = append(s.Subs, sub)

	unsub := func() {
		s.Mu.Lock()
		defer s.Mu.Unlock()
		for i, current := range s.Subs {
			if current == sub {
				s.Subs = append(s.Subs[:i], s.Subs[i+1:]...)
				close(sub.Ch)
				return
			}
		}
	}

	return sub, unsub
}

func (s *Subs[T]) Notify(value T) {
	if s == nil {
		return
	}

	s.Mu.Lock()
	subs := make([]*Sub[T], len(s.Subs))
	copy(subs, s.Subs)
	s.Mu.Unlock()

	for _, sub := range subs {
		if sub.Filter != nil && !sub.Filter(value) {
			continue
		}
		select {
		case sub.Ch <- value:
		default:
		}
	}
}
