package pq

import (
	"context"
)

// Hand-written deployment reads. A deployment's current desired config is its
// identity row joined with its latest deployment_spec_versions row; creation time
// and attribution come from the first version row. Version rows are
// append-only and never pruned, so both rows always exist.

// DeploymentRow is a stable identity joined with its latest version.
type DeploymentRow struct {
	DeploymentID int64
	NodeID       int64
	SpaceID      int64 // latest space version's space_id
	SpaceVersion int64 // latest space version's version
	Name         string
	DeletedAt    int64
	SpecVersion  int64
	CreatedAt    int64 // first version's created_at
	UpdatedAt    int64 // latest version's created_at
	Author       int64 // latest version's author
	SpecBlob     []byte
}

const deploymentRowSelect = `
	SELECT d.deployment_id, d.node_id, sp.space_id, sp.version, d.name, d.deleted_at,
	       v.version,
	       (SELECT f.created_at FROM deployment_spec_versions f
	        WHERE f.deployment_id = d.deployment_id ORDER BY f.version LIMIT 1),
	       v.created_at, v.author, v.spec_blob
	FROM deployments d
	JOIN deployment_spec_versions v ON v.deployment_id = d.deployment_id
	    AND v.version = (SELECT MAX(m.version) FROM deployment_spec_versions m
	                     WHERE m.deployment_id = d.deployment_id)
	JOIN deployment_space_versions sp ON sp.deployment_id = d.deployment_id
	    AND sp.version = (SELECT MAX(ms.version) FROM deployment_space_versions ms
	                      WHERE ms.deployment_id = d.deployment_id)`

type deploymentScanner interface {
	Scan(dest ...any) error
}

func scanDeploymentRow(scanner deploymentScanner) (DeploymentRow, error) {
	var r DeploymentRow
	err := scanner.Scan(&r.DeploymentID, &r.NodeID, &r.SpaceID, &r.SpaceVersion, &r.Name, &r.DeletedAt,
		&r.SpecVersion, &r.CreatedAt, &r.UpdatedAt, &r.Author, &r.SpecBlob)
	return r, err
}

func (q *Queries) GetDeployment(ctx context.Context, deploymentID int64) (DeploymentRow, error) {
	return scanDeploymentRow(q.db.QueryRowContext(ctx,
		deploymentRowSelect+` WHERE d.deployment_id = ?`, deploymentID))
}

func (q *Queries) ListAllDeployments(ctx context.Context) ([]DeploymentRow, error) {
	rows, err := q.db.QueryContext(ctx, deploymentRowSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentRow
	for rows.Next() {
		r, err := scanDeploymentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
