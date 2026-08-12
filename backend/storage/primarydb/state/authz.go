package state

import (
	"context"

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
	return s.q.InsertAuthzRuleTemplateRow(context.Background(), pq.InsertAuthzRuleTemplateRowParams{
		Name:      row.Name,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
		DataBlob:  notNullBlob(row.Blob),
	})
}

func (s *Service) UpdateAuthzRuleTemplate(id int64, name string, deleted bool, blob []byte) error {
	return s.q.UpdateAuthzRuleTemplateRow(context.Background(), pq.UpdateAuthzRuleTemplateRowParams{
		Name: name, Deleted: boolToInt(deleted), DataBlob: blob, ID: id,
	})
}

func (s *Service) UpsertAuthzRuleTemplate(id int64, name string, blob []byte) error {
	return s.q.UpsertAuthzRuleTemplateRow(context.Background(), pq.UpsertAuthzRuleTemplateRowParams{
		ID: id, Name: name, DataBlob: blob,
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
	return s.q.DeleteAuthzGrantRow(context.Background(), id)
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
