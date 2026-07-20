# bind443

Tiny `nixDockerBuild` test app that attempts an IPv4-only `tcp4` bind to
`0.0.0.0:443` and logs whether the bind succeeded. It also scans
`/proc/net/tcp` and `/proc/net/tcp6` for existing port 443 listeners and
attempts to map socket inodes back to visible processes under `/proc/*/fd`.

When successful, it keeps the listener open, logs a heartbeat every 10 seconds,
and logs accepted TCP connections before closing them.

Example deployment request shape:

```json
{
  "identity": {
    "name": "bind443",
    "spaceId": 1
  },
  "nodeId": 1,
  "spec": {
    "prepare": {
      "nixDockerBuild": {
        "repo": "github.com/jptrs93/opsagent",
        "flake": "testexamples/bind443/flake.nix"
      }
    },
    "runner": {
      "container": {
        "disableDataVolume": true
      }
    }
  }
}
```

Expected success log:

```text
bind443 listen successful network=tcp4 addr=0.0.0.0:443 actual=0.0.0.0:443
```

If another process owns the port or the process lacks permission, it logs
`bind443 listen failed` and exits non-zero.

If the container has host networking but not the host PID namespace, the scan may
show a listening socket inode with `visible_process=none`; that means the socket
is visible in the network namespace, but the owning host process is not visible
from this container's PID namespace.
