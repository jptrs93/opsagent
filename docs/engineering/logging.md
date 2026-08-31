# Logging

How backend code logs, and the conventions every new log line must follow.
The transport is `log/slog` with the JSON handler from
`github.com/jptrs93/goutil/logu` installed by `ainit`; contextual metadata
rides on `context.Context` via `logu.AddTag` / `logu.AddKV` and is rendered
onto every line automatically by the handler.

## The three rules

### 1. Attrs are for consistent repeated keys, not one-offs

Vararg attrs (`"key", value` pairs) are reserved for keys that repeat
consistently across many call sites and are worth filtering on:

- `err` — always its own attr, always last. Never fold the error into the
  message string.
- Identity keys, preferably attached once to the context rather than repeated
  per call: `dep` (deployment ID), `scheduled_instance`, `name` (deployment
  label), `container`, `user`, `node`.

Everything else — statuses, versions, ports, paths, hostnames, counts,
durations — goes into the message itself with `fmt.Sprintf`. Natural phrasing
where it reads well, `key=value` for status dumps:

```go
// one-off detail in the message, err as attr
slog.ErrorContext(ctx, fmt.Sprintf("creating prepare log file %s failed", logPath), "err", err)

// status-dump style
slog.InfoContext(ctx, fmt.Sprintf("Run: starting prepare configSeqNo=%d preparerSeqNo=%d",
    config.Version, currentPreparer.Version()))
```

Not this:

```go
slog.Error("image pull failed", "ref", ref, "log_path", logPath, "err", err) // one-off attrs
```

### 2. Always the `Context` variant

Every log call is `slog.InfoContext` / `WarnContext` / `ErrorContext` /
`DebugContext` with a real context. If the enclosing function has no `ctx`,
thread one through from the caller rather than reaching for
`context.Background()`; a bare Background context is acceptable only at
process bootstrap before any component root exists.

### 3. Every component has a tagged root context

Each feature component or service with a clear boundary creates its root
context once — in its `Run`/`Start` function or constructor — with
`logu.AddTag(ctx, "ComponentName")`, and passes that context down everywhere.
All logs from that component then carry the tag, so one filter surfaces the
whole component. Long-lived identity is attached the same way with
`logu.AddKV` so it never has to be repeated per call:

```go
func operatorCtx(instanceID int32, cfg *apigen.DeploymentConfig) context.Context {
    ctx := logu.AddTag(context.Background(), "DeploymentOperator")
    ctx = logu.AddKV(ctx, "scheduled_instance", instanceID)
    ctx = logu.AddKV(ctx, "dep", cfg.ID)
    return logu.AddKV(ctx, "name", configName(cfg))
}
```

`AddTag`/`AddKV` are copy-on-write: deriving a child context never mutates the
parent's tags, so components can layer (a preparer run carries both its
cancellation ctx and the `Preparer` tag).

## Tag registry

One tag per component boundary; PascalCase. Current tags:

| Tag | Root context created in |
| --- | --- |
| `DeploymentOperator` | `lib/engine/operator.go` (`operatorCtx`) |
| `Preparer` | `lib/engine/operator.go` (`preparerCtx`), `lib/engine/prepare` (`WriteStatus`) |
| `Runner` | `lib/engine/runner/runner.go` (`deploymentLogContext`) |
| `AssetStore` | `lib/engine/assetstore/reconcile.go` (`StartReconciler`) |
| `NetProxy` | `app/netproxy/startup.go` (`Run`) — process-wide root |
| `DNS` | `app/netproxy/dns.go` (`RunDNS`) |
| `Ingress` | `app/netproxy/ingress.go` (`RunTLSIngress`) |
| `Certs` | `app/netproxy/certs.go` (`certStore.Run`) |
| `NetstateWatch` | `app/netproxy/netstatewatch/watcher.go` |
| `NetStateWriter` | `app/netproxy/netstate.go` (`RunNetStateWriter`, agent side) |
| `Worker` | `app/secondary` (worker agent root) |
| `ClusterSession` | `app/secondary/cluster_session.go` (worker→primary session) |
| `Retention` | `app/secondary/retention.go` |
| `LogShipper` | `app/secondary/logs.go` |
| `Enrollment` | `app/secondary/enrollment.go`, `app/primary/enrollmenthandler` |
| `CertRenewal` | `app/secondary/certrenewal.go` |
| `Primary` | `app/primary` (primary process root) |
| `Scheduler` | `app/primary/scheduler` |
| `Backup` | `app/primary/backup` |
| `ClusterServer` | `app/primary/clusterserver`, `app/primary/clusterhandler` |
| `NetmapPublisher` | `app/primary/netmappublisher` |
| `NetmapApplier` | `app/primary/netmapapply.go` (primary's in-process map applier) |
| `WebUI` | `app/primary/webui` |
| `Secrets` | `lib/secrets` |
| `Network` | `lib/network` |
| `NetAudit` | `lib/netaudit` |
| `AcmeIssue` | `lib/acmeissue` |
| `LogCollector` | `lib/log/logmanager` |
| `LogMigrate` | `lib/log/logmigrate` (`Run`) — one-time format migration, removed together with legacy-format compat |
| `LocalInputs` | `lib/localinputs` (`Open`, tags the worker run ctx) |
| `Store` | storage layer fallbacks where no caller ctx exists |

When adding a component, pick a new tag, create the root context in one place,
and add a row here.

## Practicalities

- HTTP handlers already receive a per-request context (`apigen.Context.Ctx`);
  use it — never build a fresh Background context inside a request handler.
  The generated mux attaches the request path/user KVs.
- `apigen/mux_util.gen.go` and other `*.gen.go` files are codegen output —
  never hand-edit their log calls.
- Never log secret values, tokens, or PEM material; redact command lines that
  may embed credentials (see `nixdocker.sanitizeCommandForLogs`).
- Log levels: 4xx-class client feedback and routine progress are Info; degraded
  but self-healing conditions are Warn; faults needing attention are Error;
  per-connection/per-query noise is Debug, rate-limited where high-volume
  (`newOperationalWarningLimiter` in netproxy).
- Tests that assert on log output should match the new message text, not attr
  keys that have been folded away.
