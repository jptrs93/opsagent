# CLAUDE

A deployment management tool with a Go backend and VanJS frontend.

## Project structure

```
api-contract/        Protobuf API schema and code generation.
backend/             Go HTTP server, handlers, auth, engine, and storage.
frontend/            VanJS + Vite SPA.
docs/                Project documentation.
```

Backend import tiers, top to bottom (a package only imports packages below it):

```
app/primary                      wiring (run.go, runtime.go)
app/primary/{webuihandler,clusterhandler,enrollmenthandler,scheduler,netmappublisher}
app/primary/domain/{deployments,scheduledinstances,nodes,networkpolicies,assets,secrets,values,authz,users,agentsessions,systemconfig,pki,acmeissue}
                                 primary domain logic: functions over *state.Service / *pq.Queries
storage/primarydb/{state,pq}
                                 state = Commit, write mutex, update triggers, pubsub, snapshot; pq = SQL only
storage, storage/secondarydb/*, storage/sqlitedb, storage/logdb
lib/*                            shared by primary and secondary; never imports app/* or a concrete store
apigen, util, ainit
```

`app/secondary` imports only `lib/*`, `storage`, and `storage/secondarydb/*`.

## Documentation index

- [docs/documentation.md](docs/documentation.md) — Documentation design and organisation.
- [docs/engineering/api.md](docs/engineering/api.md) — HTTP API design, code generation, and handler flow. Consult when making API changes.
- [docs/engineering/auth.md](docs/engineering/auth.md) — Authentication, passkeys, master password bootstrap, and access control.
- [docs/engineering/assets.md](docs/engineering/assets.md) — Versioned file assets, large-asset storage and backup semantics, and read-only container mounts.
- [docs/engineering/frontend.md](docs/engineering/frontend.md) — Frontend architecture, rendering, state, and styling.
- [docs/engineering/logging.md](docs/engineering/logging.md) — Logging conventions: sparse attrs (err + identity keys), slog Context variants everywhere, per-component tagged root contexts, and the tag registry. Follow when writing any backend log line.
- [docs/engineering/log-storage.md](docs/engineering/log-storage.md) — Shipped node-local log storage: WAL bucket layout and frame format, the typed shredder (JSON type is the column type, dotted keys, caps), collector and two-pass commit (day deadline, 64MB or producer exit; tally, schema, write), the level ladder and level 1 parquet schema (dense `f_<key>__<t>` leaves plus typed spill maps), the `log_files`/`log_file_keys`/commit-marker catalog, the maintenance loop (roll-up, backfill, reconciliation, 30-day retention, rewrite protocol with grace unlink), and the query engine (typed cross-variant filter semantics, per-file column binding, row-group pruning).
- [docs/engineering/engine.md](docs/engineering/engine.md) — Deployment operator, preparers (nix build, github release, container image), runners (os process, systemd, container).
- [docs/engineering/networking.md](docs/engineering/networking.md) — Current machine-local virtual networking implementation: ULA addressing, netns/veth setup, nftables host ports, netproxy DNS, and rollover route flips.
- [docs/engineering/secrets.md](docs/engineering/secrets.md) — Encrypted versioned secrets store, typed secret/config env refs, key hierarchy, and the machine-key boundary (incl. Phase 2/3 plans).
- [docs/future-work/containerized-nix-builds.md](docs/future-work/containerized-nix-builds.md) — Replacing the host Nix toolchain with a build container: current build path, build-time isolation and secret exposure, sandbox vs container comparison, design sketch, and trade-offs.
- [docs/future-work/kata-networking.md](docs/future-work/kata-networking.md) — Kata Containers + Cloud Hypervisor runtime direction and networking design tradeoffs for routed L3 workload attachments.
- [docs/future-work/networking.md](docs/future-work/networking.md) — Built-in networking layer design: IPv6 ULA addressing, fixed IPv6-in-IPv6 or IPv6-in-IPv4 node tunnels, netmap distribution, DNS discovery, embedded ingress proxy, policy, load balancing, and implementation phases.
- [docs/future-work/service-balancing-and-attachment-nat.md](docs/future-work/service-balancing-and-attachment-nat.md) — Agreed design for service virtual addresses (the reserved top ordinal 4095 of the deployment `/88`) and load balancing beyond DNS (DNS → connect hook → sender-side service DNAT → L7 ladder), the conditional-SNAT attachment boundary (cooperation as optimization; why a stateless I→O rewrite is unsound; per-attachment conntrack zones), and runtime-uniform runc/Kata treatment.
- [docs/future-work/deployment-def-split-implementation-plan.md](docs/future-work/deployment-def-split-implementation-plan.md) — Planned Deployment/DeploymentDef split: envelope from event-log columns + caller-owned def, no deleted flag (event_type is the truth), created_time/event_time denormalisation, phase-1 reserved-numbers blob compat, and the worker wire/on-disk bridge fields.
- [docs/future-work/log-compaction.md](docs/future-work/log-compaction.md) — Log compaction design: parquet file/dir layout, shared multi-writer WAL v2 format with drop-and-resume, threshold-hybrid column shredding (JSON type is the column type with one column per key×type variant, text-based int/float rule, 400-leaf budget, all variants of a key dense or all spilled, typed cross-variant query resolution), the level processing ladder (L0 unshredded, L1 shredded batch, L2 day roll-up, L3 cross-node), WAL-tail query routing, and the sqlite metadata layer.
- [docs/future-work/logmanager-implementation-plan.md](docs/future-work/logmanager-implementation-plan.md) — Log storage build-out status: all eight steps landed (typed shredder, level 1 schema, two-pass commit, `log_file_keys`, column-resolved filters, reconciliation and retention, backfill, typed ops, roll-up) with deviations noted; settled decisions (JSON type is the column type, text int/float rule, 400-leaf budget, atomic key placement, level ladder, 64MB raw batch commits, 256MB parquet roll-up with day-end sweep); open items (row group size, field sidebar from the catalog, display map from columns, record identity, backfill switch, roll-up tiers, S3 upload).
- [docs/future-work/container-metrics-implementation-plan.md](docs/future-work/container-metrics-implementation-plan.md) — Default container resource metrics measurement layer (`lib/metrics`): node-level sampler with run registry, cgroup-v2-direct reads (no task.Metrics), one cgroup per run via run-numbered container ids and containerd's default cgroup path, netns-scoped procfs network/TCP reads, fast/slow tiers, raw-counter output contract with terminal samples and aligned ticks, runner hook points, node-local storage in `lib/metrics/metricstore` (`MetricsSample` proto as WAL payload and parquet row, per node-day WAL, compaction, retention, `Scan`/`Collect`/`Latest`/`Rate` query primitives, the query-time `Rollup` engine behind `/v1/metrics/query` and `/v1/metrics/latest`, stored rollup and bucket upload as later work), and wiring status.
- [docs/future-work/cross-node-routing-implementation-plan.md](docs/future-work/cross-node-routing-implementation-plan.md) — Ordered implementation plan for route ownership, netmap distribution, runtime route reports, fixed tunnels, policy, recovery, and verification.
- [docs/future-work/network-policy-e2e-coverage.md](docs/future-work/network-policy-e2e-coverage.md) — E2E coverage of the network policy boundary: the staged probe oracle, same-node semantics, cross-node and primary-node enforcement, kernel checks (spoofing, IPv4 close, drop counters, netaudit), and subsystem interactions.
- [docs/future-work/udp-reply-source-address.md](docs/future-work/udp-reply-source-address.md) — Wildcard-bound UDP servers reply from the run-scoped outbound address `O` instead of the stable inbound address `I`, so connected clients discard the reply: current addressing behaviour, scope, the adopted workload-side fix (bind `I` explicitly), and the undecided platform-side options.
- [docs/future-work/deployments-editor-tabs-implementation-plan.md](docs/future-work/deployments-editor-tabs-implementation-plan.md) — Deployments page tabs and editor footer: what is integrated (tabbed page, layered human-triggered source validation, footer version picker, Code default and palette, flat HCL identity, stopped version retarget, create defaults), how it was verified, what is still open (backend target check, stopped retarget e2e, form width, tab overflow), and commit slicing.
- [docs/future-work/global-state-stream-implementation-plan.md](docs/future-work/global-state-stream-implementation-plan.md) — Primary → browser state stream implementation: event envelopes and snapshot/reducer retention, one global seq per commit with authored events and observed statuses in one `CoreUpdate` (observed values still merged by nanosecond clocks), independently owned sidecars, per-event visibility and targeted reset, overflow recovery, and the operator/reported node split. Every writer uses lock-owning `Commit(ctx, inlockValidate, mutate)`: the `pq.Validator` and mutate callbacks run on the transaction-bound `Queries`, own every read and write, and return a `*CoreUpdate` (`state.Update`); there are no `*Locked` variants and no `GlobalLock`. Registered `UpdateTrigger`s (the scheduler registers one) extend the update inside the same transaction; the store saves the sequence for any commit with content, commits, then publishes one `CoreUpdate` stream. `state.Service` holds no domain logic: deployments, nodes, enrollment, spaces, scheduled instances, assets, secrets, configs, authz and cluster config live in `app/primary/domain/<name>` packages as functions over the store, and handlers read through `pq.Queries` directly. The store holds no in-memory state and `pq` has no caches; the scheduled-instance view is the joined instance/config/status query, and every subscription is `state.Subscribe(store, read, project)`: `read` runs under the write lock and returns the snapshot, `project(update) (T, bool)` runs post-commit under the same lock and returns what to send (callers capture `store.Queries()` themselves), and a subscriber whose channel is full is closed and dropped so the consumer resubscribes. The scheduled-instance feed delivers one `[]ScheduledInstanceState` batch per commit. Primary SQLite transactions reserve the writer before reading; sidecars retain independent locks. Startup recovery is synchronous, ack/timer ticks reconcile persisted drain decisions, and rendering stays outside transactions. `*AtSeq` queries remain solely as a test oracle. Grant tombstones retain their subjects, so visibility resets derive from events without routing metadata. All authored and observed history is retained.
- [docs/future-work/netstate-split-implementation-plan.md](docs/future-work/netstate-split-implementation-plan.md) — Deferred netstate.pb split (NetConfig + liveness overlay) and the shipped fused-artifact patch: diff-gated writes, complete DNS catalog with authoritative empty answers, wall-clock seq floor.
- [docs/future-work/ingress-listen-implementation-plan.md](docs/future-work/ingress-listen-implementation-plan.md) — Ingress `listen` selectors (node × address algebra) replacing the primary-node `:443` reservation: host address inventory, the `lib/ingressplan` evaluator with reserved claims and collision rules, per-node `ingress_publish` in the cluster map, destination-restricted DNAT, the block-form HCL `network` section with explicit `container_port`/`host_port`, phases, and compatibility.
- [docs/product/deployments.md](docs/product/deployments.md) — Deployment config, lifecycle state, and deploy workflow.
- [docs/product/todo.md](docs/product/todo.md) — Product and engineering backlog items.

## Commands

- Backend primary server: `go run . primary` in `backend/` using an installer-initialized primary data directory.
- Frontend dev server: `pnpm install` then `pnpm run dev` in `frontend/`.
- Frontend build (embedded by Go): `go generate ./...` in `backend/`, or `pnpm run build` in `frontend/`.
- Proto codegen: `bash api-contract/proto_generate.sh` (requires `cleanproto`).

## Notes

- Installed/runtime data uses `/var/lib/opendeploy` plus sibling roots for assets, releases, volumes, containerd, and logs; tests use OS-appropriate app data directories.
- In VanJS components, return `''` for an empty node; do not return `null`.
- API services are defined one per file in `api-contract/*_service.proto` (`api_service.proto` = `ApiServer`, the public HTTP surface; `cluster_service.proto`; `enrollment_service.proto`); models are split per entity into `api-contract/model/<entity>.proto` (data model shapes) and `api-contract/model_<entity>_operations.proto` (endpoint request/response shapes; an endpoint returning a clean data model shape directly uses the `model/<entity>.proto` definition). Go and JS models are generated by `cleanproto`.
- Binary protobuf encoding is used for all request/response bodies.
- Normal login is passkey-based; master-password login (username plus the master password opens a full session, creating the user on first use) is an opt-in cluster setting (`auth.password_login_enabled`, installer `--password-login true`) for local and evaluation installs where browsers refuse WebAuthn (plain HTTP away from localhost, untrusted certificates). Fresh primary installs generate and print a high-entropy master password; without password login it only registers passkeys during bootstrap or recovery, until rotated. Self-managed Web UI TLS without a supplied bundle uses a locally generated CA (exported to `web-ca.crt` and served at `/v1/tls/ca.crt`).
- Set `OPENDEPLOY_GITHUB_TOKEN` for private repo access.
- Primary cluster mTLS material is generated by the installer and stored as internal encrypted secrets; workers cache enrolled TLS files under `/var/lib/opendeploy/tls/`.
