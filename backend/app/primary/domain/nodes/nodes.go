package nodes

import (
	"cmp"
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
	NodeRolePrimary   int32 = 0
	NodeRoleSecondary int32 = 1
)

var ErrDuplicateNodeName = errors.New("node name already exists")

type Node struct {
	Seq                   int64
	Author                int32
	EventTime             int64
	EnrollmentRequestedAt int64
	ID                    int32
	Name                  string
	Identifier            string
	Status                apigen.NodeLifecycleStatus
	Roles                 []int32
	Addresses             []string
	WGPublicKey           string
	CreatedAt             time.Time
	EnrolledAt            time.Time
	AllowedSpaces         []int32
	HostAddresses         []string
}

func isMemberStatus(status apigen.NodeLifecycleStatus) bool {
	for _, s := range pq.MemberNodeStatuses {
		if int64(status) == s {
			return true
		}
	}
	return false
}
func normalizeAllowedSpaces(spaces []int32) []int32 {
	seen := map[int32]struct{}{internaldeploy.SpaceID: {}}
	out := []int32{internaldeploy.SpaceID}
	for _, id := range spaces {
		if id < 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
func EnsurePrimaryNode(store *state.Service, name, identifier string) *Node {
	ctx := context.Background()
	row, err := store.Queries().GetNodeRowByIdentifier(ctx, identifier)
	if errors.Is(err, sql.ErrNoRows) {
		err = store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
			now := time.Now().UnixMilli()
			var txErr error
			row, txErr = q.NewNode(ctx, seq, now, apigen.Node{
				Status:   apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL,
				Operator: apigen.NodeOperator{Name: name, EnrolledTime: now, Roles: []int32{NodeRolePrimary}},
				Reported: apigen.NodeReported{Identifier: identifier, HostAddresses: []string{}},
			})
			if txErr != nil {
				return nil, txErr
			}
			return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, &row.Event)), nil
		})
	}
	if err != nil {
		panic(fmt.Sprintf("ensure primary node: %v", err))
	}
	node := nodeRowToNode(row)

	return node
}

type nodeEventSpec struct {
	HostAddressesJSON     string
	EnrollmentRequestedAt int64
	Name                  string
	EnrolledTime          int64
	Status                apigen.NodeLifecycleStatus
	RolesJSON             string
	AddressesJSON         string
	WGPublicKey           string
	AllowedSpacesJSON     string
}

func nodeEventSpecOf(row pq.CurrentNode) nodeEventSpec {
	return nodeEventSpec{
		HostAddressesJSON:     nodeAddressesJSON(row.Event.Value.Reported.HostAddresses),
		EnrollmentRequestedAt: row.Event.Value.EnrollmentRequestedAt,
		Name:                  row.Event.Value.Operator.Name,
		EnrolledTime:          row.Event.Value.Operator.EnrolledTime,
		Status:                apigen.NodeLifecycleStatus(int64(row.Event.Value.Status)),
		RolesJSON:             nodeRolesJSON(row.Event.Value.Operator.Roles),
		AddressesJSON:         nodeAddressesJSON([]string{row.Event.Value.Reported.UnderlayAddress}),
		WGPublicKey:           row.Event.Value.Reported.WgPublicKey,
		AllowedSpacesJSON:     allowedSpacesJSON(row.Event.Value.Operator.AllowedSpaces),
	}
}

func parseStringList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func parseRoles(s string) []int32 {
	var out []int32
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []int32{}
	}
	return out
}

// appendNodeVersion is the node after mutate under the commit's envelope.
// Nothing is written: the caller returns the node's mutation from Commit,
// and reads inside the same commit keep seeing the row as it was.
func appendNodeVersion(seq, now int64, current pq.CurrentNode, author int32, mutate func(*nodeEventSpec)) (pq.CurrentNode, bool) {
	spec := nodeEventSpecOf(current)
	mutate(&spec)
	if spec == nodeEventSpecOf(current) {
		return current, false
	}
	next := current
	e := &next.Event
	e.Seq, e.EventTime, e.Author = seq, now, author
	e.Value.EnrollmentRequestedAt = spec.EnrollmentRequestedAt
	e.Value.Status = spec.Status
	e.Value.Operator = apigen.NodeOperator{Name: spec.Name, EnrolledTime: spec.EnrolledTime, Roles: parseRoles(spec.RolesJSON), AllowedSpaces: parseAllowedSpaces(spec.AllowedSpacesJSON)}
	addresses := parseStringList(spec.AddressesJSON)
	underlay := ""
	if len(addresses) > 0 {
		underlay = addresses[0]
	}
	e.Value.Reported = apigen.NodeReported{Identifier: current.Event.Value.Reported.Identifier, UnderlayAddress: underlay, WgPublicKey: spec.WGPublicKey, HostAddresses: parseStringList(spec.HostAddressesJSON)}
	return next, true
}
func mustAppendNodeVersion(store *state.Service, id int32, what string, mutate func(*nodeEventSpec)) *Node {
	ctx := context.Background()
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetNodeRowByID(ctx, int64(id))
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
func ReportNode(store *state.Service, identifier string, reported apigen.NodeReported) *Node {
	if reported.Identifier != "" && reported.Identifier != identifier {
		panic("node report identifier mismatch")
	}
	id, err := NodeIDByIdentifier(store.Queries(), identifier)
	if err != nil {
		panic(fmt.Sprintf("report node %q: %v", identifier, err))
	}
	return mustAppendNodeVersion(store, id, "report node", func(spec *nodeEventSpec) {
		spec.AddressesJSON = nodeAddressesJSON([]string{reported.UnderlayAddress})
		spec.WGPublicKey = reported.WgPublicKey
		if !reported.HostAddressesUnknown {
			spec.HostAddressesJSON = nodeAddressesJSON(canonicalHostAddresses(reported.HostAddresses))
		}
	})
}
func NormalizeNodeUnderlay(q *pq.Queries, identifier, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("underlay address is required")
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return "", fmt.Errorf("invalid underlay address %q", value)
	}
	addr = addr.Unmap()
	for _, node := range ListNodes(q) {
		if node == nil || node.Identifier == identifier || len(node.Addresses) == 0 || strings.TrimSpace(node.Addresses[0]) == "" {
			continue
		}
		existing, err := netip.ParseAddr(strings.TrimSpace(node.Addresses[0]))
		if err != nil || existing.Zone() != "" {
			return "", fmt.Errorf("node %d has invalid stored underlay address", node.ID)
		}
		if existing.Unmap().BitLen() != addr.BitLen() {
			return "", fmt.Errorf("underlay address family differs from cluster")
		}
	}
	return addr.String(), nil
}

func ListNodes(q *pq.Queries) []*Node {
	rows := erru.Must(q.ListNodeRows(context.Background(), pq.MemberNodeStatuses))
	out := make([]*Node, 0, len(rows))
	for _, row := range rows {
		out = append(out, nodeRowToNode(row))
	}
	return out
}

type NetworkMapInputs struct {
	Nodes            []*Node
	Instances        []apigen.ScheduledInstanceState
	Policies         []*pq.NetworkPolicyEvent
	Deployments      []*apigen.DeploymentEvent
	DeploymentSpaces map[int32]int32
	Seq              int64
}

func FetchNetworkMapInputs(store *state.Service) NetworkMapInputs {
	store.Mu.Lock()
	defer store.Mu.Unlock()
	ctx := context.Background()
	q := store.Queries()
	seq := erru.Must(q.GetGlobalSeq(ctx))
	deployments := erru.Must(q.ListActiveDeployments(ctx))
	spaces := make(map[int32]int32, len(deployments))
	for _, cfg := range deployments {
		spaces[cfg.DeploymentID] = cfg.Value.SpaceID
	}
	slices.SortFunc(deployments, func(a, b *apigen.DeploymentEvent) int { return cmp.Compare(a.DeploymentID, b.DeploymentID) })
	return NetworkMapInputs{
		Nodes:            ListNodes(store.Queries()),
		Instances:        store.FetchScheduledSnapshot(nil),
		Policies:         erru.Must(q.ListNetworkPolicies(ctx)),
		Deployments:      deployments,
		DeploymentSpaces: spaces,
		Seq:              seq,
	}
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

func canonicalHostAddresses(addresses []string) []string {
	out := make([]string, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for _, raw := range addresses {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || addr.Zone() != "" {
			continue
		}
		value := addr.Unmap().String()
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func NodeIDByIdentifier(q *pq.Queries, identifier string) (int32, error) {
	nodeID, err := q.GetNodeIDByIdentifier(context.Background(), identifier)
	return int32(nodeID), err
}

func PrimaryNodeID(q *pq.Queries) (int32, error) {
	nodeID, err := q.GetNodeIDWithRole(context.Background(), int64(NodeRolePrimary), pq.MemberNodeStatuses)
	return int32(nodeID), err
}

func RenameNode(store *state.Service, identifier, name string) (*pq.NodeEvent, error) {
	ctx := context.Background()
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetNodeRowByIdentifier(ctx, identifier)
		if err != nil {
			return nil, err
		}
		taken, err := q.CountNodesWithName(ctx, name, int64(current.Event.NodeID))
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
func SetNodeAllowedSpaces(store *state.Service, identifier string, spaces []int32) (*pq.NodeEvent, error) {
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

func updateAllNodeAllowedSpaces(ctx context.Context, q *pq.Queries, seq, now int64, fn func([]int32) []int32) ([]pq.Mutation, error) {
	rows, err := q.ListNodeRows(ctx, pq.AllNodeStatuses)
	if err != nil {
		return nil, err
	}
	var events []pq.Mutation
	for _, current := range rows {
		if current.Event.Value.Status == apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED {
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
		previous, err := q.GetLatestNodeStatus(ctx, int32(id))
		if errors.Is(err, sql.ErrNoRows) {
			previous = &apigen.NodeStatus{NodeID: int32(id)}
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
		if status == *previous {
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
	var addresses []string
	if e.Value.Reported.UnderlayAddress != "" {
		addresses = []string{e.Value.Reported.UnderlayAddress}
	}
	return &Node{
		Seq: e.Seq, Author: e.Author, EventTime: e.EventTime,
		EnrollmentRequestedAt: e.Value.EnrollmentRequestedAt, ID: e.NodeID, Name: e.Value.Operator.Name, Identifier: e.Value.Reported.Identifier,
		Status: e.Value.Status, Roles: e.Value.Operator.Roles, Addresses: addresses, WGPublicKey: e.Value.Reported.WgPublicKey,
		CreatedAt: time.UnixMilli(e.CreatedTime), EnrolledAt: millisToTime(e.Value.Operator.EnrolledTime), AllowedSpaces: e.Value.Operator.AllowedSpaces,
		HostAddresses: e.Value.Reported.HostAddresses,
	}
}

func nodeRolesJSON(roles []int32) string {
	b := erru.Must(json.Marshal(roles))
	return string(b)
}

func nodeAddressesJSON(addresses []string) string {
	b := erru.Must(json.Marshal(addresses))
	return string(b)
}

func parseAllowedSpaces(s string) []int32 {
	var spaces []int32
	if err := json.Unmarshal([]byte(s), &spaces); err != nil {
		return normalizeAllowedSpaces(nil)
	}
	return normalizeAllowedSpaces(spaces)
}

func allowedSpacesJSON(spaces []int32) string {
	b := erru.Must(json.Marshal(normalizeAllowedSpaces(spaces)))
	return string(b)
}

func (node *Node) Reported() apigen.NodeReported {
	underlay := ""
	if len(node.Addresses) > 0 {
		underlay = node.Addresses[0]
	}
	return apigen.NodeReported{Identifier: node.Identifier, UnderlayAddress: underlay, WgPublicKey: node.WGPublicKey, HostAddresses: node.HostAddresses}
}

func enrollmentRequestFromRow(r pq.CurrentNode) *apigen.EnrollmentRequestStatus {
	status := r.Event.Value.Status
	if isMemberStatus(status) &&
		r.Event.Value.EnrollmentRequestedAt != 0 {
		status = apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED
	}
	createdAt := r.Event.CreatedTime

	if r.Event.Value.EnrollmentRequestedAt != 0 {
		createdAt = r.Event.Value.EnrollmentRequestedAt

	}
	return &apigen.EnrollmentRequestStatus{
		ID:                  r.Event.NodeID,
		CreatedAt:           millisToTime(createdAt),
		RequestingIpAddress: r.Status.RemoteAddress,
		RequestingMachineID: r.Event.Value.Reported.Identifier,
		OpendeployVersion:   r.Status.OpendeployVersion,
		UnderlayAddress:     r.Event.Value.Reported.UnderlayAddress,
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
