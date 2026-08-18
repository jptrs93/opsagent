# OpenDeploy Playwright Flows

This directory contains the Playwright specs and helpers used by the E2E harness.
The harness runs them in Docker against the Lima primary/secondary VM cluster.
For mock remote mode, start the persistent repo mirror first:

```sh
go run ./testing-vms/test-orchestrator repo-mirror-up
```

Then run the main harness:

```sh
bash testing-vms/run.sh
```

The harness copies this suite inside the Playwright Docker container, points it at
the HTTPS test origin, and writes `test-results` / `playwright-report` on the host.

Select one or more flow files with a comma-separated list:

```sh
FLOWS=bootstrap-enroll-nixdocker bash testing-vms/run.sh
```

`bootstrap-enroll-nixdocker` ends with the independent cases in
`cases/postgres-pgbackrest.js`. Those cases deliberately use dedicated resource
names and run serially after the baseline suite in the same authenticated browser
context.

`cases/access-enforcement.js` runs mid-flow, after the asset-backed baseline
content exists. It opens a second browser context with its own virtual
authenticator, creates a second user through the setup-password bootstrap flow,
revokes its automatic cluster_admin grant, and verifies the live state stream
empties that session. The admin then creates the `e2e-restricted` space and
grants space_admin bound to it, and the restricted session verifies scoped
visibility (its space, the node/user directory, no default-space content), the
cluster settings withheld on a fresh stream (the settings check reloads first,
because `config_snapshot` is only sent when the caller may view cluster state
and an already-delivered snapshot is not cleared by the live re-emit), the
full create/manage lifecycle for deployments, secrets, configs, folders, and
assets inside its space, and access-denied errors on cluster-level actions
(space creation, grant management, node rename). The restricted deployment and
values are deleted before the flow continues; the extra user and space remain.

`cases/https-headers.js`, `cases/proto-stream.js`, and `cases/websocket.js`
extend the HTTPS ingress coverage. The header cases drive the existing
`httpecho` backends (`/headers` and `/setheaders` endpoints) to verify
request/response header forwarding, multi-value headers, hop-by-hop
stripping, and X-Forwarded-For spoof protection. The proto-stream cases
deploy `testexamples/protostream` (a cleanproto service with unary,
server-stream, client-stream, and bidi RPCs) behind two hostnames — one
`backend = "h2c"`, one HTTP/1.1 — and verify frame ordering, incremental
delivery, upload digests, bidi ping-pong interleaving, server half-close,
and cancellation from both ends via the app's stream-report RPC
(`helpers/streamClient.js` hand-drives the framing over raw http1/http2
clients; regenerate its `helpers/protostreamgen` models with
`testexamples/protostream/generate.sh`). The websocket cases deploy
`testexamples/wsecho` (a dependency-free RFC 6455 server) and verify echo,
large frames, ping/pong, close-code propagation in both directions, and
abrupt-disconnect propagation via its `/state` endpoint
(`helpers/wsClient.js` implements the client side, including masking). All
three route groups are re-verified after the agent and opendeploy-net
upgrade rollouts.

`cases/space-moves.js` runs inside that window, while the restricted
deployment and values still pin references and the restricted session is a
live second observer. It moves an unreferenced secret and asset between
global and the restricted space through the Move dialog's space picker,
verifying the restricted session sees rows appear and vanish live (the
tombstone path) and can reveal a secret just moved into its space; verifies
referenced values refuse to move away from their referencing space in both
directions, and that a value referenced only from another space may move
toward it; verifies a mounted asset also refuses delete; and checks the
restricted user's Move dialog offers only its visible spaces while folder
moves offer no space picker at all.
