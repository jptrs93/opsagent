#!/usr/bin/env bash
# Spin up systemd containers and run `opendeploy install` from a GitHub release
# binary. This intentionally does not build or copy any local opendeploy binary;
# the bootstrap installer and installed services both come from release artifacts.
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
VERSION=v0.0.116

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

echo "==> Building systemd container image"
docker build -t "$IMG" .

echo "==> Resetting test containers"
docker rm -f "$PRIMARY_NAME" "$SECONDARY_NAME" >/dev/null 2>&1 || true
docker volume rm "$PRIMARY_VOLUME" "$SECONDARY_VOLUME" >/dev/null 2>&1 || true
docker network rm "$NETWORK" >/dev/null 2>&1 || true
docker network create "$NETWORK" >/dev/null

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
	echo "==> Downloading opendeploy ${VERSION} for linux/${ARCH} in ${name}"
	docker exec \
		-e OPD_REPO="$REPO" \
		-e OPD_VERSION="$VERSION" \
		-e OPD_ARCH="$ARCH" \
		"$name" bash -lc '
			set -euo pipefail
			url="https://github.com/${OPD_REPO}/releases/download/${OPD_VERSION}/opendeploy-linux-${OPD_ARCH}"
			curl -fsSL "$url" -o /usr/local/bin/opendeploy
			chmod 0755 /usr/local/bin/opendeploy
		'
}

install_primary() {
	download_opendeploy "$PRIMARY_NAME"
	docker exec "$PRIMARY_NAME" opendeploy install primary --version "$VERSION" --http-only true --web-listen :8080
	docker exec "$PRIMARY_NAME" systemctl start opendeploy.service
}

install_secondary() {
	download_opendeploy "$SECONDARY_NAME"
	docker exec "$SECONDARY_NAME" opendeploy install secondary --version "$VERSION" --primary-addr "$PRIMARY_NAME:9444"
	docker exec "$SECONDARY_NAME" systemctl start opendeploy.service
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
