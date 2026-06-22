#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

IMG=${IMG:-opendeploy-local-repo}
NAME=${NAME:-opendeploy-local-repo}
NETWORK=${NETWORK:-opendeploy-install-test}
HOST_PORT=${HOST_PORT:-8081}

docker network create "$NETWORK" >/dev/null 2>&1 || true
docker build -t "$IMG" .
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" --network "$NETWORK" -p "${HOST_PORT}:8080" "$IMG" >/dev/null

echo "local repo container: http://localhost:${HOST_PORT}"
echo "docker network name: ${NETWORK}"
