# postgresclient

Tiny `nixDockerBuild` test app that connects to a Postgres deployment, creates a
table, inserts rows, queries them back, logs the results, and then keeps running.

The E2E flow uses it with the official `postgres:18` image running as a separate
virtual-network deployment on a different OpenDeploy worker. The cross-node
client receives the server's stable IPv6 address through an Address-typed
environment variable and connects without host-port forwarding. A same-worker
client separately verifies node-local internal DNS.

Required environment variables:

```text
PGHOST=postgres18.default.internal
PGPORT=5432
PGUSER=<secret ref: postgres>
PGPASSWORD=<secret ref: postgrespass>
PGDATABASE=postgres
```
