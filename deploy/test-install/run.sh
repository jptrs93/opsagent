#!/usr/bin/env bash
# Spin up a systemd container and run the locally-built `opendeploy install`
# inside it. The installer still exercises its normal release download path, then
# this test swaps the active service symlink to the local build so the running
# container uses the current workspace code.
#
# Usage:
#   ./run.sh            # build, start container, run `opendeploy install`
#   ./run.sh --dry-run  # same but `opendeploy install --dry-run`
#   ./run.sh --shell    # just drop into a shell in the running container
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
ARCH=$(docker version --format '{{.Server.Arch}}')

if [[ "${1:-}" != "--shell" ]]; then
    echo "==> Cross-compiling opendeploy for linux/$ARCH"
    (cd ../../backend && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o ../deploy/test-install/.build/opendeploy .)

    echo "==> Building systemd container image"
    docker build -t "$IMG" .
fi

if ! docker container inspect "$NAME" >/dev/null 2>&1; then
    echo "==> Starting systemd container"
    # --privileged + host cgroupns: systemd as PID 1, and lets the installed
    # opendeploy-containerd manage cgroups for real.
    # The named volume gives containerd an ext4-backed dir so its overlayfs
    # snapshotter isn't stacked on Docker's own overlayfs.
	docker run -d --name "$NAME" \
		--privileged \
		--cgroupns=host \
		--tmpfs /run --tmpfs /run/lock \
		-v opendeploy-test-containerd:/var/lib/opendeploy-containerd \
		-p 8080:18080 \
		"$IMG" >/dev/null

    echo "==> Waiting for systemd to boot"
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
fi

configure_http_only() {
	docker exec "$NAME" bash -lc '
		set -euo pipefail
		install -m 0755 /usr/local/bin/opendeploy /var/lib/opendeploy/bin/opendeploy-local
		ln -sfn /var/lib/opendeploy/bin/opendeploy-local /var/lib/opendeploy/bin/opendeploy
		if grep -q "^OPENDEPLOY_HTTP_ONLY=" /etc/opendeploy/env; then
			sed -i "s/^OPENDEPLOY_HTTP_ONLY=.*/OPENDEPLOY_HTTP_ONLY=true/" /etc/opendeploy/env
		else
			printf "\nOPENDEPLOY_HTTP_ONLY=true\n" >> /etc/opendeploy/env
		fi
		cat >/etc/systemd/system/opendeploy-http-forward.service <<"EOF"
[Unit]
Description=Forward published Docker port to OpenDeploy HTTP-only loopback listener
After=network.target opendeploy.service

[Service]
ExecStart=/usr/bin/socat TCP-LISTEN:18080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:8080
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
EOF
		systemctl daemon-reload
		systemctl restart opendeploy.service
		systemctl enable --now opendeploy-http-forward.service
	'
}

# allocate a TTY only when we have one (so the script also works from CI/pipes)
TTYFLAG=""; [ -t 0 ] && TTYFLAG="-it"

case "${1:-}" in
    --shell)
        exec docker exec $TTYFLAG "$NAME" bash
        ;;
    --dry-run)
        docker cp .build/opendeploy "$NAME":/usr/local/bin/opendeploy
        docker exec $TTYFLAG "$NAME" opendeploy install --dry-run
        ;;
	*)
		docker cp .build/opendeploy "$NAME":/usr/local/bin/opendeploy
		docker exec $TTYFLAG "$NAME" opendeploy install
		configure_http_only
		echo
		echo "==> Post-install state"
		docker exec "$NAME" systemctl status opendeploy-containerd --no-pager || true
		docker exec "$NAME" systemctl status opendeploy --no-pager || true
		docker exec "$NAME" systemctl status opendeploy-http-forward --no-pager || true
		echo "==> OpenDeploy HTTP-only API: http://localhost:8080"
		;;
esac
