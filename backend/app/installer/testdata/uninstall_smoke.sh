#!/bin/sh
# Smoke test for `opendeploy uninstall` inside a privileged Linux container:
# start a real containerd with a running container, fabricate the rest of the
# host state the agent leaves behind (stray processes, a container cgroup,
# netns/veth/WireGuard-named links, routes, nftables tables, bind mounts under
# the containerd state dir, every data root), run `uninstall --purge` (dry-run
# first), and verify nothing is left while unrelated state survives.
#
# Run from the repo root (expects /od/opendeploy to be a linux build of the
# agent for the docker host's architecture):
#
#   GOOS=linux GOARCH=arm64 go build -C backend -o /tmp/od/opendeploy .
#   cp backend/app/installer/testdata/uninstall_smoke.sh /tmp/od/
#   docker run --rm --privileged -v /tmp/od:/od:ro alpine:3.20 sh /od/uninstall_smoke.sh
set -eu
apk add --no-cache iproute2 nftables containerd containerd-ctr runc >/dev/null 2>&1

# Fake systemctl: no systemd here. opendeploy-containerd.service is "installed
# and active" while the containerd we start below is alive; stop kills only the
# daemon (KillMode=process), like the real unit.
cat > /usr/local/bin/systemctl <<'SC'
#!/bin/sh
verb="$1"; shift
[ "$1" = "--quiet" ] && shift
case "$verb $1" in
  "cat opendeploy-containerd.service"|"is-active opendeploy-containerd.service")
    test -f /tmp/containerd.pid && kill -0 "$(cat /tmp/containerd.pid)" 2>/dev/null ;;
  "stop opendeploy-containerd.service")
    echo "systemctl $verb $*"; kill "$(cat /tmp/containerd.pid)"; rm -f /tmp/containerd.pid; sleep 1 ;;
  cat*|is-active*|is-enabled*) exit 1 ;;
  show*) exit 0 ;;
  *) echo "systemctl $verb $*"; exit 0 ;;
esac
SC
chmod +x /usr/local/bin/systemctl

echo "--- starting containerd with a running container"
# Move this shell out of the root cgroup so runc can create sibling cgroups
# with controllers (cgroup v2 "no internal processes" rule).
mkdir -p /sys/fs/cgroup/init && echo $$ > /sys/fs/cgroup/init/cgroup.procs
echo "+pids +memory" > /sys/fs/cgroup/cgroup.subtree_control || true
mkdir -p /run/opendeploy /var/lib/opendeploy-containerd /run/opendeploy-containerd
cat > /tmp/ctrd.toml <<X
version = 2
root = "/var/lib/opendeploy-containerd"
state = "/run/opendeploy-containerd"
[grpc]
  address = "/run/opendeploy/containerd.sock"
X
containerd --config /tmp/ctrd.toml >/tmp/containerd.log 2>&1 &
echo $! > /tmp/containerd.pid
CTR="ctr -a /run/opendeploy/containerd.sock -n opendeploy"
for i in $(seq 1 100); do $CTR version >/dev/null 2>&1 && break; sleep 0.2; done
$CTR image pull docker.io/library/alpine:3.20 >/dev/null
$CTR run -d --snapshotter native docker.io/library/alpine:3.20 opendeploy-1-1-1-1 sleep 1000
$CTR task ls
CONTAINER_PID=$($CTR task ls | awk '$1 == "opendeploy-1-1-1-1" { print $2 }')
echo "container pid $CONTAINER_PID"

echo "--- fabricating state"
for d in /var/lib/opendeploy/bin /var/lib/opendeploy/runtime/bin /var/lib/opendeploy-assets /var/lib/opendeploy-releases /var/lib/opendeploy-volumes /var/lib/opendeploy-build-logs /var/lib/opendeploy-run-logs /var/lib/opendeploy-log-archive /var/lib/opendeploy-metrics /var/lib/opendeploy-containerd /var/lib/opendeploy-future /etc/opendeploy /run/opendeploy /run/opendeploy-containerd/task/rootfs; do
  mkdir -p "$d"; echo x > "$d/marker"
done
echo env > /etc/opendeploy/env
touch /etc/sudoers.d/opendeploy /etc/sudoers.d/opendeploy.new 2>/dev/null || mkdir -p /etc/sudoers.d && touch /etc/sudoers.d/opendeploy /etc/sudoers.d/opendeploy.new
ln -sfn /var/lib/opendeploy-releases/opendeploy /var/lib/opendeploy/bin/opendeploy

# A host dir bind-mounted into a "rootfs": must be unmounted, never deleted through.
mkdir -p /host-data && echo precious > /host-data/keep
mount --bind /host-data /run/opendeploy-containerd/task/rootfs
mount -t tmpfs tmpfs /run/opendeploy-containerd/task/rootfs/marker 2>/dev/null || true

# Stray processes: one running from the runtime dir, one from the releases dir.
# busybox picks its applet by argv[0], so the copies must keep the name sleep.
cp /bin/busybox /var/lib/opendeploy/runtime/bin/sleep
cp /bin/busybox /var/lib/opendeploy-releases/sleep
/var/lib/opendeploy/runtime/bin/sleep 1000 & SHIM=$!
/var/lib/opendeploy-releases/sleep 1000 & AGENT=$!
sleep 0.2; kill -0 $SHIM && kill -0 $AGENT && echo "stray processes running: $SHIM $AGENT"

# Container cgroup with a process in it.
CG_OK=0
if mkdir -p /sys/fs/cgroup/opendeploy/opendeploy-9-9-9-9 2>/dev/null; then
  sleep 1000 & CGPID=$!
  if echo $CGPID > /sys/fs/cgroup/opendeploy/opendeploy-9-9-9-9/cgroup.procs 2>/dev/null; then CG_OK=1; fi
fi

# Network state.
ip netns add opendeploy-1-1-1-1
ip link add od1s0 type veth peer name eth0 netns opendeploy-1-1-1-1
ip link add odwg0 type dummy
ip link add keepme type dummy
ip -6 route add unreachable fd00:1234::/64 proto 200
ip route add unreachable 10.99.0.0/16 proto 200
nft add table ip opendeploy; nft add table ip6 opendeploy; nft add table inet keepme

echo "--- dry run"
/od/opendeploy uninstall --purge --dry-run
echo "--- real run"
/od/opendeploy uninstall --purge --yes

echo "--- verifying"
fail=0
chk() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
chk "host data survived" 'test -f /host-data/keep'
chk "container process killed" "! kill -0 $CONTAINER_PID 2>/dev/null"
chk "containerd stopped" '! test -f /tmp/containerd.pid'
chk "no shim left" '! ps -o comm | grep -q containerd-shim'
for d in /var/lib/opendeploy /var/lib/opendeploy-assets /var/lib/opendeploy-releases /var/lib/opendeploy-volumes /var/lib/opendeploy-build-logs /var/lib/opendeploy-run-logs /var/lib/opendeploy-log-archive /var/lib/opendeploy-metrics /var/lib/opendeploy-containerd /var/lib/opendeploy-future /etc/opendeploy /run/opendeploy /run/opendeploy-containerd; do
  chk "removed $d" "! test -e $d"
done
chk "sudoers removed" '! test -e /etc/sudoers.d/opendeploy && ! test -e /etc/sudoers.d/opendeploy.new'
chk "shim process killed" "! kill -0 $SHIM 2>/dev/null"
chk "agent process killed" "! kill -0 $AGENT 2>/dev/null"
if [ $CG_OK = 1 ]; then chk "cgroup process killed" "! kill -0 $CGPID 2>/dev/null"; chk "cgroup removed" '! test -e /sys/fs/cgroup/opendeploy'; else echo "skip cgroup (not writable here)"; fi
chk "netns removed" '! ip netns list | grep -q opendeploy-1-1-1-1'
chk "veth removed" '! ip link show od1s0 >/dev/null 2>&1'
chk "wg link removed" '! ip link show odwg0 >/dev/null 2>&1'
chk "unrelated link kept" 'ip link show keepme >/dev/null 2>&1'
chk "v6 route removed" '! ip -6 route show proto 200 | grep -q fd00:1234'
chk "v4 route removed" '! ip route show proto 200 | grep -q 10.99'
chk "nft ip table removed" '! nft list tables | grep -q "table ip opendeploy"'
chk "nft ip6 table removed" '! nft list tables | grep -q "table ip6 opendeploy"'
chk "unrelated nft table kept" 'nft list tables | grep -q "table inet keepme"'
chk "no leftover mounts" '! grep -q opendeploy-containerd /proc/self/mountinfo'
exit $fail
