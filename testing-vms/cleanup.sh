#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
source ./lib.sh

require_host_tools
delete_all_vms
rm -rf "$STATE_DIR"

log "Deleted VM harness instances and $STATE_DIR"
