# Systemd-enabled Ubuntu for exercising `opendeploy install` for real:
# real systemctl, real units, real sudoers, real (nested) containerd.
# Must be run --privileged with the host cgroup namespace (see run.sh).
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
		systemd systemd-sysv \
		sudo \
		ca-certificates \
		git \
		curl \
		nix-bin nix-setup-systemd \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /etc/nix \
    && printf 'experimental-features = nix-command flakes\nsandbox = false\nallowed-users = *\ntrusted-users = root opendeploy\n' > /etc/nix/nix.conf \
    # don't let getty/udev etc. spin in a container
    && systemctl mask getty@tty1.service systemd-udevd.service systemd-udevd-kernel.socket

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
