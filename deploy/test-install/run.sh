#!/usr/bin/env bash
# Spin up a systemd container and run `opendeploy install` from a GitHub release
# binary. This intentionally does not build or copy any local opendeploy binary;
# the bootstrap installer and installed service both come from release artifacts.
#
# Usage:
#   ./run.sh
#
# Afterwards, poke at it:
#   docker exec -it opendeploy-install-test bash
#   docker exec opendeploy-install-test systemctl status opendeploy-containerd
#   open http://localhost:8080
# Tear down:
#   docker rm -f opendeploy-install-test && docker volume rm opendeploy-test-containerd
set -euo pipefail

cd "$(dirname "$0")"
NAME=opendeploy-install-test
IMG=opendeploy-install-test
VOLUME=opendeploy-test-containerd
REPO=jptrs93/opsagent
VERSION=v0.0.110

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

echo "==> Resetting test container"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker volume rm "$VOLUME" >/dev/null 2>&1 || true

echo "==> Starting systemd container"
# --privileged + host cgroupns: systemd as PID 1, and lets the installed
# opendeploy-containerd manage cgroups for real.
# The named volume gives containerd an ext4-backed dir so its overlayfs
# snapshotter isn't stacked on Docker's own overlayfs.
docker run -d --name "$NAME" \
	--privileged \
	--cgroupns=host \
	--tmpfs /run --tmpfs /run/lock \
	-v "$VOLUME":/var/lib/opendeploy-containerd \
	-p 8080:8080 \
	"$IMG" >/dev/null

echo "==> Waiting for systemd to boot"
state=""
for _ in $(seq 1 30); do
	state=$(docker exec "$NAME" systemctl is-system-running 2>/dev/null || true)
	[[ "$state" == running || "$state" == degraded ]] && break
	sleep 1
done
if [[ "$state" != running && "$state" != degraded ]]; then
	echo "systemd did not come up (state: ${state:-unknown}); container logs:" >&2
	docker logs "$NAME" | tail -20 >&2
	exit 1
fi

download_opendeploy() {
	echo "==> Downloading opendeploy ${VERSION} for linux/${ARCH}"
	docker exec \
		-e OPD_REPO="$REPO" \
		-e OPD_VERSION="$VERSION" \
		-e OPD_ARCH="$ARCH" \
		"$NAME" bash -lc '
			set -euo pipefail
			url="https://github.com/${OPD_REPO}/releases/download/${OPD_VERSION}/opendeploy-linux-${OPD_ARCH}"
			curl -fsSL "$url" -o /usr/local/bin/opendeploy
			chmod 0755 /usr/local/bin/opendeploy
		'
}

download_opendeploy
docker exec "$NAME" opendeploy install --version "$VERSION" --http-only true --web-listen :8080
docker exec "$NAME" systemctl start opendeploy.service

echo
echo "==> Post-install state"
docker exec "$NAME" systemctl status opendeploy-containerd --no-pager || true
docker exec "$NAME" systemctl status opendeploy --no-pager || true
echo "==> OpenDeploy HTTP-only API: http://localhost:8080"
