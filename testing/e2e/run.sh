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
  if tag=$(curl -fsSL "${headers[@]}" "https://api.github.com/repos/${RELEASE_REPO}/releases/latest" \
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
  -e OPENDEPLOY_GITHUB_TOKEN="${OPENDEPLOY_GITHUB_TOKEN:-}" \
  -v "$(pwd)/test-results:/e2e/test-results" \
  -v "$(pwd)/playwright-report:/e2e/playwright-report" \
  "$IMG" npx playwright test "${flow_args[@]}"
