package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const spaceDomainPolicyColumns = `id, data_blob, created_time, seq, event_time, author`

func scanSpaceDomainPolicy(row scanner) (*SpaceDomainPolicyEvent, error) {
	var e SpaceDomainPolicyEvent
	var blob []byte
	if err := row.Scan(&e.SpaceDomainPolicyID, &blob, &e.CreatedTime, &e.Seq, &e.EventTime, &e.Author); err != nil {
		return nil, err
	}
	value, err := apigen.DecodeSpaceDomainPolicy(blob)
	if err != nil {
		return nil, err
	}
	e.Value = *value
	e.Value.ID = e.SpaceDomainPolicyID
	return &e, nil
}

// GetSpaceDomainPolicy returns the live policy, or sql.ErrNoRows.
func (q *Queries) GetSpaceDomainPolicy(ctx context.Context, id uint64) (*SpaceDomainPolicyEvent, error) {
	return scanSpaceDomainPolicy(q.db.QueryRowContext(ctx, `SELECT `+spaceDomainPolicyColumns+` FROM space_domain_policies WHERE id = ?`, id))
}

func (q *Queries) ListSpaceDomainPolicies(ctx context.Context) ([]*SpaceDomainPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+spaceDomainPolicyColumns+` FROM space_domain_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SpaceDomainPolicyEvent
	for rows.Next() {
		e, err := scanSpaceDomainPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SpaceDomainPolicyNameEverLogged reports whether the write log ever carried
// a policy with this name, deleted ones included, so a seeded policy an
// operator removed is not seeded again.
func (q *Queries) SpaceDomainPolicyNameEverLogged(ctx context.Context, name string) (bool, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT payload FROM write_event_mutations WHERE entity_type = ? AND payload IS NOT NULL`, int64(apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		e, err := apigen.DecodeCoreEntity(payload)
		if err != nil {
			return false, err
		}
		if p := e.Value.SpaceDomainPolicy; p != nil && p.Name == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (q *Queries) reduceSpaceDomainPolicy(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, p *apigen.SpaceDomainPolicy) error {
	if p == nil {
		return fmt.Errorf("payload has no policy")
	}
	value := *p
	value.ID = 0
	return q.upsert(ctx, meta, `INSERT INTO space_domain_policies (id, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, notNullBlob(value.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteSpaceDomainPolicyRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM space_domain_policies WHERE id = ?`, id)
	return err
}
