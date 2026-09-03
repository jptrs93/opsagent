# CLAUDE

A deployment management tool with a Go backend and VanJS frontend.

## Project structure

```
api-contract/        Protobuf API schema and code generation.
backend/             Go HTTP server, handlers, auth, engine, and storage.
frontend/            VanJS + Vite SPA.
docs/                Project documentation.
```

## Documentation index

- [docs/documentation.md](docs/documentation.md) — Documentation design and organisation.
- [docs/engineering/api.md](docs/engineering/api.md) — HTTP API design, code generation, and handler flow. Consult when making API changes.
- [docs/engineering/auth.md](docs/engineering/auth.md) — Authentication, passkeys, master password bootstrap, and access control.
- [docs/engineering/assets.md](docs/engineering/assets.md) — Versioned file assets, large-asset storage and backup semantics, and read-only container mounts.
- [docs/engineering/frontend.md](docs/engineering/frontend.md) — Frontend architecture, rendering, state, and styling.
- [docs/engineering/logging.md](docs/engineering/logging.md) — Logging conventions: sparse attrs (err + identity keys), slog Context variants everywhere, per-component tagged root contexts, and the tag registry. Follow when writing any backend log line.
- [docs/engineering/engine.md](docs/engineering/engine.md) — Deployment operator, preparers (nix build, github release, container image), runners (os process, systemd, container).
- [docs/engineering/networking.md](docs/engineering/networking.md) — Current machine-local virtual networking implementation: ULA addressing, netns/veth setup, nftables host ports, netproxy DNS, and rollover route flips.
- [docs/engineering/secrets.md](docs/engineering/secrets.md) — Encrypted versioned secrets store, typed secret/config env refs, key hierarchy, and the machine-key boundary (incl. Phase 2/3 plans).
- [docs/future-work/containerized-nix-builds.md](docs/future-work/containerized-nix-builds.md) — Replacing the host Nix toolchain with a build container: current build path, build-time isolation and secret exposure, sandbox vs container comparison, design sketch, and trade-offs.
- [docs/future-work/kata-networking.md](docs/future-work/kata-networking.md) — Kata Containers + Cloud Hypervisor runtime direction and networking design tradeoffs for routed L3 workload attachments.
- [docs/future-work/networking.md](docs/future-work/networking.md) — Built-in networking layer design: IPv6 ULA addressing, fixed IPv6-in-IPv6 or IPv6-in-IPv4 node tunnels, netmap distribution, DNS discovery, embedded ingress proxy, policy, load balancing, and implementation phases.
- [docs/future-work/service-balancing-and-attachment-nat.md](docs/future-work/service-balancing-and-attachment-nat.md) — Agreed design for service virtual addresses and load balancing beyond DNS (DNS → connect hook → sender-side service DNAT → L7 ladder), the conditional-SNAT attachment boundary (cooperation as optimization; why a stateless I→O rewrite is unsound; per-attachment conntrack zones), and runtime-uniform runc/Kata treatment.
- [docs/future-work/deployment-def-split-implementation-plan.md](docs/future-work/deployment-def-split-implementation-plan.md) — Planned Deployment/DeploymentDef split: envelope from event-log columns + caller-owned def, no deleted flag (event_type is the truth), created_time/event_time denormalisation, phase-1 reserved-numbers blob compat, and the worker wire/on-disk bridge fields.
- [docs/future-work/log-compaction.md](docs/future-work/log-compaction.md) — Log compaction design: parquet file/dir layout, shared multi-writer WAL v2 format with drop-and-resume, threshold-hybrid column shredding, WAL-tail query routing, node-level and cross-node compaction passes, and the sqlite metadata layer.
- [docs/future-work/logmanager-implementation-plan.md](docs/future-work/logmanager-implementation-plan.md) — Node-local log storage backend build-out: package layout, sqlite catalog schema, WAL byte-range consumption, commit ordering and crash windows, day roll-up, query planning across parquet and the WAL tail, retention, and milestones.
- [docs/future-work/container-metrics-implementation-plan.md](docs/future-work/container-metrics-implementation-plan.md) — Default container resource metrics measurement layer (`lib/metrics`): node-level sampler with run registry, cgroup-v2-direct reads (no task.Metrics), one cgroup per run via run-numbered container ids and containerd's default cgroup path, netns-scoped procfs network/TCP reads, fast/slow tiers, raw-counter output contract with terminal samples and aligned ticks, runner hook points, node-local storage in `lib/metrics/metricstore` (`MetricsSample` proto as WAL payload and parquet row, per node-day WAL, compaction, retention, `Scan`/`Collect`/`Latest`/`Rate` query primitives, the query-time `Rollup` engine behind `/v1/metrics/query` and `/v1/metrics/latest`, stored rollup and bucket upload as later work), and wiring status.
- [docs/future-work/cross-node-routing-implementation-plan.md](docs/future-work/cross-node-routing-implementation-plan.md) — Ordered implementation plan for route ownership, netmap distribution, runtime route reports, fixed tunnels, policy, recovery, and verification.
- [docs/future-work/network-policy-e2e-coverage.md](docs/future-work/network-policy-e2e-coverage.md) — E2E coverage of the network policy boundary: the staged probe oracle, same-node semantics, cross-node and primary-node enforcement, kernel checks (spoofing, IPv4 close, drop counters, netaudit), and subsystem interactions.
- [docs/future-work/udp-reply-source-address.md](docs/future-work/udp-reply-source-address.md) — Wildcard-bound UDP servers reply from the run-scoped outbound address `O` instead of the stable inbound address `I`, so connected clients discard the reply: current addressing behaviour, scope, the adopted workload-side fix (bind `I` explicitly), and the undecided platform-side options.
- [docs/future-work/netstate-split-implementation-plan.md](docs/future-work/netstate-split-implementation-plan.md) — Deferred netstate.pb split (NetConfig + liveness overlay) and the shipped fused-artifact patch: diff-gated writes, complete DNS catalog with authoritative empty answers, wall-clock seq floor.
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
- Normal login is passkey-only. Fresh primary installs generate and print a high-entropy setup password; it can register passkeys during bootstrap or recovery until rotated.
- Set `OPENDEPLOY_GITHUB_TOKEN` for private repo access.
- Primary cluster mTLS material is generated by the installer and stored as internal encrypted secrets; workers cache enrolled TLS files under `/var/lib/opendeploy/tls/`.
