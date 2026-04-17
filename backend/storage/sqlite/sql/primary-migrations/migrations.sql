-- Primary-side startup migrations. Each statement should be idempotent —
-- this file is executed on every primary startup after the schema has been
-- applied. Remove entries from this file once the bad data they target has
-- been cleaned up in all deployments.

-- 2026-04: wipe accumulated phantom status rows produced by the old
-- primary-owned seq_no scheme. The replication path now uses the
-- secondary's seq_no as the authoritative identity, and the secondary
-- backfills canonical history on reconnect via the snapshot+backlog
-- replay. Deployments on the primary's own machine (parameter :machine)
-- are preserved because they are written directly by the primary's
-- engine, not via replication.
DELETE FROM deployment_status_history
WHERE deployment_id IN (
    SELECT deployment_id FROM deployment_configs WHERE machine != ?
);

DELETE FROM deployment_status
WHERE deployment_id IN (
    SELECT deployment_id FROM deployment_configs WHERE machine != ?
);
