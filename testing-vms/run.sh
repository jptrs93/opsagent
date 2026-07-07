#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
source ./lib.sh

RESET=${RESET:-true}

require_host_tools
ensure_mock_artifacts

if [[ "$RESET" == "true" ]]; then
  delete_all_vms
  rm -rf "$STATE_DIR" "$RESULTS_DIR" "$REPORT_DIR"
  rm -rf "$SCRIPT_DIR"/bootstrap-*-chromium "$SCRIPT_DIR"/.last-run.json
fi

mkdir -p "$STATE_DIR" "$RESULTS_DIR" "$REPORT_DIR"

build_self_opendeploy
ensure_test_certs
start_all_vms
sync_hosts_all
install_test_ca_all
setup_repo_mirror_vm
sync_hosts_all
install_cluster

if run_playwright_flows; then
  if [[ "$BACKUP_RESTORE" == "true" ]]; then
    OPD_BACKUP_RESTORE_STATE_HOST="$RESULTS_DIR/backup-restore.env" \
      OPD_RESTORE_INSTALL_VERSION="$UPGRADE_VERSION" \
      ./backup_restore.sh
  fi
  log "VM e2e run complete"
else
  status=$?
  log "VM e2e run failed; results copied to $RESULTS_DIR"
  exit "$status"
fi
