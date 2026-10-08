// Package nodepublisher owns the per-node projection a secondary consumes: the
// scheduled instances assigned to the node, its cluster network map and its
// ACME subset. It folds every commit into an in-memory cache inside the write
// lock and hands each connected session one NodeProjection per commit that touches
// its node. Only the stamp of each node's newest map is persisted.
package nodepublisher

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// queueSize bounds a subscriber's pending updates. A session that cannot
// drain this many commits is dropped and reconnects to a fresh snapshot.
const queueSize = 256

type subscriber struct {
	nodeID uint64
	ch     chan *apigen.NodeProjection
}

type nodeMap struct {
	current *apigen.ClusterNetMap
	content []byte
	hash    string
}

type acmeEntry struct {
	state apigen.AcmeState
	hash  string
}

type seqWaiter struct {
	seq int64
	ch  chan struct{}
}

type Publisher struct {
	store      *state.Service
	prefix     network.Prefix
	acmeHolder *acmestate.Holder

	mu          sync.Mutex
	cache       *cache
	rc          *renderContext
	plan        ingressPlan
	maps        map[uint64]*nodeMap
	persisted   map[uint64]pq.NodeNetmap
	appliedSeq  int64
	waiters     []seqWaiter
	subscribers map[*subscriber]struct{}
	closed      bool
	unsubscribe func()

	acme       *apigen.AcmeState
	acmeByNode map[uint64]*acmeEntry

	diagnostics     *apigen.IngressDiagnosticList
	diagnosticsSubs map[chan *apigen.IngressDiagnosticList]struct{}

	// Acknowledgement state is kept under its own lock so recording a secondary's
	// applied sequence never contends with folding a commit.
	ackMu      sync.Mutex
	applied    map[uint64]int64
	ackUpdates chan struct{}
}

type outputs struct {
	seq  int64
	rows map[uint64][]apigen.NodeInstance
	maps map[uint64]*apigen.ClusterNetMap
	acme map[uint64]*apigen.AcmeState
}

func New(store *state.Service, prefix network.Prefix, acme *acmestate.Holder) (*Publisher, error) {
	rows, err := store.Queries().ListNodeNetmaps(context.Background())
	if err != nil {
		return nil, fmt.Errorf("loading network map stamps: %w", err)
	}
	p := &Publisher{
		store:           store,
		prefix:          prefix,
		acmeHolder:      acme,
		maps:            make(map[uint64]*nodeMap),
		persisted:       make(map[uint64]pq.NodeNetmap, len(rows)),
		subscribers:     make(map[*subscriber]struct{}),
		acmeByNode:      make(map[uint64]*acmeEntry),
		diagnostics:     &apigen.IngressDiagnosticList{Items: []apigen.IngressDiagnostic{}},
		diagnosticsSubs: make(map[chan *apigen.IngressDiagnosticList]struct{}),
		applied:         make(map[uint64]int64),
		ackUpdates:      make(chan struct{}, 1),
	}
	for _, row := range rows {
		p.persisted[row.NodeID] = row
	}
	p.acme = acme.Get()
	if err := p.seed(); err != nil {
		return nil, err
	}
	return p, nil
}

// seed reads the tables under the write lock and registers the commit
// handler behind that read, so the first handled commit is the first one
// after the snapshot.
func (p *Publisher) seed() error {
	var seedErr error
	_, _, unsubscribe := state.Subscribe(p.store, func() struct{} {
		p.mu.Lock()
		defer p.mu.Unlock()
		seedErr = p.seedLocked()
		return struct{}{}
	}, func(u state.WriteUpdate) (struct{}, bool) {
		p.handle(u)
		return struct{}{}, false
	})
	if seedErr != nil {
		unsubscribe()
		return seedErr
	}
	p.mu.Lock()
	p.unsubscribe = unsubscribe
	p.mu.Unlock()
	return nil
}

func (p *Publisher) seedLocked() error {
	c, seq, err := seedCache(context.Background(), p.store.Queries())
	if err != nil {
		return fmt.Errorf("seeding node projection: %w", err)
	}
	p.cache, p.rc, p.maps = c, nil, make(map[uint64]*nodeMap)
	p.acmeByNode = make(map[uint64]*acmeEntry)
	inputs := c.inputs()
	p.plan = renderIngressPlan(inputs)
	p.appliedSeq = seq
	p.renderLocked(inputs, nil)
	p.refreshAcmeLocked(nil)
	p.wakeWaitersLocked()
	return nil
}

func (p *Publisher) renderLocked(inputs renderInputs, dirty *dirtySet) map[uint64]*apigen.ClusterNetMap {
	rc := prepareRender(p.prefix, inputs, p.plan)
	p.rc = rc
	p.publishDiagnosticsLocked(rc.diagnostics)
	var targets []uint64
	for _, nodeID := range rc.nodeIDs {
		if dirty == nil || dirty.has(nodeID) {
			targets = append(targets, nodeID)
		}
	}
	return p.renderNodesLocked(p.appliedSeq, targets)
}

func (p *Publisher) handle(u state.WriteUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.enqueueLocked(p.applyLocked(&u))
	p.wakeWaitersLocked()
}

var mutationOrder = map[apigen.CoreEntityType]int{
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:                1,
	apigen.CoreEntityType_CORE_ENTITY_NODE:                      2,
	apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:            3,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:        4,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS: 5,
}

func (p *Publisher) applyLocked(u *state.WriteUpdate) outputs {
	out := outputs{seq: u.Seq, rows: make(map[uint64][]apigen.NodeInstance)}
	var ordered []*apigen.CoreMutation
	for i := range u.Mutations {
		if _, ok := mutationOrder[u.Mutations[i].Type()]; ok {
			ordered = append(ordered, &u.Mutations[i])
		}
	}
	p.appliedSeq = u.Seq
	if len(ordered) == 0 {
		return out
	}
	slices.SortStableFunc(ordered, func(a, b *apigen.CoreMutation) int {
		return cmp.Compare(mutationOrder[a.Type()], mutationOrder[b.Type()])
	})

	dirty := newDirtySet()
	touched := make(map[uint64]struct{})
	inputsTouched, planDirty := false, false
	for _, m := range ordered {
		switch m.Type() {
		case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
			prev, next := p.cache.applyDeployment(m)
			inputsTouched = true
			p.dirtyDeployment(dirty, prev, next, &planDirty)
		case apigen.CoreEntityType_CORE_ENTITY_NODE:
			prev, next := p.cache.applyNode(m)
			if prev == nil && next == nil {
				continue
			}
			inputsTouched = true
			p.dirtyNode(dirty, prev, next, &planDirty)
		case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
			prev, next := p.cache.applyPolicy(m)
			inputsTouched = true
			p.dirtyPolicy(dirty, prev, next)
		case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
			row, prev := p.cache.applyInstance(m)
			inputsTouched = true
			out.rows[row.Instance.NodeID] = append(out.rows[row.Instance.NodeID], p.cache.nodeInstance(row))
			touched[row.Instance.NodeID] = struct{}{}
			p.dirtyInstance(dirty, prev, row)
		case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
			p.cache.applyStatus(m)
		}
	}
	if !inputsTouched {
		return out
	}
	inputs := p.cache.inputs()
	if planDirty {
		next := renderIngressPlan(inputs)
		p.dirtyPlan(dirty, p.plan, next)
		p.plan = next
	}
	out.maps = p.renderLocked(inputs, dirty)
	var acmeNodes []uint64
	if !dirty.all {
		for nodeID := range dirty.nodes {
			touched[nodeID] = struct{}{}
		}
		for nodeID := range touched {
			acmeNodes = append(acmeNodes, nodeID)
		}
	}
	out.acme = p.refreshAcmeLocked(acmeNodes)
	return out
}

func (p *Publisher) renderNodesLocked(seq int64, targets []uint64) map[uint64]*apigen.ClusterNetMap {
	changed := make(map[uint64]*apigen.ClusterNetMap)
	var upserts []pq.NodeNetmap
	var deletes []uint64
	for _, nodeID := range targets {
		next := p.rc.renderNode(nodeID)
		content := canonicalContent(next)
		existing := p.maps[nodeID]
		if existing != nil && bytes.Equal(existing.content, content) {
			continue
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		next.DerivedFromSeq = seq
		if existing == nil {
			if row, ok := p.persisted[nodeID]; ok && row.ContentHash == hash {
				next.DerivedFromSeq = row.DerivedSeq
			}
		}
		delete(p.persisted, nodeID)
		p.maps[nodeID] = &nodeMap{current: next, content: content, hash: hash}
		changed[nodeID] = next
		upserts = append(upserts, pq.NodeNetmap{NodeID: nodeID, DerivedSeq: next.DerivedFromSeq, ContentHash: hash})
	}
	for nodeID := range p.maps {
		if _, ok := p.rc.nodes[nodeID]; !ok {
			delete(p.maps, nodeID)
			deletes = append(deletes, nodeID)
		}
	}
	for nodeID := range p.persisted {
		if _, ok := p.rc.nodes[nodeID]; !ok {
			delete(p.persisted, nodeID)
			deletes = append(deletes, nodeID)
		}
	}
	slices.SortFunc(upserts, func(a, b pq.NodeNetmap) int { return cmp.Compare(a.NodeID, b.NodeID) })
	slices.Sort(deletes)
	p.persistStamps(upserts, deletes)
	notifyAck(p.ackUpdates)
	return changed
}

func (p *Publisher) persistStamps(upserts []pq.NodeNetmap, deletes []uint64) {
	ctx := context.Background()
	q := p.store.Queries()
	for _, row := range upserts {
		if err := q.UpsertNodeNetmap(ctx, row); err != nil {
			slog.ErrorContext(ctx, "persisting network map stamp failed", "err", err, "node_id", row.NodeID)
		}
	}
	for _, nodeID := range deletes {
		if err := q.DeleteNodeNetmap(ctx, nodeID); err != nil {
			slog.ErrorContext(ctx, "deleting network map stamp failed", "err", err, "node_id", nodeID)
		}
	}
}

func (p *Publisher) enqueueLocked(out outputs) {
	updates := make(map[uint64]*apigen.NodeProjection)
	for sub := range p.subscribers {
		update, built := updates[sub.nodeID]
		if !built {
			update = p.updateFor(out, sub.nodeID)
			updates[sub.nodeID] = update
		}
		if update == nil {
			continue
		}
		select {
		case sub.ch <- update:
		default:
			slog.ErrorContext(context.Background(), fmt.Sprintf("node %d session queue overflowed; dropping it so the secondary reconnects", sub.nodeID))
			close(sub.ch)
			delete(p.subscribers, sub)
		}
	}
}

func (p *Publisher) updateFor(out outputs, nodeID uint64) *apigen.NodeProjection {
	update := &apigen.NodeProjection{Seq: out.seq, Instances: out.rows[nodeID]}
	if m := out.maps[nodeID]; m != nil {
		update.NetMap = apigen.Some(*m)
	}
	if a := out.acme[nodeID]; a != nil {
		update.Acme = apigen.Some(*a)
	}
	if len(update.Instances) == 0 && !update.NetMap.Present && !update.Acme.Present {
		return nil
	}
	return update
}

func (p *Publisher) snapshotLocked(nodeID uint64) (apigen.NodeProjection, error) {
	if _, member := p.cache.nodes[nodeID]; !member {
		return apigen.NodeProjection{}, fmt.Errorf("node %d is not a cluster member", nodeID)
	}
	return apigen.NodeProjection{Seq: p.appliedSeq, Instances: p.cache.instancesOn(nodeID), NetMap: apigen.Some(*p.maps[nodeID].current), Acme: apigen.Some(p.acmeByNode[nodeID].state)}, nil
}

// Subscribe returns the node's projection at the applied seq and a queue of
// the updates after it. The queue closes when it overflows; the subscriber
// then subscribes again for a fresh snapshot.
func (p *Publisher) Subscribe(nodeID uint64) (apigen.NodeProjection, <-chan *apigen.NodeProjection, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.snapshotLocked(nodeID)
	if err != nil {
		return apigen.NodeProjection{}, nil, func() {}, err
	}
	sub := &subscriber{nodeID: nodeID, ch: make(chan *apigen.NodeProjection, queueSize)}
	if p.closed {
		close(sub.ch)
	} else {
		p.subscribers[sub] = struct{}{}
	}
	var once sync.Once
	return snapshot, sub.ch, func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if _, ok := p.subscribers[sub]; ok {
				delete(p.subscribers, sub)
				close(sub.ch)
			}
		})
	}, nil
}

func (p *Publisher) SnapshotForNode(nodeID uint64) (apigen.NodeProjection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked(nodeID)
}

func (p *Publisher) AppliedSeq() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.appliedSeq
}

func (p *Publisher) WaitForSeq(ctx context.Context, seq int64) error {
	p.mu.Lock()
	if p.appliedSeq >= seq {
		p.mu.Unlock()
		return nil
	}
	waiter := seqWaiter{seq: seq, ch: make(chan struct{})}
	p.waiters = append(p.waiters, waiter)
	p.mu.Unlock()
	select {
	case <-waiter.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Publisher) wakeWaitersLocked() {
	p.waiters = slices.DeleteFunc(p.waiters, func(w seqWaiter) bool {
		if w.seq <= p.appliedSeq {
			close(w.ch)
			return true
		}
		return false
	})
}

func (p *Publisher) Run(ctx context.Context) {
	ctx = logu.AddTag(ctx, "NodePublisher")
	defer p.Close()
	current, updates, unsubscribe := p.acmeHolder.SnapshotAndSubscribe()
	defer unsubscribe()
	if current != nil {
		p.SetAcme(current)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-updates:
			p.SetAcme(next)
		}
	}
}

func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.mu.Unlock()
	p.unsubscribe()
	p.mu.Lock()
	for sub := range p.subscribers {
		close(sub.ch)
	}
	p.subscribers = make(map[*subscriber]struct{})
}

func (p *Publisher) Diagnostics() *apigen.IngressDiagnosticList {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.diagnostics
}

func (p *Publisher) DiagnosticsSnapshotAndSubscribe() (*apigen.IngressDiagnosticList, <-chan *apigen.IngressDiagnosticList, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan *apigen.IngressDiagnosticList, 1)
	p.diagnosticsSubs[ch] = struct{}{}
	var once sync.Once
	return p.diagnostics, ch, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.diagnosticsSubs, ch)
			p.mu.Unlock()
		})
	}
}

func (p *Publisher) publishDiagnosticsLocked(next *apigen.IngressDiagnosticList) {
	if bytes.Equal(p.diagnostics.Encode(), next.Encode()) {
		return
	}
	p.diagnostics = next
	for ch := range p.diagnosticsSubs {
		publishLatest(ch, next)
	}
}

func publishLatest[T any](ch chan T, next T) {
	select {
	case ch <- next:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- next:
		default:
		}
	}
}

func (p *Publisher) SnapshotAndSubscribeMap(nodeID uint64) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	out := make(chan *apigen.ClusterNetMap, 1)
	ctx, cancel := context.WithCancel(context.Background())
	snapshot, updates, unsubscribe := p.subscribeMember(nodeID)
	go func() {
		defer func() { unsubscribe() }()
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					if p.isClosed() {
						return
					}
					var next apigen.NodeProjection
					next, updates, unsubscribe = p.subscribeMember(nodeID)
					m := next.NetMap.Value
					publishLatest(out, &m)
					continue
				}
				if update.NetMap.Present {
					m := update.NetMap.Value
					publishLatest(out, &m)
				}
			}
		}
	}()
	initial := snapshot.NetMap.Value
	return &initial, out, cancel
}

func (p *Publisher) subscribeMember(nodeID uint64) (apigen.NodeProjection, <-chan *apigen.NodeProjection, func()) {
	snapshot, updates, unsubscribe, err := p.Subscribe(nodeID)
	if err != nil {
		panic(err)
	}
	return snapshot, updates, unsubscribe
}

func (p *Publisher) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
