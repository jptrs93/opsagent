#!/usr/bin/env bash
# Spin up systemd containers and run `opendeploy install`. By default this uses a
# GitHub release binary and remote GitHub/runtime downloads. Set
# OPENDEPLOY_LOCAL_TEST=true to build the local checkout, copy that binary into
# each container, install it as v0.0.0 with `--use-self`, and route GitHub/runtime
# downloads through the local repo container.
#
# Usage:
#   ./run.sh
#
# Afterwards, poke at it:
#   docker exec -it opendeploy-install-primary bash
#   docker exec opendeploy-install-primary systemctl status opendeploy-containerd
#   docker exec opendeploy-install-secondary systemctl status opendeploy
#   open http://localhost:8080
# Tear down:
#   docker rm -f opendeploy-install-primary opendeploy-install-secondary && docker volume rm opendeploy-primary-containerd opendeploy-secondary-containerd && docker network rm opendeploy-install-test
set -euo pipefail

cd "$(dirname "$0")"
PRIMARY_NAME=opendeploy-install-primary
SECONDARY_NAME=opendeploy-install-secondary
IMG=opendeploy-install-test
NETWORK=opendeploy-install-test
PRIMARY_VOLUME=opendeploy-primary-containerd
SECONDARY_VOLUME=opendeploy-secondary-containerd
REPO=jptrs93/opsagent
VERSION=v0.0.160
SELF_VERSION=v0.0.0
LOCAL_TEST=${OPENDEPLOY_LOCAL_TEST:-false}
LOCAL_REPO_NAME=${OPD_LOCAL_REPO_CONTAINER:-opendeploy-local-repo}
STATE_DIR="$(pwd)/.tmp"
E2E_ENV_FILE="$STATE_DIR/e2e.env"

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

SELF_BIN="$(pwd)/.tmp/opendeploy-linux-${ARCH}"

build_self_opendeploy() {
	if [[ "$LOCAL_TEST" != "true" ]]; then
		return
	fi

	echo "==> Building local opendeploy ${SELF_VERSION} for linux/${ARCH}"
	(
		cd ../frontend
		pnpm run build
	)
	mkdir -p "$(dirname "$SELF_BIN")"
	(
		cd ../backend
		CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w -X github.com/jptrs93/opsagent/backend/version.Version=${SELF_VERSION}" -o "$SELF_BIN" .
	)
}

publish_self_to_local_repo() {
	if [[ "$LOCAL_TEST" != "true" ]]; then
		return
	fi
	if ! docker inspect "$LOCAL_REPO_NAME" >/dev/null 2>&1; then
		echo "local repo container ${LOCAL_REPO_NAME} is not running; start it with testing/localrepocontainer/run.sh" >&2
		exit 1
	fi
	local release_dir="/srv/releases/${REPO}/${SELF_VERSION}"
	local sum
	sum=$(shasum -a 256 "$SELF_BIN" | cut -d ' ' -f 1)
	echo "==> Publishing local opendeploy ${SELF_VERSION} to ${LOCAL_REPO_NAME}"
	docker exec \
		-e OPD_RELEASE_DIR="$release_dir" \
		-e OPD_VERSION="$SELF_VERSION" \
		-e OPD_ARCH="$ARCH" \
		-e OPD_SHA256="$sum" \
		"$LOCAL_REPO_NAME" bash -lc '
			set -euo pipefail
			mkdir -p "$OPD_RELEASE_DIR"
			printf "%s  opendeploy-linux-%s\n" "$OPD_SHA256" "$OPD_ARCH" > "$OPD_RELEASE_DIR/sha256sums.txt"
			printf "%s\n" "$OPD_VERSION" > /srv/releases/jptrs93/opsagent/latest
		'
	docker cp "$SELF_BIN" "$LOCAL_REPO_NAME:${release_dir}/opendeploy-linux-${ARCH}"
}

build_self_opendeploy
publish_self_to_local_repo

echo "==> Building systemd container image"
docker build -t "$IMG" .

echo "==> Resetting test containers"
docker rm -f "$PRIMARY_NAME" "$SECONDARY_NAME" >/dev/null 2>&1 || true
docker volume rm "$PRIMARY_VOLUME" "$SECONDARY_VOLUME" >/dev/null 2>&1 || true
docker network rm "$NETWORK" >/dev/null 2>&1 || true
docker network create "$NETWORK" >/dev/null 2>&1 || true
mkdir -p "$STATE_DIR"
rm -f "$E2E_ENV_FILE"

start_container() {
	local name=$1
	local volume=$2
	shift 2

	# --privileged + host cgroupns: systemd as PID 1, and lets the installed
	# opendeploy-containerd manage cgroups for real.
	# The named volume gives containerd an ext4-backed dir so its overlayfs
	# snapshotter isn't stacked on Docker's own overlayfs.
	docker run -d --name "$name" \
		--privileged \
		--cgroupns=host \
		--network "$NETWORK" \
		--tmpfs /run --tmpfs /run/lock \
		-v "$volume":/var/lib/opendeploy-containerd \
		"$@" \
		"$IMG" >/dev/null
}

echo "==> Starting primary systemd container"
start_container "$PRIMARY_NAME" "$PRIMARY_VOLUME" -p 8080:8080

echo "==> Starting secondary systemd container"
start_container "$SECONDARY_NAME" "$SECONDARY_VOLUME"

wait_for_systemd() {
	local name=$1
	local state=""
	for _ in $(seq 1 30); do
		state=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
		[[ "$state" == running || "$state" == degraded ]] && break
		sleep 1
	done
	if [[ "$state" != running && "$state" != degraded ]]; then
		echo "systemd did not come up in $name (state: ${state:-unknown}); container logs:" >&2
		docker logs "$name" | tail -20 >&2
		exit 1
	fi
}

echo "==> Waiting for systemd to boot"
wait_for_systemd "$PRIMARY_NAME"
wait_for_systemd "$SECONDARY_NAME"

download_opendeploy() {
	local name=$1
	if [[ "$LOCAL_TEST" == "true" ]]; then
		echo "==> Copying local opendeploy ${SELF_VERSION} for linux/${ARCH} into ${name}"
		docker cp "$SELF_BIN" "$name:/usr/local/bin/opendeploy"
		docker exec "$name" chmod 0755 /usr/local/bin/opendeploy
		return
	fi
	echo "==> Downloading opendeploy ${VERSION} for linux/${ARCH} in ${name}"
	docker exec \
		-e OPD_REPO="$REPO" \
		-e OPD_VERSION="$VERSION" \
		-e OPD_ARCH="$ARCH" \
		-e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" \
		"$name" bash -lc '
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

install_primary() {
	download_opendeploy "$PRIMARY_NAME"
	local install_output
	if [[ "$LOCAL_TEST" == "true" ]]; then
		if ! install_output=$(docker exec -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" "$PRIMARY_NAME" opendeploy install primary --use-self --http-only true --web-listen :8080 2>&1); then
			printf '%s\n' "$install_output" >&2
			exit 1
		fi
	else
		if ! install_output=$(docker exec -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" "$PRIMARY_NAME" opendeploy install primary --version "$VERSION" --http-only true --web-listen :8080 2>&1); then
			printf '%s\n' "$install_output" >&2
			exit 1
		fi
	fi
	printf '%s\n' "$install_output"
	configure_local_test "$PRIMARY_NAME"
	if [[ "$install_output" =~ Temporary[[:space:]]setup[[:space:]]password:[[:space:]]([^[:space:]]+) ]]; then
		local install_version="$VERSION"
		if [[ "$LOCAL_TEST" == "true" ]]; then
			install_version="$SELF_VERSION"
		fi
		printf 'OPD_SETUP_PASSWORD=%q\nOPD_INSTALL_VERSION=%q\n' "${BASH_REMATCH[1]}" "$install_version" > "$E2E_ENV_FILE"
	else
		echo "installer output did not include a temporary setup password" >&2
		exit 1
	fi
	configure_github_token "$PRIMARY_NAME"
	# Ubuntu's Nix daemon socket directory is restricted to nix-users.
	docker exec "$PRIMARY_NAME" usermod -aG nix-users opendeploy
	docker exec "$PRIMARY_NAME" systemctl restart opendeploy.service
	wait_for_service "$PRIMARY_NAME" opendeploy.service
}

install_secondary() {
	download_opendeploy "$SECONDARY_NAME"
	if [[ "$LOCAL_TEST" == "true" ]]; then
		docker exec -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" "$SECONDARY_NAME" opendeploy install secondary --use-self --cluster-addr "$PRIMARY_NAME:9443" --enrollment-addr "$PRIMARY_NAME:9444"
	else
		docker exec -e OPENDEPLOY_LOCAL_TEST="$LOCAL_TEST" "$SECONDARY_NAME" opendeploy install secondary --version "$VERSION" --cluster-addr "$PRIMARY_NAME:9443" --enrollment-addr "$PRIMARY_NAME:9444"
	fi
	configure_local_test "$SECONDARY_NAME"
	configure_github_token "$SECONDARY_NAME"
	# Ubuntu's Nix daemon socket directory is restricted to nix-users.
	docker exec "$SECONDARY_NAME" usermod -aG nix-users opendeploy
	docker exec "$SECONDARY_NAME" systemctl restart opendeploy.service
	wait_for_service "$SECONDARY_NAME" opendeploy.service
}

wait_for_service() {
	local name=$1
	local service=$2
	local state=""
	for _ in $(seq 1 30); do
		state=$(docker exec "$name" systemctl is-active "$service" 2>/dev/null || true)
		[[ "$state" == active ]] && return
		sleep 1
	done
	echo "$service did not become active in $name (state: ${state:-unknown})" >&2
	docker exec "$name" systemctl status "$service" --no-pager >&2 || true
	exit 1
}

configure_github_token() {
	local name=$1
	if [[ -z "${OPENDEPLOY_GITHUB_TOKEN:-}" ]]; then
		return
	fi
	docker exec -e OPENDEPLOY_GITHUB_TOKEN="$OPENDEPLOY_GITHUB_TOKEN" "$name" bash -lc '
		set -euo pipefail
		printf "\nOPENDEPLOY_GITHUB_TOKEN=%q\n" "$OPENDEPLOY_GITHUB_TOKEN" >> /etc/opendeploy/env
	'
}

configure_local_test() {
	local name=$1
	if [[ "$LOCAL_TEST" != "true" ]]; then
		return
	fi
	docker exec "$name" bash -lc '
		set -euo pipefail
		grep -q "^OPENDEPLOY_LOCAL_TEST=" /etc/opendeploy/env 2>/dev/null || printf "\nOPENDEPLOY_LOCAL_TEST=true\n" >> /etc/opendeploy/env
	'
}

install_primary
install_secondary

echo
echo "==> Post-install state"
docker exec "$PRIMARY_NAME" systemctl status opendeploy-containerd --no-pager || true
docker exec "$PRIMARY_NAME" systemctl status opendeploy --no-pager || true
docker exec "$SECONDARY_NAME" systemctl status opendeploy-containerd --no-pager || true
docker exec "$SECONDARY_NAME" systemctl status opendeploy --no-pager || true
echo "==> OpenDeploy HTTP-only API: http://localhost:8080"
