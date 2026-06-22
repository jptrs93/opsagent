#!/usr/bin/env bash
# Host-side half of the optional OPD_BACKUP_RESTORE E2E extension. Playwright
# configures MinIO and writes restore args; this script restarts backup, waits,
# destroys the primary container and its mounted volume, then restores it.
set -euo pipefail

cd "$(dirname "$0")"

PRIMARY_NAME=${PRIMARY_NAME:-opendeploy-install-primary}
PRIMARY_VOLUME=${PRIMARY_VOLUME:-opendeploy-primary-containerd}
IMG=${IMG:-opendeploy-install-test}
NETWORK=${NETWORK:-opendeploy-install-test}
REPO=${RELEASE_REPO:-jptrs93/opsagent}
STATE_FILE=${OPD_BACKUP_RESTORE_STATE_HOST:-$(pwd)/test-results/backup-restore.env}
RESTORE_WAIT_SECONDS=${OPD_BACKUP_RESTORE_WAIT_SECONDS:-20}
RESTORE_INSTALL_VERSION=${OPD_RESTORE_INSTALL_VERSION:-${OPD_UPGRADE_VERSION:-${OPD_INSTALL_VERSION:-v0.0.173}}}
LOCAL_TEST=${OPENDEPLOY_LOCAL_TEST:-false}

if [[ ! -f "$STATE_FILE" ]]; then
  echo "backup restore state file not found: $STATE_FILE" >&2
  exit 1
fi

set -a
source "$STATE_FILE"
set +a

require_var() {
  local name=$1
  if [[ -z "${!name:-}" ]]; then
    echo "$name is required in $STATE_FILE" >&2
    exit 1
  fi
}

for name in \
  OPD_RESTORE_S3_ACCESS_KEY_ID \
  OPD_RESTORE_S3_SECRET_ACCESS_KEY \
  OPD_RESTORE_S3_BUCKET \
  OPD_RESTORE_S3_PATH \
  OPD_RESTORE_S3_REGION \
  OPD_RESTORE_S3_ENDPOINT \
  OPD_RESTORE_RECOVERY_CODE; do
  require_var "$name"
done

docker_arch=$(docker version --format '{{.Server.Arch}}')
case "$docker_arch" in
  amd64|x86_64)
    ARCH=amd64
    ;;
  arm64|aarch64)
    ARCH=arm64
    ;;
  *)
    echo "unsupported Docker server arch: $docker_arch" >&2
    exit 1
    ;;
esac

SELF_BIN="../.tmp/opendeploy-linux-${ARCH}"

wait_for_systemd() {
  local name=$1
  local state=""
  for _ in $(seq 1 30); do
    state=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
    [[ "$state" == running || "$state" == degraded ]] && return
    sleep 1
  done
  echo "systemd did not come up in $name (state: ${state:-unknown})" >&2
  docker logs "$name" >&2 || true
  exit 1
}

wait_for_service() {
  local name=$1
  local service=$2
  local state=""
  for _ in $(seq 1 45); do
    state=$(docker exec "$name" systemctl is-active "$service" 2>/dev/null || true)
    [[ "$state" == active ]] && return
    sleep 1
  done
  echo "$service did not become active in $name (state: ${state:-unknown})" >&2
  docker exec "$name" systemctl status "$service" --no-pager >&2 || true
  exit 1
}

wait_for_healthz() {
  for _ in $(seq 1 60); do
    if curl -fsS http://localhost:8080/v1/healthz >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  echo "restored primary did not become healthy" >&2
  docker exec "$PRIMARY_NAME" systemctl status opendeploy --no-pager >&2 || true
  docker logs "$PRIMARY_NAME" >&2 || true
  exit 1
}

download_opendeploy() {
  if [[ "$LOCAL_TEST" == "true" ]]; then
    if [[ ! -f "$SELF_BIN" ]]; then
      echo "local opendeploy binary not found: $SELF_BIN" >&2
      exit 1
    fi
    echo "==> Copying local opendeploy into restored primary"
    docker cp "$SELF_BIN" "$PRIMARY_NAME:/usr/local/bin/opendeploy"
    docker exec "$PRIMARY_NAME" chmod 0755 /usr/local/bin/opendeploy
    return
  fi

  echo "==> Downloading opendeploy ${RESTORE_INSTALL_VERSION} for linux/${ARCH} into restored primary"
  docker exec \
    -e OPD_REPO="$REPO" \
    -e OPD_VERSION="$RESTORE_INSTALL_VERSION" \
    -e OPD_ARCH="$ARCH" \
    -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" \
    "$PRIMARY_NAME" bash -lc '
      set -euo pipefail
      if [[ "$OPENDEPLOY_LOCAL_TEST" == "true" ]]; then
        url="http://opendeploy-local-repo:8080/${OPD_REPO}/releases/download/${OPD_VERSION}/opendeploy-linux-${OPD_ARCH}"
      else
        url="https://github.com/${OPD_REPO}/releases/download/${OPD_VERSION}/opendeploy-linux-${OPD_ARCH}"
      fi
      curl -fsSL "$url" -o /usr/local/bin/opendeploy
      chmod 0755 /usr/local/bin/opendeploy
    '
}

start_restored_primary_container() {
  docker run -d --name "$PRIMARY_NAME" \
    --privileged \
    --cgroupns=host \
    --network "$NETWORK" \
    --tmpfs /run --tmpfs /run/lock \
    -v "$PRIMARY_VOLUME":/var/lib/opendeploy-containerd \
    -p 8080:8080 \
    "$IMG" >/dev/null
}

install_restored_primary() {
  download_opendeploy
  local cmd=(
    opendeploy install primary
    --http-only true
    --web-listen :8080
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

  echo "==> Installing restored primary"
  docker exec -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" "$PRIMARY_NAME" "${cmd[@]}"
  if [[ "$LOCAL_TEST" == "true" ]]; then
    docker exec "$PRIMARY_NAME" bash -lc '
      set -euo pipefail
      grep -q "^OPENDEPLOY_LOCAL_TEST=" /etc/opendeploy/env 2>/dev/null || printf "\nOPENDEPLOY_LOCAL_TEST=true\n" >> /etc/opendeploy/env
    '
  fi
  docker exec "$PRIMARY_NAME" usermod -aG nix-users opendeploy
  docker exec "$PRIMARY_NAME" systemctl restart opendeploy.service
  wait_for_service "$PRIMARY_NAME" opendeploy.service
}

echo "==> Restarting primary so backup replication reads updated settings"
docker exec "$PRIMARY_NAME" systemctl restart opendeploy.service
wait_for_service "$PRIMARY_NAME" opendeploy.service
wait_for_healthz

echo "==> Waiting ${RESTORE_WAIT_SECONDS}s for backup replication"
sleep "$RESTORE_WAIT_SECONDS"

echo "==> Destroying primary container and mounted volume"
docker exec "$PRIMARY_NAME" systemctl stop opendeploy.service >/dev/null 2>&1 || true
docker rm -f "$PRIMARY_NAME" >/dev/null 2>&1 || true
docker volume rm "$PRIMARY_VOLUME" >/dev/null 2>&1 || true

echo "==> Starting fresh primary container for restore"
start_restored_primary_container
wait_for_systemd "$PRIMARY_NAME"
install_restored_primary
wait_for_healthz

echo "==> Backup restore extension complete"
