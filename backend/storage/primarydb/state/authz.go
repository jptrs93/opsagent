package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func notNullBlob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func (s *Service) ListAuthzRuleTemplates() ([]authz.RuleTemplateRow, error) {
	rows, err := s.q.ListAuthzRuleTemplateRows(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]authz.RuleTemplateRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, authz.RuleTemplateRow{
			ID:        row.ID,
			Name:      row.Name,
			Builtin:   row.Builtin != 0,
			Deleted:   row.Deleted != 0,
			CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt,
			Blob:      row.DataBlob,
		})
	}
	return out, nil
}

func (s *Service) InsertAuthzRuleTemplate(row authz.RuleTemplateRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		id, err = q.CreateAuthzRuleTemplate(ctx, row.Name)
		if err != nil {
			return err
		}
		return q.AppendAuthzRuleTemplateVersion(ctx, pq.AppendAuthzRuleTemplateVersionParams{
			TemplateID: id,
			CreatedAt:  row.CreatedAt,
			CreatedBy:  row.CreatedBy,
			DataBlob:   notNullBlob(row.Blob),
		})
	})
	return id, err
}

func (s *Service) UpdateAuthzRuleTemplate(id int64, name string, blob []byte, updatedBy, updatedAt int64) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.UpdateAuthzRuleTemplateName(ctx, pq.UpdateAuthzRuleTemplateNameParams{
			Name: name, ID: id,
		}); err != nil {
			return err
		}
		return q.AppendAuthzRuleTemplateVersion(ctx, pq.AppendAuthzRuleTemplateVersionParams{
			TemplateID: id,
			CreatedAt:  updatedAt,
			CreatedBy:  updatedBy,
			DataBlob:   notNullBlob(blob),
		})
	})
}

func (s *Service) DeleteAuthzRuleTemplate(id int64) error {
	return s.q.SetAuthzRuleTemplateDeleted(context.Background(), id)
}

// UpsertAuthzRuleTemplate seeds a builtin template. It runs on every startup,
// so a version is appended only when the content actually changed.
func (s *Service) UpsertAuthzRuleTemplate(id int64, name string, blob []byte) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.UpsertAuthzRuleTemplateIdentity(ctx, pq.UpsertAuthzRuleTemplateIdentityParams{
			ID: id, Name: name,
		}); err != nil {
			return err
		}
		latest, err := q.GetLatestAuthzRuleTemplateVersionBlob(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && bytes.Equal(latest, blob) {
			return nil
		}
		return q.AppendAuthzRuleTemplateVersion(ctx, pq.AppendAuthzRuleTemplateVersionParams{
			TemplateID: id,
			DataBlob:   notNullBlob(blob),
		})
	})
}

func (s *Service) ListAuthzGrants() ([]authz.GrantRow, error) {
	rows, err := s.q.ListAuthzGrantRows(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]authz.GrantRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, authz.GrantRow{
			ID:         row.ID,
			UserID:     row.UserID,
			TemplateID: row.TemplateID,
			CreatedBy:  row.CreatedBy,
			CreatedAt:  row.CreatedAt,
			Blob:       row.DataBlob,
		})
	}
	return out, nil
}

func (s *Service) InsertAuthzGrant(row authz.GrantRow) (int64, error) {
	return s.q.InsertAuthzGrantRow(context.Background(), pq.InsertAuthzGrantRowParams{
		UserID:     row.UserID,
		TemplateID: row.TemplateID,
		CreatedBy:  row.CreatedBy,
		CreatedAt:  row.CreatedAt,
		DataBlob:   notNullBlob(row.Blob),
	})
}

func (s *Service) DeleteAuthzGrant(id int64) error {
	return s.q.SetAuthzGrantDeleted(context.Background(), id)
}

func (s *Service) ListAuthzGlobalRules() ([]authz.GlobalRuleRow, error) {
	rows, err := s.q.ListGlobalAccessRuleRows(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]authz.GlobalRuleRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, authz.GlobalRuleRow{
			ID:        row.ID,
			Name:      row.Name,
			CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt,
			Blob:      row.DataBlob,
		})
	}
	return out, nil
}

func (s *Service) InsertAuthzGlobalRule(row authz.GlobalRuleRow) (int64, error) {
	return s.q.InsertGlobalAccessRuleRow(context.Background(), pq.InsertGlobalAccessRuleRowParams{
		Name:      row.Name,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
		DataBlob:  notNullBlob(row.Blob),
	})
}

func (s *Service) DeleteAuthzGlobalRule(id int64) error {
	return s.q.DeleteGlobalAccessRuleRow(context.Background(), id)
}
