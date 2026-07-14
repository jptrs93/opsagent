#!/usr/bin/env bash
set -euo pipefail

vm="${OPD_VM_PRIMARY:-opendeploy-primary}"
local_port="${OPD_PLAYWRIGHT_HOST_PORT:-8443}"
web_host="${OPD_WEB_HOST:-primary.opendeploy.test}"
ssh_config="${HOME}/.lima/${vm}/ssh.config"

if [[ ! -s "$ssh_config" ]]; then
  printf 'Lima SSH config not found: %s\nRun the E2E harness first to create the VM.\n' "$ssh_config" >&2
  exit 1
fi

limactl start --tty=false "$vm"

printf 'Forwarding https://%s:%s to %s:443; press Ctrl-C to stop.\n' "$web_host" "$local_port" "$vm"
exec ssh \
  -F "$ssh_config" \
  -o ControlMaster=no \
  -o ControlPath=none \
  -o ExitOnForwardFailure=yes \
  -N \
  -L "127.0.0.1:${local_port}:127.0.0.1:443" \
  "lima-${vm}"
