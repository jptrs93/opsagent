package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) ListAuthzGlobalRules(ctx context.Context) ([]*apigen.AuthzGlobalRuleRecord, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.rule_id, e.name, e.author, e.created_time, e.data_blob FROM global_access_rule_event_log e
JOIN (SELECT rule_id, MAX(version) AS version
      FROM global_access_rule_event_log GROUP BY rule_id) latest
  ON latest.rule_id = e.rule_id AND latest.version = e.version
WHERE e.event_type != 3
ORDER BY e.rule_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.AuthzGlobalRuleRecord
	for rows.Next() {
		var e apigen.AuthzGlobalRuleRecord
		var blob []byte
		if err := rows.Scan(&e.ID, &e.Name, &e.Author, &e.CreatedAt, &blob); err != nil {
			return nil, err
		}
		e.Rule, err = apigen.DecodeAuthzGlobalRule(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
