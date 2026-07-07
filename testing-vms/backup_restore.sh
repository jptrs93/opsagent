#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
source ./lib.sh

STATE_FILE=${OPD_BACKUP_RESTORE_STATE_HOST:-$RESULTS_DIR/backup-restore.env}
RESTORE_WAIT_SECONDS=${OPD_BACKUP_RESTORE_WAIT_SECONDS:-20}
RESTORE_INSTALL_VERSION=${OPD_RESTORE_INSTALL_VERSION:-$UPGRADE_VERSION}

[[ -f "$STATE_FILE" ]] || die "backup restore state file not found: $STATE_FILE"

set -a
source "$STATE_FILE"
set +a

for name in \
  OPD_RESTORE_S3_ACCESS_KEY_ID \
  OPD_RESTORE_S3_SECRET_ACCESS_KEY \
  OPD_RESTORE_S3_BUCKET \
  OPD_RESTORE_S3_PATH \
  OPD_RESTORE_S3_REGION \
  OPD_RESTORE_S3_ENDPOINT \
  OPD_RESTORE_RECOVERY_CODE; do
  [[ -n "${!name:-}" ]] || die "$name is required in $STATE_FILE"
done

require_host_tools

log "Restarting primary so backup replication reads updated settings"
vm_exec "$PRIMARY_NAME" sudo systemctl restart opendeploy.service
wait_for_service "$PRIMARY_NAME" opendeploy.service
wait_for_healthz "$PRIMARY_NAME"

log "Waiting ${RESTORE_WAIT_SECONDS}s for backup replication"
sleep "$RESTORE_WAIT_SECONDS"

log "Destroying primary VM"
vm_exec "$PRIMARY_NAME" sudo systemctl stop opendeploy.service >/dev/null 2>&1 || true
delete_vm "$PRIMARY_NAME"

log "Starting fresh primary VM for restore"
start_vm "$PRIMARY_NAME" node
sync_hosts_all
install_test_ca "$PRIMARY_NAME"
wait_for_systemd "$PRIMARY_NAME"
if [[ "$LOCAL_TEST" != "true" ]]; then
  INSTALL_VERSION="$RESTORE_INSTALL_VERSION"
fi
download_opendeploy "$PRIMARY_NAME"
limactl copy "$SERVER_BUNDLE" "$PRIMARY_NAME:/tmp/opendeploy-web.pem"

cmd=(
  opendeploy install primary
  --web-listen :443
  --acme-hosts "$WEB_HOST"
  --web-tls-self-managed true
  --web-tls-cert-pem-file /tmp/opendeploy-web.pem
  --restore-backup true
  --restore-s3-access-key-id "$OPD_RESTORE_S3_ACCESS_KEY_ID"
  --restore-s3-secret-access-key "$OPD_RESTORE_S3_SECRET_ACCESS_KEY"
  --restore-s3-bucket "$OPD_RESTORE_S3_BUCKET"
  --restore-s3-path "$OPD_RESTORE_S3_PATH"
  --restore-s3-region "$OPD_RESTORE_S3_REGION"
  --restore-s3-endpoint "$OPD_RESTORE_S3_ENDPOINT"
  --recovery-code "$OPD_RESTORE_RECOVERY_CODE"
)

if [[ "$LOCAL_TEST" == "true" ]]; then
  cmd+=(--use-self)
else
  cmd+=(--version "$RESTORE_INSTALL_VERSION")
fi

log "Installing restored primary"
vm_exec "$PRIMARY_NAME" sudo "${cmd[@]}"
configure_github_token "$PRIMARY_NAME"
vm_exec "$PRIMARY_NAME" sudo usermod -aG nix-users opendeploy
vm_exec "$PRIMARY_NAME" sudo systemctl restart opendeploy.service
wait_for_service "$PRIMARY_NAME" opendeploy.service
wait_for_healthz "$PRIMARY_NAME"

log "Backup restore extension complete"
