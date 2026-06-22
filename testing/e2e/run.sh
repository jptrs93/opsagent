#!/usr/bin/env bash
# Reset the local OpenDeploy install-test environment, then run selected
# Playwright flows from a separate container on the same Docker network.
set -euo pipefail

cd "$(dirname "$0")"

IMG=${IMG:-opendeploy-e2e}
NETWORK_MODE=${NETWORK_MODE:-container:opendeploy-install-primary}
BASE_URL=${OPD_BASE_URL:-http://localhost:8080}
RESET=${RESET:-true}
FLOWS=${FLOWS:-bootstrap-enroll-nixdocker}
STATE_ENV=${STATE_ENV:-../.tmp/e2e.env}
RELEASE_REPO=${RELEASE_REPO:-jptrs93/opsagent}
BACKUP_RESTORE=${OPD_BACKUP_RESTORE:-false}
BACKUP_RESTORE_STATE_HOST=${OPD_BACKUP_RESTORE_STATE_HOST:-$(pwd)/test-results/backup-restore.env}
BACKUP_RESTORE_STATE_CONTAINER=${OPD_BACKUP_RESTORE_STATE:-/e2e/test-results/backup-restore.env}
LOCAL_TEST=${OPENDEPLOY_LOCAL_TEST:-false}

resolve_upgrade_version() {
  if [[ -n "${OPD_UPGRADE_VERSION:-}" ]]; then
    printf '%s' "$OPD_UPGRADE_VERSION"
    return
  fi

  local headers=(-H "Accept: application/vnd.github+json")
  if [[ -n "${OPENDEPLOY_GITHUB_TOKEN:-}" ]]; then
    headers+=(-H "Authorization: Bearer ${OPENDEPLOY_GITHUB_TOKEN}")
  fi

  local tag=""
  local release_url="https://api.github.com/repos/${RELEASE_REPO}/releases/latest"
  if [[ "$LOCAL_TEST" == "true" ]]; then
    release_url="http://opendeploy-local-repo:8080/repos/${RELEASE_REPO}/releases/latest"
  fi
  if tag=$(curl -fsSL "${headers[@]}" "$release_url" \
    | python3 -c 'import json, sys; print(json.load(sys.stdin).get("tag_name", ""))' 2>/dev/null); then
    if [[ -n "$tag" ]]; then
      printf '%s' "$tag"
      return
    fi
  fi

  echo "warning: could not resolve latest release for ${RELEASE_REPO}; falling back to v0.0.173" >&2
  printf '%s' "v0.0.173"
}

if [[ "$RESET" == "true" ]]; then
  echo "==> Resetting OpenDeploy install-test environment"
  bash ../run.sh
fi

if [[ -f "$STATE_ENV" ]]; then
  set -a
  source "$STATE_ENV"
  set +a
fi

OPD_UPGRADE_VERSION=$(resolve_upgrade_version)
echo "==> Upgrade target version: ${OPD_UPGRADE_VERSION}"
mkdir -p test-results
if [[ "$BACKUP_RESTORE" == "true" ]]; then
  rm -f "$BACKUP_RESTORE_STATE_HOST"
fi

echo "==> Building Playwright image"
docker build -t "$IMG" .

flow_args=()
IFS=',' read -ra flow_names <<< "$FLOWS"
for flow in "${flow_names[@]}"; do
  flow=${flow//[[:space:]]/}
  [[ -z "$flow" ]] && continue
  flow_args+=("flows/${flow}.spec.js")
done

if [[ ${#flow_args[@]} -eq 0 ]]; then
  echo "no flows selected" >&2
  exit 1
fi

echo "==> Running Playwright flows: ${flow_args[*]}"
docker run --rm \
  --network "$NETWORK_MODE" \
  -e OPD_BASE_URL="$BASE_URL" \
  -e OPD_SETUP_PASSWORD="${OPD_SETUP_PASSWORD:-}" \
  -e OPD_INSTALL_VERSION="${OPD_INSTALL_VERSION:-}" \
  -e OPD_UPGRADE_VERSION="$OPD_UPGRADE_VERSION" \
  -e OPD_BACKUP_RESTORE="$BACKUP_RESTORE" \
  -e OPD_BACKUP_RESTORE_STATE="$BACKUP_RESTORE_STATE_CONTAINER" \
  -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" \
  -e OPENDEPLOY_GITHUB_TOKEN="${OPENDEPLOY_GITHUB_TOKEN:-}" \
  -v "$(pwd)/test-results:/e2e/test-results" \
  -v "$(pwd)/playwright-report:/e2e/playwright-report" \
  "$IMG" npx playwright test "${flow_args[@]}"

if [[ "$BACKUP_RESTORE" == "true" ]]; then
  echo "==> Running backup restore extension"
  OPD_BACKUP_RESTORE_STATE_HOST="$BACKUP_RESTORE_STATE_HOST" \
    OPD_UPGRADE_VERSION="$OPD_UPGRADE_VERSION" \
    OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" \
    bash backup_restore.sh
fi
