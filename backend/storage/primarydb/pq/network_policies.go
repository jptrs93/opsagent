package pq

import (
	"context"
)

type NetworkPolicyRow struct {
	ID        int64
	DeletedAt int64
	Version   int64
	Author    int64
	CreatedAt int64
	DataBlob  []byte
}

func (q *Queries) ListNetworkPolicyRows(ctx context.Context) ([]NetworkPolicyRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT p.id, p.deleted_at, v.version, v.author, v.created_at, v.data_blob
		FROM network_policies p
		JOIN network_policy_versions v ON v.id =
		    (SELECT MAX(m.id) FROM network_policy_versions m WHERE m.policy_id = p.id)
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkPolicyRow
	for rows.Next() {
		var row NetworkPolicyRow
		if err := rows.Scan(&row.ID, &row.DeletedAt, &row.Version, &row.Author, &row.CreatedAt, &row.DataBlob); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
