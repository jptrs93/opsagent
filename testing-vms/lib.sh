#!/usr/bin/env bash

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "testing-vms scripts require bash" >&2
  exit 1
fi

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

STATE_DIR=${OPD_VM_STATE_DIR:-$SCRIPT_DIR/.tmp}
MOCK_ARTIFACT_DIR=${OPD_MOCK_ARTIFACT_DIR:-$SCRIPT_DIR/.mock-artifacts}
RESULTS_DIR=${OPD_VM_RESULTS_DIR:-$SCRIPT_DIR/test-results}
REPORT_DIR=${OPD_VM_REPORT_DIR:-$SCRIPT_DIR/playwright-report}
E2E_ENV_FILE=${OPD_VM_E2E_ENV_FILE:-$STATE_DIR/e2e.env}
CERT_DIR=${OPD_VM_CERT_DIR:-$STATE_DIR/certs}
CA_CERT=$CERT_DIR/ca.crt
CA_KEY=$CERT_DIR/ca.key
SERVER_CERT=$CERT_DIR/server.crt
SERVER_KEY=$CERT_DIR/server.key
SERVER_BUNDLE=$CERT_DIR/server-bundle.pem

PRIMARY_NAME=${OPD_VM_PRIMARY:-opendeploy-primary}
SECONDARY_NAME=${OPD_VM_SECONDARY:-opendeploy-secondary}
PLAYWRIGHT_NAME=${OPD_VM_PLAYWRIGHT:-opendeploy-playwright}
REPO_MIRROR_NAME=${OPD_VM_REPO_MIRROR:-opendeploy-repo-mirror}
NETWORK_NAME=${OPD_VM_NETWORK:-user-v2}
VM_TYPE=${OPD_VM_TYPE:-vz}
WEB_HOST=${OPD_WEB_HOST:-primary.opendeploy.test}
WEB_BASE_URL=${OPD_BASE_URL:-https://$WEB_HOST}

RELEASE_REPO=${RELEASE_REPO:-jptrs93/opsagent}
INSTALL_VERSION=${OPD_INSTALL_VERSION:-v0.0.256}
UPGRADE_VERSION=${OPD_UPGRADE_VERSION:-v0.0.256}
SELF_VERSION=${OPD_SELF_VERSION:-v0.0.0}
LOCAL_TEST=${OPD_LOCAL_CHECKOUT:-false}
BACKUP_RESTORE=${OPD_BACKUP_RESTORE:-false}
REMOTE_MODE=${OPD_REMOTE:-mock}
MOCK_OPENDEPLOY_SOURCE=${OPD_MOCK_OPENDEPLOY_SOURCE:-local}

NODE_CPUS=${OPD_VM_NODE_CPUS:-4}
NODE_MEMORY=${OPD_VM_NODE_MEMORY:-6GiB}
NODE_DISK=${OPD_VM_NODE_DISK:-80GiB}
PLAYWRIGHT_CPUS=${OPD_VM_PLAYWRIGHT_CPUS:-4}
PLAYWRIGHT_MEMORY=${OPD_VM_PLAYWRIGHT_MEMORY:-6GiB}
PLAYWRIGHT_DISK=${OPD_VM_PLAYWRIGHT_DISK:-40GiB}
REPO_MIRROR_CPUS=${OPD_VM_REPO_MIRROR_CPUS:-4}
REPO_MIRROR_MEMORY=${OPD_VM_REPO_MIRROR_MEMORY:-6GiB}
REPO_MIRROR_DISK=${OPD_VM_REPO_MIRROR_DISK:-80GiB}

if [[ "$LOCAL_TEST" == "true" ]]; then
  REPO_MIRROR_RELEASES=${OPD_REPO_MIRROR_RELEASES:-}
  REPO_MIRROR_LATEST=${OPD_REPO_MIRROR_LATEST:-$SELF_VERSION}
else
  REPO_MIRROR_RELEASES=${OPD_REPO_MIRROR_RELEASES:-$INSTALL_VERSION $UPGRADE_VERSION}
  REPO_MIRROR_LATEST=${OPD_REPO_MIRROR_LATEST:-$UPGRADE_VERSION}
fi
REPO_REGISTRY_HOST=${OPD_REPO_REGISTRY_HOST:-$REPO_MIRROR_NAME}
REPO_REGISTRY_PORT=${OPD_REPO_REGISTRY_PORT:-5000}
if [[ "$REMOTE_MODE" == "real" ]]; then
  POSTGRES_IMAGE=${OPD_POSTGRES_IMAGE:-docker.io/library/postgres:18}
  MINIO_IMAGE=${OPD_MINIO_IMAGE:-docker.io/bitnamilegacy/minio:latest}
else
  POSTGRES_IMAGE=${OPD_POSTGRES_IMAGE:-$REPO_REGISTRY_HOST:$REPO_REGISTRY_PORT/library/postgres:18}
  MINIO_IMAGE=${OPD_MINIO_IMAGE:-$REPO_REGISTRY_HOST:$REPO_REGISTRY_PORT/bitnamilegacy/minio:latest}
fi
REPO_MIRROR_OCI_IMAGES=${OPD_REPO_MIRROR_OCI_IMAGES:-docker.io/library/postgres:18=$POSTGRES_IMAGE docker.io/bitnamilegacy/minio:latest=$MINIO_IMAGE}
CONTAINERD_VERSION=${CONTAINERD_VERSION:-2.0.5}
RUNC_VERSION=${RUNC_VERSION:-1.2.6}

case "$(uname -m)" in
  arm64|aarch64)
    GOARCH=arm64
    LIMA_ARCH=aarch64
    ;;
  x86_64|amd64)
    GOARCH=amd64
    LIMA_ARCH=x86_64
    ;;
  *)
    echo "unsupported host architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

SELF_BIN="$STATE_DIR/opendeploy-linux-$GOARCH"
MOCK_RELEASE_BIN_DIR="$STATE_DIR/mock-releases"
PLAYWRIGHT_WORKDIR=/tmp/opendeploy-playwright
REPO_MIRROR_REFRESH=${OPD_REPO_MIRROR_REFRESH:-false}
PREPARE_MOCK_ARTIFACTS=${OPD_PREPARE_MOCK_ARTIFACTS:-false}
REPO_MIRROR_PROXY_UNKNOWN=${OPD_REPO_MIRROR_PROXY_UNKNOWN:-nixpkgs}
LIMA_IMAGE_ARM64_URL=${OPD_LIMA_IMAGE_ARM64_URL:-https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img}
LIMA_IMAGE_AMD64_URL=${OPD_LIMA_IMAGE_AMD64_URL:-https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img}
LIMA_IMAGE_ARM64=${OPD_LIMA_IMAGE_ARM64:-$MOCK_ARTIFACT_DIR/lima/ubuntu-24.04-server-cloudimg-arm64.img}
LIMA_IMAGE_AMD64=${OPD_LIMA_IMAGE_AMD64:-$MOCK_ARTIFACT_DIR/lima/ubuntu-24.04-server-cloudimg-amd64.img}

log() {
  printf '==> %s\n' "$*"
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

repo_mirror_enabled() {
  [[ "$REMOTE_MODE" != "real" ]]
}

if repo_mirror_enabled && [[ " $REPO_MIRROR_RELEASES " != *" $UPGRADE_VERSION "* ]]; then
  REPO_MIRROR_RELEASES="$REPO_MIRROR_RELEASES $UPGRADE_VERSION"
fi

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

require_host_tools() {
  require_cmd limactl
  require_cmd python3
  require_cmd curl
  require_cmd openssl
  case "$REMOTE_MODE" in
    mock|real) ;;
    *) die "OPD_REMOTE must be mock or real" ;;
  esac
  if [[ "$LOCAL_TEST" == "true" && "$REMOTE_MODE" == "real" ]]; then
    die "OPD_LOCAL_CHECKOUT=true requires OPD_REMOTE=mock so the local build can be published as a release"
  fi
  case "$MOCK_OPENDEPLOY_SOURCE" in
    local|real) ;;
    *) die "OPD_MOCK_OPENDEPLOY_SOURCE must be local or real" ;;
  esac
  if [[ "$LOCAL_TEST" == "true" || ( "$REMOTE_MODE" == "mock" && "$MOCK_OPENDEPLOY_SOURCE" == "local" ) ]]; then
    require_cmd go
    require_cmd pnpm
  fi
  if [[ "$PREPARE_MOCK_ARTIFACTS" == "true" ]]; then
    require_cmd git
    require_cmd shasum
    if ! command -v skopeo >/dev/null 2>&1 && ! command -v docker >/dev/null 2>&1; then
      die "skopeo or docker is required to prepare mock OCI artifacts"
    fi
  fi
}

mock_artifact_release_file() {
  local version=$1
  local arch=$2
  printf '%s/releases/%s/%s/%s' "$MOCK_ARTIFACT_DIR" "$RELEASE_REPO" "$version" "opendeploy-linux-$arch"
}

mock_artifact_oci_archive() {
  local image=$1
  local safe
  safe=${image//\//_}
  safe=${safe//:/_}
  printf '%s/oci/%s.tar' "$MOCK_ARTIFACT_DIR" "$safe"
}

ensure_mock_artifacts() {
  repo_mirror_enabled || return 0
  if [[ "$PREPARE_MOCK_ARTIFACTS" == "true" ]]; then
    "$SCRIPT_DIR/prepare_mock_artifacts.sh"
  fi
  local version arch spec src archive path missing=false
  [[ -d "$MOCK_ARTIFACT_DIR" ]] || missing=true
  for version in $REPO_MIRROR_RELEASES; do
    [[ -n "$version" ]] || continue
    for arch in amd64 arm64; do
      path=$(mock_artifact_release_file "$version" "$arch")
      [[ -s "$path" ]] || { printf 'missing mock release artifact: %s\n' "$path" >&2; missing=true; }
    done
    path="$MOCK_ARTIFACT_DIR/releases/$RELEASE_REPO/$version/sha256sums.txt"
    [[ -s "$path" ]] || { printf 'missing mock release checksum: %s\n' "$path" >&2; missing=true; }
  done
  for arch in amd64 arm64; do
    path="$MOCK_ARTIFACT_DIR/releases/containerd/containerd/v$CONTAINERD_VERSION/containerd-$CONTAINERD_VERSION-linux-$arch.tar.gz"
    [[ -s "$path" ]] || { printf 'missing mock runtime artifact: %s\n' "$path" >&2; missing=true; }
    path="$MOCK_ARTIFACT_DIR/releases/opencontainers/runc/v$RUNC_VERSION/runc.$arch"
    [[ -s "$path" ]] || { printf 'missing mock runtime artifact: %s\n' "$path" >&2; missing=true; }
  done
  for spec in $REPO_MIRROR_OCI_IMAGES; do
    src=${spec%%=*}
    archive=$(mock_artifact_oci_archive "$src")
    [[ -s "$archive" ]] || { printf 'missing mock OCI archive: %s\n' "$archive" >&2; missing=true; }
  done
  [[ -s "$LIMA_IMAGE_ARM64" ]] || { printf 'missing Lima arm64 image: %s\n' "$LIMA_IMAGE_ARM64" >&2; missing=true; }
  [[ -s "$LIMA_IMAGE_AMD64" ]] || { printf 'missing Lima amd64 image: %s\n' "$LIMA_IMAGE_AMD64" >&2; missing=true; }
  if [[ "$missing" == "true" ]]; then
    die "mock artifacts are incomplete; run OPD_PREPARE_MOCK_ARTIFACTS=true bash testing-vms/run.sh or bash testing-vms/prepare_mock_artifacts.sh"
  fi
}

cluster_vm_names() {
  printf '%s\n' "$PRIMARY_NAME"
  printf '%s\n' "$SECONDARY_NAME"
}

all_vm_names() {
  cluster_vm_names
  printf '%s\n' "$PLAYWRIGHT_NAME"
  printf '%s\n' "$REPO_MIRROR_NAME"
}

vm_exists() {
  local name=$1
  limactl list --json 2>/dev/null | python3 -c '
import json, sys
want = sys.argv[1]
text = sys.stdin.read().strip()
if not text:
    sys.exit(1)
try:
    data = json.loads(text)
except Exception:
    data = []
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            data.append(json.loads(line))
        except Exception:
            pass
if isinstance(data, dict):
    data = [data]
if any(item.get("name") == want for item in data):
    sys.exit(0)
sys.exit(1)
' "$name"
}

vm_exec() {
  local name=$1
  shift
  limactl shell --tty=false "$name" "$@"
}

vm_bash() {
  local name=$1
  local script=$2
  vm_exec "$name" bash -lc "$script"
}

vm_sudo_bash() {
  local name=$1
  shift
  limactl shell --tty=false "$name" sudo bash -s "$@"
}

write_lima_yaml() {
  local name=$1
  local role=$2
  local cpus=$3
  local memory=$4
  local disk=$5
  local yaml=$6
  local node_packages="sudo ca-certificates curl git openssl python3 nix-bin nix-setup-systemd"
  local playwright_packages="sudo ca-certificates curl git openssl python3 nodejs npm openssh-client"
  local repo_mirror_packages="sudo ca-certificates curl git openssl python3 bash tar gzip docker-registry skopeo"
  local packages

  case "$role" in
    node) packages=$node_packages ;;
    playwright) packages=$playwright_packages ;;
    repo-mirror) packages=$repo_mirror_packages ;;
    *) die "unknown Lima VM role: $role" ;;
  esac

  mkdir -p "$(dirname "$yaml")"
  cat >"$yaml" <<EOF
vmType: "$VM_TYPE"
os: "Linux"
arch: "$LIMA_ARCH"
images:
- location: "$(repo_mirror_enabled && printf '%s' "$LIMA_IMAGE_ARM64" || printf '%s' "$LIMA_IMAGE_ARM64_URL")"
  arch: "aarch64"
- location: "$(repo_mirror_enabled && printf '%s' "$LIMA_IMAGE_AMD64" || printf '%s' "$LIMA_IMAGE_AMD64_URL")"
  arch: "x86_64"
cpus: $cpus
memory: "$memory"
disk: "$disk"
mounts: []
containerd:
  system: false
  user: false
networks:
- lima: "$NETWORK_NAME"
provision:
- mode: system
  script: |
    #!/usr/bin/env bash
    set -euxo pipefail
    export DEBIAN_FRONTEND=noninteractive
    mkdir -p /var/lib/opendeploy-vm-harness
    if [[ ! -f /var/lib/opendeploy-vm-harness/provisioned-$role ]]; then
      apt-get update
      apt-get install -y --no-install-recommends $packages
      if [[ "$role" == "node" ]]; then
        mkdir -p /etc/nix
        printf 'experimental-features = nix-command flakes\nsandbox = false\nallowed-users = *\ntrusted-users = root opendeploy\n' > /etc/nix/nix.conf
        systemctl enable --now nix-daemon.service || true
      fi
      touch /var/lib/opendeploy-vm-harness/provisioned-$role
    fi
EOF
}

start_vm() {
  local name=$1
  local role=$2
  local cpus memory disk
  case "$role" in
    node)
      cpus=$NODE_CPUS
      memory=$NODE_MEMORY
      disk=$NODE_DISK
      ;;
    playwright)
      cpus=$PLAYWRIGHT_CPUS
      memory=$PLAYWRIGHT_MEMORY
      disk=$PLAYWRIGHT_DISK
      ;;
    repo-mirror)
      cpus=$REPO_MIRROR_CPUS
      memory=$REPO_MIRROR_MEMORY
      disk=$REPO_MIRROR_DISK
      ;;
    *) die "unknown VM role: $role" ;;
  esac

  if vm_exists "$name"; then
    log "Starting existing Lima VM $name"
    limactl start --tty=false --timeout=30m "$name"
    return
  fi

  local yaml="$STATE_DIR/lima-$name.yaml"
  write_lima_yaml "$name" "$role" "$cpus" "$memory" "$disk" "$yaml"
  log "Creating Lima VM $name ($role)"
  limactl start --tty=false --timeout=30m --name="$name" "$yaml"
}

start_all_vms() {
  mkdir -p "$STATE_DIR"
  start_vm "$REPO_MIRROR_NAME" repo-mirror
  start_vm "$PRIMARY_NAME" node
  start_vm "$SECONDARY_NAME" node
  start_vm "$PLAYWRIGHT_NAME" playwright
  sync_hosts_all
}

delete_vm() {
  local name=$1
  log "Deleting Lima VM $name"
  limactl delete --force "$name" >/dev/null 2>&1 || true
}

delete_all_vms() {
  delete_vm "$PRIMARY_NAME"
  delete_vm "$SECONDARY_NAME"
  delete_vm "$PLAYWRIGHT_NAME"
  delete_vm "$REPO_MIRROR_NAME"
  delete_vm opendeploy-install-primary
  delete_vm opendeploy-install-secondary
  delete_vm opendeploy-e2e-runner
}

sync_hosts_all() {
  local names
  names=$(all_vm_names | tr '\n' ' ')
  local vm
  for vm in $names; do
    sync_hosts_for_vm "$vm" "$names"
  done
}

sync_hosts_for_vm() {
  local vm=$1
  local names=$2
  log "Syncing /etc/hosts in $vm"
  limactl shell --tty=false "$vm" sudo env \
    OPD_WEB_HOST="$WEB_HOST" \
    OPD_PRIMARY_NAME="$PRIMARY_NAME" \
    OPD_THIS_VM="$vm" \
    OPD_REPO_MIRROR_ENABLED="$(repo_mirror_enabled && printf true || printf false)" \
    OPD_REPO_MIRROR_NAME="$REPO_MIRROR_NAME" \
    OPD_REPO_REGISTRY_HOST="$REPO_REGISTRY_HOST" \
    bash -s -- $names <<'EOS'
set -euo pipefail
names=("$@")
tmp=$(mktemp)
host_line() {
  local target=$1
  shift
  local ip
  ip=$(getent hosts "lima-${target}.internal" 2>/dev/null | awk '{print $1; exit}' || true)
  if [[ -n "$ip" ]]; then
    printf '%s' "$ip"
    local alias
    for alias in "$@"; do
      [[ -n "$alias" ]] && printf ' %s' "$alias"
    done
    printf '\n'
  fi
}
{
  echo "# opendeploy-vm-hosts-begin"
  for name in "${names[@]}"; do
    host_line "$name" "$name" "lima-${name}.internal"
  done
  host_line "$OPD_PRIMARY_NAME" "$OPD_WEB_HOST"
  if [[ "$OPD_REPO_MIRROR_ENABLED" == "true" && "$OPD_THIS_VM" != "$OPD_REPO_MIRROR_NAME" ]]; then
    host_line "$OPD_REPO_MIRROR_NAME" github.com api.github.com "$OPD_REPO_REGISTRY_HOST"
  fi
  echo "# opendeploy-vm-hosts-end"
} > "$tmp.block"
awk '
  $0 == "# opendeploy-vm-hosts-begin" {skip=1; next}
  $0 == "# opendeploy-vm-hosts-end" {skip=0; next}
  skip != 1 {print}
' /etc/hosts > "$tmp"
cat "$tmp.block" >> "$tmp"
install -m 0644 "$tmp" /etc/hosts
rm -f "$tmp" "$tmp.block"
EOS
}

wait_for_systemd() {
  local name=$1
  local state=""
  local i
  for i in $(seq 1 60); do
    state=$(vm_exec "$name" systemctl is-system-running 2>/dev/null || true)
    if [[ "$state" == "running" || "$state" == "degraded" ]]; then
      return
    fi
    sleep 2
  done
  vm_exec "$name" systemctl --no-pager status || true
  die "systemd did not become ready in $name (state: ${state:-unknown})"
}

wait_for_service() {
  local name=$1
  local service=$2
  local state=""
  local i
  for i in $(seq 1 60); do
    state=$(vm_exec "$name" systemctl is-active "$service" 2>/dev/null || true)
    if [[ "$state" == "active" ]]; then
      return
    fi
    sleep 2
  done
  vm_exec "$name" systemctl status "$service" --no-pager || true
  die "$service did not become active in $name (state: ${state:-unknown})"
}

wait_for_healthz() {
  local name=$1
  local i
  for i in $(seq 1 90); do
    if vm_exec "$name" curl -fsS "$WEB_BASE_URL/v1/healthz" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  vm_exec "$name" systemctl status opendeploy --no-pager || true
  die "OpenDeploy health check failed in $name"
}

ensure_test_certs() {
  mkdir -p "$CERT_DIR"
  if [[ -s "$CA_CERT" && -s "$CA_KEY" && -s "$SERVER_CERT" && -s "$SERVER_KEY" && -s "$SERVER_BUNDLE" ]]; then
    return
  fi
  log "Generating VM test CA and server certificate"
  rm -f "$CA_CERT" "$CA_KEY" "$SERVER_CERT" "$SERVER_KEY" "$SERVER_BUNDLE" "$CERT_DIR/server.csr" "$CERT_DIR/server.cnf"
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
    -subj "/CN=OpenDeploy E2E Test CA" \
    -keyout "$CA_KEY" -out "$CA_CERT" >/dev/null 2>&1
  cat >"$CERT_DIR/server.cnf" <<EOF
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = $WEB_HOST

[v3_req]
keyUsage = keyEncipherment, dataEncipherment, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = $WEB_HOST
DNS.2 = github.com
DNS.3 = api.github.com
DNS.4 = $REPO_MIRROR_NAME
DNS.5 = $REPO_REGISTRY_HOST
DNS.6 = localhost
IP.1 = 127.0.0.1
EOF
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$SERVER_KEY" -out "$CERT_DIR/server.csr" \
    -config "$CERT_DIR/server.cnf" >/dev/null 2>&1
  openssl x509 -req -days 30 \
    -in "$CERT_DIR/server.csr" \
    -CA "$CA_CERT" -CAkey "$CA_KEY" -CAcreateserial \
    -out "$SERVER_CERT" \
    -extensions v3_req -extfile "$CERT_DIR/server.cnf" >/dev/null 2>&1
  cat "$SERVER_CERT" "$SERVER_KEY" > "$SERVER_BUNDLE"
}

install_test_ca() {
  local name=$1
  limactl copy "$CA_CERT" "$name:/tmp/opendeploy-e2e-ca.crt"
  vm_exec "$name" sudo install -m 0644 /tmp/opendeploy-e2e-ca.crt /usr/local/share/ca-certificates/opendeploy-e2e-ca.crt
  vm_exec "$name" sudo update-ca-certificates >/dev/null
}

install_test_ca_all() {
  local vm
  for vm in $(all_vm_names); do
    log "Installing VM test CA in $vm"
    install_test_ca "$vm"
  done
}

build_self_opendeploy() {
  if [[ "$LOCAL_TEST" != "true" ]] && ! repo_mirror_enabled; then
    return
  fi
  if [[ "$LOCAL_TEST" != "true" && "$MOCK_OPENDEPLOY_SOURCE" != "local" ]]; then
    return
  fi
  log "Building frontend assets for VM test binaries"
  (cd "$REPO_ROOT/frontend" && pnpm run build)
  if [[ "$LOCAL_TEST" == "true" ]]; then
    build_opendeploy_linux "$SELF_VERSION" "$SELF_BIN"
  fi
  if repo_mirror_enabled && [[ "$MOCK_OPENDEPLOY_SOURCE" == "local" ]]; then
    local version
    for version in $REPO_MIRROR_RELEASES; do
      [[ -n "$version" ]] || continue
      build_opendeploy_linux "$version" "$MOCK_RELEASE_BIN_DIR/$version/opendeploy-linux-$GOARCH"
    done
  fi
}

build_opendeploy_linux() {
  local version=$1
  local output=$2
  log "Building opendeploy $version for linux/$GOARCH"
  mkdir -p "$(dirname "$output")"
  (cd "$REPO_ROOT/backend" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w -X github.com/jptrs93/opsagent/backend/util/version.Version=$version" -o "$output" .)
}

download_opendeploy() {
  local name=$1
  if [[ "$LOCAL_TEST" == "true" ]]; then
    [[ -f "$SELF_BIN" ]] || die "local opendeploy binary not found: $SELF_BIN"
    log "Copying local opendeploy $SELF_VERSION into $name"
    limactl copy "$SELF_BIN" "$name:/tmp/opendeploy"
    vm_exec "$name" sudo install -m 0755 /tmp/opendeploy /usr/local/bin/opendeploy
    return
  fi

  log "Downloading opendeploy $INSTALL_VERSION for linux/$GOARCH in $name"
  vm_exec "$name" env OPD_REPO="$RELEASE_REPO" OPD_VERSION="$INSTALL_VERSION" OPD_ARCH="$GOARCH" bash -lc '
set -euo pipefail
curl -fsSL "https://github.com/${OPD_REPO}/releases/download/${OPD_VERSION}/opendeploy-linux-${OPD_ARCH}" -o /tmp/opendeploy
sudo install -m 0755 /tmp/opendeploy /usr/local/bin/opendeploy
'
}

setup_repo_mirror_vm() {
  repo_mirror_enabled || return 0
  log "Configuring repo mirror VM $REPO_MIRROR_NAME"
  limactl copy "$SCRIPT_DIR/repo-mirror/prepare.sh" "$REPO_MIRROR_NAME:/tmp/prepare-repo-mirror"
  limactl copy "$SCRIPT_DIR/repo-mirror/server.py" "$REPO_MIRROR_NAME:/tmp/repo-mirror-server"
  limactl copy "$SERVER_CERT" "$REPO_MIRROR_NAME:/tmp/opendeploy-e2e-server.crt"
  limactl copy "$SERVER_KEY" "$REPO_MIRROR_NAME:/tmp/opendeploy-e2e-server.key"
  vm_exec "$REPO_MIRROR_NAME" sudo install -m 0755 /tmp/prepare-repo-mirror /usr/local/bin/prepare-repo-mirror
  vm_exec "$REPO_MIRROR_NAME" sudo install -m 0755 /tmp/repo-mirror-server /usr/local/bin/repo-mirror-server
  vm_exec "$REPO_MIRROR_NAME" sudo install -d -m 0755 /etc/opendeploy-repo-mirror /srv/registry
  vm_exec "$REPO_MIRROR_NAME" sudo install -m 0644 /tmp/opendeploy-e2e-server.crt /etc/opendeploy-repo-mirror/server.crt
  vm_exec "$REPO_MIRROR_NAME" sudo install -m 0600 /tmp/opendeploy-e2e-server.key /etc/opendeploy-repo-mirror/server.key
  configure_repo_mirror_registry
  copy_mock_artifacts_to_repo_mirror
  vm_exec "$REPO_MIRROR_NAME" sudo env \
    OPD_REPO_MIRROR_REFRESH="$REPO_MIRROR_REFRESH" \
    OPD_MOCK_ARTIFACT_DIR="/srv/mock-artifacts" \
    OPD_RELEASES="$REPO_MIRROR_RELEASES" \
    OPD_LATEST_RELEASE="$REPO_MIRROR_LATEST" \
    OPD_ARCHES="amd64 arm64" \
    OPD_OCI_IMAGES="$REPO_MIRROR_OCI_IMAGES" \
    CONTAINERD_VERSION="$CONTAINERD_VERSION" \
    RUNC_VERSION="$RUNC_VERSION" \
    /usr/local/bin/prepare-repo-mirror
  publish_local_repo_to_repo_mirror
  publish_mock_releases_to_repo_mirror
  configure_repo_mirror_http
}

copy_mock_artifacts_to_repo_mirror() {
  log "Copying mock artifact cache to $REPO_MIRROR_NAME"
  vm_exec "$REPO_MIRROR_NAME" sudo rm -rf /srv/mock-artifacts /tmp/mock-artifacts
  limactl copy -r "$MOCK_ARTIFACT_DIR" "$REPO_MIRROR_NAME:/tmp/mock-artifacts"
  vm_exec "$REPO_MIRROR_NAME" sudo bash -lc 'set -euo pipefail; mv /tmp/mock-artifacts /srv/mock-artifacts; chmod -R a+rX /srv/mock-artifacts'
}

configure_repo_mirror_registry() {
  vm_sudo_bash "$REPO_MIRROR_NAME" <<EOS
set -euo pipefail
install -d -m 0755 /etc/docker/registry /srv/registry
cat >/etc/docker/registry/config.yml <<'CFG'
version: 0.1
log:
  fields:
    service: opendeploy-repo-mirror-registry
storage:
  filesystem:
    rootdirectory: /srv/registry
http:
  addr: :$REPO_REGISTRY_PORT
  tls:
    certificate: /etc/opendeploy-repo-mirror/server.crt
    key: /etc/opendeploy-repo-mirror/server.key
CFG
systemctl disable --now docker-registry.service >/dev/null 2>&1 || true
cat >/etc/systemd/system/opendeploy-repo-mirror-registry.service <<'UNIT'
[Unit]
Description=OpenDeploy repo mirror OCI registry
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/docker-registry serve /etc/docker/registry/config.yml
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now opendeploy-repo-mirror-registry.service
EOS
  wait_for_service "$REPO_MIRROR_NAME" opendeploy-repo-mirror-registry.service
  vm_exec "$REPO_MIRROR_NAME" bash -lc "for i in \$(seq 1 60); do curl -fsS https://$REPO_REGISTRY_HOST:$REPO_REGISTRY_PORT/v2/ >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1"
}

configure_repo_mirror_http() {
  vm_exec "$REPO_MIRROR_NAME" sudo env OPD_PROXY_UNKNOWN="$REPO_MIRROR_PROXY_UNKNOWN" bash -s <<'EOS'
set -euo pipefail
cat >/etc/systemd/system/opendeploy-repo-mirror.service <<UNIT
[Unit]
Description=OpenDeploy GitHub-shaped repo mirror
After=network-online.target opendeploy-repo-mirror-registry.service
Wants=network-online.target opendeploy-repo-mirror-registry.service

[Service]
Environment=OPD_REPO_MIRROR_PORT=443
Environment=OPD_REPO_MIRROR_TLS=true
Environment=OPD_REPO_MIRROR_CERT=/etc/opendeploy-repo-mirror/server.crt
Environment=OPD_REPO_MIRROR_KEY=/etc/opendeploy-repo-mirror/server.key
Environment=OPD_REPO_MIRROR_PROXY_UNKNOWN=$OPD_PROXY_UNKNOWN
ExecStart=/usr/local/bin/repo-mirror-server
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now opendeploy-repo-mirror.service
EOS
  wait_for_service "$REPO_MIRROR_NAME" opendeploy-repo-mirror.service
  vm_exec "$REPO_MIRROR_NAME" bash -lc "for i in \$(seq 1 60); do curl -fsS https://$REPO_MIRROR_NAME/healthz >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1"
}

publish_local_repo_to_repo_mirror() {
  log "Publishing local git checkout to $REPO_MIRROR_NAME"
  vm_exec "$REPO_MIRROR_NAME" sudo rm -rf /tmp/opendeploy-local-git
  limactl copy -r "$REPO_ROOT/.git" "$REPO_MIRROR_NAME:/tmp/opendeploy-local-git"
  vm_exec "$REPO_MIRROR_NAME" sudo env OPD_OWNER_REPO="jptrs93/opsagent" bash -lc '
set -euo pipefail
dst="/srv/git/${OPD_OWNER_REPO}.git"
rm -rf "$dst"
mkdir -p "$(dirname "$dst")"
git clone --mirror /tmp/opendeploy-local-git "$dst" >/dev/null
git -C "$dst" update-server-info
rm -rf /tmp/opendeploy-local-git
'
}

publish_mock_releases_to_repo_mirror() {
  if [[ "$LOCAL_TEST" != "true" && "$MOCK_OPENDEPLOY_SOURCE" != "local" ]]; then
    vm_exec "$REPO_MIRROR_NAME" sudo env OPD_RELEASE_REPO="$RELEASE_REPO" OPD_LATEST="$REPO_MIRROR_LATEST" bash -lc '
set -euo pipefail
mkdir -p "/srv/releases/$OPD_RELEASE_REPO"
printf "%s\n" "$OPD_LATEST" > "/srv/releases/$OPD_RELEASE_REPO/latest"
'
    return
  fi
  local version
  if [[ "$MOCK_OPENDEPLOY_SOURCE" == "local" ]]; then
    for version in $REPO_MIRROR_RELEASES; do
      [[ -n "$version" ]] || continue
      publish_release_to_repo_mirror "$version" "$MOCK_RELEASE_BIN_DIR/$version/opendeploy-linux-$GOARCH"
    done
  fi
  if [[ "$LOCAL_TEST" == "true" ]]; then
    publish_release_to_repo_mirror "$SELF_VERSION" "$SELF_BIN"
  fi
  vm_exec "$REPO_MIRROR_NAME" sudo env OPD_RELEASE_REPO="$RELEASE_REPO" OPD_LATEST="$REPO_MIRROR_LATEST" bash -lc '
set -euo pipefail
mkdir -p "/srv/releases/$OPD_RELEASE_REPO"
printf "%s\n" "$OPD_LATEST" > "/srv/releases/$OPD_RELEASE_REPO/latest"
'
}

publish_release_to_repo_mirror() {
  local version=$1
  local bin=$2
  [[ -f "$bin" ]] || die "mock opendeploy binary not found: $bin"
  local sum
  sum=$(shasum -a 256 "$bin" | cut -d ' ' -f 1)
  log "Publishing opendeploy $version to $REPO_MIRROR_NAME"
  vm_exec "$REPO_MIRROR_NAME" sudo env \
    OPD_RELEASE_DIR="/srv/releases/$RELEASE_REPO/$version" \
    OPD_VERSION="$version" \
    OPD_ARCH="$GOARCH" \
    OPD_SHA256="$sum" \
    bash -lc '
set -euo pipefail
mkdir -p "$OPD_RELEASE_DIR"
printf "%s  opendeploy-linux-%s\n" "$OPD_SHA256" "$OPD_ARCH" > "$OPD_RELEASE_DIR/sha256sums.txt"
'
  limactl copy "$bin" "$REPO_MIRROR_NAME:/tmp/opendeploy-release"
  vm_exec "$REPO_MIRROR_NAME" sudo install -m 0755 /tmp/opendeploy-release "/srv/releases/$RELEASE_REPO/$version/opendeploy-linux-$GOARCH"
}

configure_github_token() {
  local name=$1
  [[ -n "${OPENDEPLOY_GITHUB_TOKEN:-}" ]] || return 0
  vm_exec "$name" sudo env OPENDEPLOY_GITHUB_TOKEN="$OPENDEPLOY_GITHUB_TOKEN" bash -lc 'printf "\nOPENDEPLOY_GITHUB_TOKEN=%q\n" "$OPENDEPLOY_GITHUB_TOKEN" >> /etc/opendeploy/env'
}

primary_enrollment_fingerprint() {
  vm_exec "$PRIMARY_NAME" bash -lc '
set -euo pipefail
hex=$(openssl s_client -connect 127.0.0.1:9444 -servername primary </dev/null 2>/dev/null \
  | openssl x509 -pubkey -noout \
  | openssl pkey -pubin -outform DER \
  | openssl dgst -sha256 -hex \
  | awk "{print \$2}")
printf "sha256:%s\n" "$hex"
'
}

install_primary() {
  download_opendeploy "$PRIMARY_NAME"
  limactl copy "$SERVER_BUNDLE" "$PRIMARY_NAME:/tmp/opendeploy-web.pem"
  log "Installing primary in $PRIMARY_NAME"
  local install_output install_version
  local args=(
    opendeploy install primary
    --web-listen :443
    --acme-hosts "$WEB_HOST"
    --web-tls-self-managed true
    --web-tls-cert-pem-file /tmp/opendeploy-web.pem
  )
  if [[ "$LOCAL_TEST" == "true" ]]; then
    args+=(--use-self)
    if ! install_output=$(vm_exec "$PRIMARY_NAME" sudo "${args[@]}" 2>&1); then
      printf '%s\n' "$install_output" >&2
      exit 1
    fi
    install_version=$SELF_VERSION
  else
    args+=(--version "$INSTALL_VERSION")
    if ! install_output=$(vm_exec "$PRIMARY_NAME" sudo "${args[@]}" 2>&1); then
      printf '%s\n' "$install_output" >&2
      exit 1
    fi
    install_version=$INSTALL_VERSION
  fi
  printf '%s\n' "$install_output"
  mkdir -p "$STATE_DIR"
  if [[ "$install_output" =~ Temporary[[:space:]]setup[[:space:]]password:[[:space:]]([^[:space:]]+) ]]; then
    printf 'OPD_SETUP_PASSWORD=%q\nOPD_INSTALL_VERSION=%q\n' "${BASH_REMATCH[1]}" "$install_version" > "$E2E_ENV_FILE"
  else
    die "installer output did not include a temporary setup password"
  fi
  configure_github_token "$PRIMARY_NAME"
  vm_exec "$PRIMARY_NAME" sudo usermod -aG nix-users opendeploy
  vm_exec "$PRIMARY_NAME" sudo systemctl restart opendeploy.service
  wait_for_service "$PRIMARY_NAME" opendeploy.service
  wait_for_healthz "$PRIMARY_NAME"
}

install_worker() {
  local name=$1
  download_opendeploy "$name"
  local enrollment_fingerprint help_text
  local args
  enrollment_fingerprint=$(primary_enrollment_fingerprint)
  log "Installing secondary in $name"
  help_text=$(vm_exec "$name" opendeploy install secondary -h 2>&1 || true)
  args=(opendeploy install secondary)
  if [[ "$LOCAL_TEST" == "true" ]]; then
    args+=(--use-self)
  else
    args+=(--version "$INSTALL_VERSION")
  fi
  args+=(--cluster-addr "$PRIMARY_NAME:9443" --enrollment-addr "$PRIMARY_NAME:9444")
  if [[ "$help_text" == *"enrollment-fingerprint"* ]]; then
    args+=(--enrollment-fingerprint "$enrollment_fingerprint")
  else
    log "$name installer does not support --enrollment-fingerprint; continuing without it"
  fi
  vm_exec "$name" sudo "${args[@]}"
  configure_github_token "$name"
  vm_exec "$name" sudo usermod -aG nix-users opendeploy
  vm_exec "$name" sudo systemctl restart opendeploy.service
  wait_for_service "$name" opendeploy.service
}

install_cluster() {
  local vm
  for vm in $(cluster_vm_names); do
    wait_for_systemd "$vm"
  done
  install_primary
  install_worker "$SECONDARY_NAME"
}

resolve_upgrade_version() {
  if [[ -n "${OPD_UPGRADE_VERSION:-}" ]]; then
    printf '%s' "$UPGRADE_VERSION"
    return
  fi

  local url headers tag
  url="https://api.github.com/repos/$RELEASE_REPO/releases/latest"
  if repo_mirror_enabled; then
    if tag=$(vm_exec "$PLAYWRIGHT_NAME" curl -fsSL -H "Accept: application/vnd.github+json" "$url" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("tag_name", ""))' 2>/dev/null); then
      [[ -n "$tag" ]] && printf '%s' "$tag" && return
    fi
  else
    headers=(-H "Accept: application/vnd.github+json")
    if [[ -n "${OPENDEPLOY_GITHUB_TOKEN:-}" ]]; then
      headers+=(-H "Authorization: Bearer ${OPENDEPLOY_GITHUB_TOKEN}")
    fi
    if tag=$(curl -fsSL "${headers[@]}" "$url" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("tag_name", ""))' 2>/dev/null); then
      [[ -n "$tag" ]] && printf '%s' "$tag" && return
    fi
  fi

  printf '%s' "$UPGRADE_VERSION"
}

quote_words() {
  local out="" item
  for item in "$@"; do
    out+=" $(printf '%q' "$item")"
  done
  printf '%s' "$out"
}

prepare_playwright_e2e() {
  log "Copying Playwright suite into $PLAYWRIGHT_NAME"
  vm_bash "$PLAYWRIGHT_NAME" "rm -rf $(printf '%q' "$PLAYWRIGHT_WORKDIR") && mkdir -p $(printf '%q' "$PLAYWRIGHT_WORKDIR")"
  limactl copy "$SCRIPT_DIR/e2e/package.json" "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/"
  limactl copy "$SCRIPT_DIR/e2e/package-lock.json" "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/"
  limactl copy "$SCRIPT_DIR/e2e/playwright.config.js" "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/"
  limactl copy -r "$SCRIPT_DIR/e2e/helpers" "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/"
  limactl copy -r "$SCRIPT_DIR/e2e/flows" "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/"
  vm_bash "$PLAYWRIGHT_NAME" "
set -euo pipefail
cd $(printf '%q' "$PLAYWRIGHT_WORKDIR")
npm install
npx playwright install --with-deps chromium
mkdir -p test-results playwright-report
"
}

wait_for_playwright_web() {
  log "Waiting for Playwright access to $WEB_BASE_URL"
  vm_bash "$PLAYWRIGHT_NAME" "
set -euo pipefail
for i in \$(seq 1 60); do
  if curl -fsS $(printf '%q' "$WEB_BASE_URL")/v1/healthz >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
exit 1
"
}

copy_playwright_results() {
  rm -rf "$RESULTS_DIR" "$REPORT_DIR"
  mkdir -p "$(dirname "$RESULTS_DIR")" "$(dirname "$REPORT_DIR")"
  limactl copy -r "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/test-results" "$RESULTS_DIR" >/dev/null 2>&1 || mkdir -p "$RESULTS_DIR"
  limactl copy -r "$PLAYWRIGHT_NAME:$PLAYWRIGHT_WORKDIR/playwright-report" "$REPORT_DIR" >/dev/null 2>&1 || mkdir -p "$REPORT_DIR"
}

run_playwright_flows() {
  local flows=${FLOWS:-bootstrap-enroll-nixdocker}
  local flow_args=()
  local flow
  IFS=',' read -ra flow_names <<< "$flows"
  for flow in "${flow_names[@]}"; do
    flow=${flow//[[:space:]]/}
    [[ -z "$flow" ]] && continue
    flow_args+=("flows/${flow}.spec.js")
  done
  [[ ${#flow_args[@]} -gt 0 ]] || die "no flows selected"

  if [[ -f "$E2E_ENV_FILE" ]]; then
    set -a
    source "$E2E_ENV_FILE"
    set +a
  fi

  local upgrade_version
  upgrade_version=$(resolve_upgrade_version)
  log "Upgrade target version: $upgrade_version"
  prepare_playwright_e2e
  wait_for_playwright_web

  local args remote_cmd status
  args=$(quote_words "${flow_args[@]}")
  remote_cmd="cd $(printf '%q' "$PLAYWRIGHT_WORKDIR") && env OPD_BASE_URL=$(printf '%q' "$WEB_BASE_URL") OPD_IGNORE_HTTPS_ERRORS=true OPD_SECONDARY_HOST=$(printf '%q' "$SECONDARY_NAME") OPD_SETUP_PASSWORD=$(printf '%q' "${OPD_SETUP_PASSWORD:-}") OPD_INSTALL_VERSION=$(printf '%q' "${OPD_INSTALL_VERSION:-}") OPD_UPGRADE_VERSION=$(printf '%q' "$upgrade_version") OPD_BACKUP_RESTORE=$(printf '%q' "$BACKUP_RESTORE") OPD_BACKUP_RESTORE_STATE=$(printf '%q' "$PLAYWRIGHT_WORKDIR/test-results/backup-restore.env") OPD_POSTGRES_IMAGE=$(printf '%q' "$POSTGRES_IMAGE") OPD_MINIO_IMAGE=$(printf '%q' "$MINIO_IMAGE") OPENDEPLOY_GITHUB_TOKEN=$(printf '%q' "${OPENDEPLOY_GITHUB_TOKEN:-}") npx playwright test$args"
  log "Running Playwright flows:$args"
  set +e
  vm_exec "$PLAYWRIGHT_NAME" bash -lc "$remote_cmd"
  status=$?
  set -e
  copy_playwright_results
  return "$status"
}
