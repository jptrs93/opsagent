# Single global write lock and deployment validation layers: implementation plan

Status as of 2026-09-01: **implemented**. All eleven `LockReferences` sites
were collapsed into `s.Mu` sections in one change, `referenceMu` is deleted,
and the deployment endpoints (create / update v2 / delete) run the two
validation layers described below. The store-level duplicate-identity scan in
`UpdateDeployment` moved into layer 2; the secrets manager and config service
now acquire the global lock before their own mutexes (order flipped as
planned); the asset operation lock orders ahead of the global lock, matching
uploads and reclaim. Race coverage lives in
`webuihandler/global_lock_race_test.go`. Current handler flow is documented
in engineering/api.md.

Follow-up cleanup (2026-09-01): the store write methods carry no validation at
all — `CreateDeploymentLocked`/`UpdateDeploymentLocked`/`DeleteDeploymentLocked`
just build and append the event; existence, tombstone, and the version CAS are
checked by the handlers under the lock (`VersionMismatchErr` and
`DeploymentUpdate.ExpectedVersion` are gone). The test-only lock-and-write
wrappers (`MustCreateDeploymentForNode`, `UpdateDeployment`,
`DeleteDeployment`) moved out of production into `state/statetest` (external
test packages) and `state/helpers_test.go` (state's own tests). Layer 2 was
then specialised per operation — `inLockValidateDeploymentCreate` / `Update` /
`SpaceMove` / `Delete` — replacing the single `inLockDeploymentValidate`
dispatch; rules that cannot apply to an operation (duplicate identity on a
pure update, networking claims on a pure space move) are omitted from that
operation's validator.

The original plan follows. Two changes that build on each other:

1. Collapse `config.Service.referenceMu` (`LockReferences`) into the one
   global lock `state.Service.Mu` — every global-state write takes `s.Mu`
   around its whole check-then-write sequence, regardless of entity.
2. Standardise validation into two layers, starting with the deployment
   entity, with contextual validation running directly under `s.Mu` against a
   `LiveState` view. No callback indirection: handlers lock `s.Mu`
   themselves and call `*Locked` store methods, the discipline
   `instancecache` already documents ("fields are exported so the owning
   package can compose its own operations; any access must hold Mu").

## Part 1: one global write lock

### Today's two locks

`s.Mu` (the storage-wide mutex on `instancecache.Cache`, embedded in
`state.Service`) serializes individual store operations. `referenceMu`
(`config.Service`, via `LockReferences()`) is a second, outer lock protecting
cross-entity check-then-write sequences that span multiple `s.Mu` sections —
the invariant "a deployment (or the cluster settings) only references
secrets/configs/assets from an allowed space" is checked from both directions
(deployment write validates its refs; a value's space move / delete scans for
referencing deployments), and without a shared outer lock the two
interleave. Current order: `referenceMu → s.Mu`; the secret-set combo path is
actually `referenceMu → secrets.Manager.mu → s.Mu`.

### Target

`s.Mu` is the only write lock. Every handler write path — deployments,
secrets, configs, assets, settings, nodes — takes `s.Mu` once around its
entire check+write sequence and calls `*Locked` variants inside.
`referenceMu` and `LockReferences` are deleted. Reads keep working as today
(snapshot methods that briefly take `s.Mu`).

Write rates are tiny (human-driven intent changes) and every section is
in-memory work plus local SQL — the event append already runs SQL under
`s.Mu` today. Holding it a little longer per write costs nothing that
matters; instance status ingestion shares the lock but tolerates
millisecond-scale writes.

### Conversion inventory

The eleven `LockReferences` sites, all in webuihandler:

- deployments.go: create, update v2 (become Part 2's structure).
- secrets.go: set-with-deployment-updates, space move, delete.
- user_configs.go: set-with-deployment-updates, space move, delete.
- assets.go: space move, delete.
- config.go: cluster settings update.

Each converts to `s.Mu.Lock()` around the same span. Everything called
inside needs a `*Locked` variant (Go mutexes are not reentrant — a missed
conversion self-deadlocks on first use): `FetchDeploymentSnapshot`
(→ `LiveState().Deployments`), `referencesOutsideSpace`,
`deploymentRefDetails`, `GetSecret`/`GetConfig`/`GetAsset`,
`SecretVersionIDs`/`ConfigVersionIDs`, `ListAssetVersionsJoinedOfAsset`,
`MoveAssetSpace`/`MoveConfigSpace`, `Secrets.MoveSpace`,
`setVersionedValueWithDeploymentUpdates`, and friends. Mechanical but must
be exhaustive; `go test -race` plus exercising every converted endpoint is
the safety net.

The combo writes get simpler and stronger: secret/config set with
`UpdateReferencingDeployments` currently splices a value write and deployment
spec bumps under `referenceMu` but separate `s.Mu` acquisitions; under one
lock the whole thing is a single critical section.

### Lock ordering and audit

New global order: **`s.Mu` outermost, always.** Subsystem micro-locks
(`secrets.Manager.mu`, `config.Service.mu`, pubsub internals) nest strictly
inside. Two things must be audited before landing:

- The secret-set path today takes `Manager.mu` then `s.Mu`
  (`SetWithDeploymentUpdates` holds `m.mu` across
  `AppendSecretVersionWithDeploymentUpdates`). After conversion the handler
  holds `s.Mu` first, and the manager's store call becomes a `Locked`
  variant. Audit that no remaining path anywhere takes a subsystem lock and
  then `s.Mu`.
- Authz checks currently sit inside some `LockReferences` spans (asset
  delete). If authz consults the store it would re-lock `s.Mu`; move all
  authz checks before the lock — they don't need it, and staleness is closed
  by the in-lock validation/CAS.

Notifications (`pubsubu.Notify`) are already invoked under `s.Mu` today
(`notifyDeploymentLocked`); no change.

`config.Service.AssetOperationMu` (the asset store's operation locker, taken
during settings updates) is out of scope; note it as a candidate for a later
fold.

### Transition constraint

The reference-locality invariant is only protected while *both* sides hold
the same lock. A half-converted state (deployments on `s.Mu`, secret moves
still on `referenceMu` alone) silently reopens the race. The collapse of all
eleven sites plus the deletion of `referenceMu` lands as one change.

## Part 2: two validation layers for deployments

**Layer 1 — `preLockDeploymentValidate(updated)`.** Globally independent
rules about the value itself: mandatory fields, ranges, shape restrictions
not expressible in the Go type, exactly-one-of restrictions. Pure function,
runs before the lock.

**Layer 2 — `inLockDeploymentValidate(existing, updated, live)`.**
Contextual rules — anything referencing other deployments or entities.
`existing` is nil on create. Be liberal: if a rule needs context, it lives
here. Runs under `s.Mu`, called directly by the handler:

```go
preLockDeploymentValidate(updated)            // pure, no lock
// authz, verifyRunningNixSource (network I/O) — before the lock
s := h.Store
s.Mu.Lock()
defer s.Mu.Unlock()
existing := s.LiveState().Deployments[id]
// re-check CAS fast-fail against existing.Version
if err := inLockDeploymentValidate(existing, updated, s.LiveState()); err != nil { return err }
return s.UpdateDeploymentLocked(ctx, id, update) // CAS + event append
```

### LiveState

Built on the fly by a method that assumes `s.Mu` is held — no stored field,
no cache maintenance, no renames:

```go
type LiveState struct {
    Scheduled   map[int32]*apigen.ScheduledInstanceState
    Deployments map[int32]*apigen.Deployment
    Nodes       map[int32]*Node
}

func (s *Service) LiveState() LiveState // caller must hold s.Mu
```

`Scheduled` and `Deployments` alias the existing cache maps (map headers —
copying the struct is free). `Nodes` is assembled from the existing
`listNodesLocked()` SQL read; validation runs once per write, so the query
cost is irrelevant, and no node cache needs building or maintaining. Hold
all lifecycle statuses in the map and let rules check `Status`. Everything
is read-only by convention — the same convention `ListActiveDeployments`
already hands pointers out under.

### The invariant framing

The store builds the full next state before writing, so layer-2 rules are
invariants over `(existing, updated, live)` independent of which v2 update
kind produced `updated`. This deletes the per-kind duplication in
`PostV2DeploymentsUpdate` and turns per-kind guards into transition rules:

- `IsInternalConfig(existing)` ⇒ `updated` differs from `existing` only in
  workload state (version/running) — the self-upgrade path stays open.
- `IsSelfConfig(existing)` ⇒ `updated.WorkloadRunning()`.
- Space may not change to or from 0.
- Address-referenced deployments keep virtual networking and their space.

### Rule assignment

Layer 1 (pre-lock, pure): name/node/space presence and ranges, internal
identity refusal (create), exactly-one-kind, spec shape (the
`validateDeploymentSpec` chain minus resolver lookups), nix version format,
mount path shapes. Normalisation (clone, `validateContainerCommand`) stays
with building `updated`.

Layer 2 (in-lock, contextual): duplicate identity; node registered and
node-allows-space (from `live.Nodes`); `validateNodeNetworkingClaims`;
`validateAddressEnvRefs` and address-referenced guards;
`validateCrossDeploymentMountSources`; `referencesOutsideSpace`; reference
existence and space locality for secrets/configs/assets (safe under the
lock: `Secrets.MetaByID` uses the manager's own inner lock,
`GetConfigVersionByID`/`GetAssetVersionRef` are direct SQL); internal/self
transition rules; delete guards (instance statuses come from
`live.Scheduled` — same mutex).

Outside both layers: authz (needs ctx; before the lock),
`verifyRunningNixSource` (network I/O; before the lock, races closed by the
in-lock CAS), derived spec rules (stale-version clear, `SetWorkloadState` —
derivation, not validation), `wakeAcme` (after unlock).

## Steps

1. Add `LiveState` + `s.LiveState()`; split the store write paths used by
   handlers into exported `*Locked` variants (deployments first:
   `UpdateDeploymentLocked`, `CreateDeploymentLocked`,
   `DeleteDeploymentLocked`; the in-store duplicate scan moves out to
   layer 2, the create-duplicate panic becomes a validation error).
2. Convert all eleven `LockReferences` sites to `s.Mu` sections in one
   change; delete `referenceMu`/`LockReferences`; add the needed `*Locked`
   read variants; move authz checks ahead of the lock; run the lock-order
   audit (no subsystem-lock → `s.Mu` acquisitions remain).
3. Refactor contextual validators to read from `LiveState` parameters
   instead of fetching via the store; split `validateDeploymentSpec` into
   the pure shape chain and the resolver half.
4. Build the two layers; convert deployment create, then update v2 (the
   switch shrinks to authz + building the update), then delete.
5. Tests: existing handler tests pass unchanged (behaviour-preserving,
   except create-duplicate panic → error). Add race coverage: concurrent
   creates claiming the same host port/identity; address ref added
   concurrently with the target's virtual-mode exit; secret space move
   racing a deployment write that pins it. Run the suite under `-race`.
6. Docs: `engineering/api.md` handler flow; note this pattern as the
   template for other entities.
