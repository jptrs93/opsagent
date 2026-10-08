package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const (
	NodeRolePrimary   = apigen.NodeRole_NODE_ROLE_PRIMARY
	NodeRoleSecondary = apigen.NodeRole_NODE_ROLE_SECONDARY
)

var ErrDuplicateNodeName = errors.New("node name already exists")
var ErrUnderlayAddressRequired = errors.New("underlay address is required")

type Node struct {
	Seq                   int64
	Author                int64
	EventTime             int64
	EnrollmentRequestedAt int64
	ID                    uint64
	Name                  string
	Identifier            string
	Status                apigen.NodeLifecycleStatus
	Roles                 []apigen.NodeRole
	UnderlayAddress       apigen.IpAddress
	WGPublicKey           string
	CreatedAt             time.Time
	EnrolledAt            time.Time
	AllowedSpaces         []uint64
	HostAddresses         []apigen.IpAddress
}

func isMemberStatus(status apigen.NodeLifecycleStatus) bool {
	for _, s := range pq.MemberNodeStatuses {
		if int64(status) == s {
			return true
		}
	}
	return false
}
func normalizeAllowedSpaces(spaces []uint64) []uint64 {
	seen := map[uint64]struct{}{internaldeploy.SpaceID: {}}
	out := []uint64{internaldeploy.SpaceID}
	for _, id := range spaces {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func EnsurePrimaryNode(store *state.Service, name, identifier string, underlay netip.Addr, wgPublicKey string) *Node {
	ctx := context.Background()
	_, err := store.Queries().GetNodeRowByIdentifier(ctx, identifier)
	if err == nil {
		return ReportNode(store, identifier, apigen.NodeReported{Identifier: identifier, UnderlayAddress: apigen.AddrOf(underlay), WgPublicKey: wgPublicKey, HostAddressesUnknown: true})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("ensure primary node: %v", err))
	}
	var row pq.CurrentNode
	err = store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		now := time.Now().UnixMilli()
		var txErr error
		row, txErr = q.NewNode(ctx, seq, now, apigen.Node{
			Status:   apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL,
			Operator: apigen.NodeOperator{Name: name, EnrolledTime: millisToMaybe(now), Roles: []apigen.NodeRole{NodeRolePrimary}},
			Reported: apigen.NodeReported{Identifier: identifier, UnderlayAddress: apigen.AddrOf(underlay), WgPublicKey: wgPublicKey, HostAddresses: []apigen.IpAddress{}},
		})
		if txErr != nil {
			return nil, txErr
		}
		return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, &row.Event)), nil
	})
	if err != nil {
		panic(fmt.Sprintf("ensure primary node: %v", err))
	}
	return nodeRowToNode(row)
}

type nodeEventSpec struct {
	HostAddressesJSON     string
	EnrollmentRequestedAt int64
	Name                  string
	EnrolledTime          int64
	Status                apigen.NodeLifecycleStatus
	RolesJSON             string
	UnderlayAddress       netip.Addr
	WGPublicKey           string
	AllowedSpacesJSON     string
}

func nodeEventSpecOf(row pq.CurrentNode) nodeEventSpec {
	return nodeEventSpec{
		HostAddressesJSON:     nodeAddressesJSON(row.Event.Value.Reported.HostAddresses),
		EnrollmentRequestedAt: maybeToMillis(row.Event.Value.EnrollmentRequestedAt),
		Name:                  row.Event.Value.Operator.Name,
		EnrolledTime:          maybeToMillis(row.Event.Value.Operator.EnrolledTime),
		Status:                row.Event.Value.Status,
		RolesJSON:             nodeRolesJSON(row.Event.Value.Operator.Roles),
		UnderlayAddress:       row.Event.Value.Reported.UnderlayAddress.Addr(),
		WGPublicKey:           row.Event.Value.Reported.WgPublicKey,
		AllowedSpacesJSON:     allowedSpacesJSON(row.Event.Value.Operator.AllowedSpaces),
	}
}

func parseAddressList(s string) []apigen.IpAddress {
	var raw []string
	_ = json.Unmarshal([]byte(s), &raw)
	out := make([]apigen.IpAddress, 0, len(raw))
	for _, item := range raw {
		if addr, err := apigen.ParseAddr(item); err == nil {
			out = append(out, addr)
		}
	}
	return out
}

func parseRoles(s string) []apigen.NodeRole {
	var out []apigen.NodeRole
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []apigen.NodeRole{}
	}
	return out
}

// appendNodeVersion is the node after mutate under the commit's envelope.
// Nothing is written: the caller returns the node's mutation from Commit,
// and reads inside the same commit keep seeing the row as it was.
func appendNodeVersion(seq, now int64, current pq.CurrentNode, author int64, mutate func(*nodeEventSpec)) (pq.CurrentNode, bool) {
	spec := nodeEventSpecOf(current)
	mutate(&spec)
	if spec == nodeEventSpecOf(current) {
		return current, false
	}
	next := current
	e := &next.Event
	e.Seq, e.EventTime, e.Author = seq, now, author
	e.Value.EnrollmentRequestedAt = millisToMaybe(spec.EnrollmentRequestedAt)
	e.Value.Status = spec.Status
	e.Value.Operator = apigen.NodeOperator{Name: spec.Name, EnrolledTime: millisToMaybe(spec.EnrolledTime), Roles: parseRoles(spec.RolesJSON), AllowedSpaces: parseAllowedSpaces(spec.AllowedSpacesJSON)}
	e.Value.Reported = apigen.NodeReported{Identifier: current.Event.Value.Reported.Identifier, UnderlayAddress: apigen.AddrOf(spec.UnderlayAddress), WgPublicKey: spec.WGPublicKey, HostAddresses: parseAddressList(spec.HostAddressesJSON)}
	return next, true
}
func mustAppendNodeVersion(store *state.Service, id uint64, what string, mutate func(*nodeEventSpec)) *Node {
	ctx := context.Background()
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetNodeRowByID(ctx, id)
		if err != nil {
			return nil, err
		}
		var applied bool
		row, applied = appendNodeVersion(seq, time.Now().UnixMilli(), current, 0, mutate)
		if !applied {
			return nil, nil
		}
		return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, &row.Event)), nil
	})
	if err != nil {
		panic(fmt.Sprintf("%s: %v", what, err))
	}
	return nodeRowToNode(row)
}

// ReportNode records what the node says about itself. A report without a
// valid underlay address keeps the stored one: the row cannot carry none.
func ReportNode(store *state.Service, identifier string, reported apigen.NodeReported) *Node {
	if reported.Identifier != "" && reported.Identifier != identifier {
		panic("node report identifier mismatch")
	}
	id, err := NodeIDByIdentifier(store.Queries(), identifier)
	if err != nil {
		panic(fmt.Sprintf("report node %q: %v", identifier, err))
	}
	return mustAppendNodeVersion(store, id, "report node", func(spec *nodeEventSpec) {
		if underlay := reported.UnderlayAddress.Addr(); underlay.IsValid() {
			spec.UnderlayAddress = underlay
		}
		spec.WGPublicKey = reported.WgPublicKey
		if !reported.HostAddressesUnknown {
			spec.HostAddressesJSON = nodeAddressesJSON(canonicalHostAddresses(reported.HostAddresses))
		}
	})
}
func NormalizeNodeUnderlay(q *pq.Queries, identifier, raw string) (apigen.IpAddress, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return apigen.IpAddress{}, ErrUnderlayAddressRequired
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return apigen.IpAddress{}, fmt.Errorf("invalid underlay address %q", value)
	}
	addr = addr.Unmap()
	for _, node := range ListNodes(q) {
		if node == nil || node.Identifier == identifier {
			continue
		}
		existing := node.UnderlayAddress.Addr()
		if !existing.IsValid() {
			continue
		}
		if existing.Unmap().BitLen() != addr.BitLen() {
			return apigen.IpAddress{}, fmt.Errorf("underlay address family differs from cluster")
		}
	}
	return apigen.AddrOf(addr), nil
}

func ListNodes(q *pq.Queries) []*Node {
	rows := erru.Must(q.ListNodeRows(context.Background(), pq.MemberNodeStatuses))
	out := make([]*Node, 0, len(rows))
	for _, row := range rows {
		out = append(out, nodeRowToNode(row))
	}
	return out
}

func SetNodeStatusByIdentifier(store *state.Service, identifier string, connected bool, connectedAt time.Time) {
	ctx := context.Background()
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		now := time.Now().UnixMilli()
		status, err := q.NodeConnectionStatus(ctx, identifier, connected, time.UnixMilli(connectedAt.UnixMilli()))
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.NodeStatusMutation(seq, now, status)), nil
	})
	if err == nil {
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("set node status %q: %v", identifier, err))
	}
}

func canonicalHostAddresses(addresses []apigen.IpAddress) []apigen.IpAddress {
	addrs := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, raw := range addresses {
		addr := raw.Addr()
		if !addr.IsValid() || addr.Zone() != "" {
			continue
		}
		addr = addr.Unmap()
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return strings.Compare(a.String(), b.String()) })
	out := make([]apigen.IpAddress, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, apigen.AddrOf(addr))
	}
	return out
}

func NodeIDByIdentifier(q *pq.Queries, identifier string) (uint64, error) {
	return q.GetNodeIDByIdentifier(context.Background(), identifier)
}

func PrimaryNodeID(q *pq.Queries) (uint64, error) {
	return q.GetNodeIDWithRole(context.Background(), NodeRolePrimary, pq.MemberNodeStatuses)
}

func RenameNode(store *state.Service, identifier, name string) (*pq.NodeEvent, error) {
	ctx := context.Background()
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetNodeRowByIdentifier(ctx, identifier)
		if err != nil {
			return nil, err
		}
		taken, err := q.CountNodesWithName(ctx, name, current.Event.NodeID)
		if err != nil {
			return nil, err
		}
		if taken > 0 {
			return nil, ErrDuplicateNodeName
		}
		var applied bool
		row, applied = appendNodeVersion(seq, time.Now().UnixMilli(), current, 0, func(spec *nodeEventSpec) { spec.Name = name })
		if !applied {
			return nil, nil
		}
		return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, &row.Event)), nil
	})
	if err != nil {
		return nil, err
	}
	return &row.Event, nil
}
func SetNodeAllowedSpaces(store *state.Service, identifier string, spaces []uint64) (*pq.NodeEvent, error) {
	ctx := context.Background()
	allowed := allowedSpacesJSON(spaces)
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetNodeRowByIdentifier(ctx, identifier)
		if err != nil {
			return nil, err
		}
		var applied bool
		row, applied = appendNodeVersion(seq, time.Now().UnixMilli(), current, 0, func(spec *nodeEventSpec) { spec.AllowedSpacesJSON = allowed })
		if !applied {
			return nil, nil
		}
		return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, &row.Event)), nil
	})
	if err != nil {
		return nil, err
	}
	return &row.Event, nil
}

func updateAllNodeAllowedSpaces(ctx context.Context, q *pq.Queries, seq, now int64, fn func([]uint64) []uint64) ([]pq.Mutation, error) {
	rows, err := q.ListNodeRows(ctx, pq.AllNodeStatuses)
	if err != nil {
		return nil, err
	}
	var events []pq.Mutation
	for _, current := range rows {
		if current.Event.Value.Status == apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_EVICTED {
			continue
		}
		row, changed := appendNodeVersion(seq, now, current, 0, func(spec *nodeEventSpec) {
			spec.AllowedSpacesJSON = allowedSpacesJSON(fn(parseAllowedSpaces(allowedSpacesJSON(current.Event.Value.Operator.AllowedSpaces))))
		})
		if changed {
			events = append(events, pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, &row.Event))
		}
	}
	return events, nil
}
func UpdateNodeObservedMeta(store *state.Service, identifier, remoteAddress, opendeployVersion, runtimeVersions string) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.GetNodeIDByIdentifier(ctx, identifier)
		if err != nil {
			return nil, err
		}
		previous, err := q.GetLatestNodeStatus(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			previous = &apigen.NodeStatus{NodeID: id}
		} else if err != nil {
			return nil, err
		}
		status := *previous
		if remoteAddress != "" {
			status.RemoteAddress = remoteAddress
		}
		if opendeployVersion != "" {
			status.OpendeployVersion = opendeployVersion
		}
		if runtimeVersions != "" {
			status.RuntimeVersions = runtimeVersions
		}
		if status.RemoteAddress == previous.RemoteAddress && status.OpendeployVersion == previous.OpendeployVersion && status.RuntimeVersions == previous.RuntimeVersions {
			return nil, nil
		}
		status.BumpUpdatedAt()
		return pq.NewUpdate(pq.NodeStatusMutation(seq, time.Now().UnixMilli(), &status)), nil
	}); err != nil {
		panic(err)
	}
}

func nodeRowToNode(r pq.CurrentNode) *Node {
	e := r.Event
	return &Node{
		Seq: e.Seq, Author: e.Author, EventTime: e.EventTime,
		EnrollmentRequestedAt: maybeToMillis(e.Value.EnrollmentRequestedAt), ID: e.NodeID, Name: e.Value.Operator.Name, Identifier: e.Value.Reported.Identifier,
		Status: e.Value.Status, Roles: e.Value.Operator.Roles, UnderlayAddress: e.Value.Reported.UnderlayAddress, WGPublicKey: e.Value.Reported.WgPublicKey,
		CreatedAt: time.UnixMilli(e.CreatedTime), EnrolledAt: e.Value.Operator.EnrolledTime.Value, AllowedSpaces: e.Value.Operator.AllowedSpaces,
		HostAddresses: e.Value.Reported.HostAddresses,
	}
}

func nodeRolesJSON(roles []apigen.NodeRole) string {
	b := erru.Must(json.Marshal(roles))
	return string(b)
}

func nodeAddressesJSON(addresses []apigen.IpAddress) string {
	out := make([]string, 0, len(addresses))
	for _, a := range addresses {
		if addr := a.Addr(); addr.IsValid() {
			out = append(out, addr.String())
		}
	}
	b := erru.Must(json.Marshal(out))
	return string(b)
}

func parseAllowedSpaces(s string) []uint64 {
	var spaces []uint64
	if err := json.Unmarshal([]byte(s), &spaces); err != nil {
		return normalizeAllowedSpaces(nil)
	}
	return normalizeAllowedSpaces(spaces)
}

func allowedSpacesJSON(spaces []uint64) string {
	b := erru.Must(json.Marshal(normalizeAllowedSpaces(spaces)))
	return string(b)
}

func (node *Node) Reported() apigen.NodeReported {
	return apigen.NodeReported{Identifier: node.Identifier, UnderlayAddress: node.UnderlayAddress, WgPublicKey: node.WGPublicKey, HostAddresses: node.HostAddresses}
}

func enrollmentRequestFromRow(r pq.CurrentNode) *apigen.EnrollmentRequestStatus {
	status := r.Event.Value.Status
	if isMemberStatus(status) &&
		r.Event.Value.EnrollmentRequestedAt.Present {
		status = apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_REQUESTED
	}
	createdAt := r.Event.CreatedTime

	if r.Event.Value.EnrollmentRequestedAt.Present {
		createdAt = r.Event.Value.EnrollmentRequestedAt.Value.UnixMilli()

	}
	underlay := ""
	if addr := r.Event.Value.Reported.UnderlayAddress.Addr(); addr.IsValid() {
		underlay = addr.String()
	}
	return &apigen.EnrollmentRequestStatus{
		ID:                  r.Event.NodeID,
		CreatedAt:           millisToTime(createdAt),
		RequestingIpAddress: r.Status.RemoteAddress,
		RequestingMachineID: r.Event.Value.Reported.Identifier,
		OpendeployVersion:   r.Status.OpendeployVersion,
		UnderlayAddress:     underlay,
		Status:              status,
		IsConnected:         r.Status.IsConnected,
	}
}

func millisToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func millisToMaybe(ms int64) apigen.Maybe[time.Time] {
	return apigen.TimeOf(millisToTime(ms))
}

func maybeToMillis(t apigen.Maybe[time.Time]) int64 {
	if !t.Present {
		return 0
	}
	return t.Value.UnixMilli()
}
