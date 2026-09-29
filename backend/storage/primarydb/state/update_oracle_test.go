package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func rereadUpdateAtSeq(ctx context.Context, q *pq.Queries, seq int64) Update {
	core := Update{Seq: seq}
	core.DeploymentEvents = erru.Must(q.ListDeploymentEventsAtSeq(ctx, seq))
	core.ScheduledInstanceEvents = erru.Must(q.ListScheduledInstanceEventsAtSeq(ctx, seq))
	core.SecretEvents = erru.Must(q.ListSecretEventsAtSeq(ctx, seq))
	core.ConfigEvents = erru.Must(q.ListConfigEventsAtSeq(ctx, seq))
	for _, row := range erru.Must(q.ListAssetEventsAtSeq(ctx, seq)) {
		core.AssetEvents = append(core.AssetEvents, ptru.To(row))
	}
	for _, row := range erru.Must(q.ListNetworkPolicyEventsAtSeq(ctx, seq)) {
		core.NetworkPolicyEvents = append(core.NetworkPolicyEvents, row)
	}
	for _, event := range erru.Must(q.ListNodeEventsAtSeq(ctx, seq)) {
		core.NodeEvents = append(core.NodeEvents, event)
	}
	for _, row := range erru.Must(q.ListAuthzGrantEventsAtSeq(ctx, seq)) {
		core.AuthzGrantEvents = append(core.AuthzGrantEvents, ptru.To(row))
	}
	if len(erru.Must(q.ListAuthzRuleTemplateEventsAtSeq(ctx, seq))) != 0 {
		core.AuthzRuleTemplates = &apigen.AuthzRuleTemplateList{Items: erru.Must(q.ListAuthzRuleTemplates(ctx))}
	}
	if len(erru.Must(q.ListGlobalAccessRuleEventsAtSeq(ctx, seq))) != 0 {
		core.AuthzGlobalRules = &apigen.AuthzGlobalRuleList{Items: erru.Must(q.ListAuthzGlobalRules(ctx))}
	}
	core.InstanceStatuses = erru.Must(q.ListScheduledInstanceStatusesAtSeq(ctx, seq))
	core.NodeStatuses = erru.Must(q.ListNodeStatusesAtSeq(ctx, seq))
	for _, row := range erru.Must(q.ListAgentSessionsAtSeq(ctx, seq)) {
		core.AgentSessions = append(core.AgentSessions, row.Proto())
	}
	for _, row := range erru.Must(q.ListUserSessionsAtSeq(ctx, seq)) {
		core.UserSessions = append(core.UserSessions, row.Proto())
	}
	core.Spaces = pointers(erru.Must(q.ListSpacesAtSeq(ctx, seq)))
	core.Users = pointers(erru.Must(q.ListUsersAtSeq(ctx, seq)))
	core.ValueDirectories = erru.Must(q.ListValueDirectoriesAtSeq(ctx, seq))
	core.AssetDirectories = pointers(erru.Must(q.ListAssetDirectoriesAtSeq(ctx, seq)))
	if cfg, err := q.GetSystemConfigAtSeq(ctx, seq); err == nil {
		core.SystemConfig = cfg
	} else if !errors.Is(err, sql.ErrNoRows) {
		panic(err)
	}
	return core
}

func canonicalUpdate(update Update) []byte {
	actual := update
	actual.InstanceStatuses = nil
	for _, st := range update.InstanceStatuses {
		cp := *st
		cp.Runner.RunningVersion = ""
		actual.InstanceStatuses = append(actual.InstanceStatuses, &cp)
	}
	return actual.Encode()
}

func assertUpdateMatchesRows(t *testing.T, s *Service, update Update) {
	t.Helper()
	seq := erru.Must(s.q.GetGlobalSeq(context.Background()))
	if update.Seq != seq {
		t.Fatalf("published sequence = %d, database sequence = %d", update.Seq, seq)
	}
	expected := rereadUpdateAtSeq(context.Background(), s.q, update.Seq)
	if !bytes.Equal(canonicalUpdate(update), canonicalUpdate(expected)) {
		t.Fatalf("published update differs from persisted rows at seq %d\ngot: %+v\nwant: %+v", update.Seq, update, expected)
	}
}
