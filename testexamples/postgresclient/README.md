# postgresclient

Tiny `nixDockerBuild` test app that connects to a Postgres deployment, creates a
table, inserts rows, queries them back, logs the results, and then keeps running.

The E2E flow uses it with the official `postgres:18` image running on the same
OpenDeploy worker. Container deployments currently use host networking, so the
client connects to `127.0.0.1:5432`.

Required environment variables:

```text
PGHOST=127.0.0.1
PGPORT=5432
PGUSER=${s:postgres}
PGPASSWORD=${s:postgrespass}
PGDATABASE=postgres
```
