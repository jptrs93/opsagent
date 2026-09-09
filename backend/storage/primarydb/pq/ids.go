package pq

import (
	"context"
)

const nextAssetID = `SELECT COALESCE(MAX(asset_id), 0) + 1 FROM asset_event_log
`

func (q *Queries) NextAssetID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextAssetID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}

const nextAuthzGrantID = `SELECT COALESCE(MAX(grant_id), 0) + 1 FROM authz_grant_event_log
`

func (q *Queries) NextAuthzGrantID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextAuthzGrantID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}

const nextAuthzRuleTemplateID = `SELECT COALESCE(MAX(template_id), 0) + 1 FROM authz_rule_template_event_log
`

func (q *Queries) NextAuthzRuleTemplateID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextAuthzRuleTemplateID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}

const nextConfigID = `SELECT COALESCE(MAX(config_id), 0) + 1 FROM config_event_log
`

func (q *Queries) NextConfigID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextConfigID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}

const nextGlobalAccessRuleID = `SELECT COALESCE(MAX(rule_id), 0) + 1 FROM global_access_rule_event_log
`

func (q *Queries) NextGlobalAccessRuleID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextGlobalAccessRuleID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}

const nextNetworkPolicyID = `SELECT COALESCE(MAX(policy_id), 0) + 1 FROM network_policy_event_log
`

func (q *Queries) NextNetworkPolicyID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextNetworkPolicyID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}

const nextSecretID = `SELECT COALESCE(MAX(secret_id), 0) + 1 FROM secret_event_log
`

func (q *Queries) NextSecretID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, nextSecretID)
	var column_1 int64
	err := row.Scan(&column_1)
	return column_1, err
}
