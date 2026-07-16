// Package netstatewatch publishes decoded netproxy state snapshots from the
// atomically replaced netstate protobuf file.
package netstatewatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/jptrs93/opsagent/backend/apigen"
)

// Watcher watches one NetState protobuf file. Snapshots are immutable after
// publication and may be shared by subscribers.
type Watcher struct {
	path string

	mu          sync.Mutex
	snapshot    *apigen.NetState
	subscribers map[chan *apigen.NetState]struct{}
}

// New creates a watcher for path. Run starts filesystem watching.
func New(path string) *Watcher {
	return &Watcher{
		path:        filepath.Clean(path),
		subscribers: make(map[chan *apigen.NetState]struct{}),
	}
}

// SnapshotAndSubscribe atomically returns the latest snapshot and a channel
// for later snapshots. The channel is coalesced: a slow subscriber always
// receives the most recent state rather than blocking the file watcher.
func (w *Watcher) SnapshotAndSubscribe() (*apigen.NetState, <-chan *apigen.NetState, func()) {
	w.mu.Lock()
	defer w.mu.Unlock()

	updates := make(chan *apigen.NetState, 1)
	w.subscribers[updates] = struct{}{}
	var once sync.Once
	return w.snapshot, updates, func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			delete(w.subscribers, updates)
		})
	}
}

// Run watches the containing directory because the state writer updates path
// with write-rename. Watching the file itself would stop observing updates
// after its first replacement.
func (w *Watcher) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating netstate watcher: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(w.path)); err != nil {
		return fmt.Errorf("watching netstate directory: %w", err)
	}
	if err := w.reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("loading initial netstate failed", "path", w.path, "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return fmt.Errorf("watching netstate: %w", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(event.Name) != w.path || event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			if err := w.reload(); err != nil {
				slog.Warn("reloading netstate failed", "path", w.path, "err", err)
			}
		}
	}
}

func (w *Watcher) reload() error {
	b, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	next, err := apigen.DecodeNetState(b)
	if err != nil {
		return err
	}
	w.publish(next)
	return nil
}

func (w *Watcher) publish(next *apigen.NetState) {
	if next == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.snapshot != nil && next.Seq <= w.snapshot.Seq {
		return
	}
	w.snapshot = next
	for updates := range w.subscribers {
		select {
		case updates <- next:
		default:
			<-updates
			updates <- next
		}
	}
}
