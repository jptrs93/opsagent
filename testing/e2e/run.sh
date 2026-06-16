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

if [[ "$RESET" == "true" ]]; then
  echo "==> Resetting OpenDeploy install-test environment"
  bash ../run.sh
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
  -e OPENDEPLOY_GITHUB_TOKEN="${OPENDEPLOY_GITHUB_TOKEN:-}" \
  -v "$(pwd)/test-results:/e2e/test-results" \
  -v "$(pwd)/playwright-report:/e2e/playwright-report" \
  "$IMG" npx playwright test "${flow_args[@]}"
