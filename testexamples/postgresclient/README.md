# postgresclient

Tiny `nixDockerBuild` test app that connects to a Postgres deployment, creates a
table, inserts rows, queries them back, logs the results, and then keeps running.

The E2E flow uses it with the official `postgres:18` image running as a separate
virtual-network deployment on the same OpenDeploy worker. The client resolves
the server through OpenDeploy's internal DNS and connects directly over its
virtual IPv6 address without host-port forwarding. A second client receives the
same stable IPv6 address directly through an Address-typed environment variable.

Required environment variables:

```text
PGHOST=postgres18.default.internal
PGPORT=5432
PGUSER=<secret ref: postgres>
PGPASSWORD=<secret ref: postgrespass>
PGDATABASE=postgres
```
