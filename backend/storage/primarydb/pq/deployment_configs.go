package pq

import (
	"context"
)

// Hand-written deployment config reads. A deployment's current desired config
// is its identity row joined with its latest deployment_versions row; creation
// time and attribution come from the first version row. Version rows are
// append-only and never pruned, so both rows always exist.

// DeploymentConfigRow is a stable identity joined with its latest version.
type DeploymentConfigRow struct {
	DeploymentID int64
	NodeID       int64
	SpaceID      int64 // latest space version's space_id
	SpaceVersion int64 // latest space version's version
	Name         string
	DeletedAt    int64
	Version      int64
	CreatedAt    int64 // first version's created_at
	UpdatedAt    int64 // latest version's created_at
	Author       int64 // latest version's author
	SpecBlob     []byte
}

const deploymentConfigRowSelect = `
	SELECT d.deployment_id, d.node_id, sp.space_id, sp.version, d.name, d.deleted_at,
	       v.version,
	       (SELECT f.created_at FROM deployment_versions f
	        WHERE f.deployment_id = d.deployment_id ORDER BY f.version LIMIT 1),
	       v.created_at, v.author, v.spec_blob
	FROM deployment_configs d
	JOIN deployment_versions v ON v.deployment_id = d.deployment_id
	    AND v.version = (SELECT MAX(m.version) FROM deployment_versions m
	                     WHERE m.deployment_id = d.deployment_id)
	JOIN deployment_space_versions sp ON sp.deployment_id = d.deployment_id
	    AND sp.version = (SELECT MAX(ms.version) FROM deployment_space_versions ms
	                      WHERE ms.deployment_id = d.deployment_id)`

type deploymentConfigScanner interface {
	Scan(dest ...any) error
}

func scanDeploymentConfigRow(scanner deploymentConfigScanner) (DeploymentConfigRow, error) {
	var r DeploymentConfigRow
	err := scanner.Scan(&r.DeploymentID, &r.NodeID, &r.SpaceID, &r.SpaceVersion, &r.Name, &r.DeletedAt,
		&r.Version, &r.CreatedAt, &r.UpdatedAt, &r.Author, &r.SpecBlob)
	return r, err
}

func (q *Queries) GetDeploymentConfig(ctx context.Context, deploymentID int64) (DeploymentConfigRow, error) {
	return scanDeploymentConfigRow(q.db.QueryRowContext(ctx,
		deploymentConfigRowSelect+` WHERE d.deployment_id = ?`, deploymentID))
}

func (q *Queries) ListAllDeploymentConfigs(ctx context.Context) ([]DeploymentConfigRow, error) {
	rows, err := q.db.QueryContext(ctx, deploymentConfigRowSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentConfigRow
	for rows.Next() {
		r, err := scanDeploymentConfigRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
