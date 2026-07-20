# API design

## Overview

The API is HTTP + binary protobuf v3. The contract is defined in `api-contract/api.proto` — message types, RPC definitions, and per-route access policies. Go and JS code is generated from the proto schema using [cleanproto](https://github.com/jptrs93/cleanproto/blob/main/README.md).

## Code generation

Regenerate after changing the proto schema:
```sh
bash api-contract/proto_generate.sh
```

Key generated files:
- `backend/apigen/model.gen.go` — Go message structs with `Encode`/`Decode` methods.
- `backend/apigen/mux.gen.go` — HTTP mux route registration and request/response wiring.
- `frontend/src/capi/model.js` — JS typedefs and protobuf encode/decode functions.
- `frontend/src/capi/capi.js` — Typed JS API client class.

## Mux and handler flow (Go)

- Routes use `http.NewServeMux()` with Go 1.22+ pattern syntax (e.g. `"POST /v1/auth/master"`).
- Each route decodes the request body, calls its generated service handler, and writes a binary response.
- Primary handlers are split by security surface: `webuihandler.Handler` implements `OpsagentHttpV1`, `clusterhandler.Handler` implements `OpsagentClusterV1`, and `enrollmenthandler.Handler` implements `EnrollmentV1`.
- Web UI auth is enforced by `webuihandler.Handler.VerifyAuth`; cluster peer identity comes from mTLS, while enrollment uses its dedicated request verifier.
- Static SPA assets are served from embedded `backend/web/dist`; unknown paths fall back to `index.html`.
- The frontend is built via `//go:generate` in `backend/main.go` before embedding.

## Client flow (JavaScript)

- `frontend/src/capi/capi.js` is the typed API wrapper.
- `frontend/src/capi/err.js` decodes `ApiErr` responses and throws JS errors.
- Protobuf encoding/decoding uses generated local runtimes, with no frontend protobuf runtime package required.

## Error handling

- UI errors are `ApiErr` with a `display_err` and `code`.
- `HandleReqErr` logs and writes a binary error body.
- The JS client surfaces the display error via `handleErr()`.

## Endpoints

### Health
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/healthz` | — | — | NO_AUTH |

### Auth
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/auth/master` | `MasterPasswordRequest` | `LoginResponse` | NO_AUTH |
| POST | `/v1/auth/master/password/save` | `MasterPasswordSaveRequest` | — | ANY_OF default |
| POST | `/v1/auth/master/password/verify` | `MasterPasswordVerifyRequest` | — | ANY_OF default |
| GET | `/v1/auth/current/session` | — | `LoginResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/register/start` | `EmptyRequest` | `WebAuthNOptionsResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/register/finish` | `WebAuthNFinishRequest` | `LoginResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/login/start` | `EmptyRequest` | `WebAuthNOptionsResponse` | NO_AUTH |
| POST | `/v1/auth/passkey/login/finish` | `WebAuthNFinishRequest` | `LoginResponse` | NO_AUTH |

### Deployments
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/state/stream` | — | stream `State` | ANY_OF default |
| POST | `/v1/deployment/create` | `DeploymentCreateRequest` | `DeploymentConfig` | ANY_OF default |
| POST | `/v1/deployment/update` | `DeploymentUpdateRequest` | `DesiredState` | ANY_OF default |
| POST | `/v1/deployment/history` | `DeploymentHistoryRequest` | `DeploymentHistory` | ANY_OF default |
| POST | `/v1/deployment/log-search` | `LogSearchRequest` | stream `LogLine` | ANY_OF default |
| POST | `/v1/deployment/prepare-output` | `PrepareOutputRequest` | stream `PrepareOutputChunk` | ANY_OF default |
| POST | `/v1/deployment/versions` | `DeploymentVersionsRequest` | `DeploymentVersions` | ANY_OF default |

`/v1/state/stream` is the UI's live state source. Its initial `State` message includes deployment, user, connected-machine, enrollment, secrets status, secret/config reference, secret/config value, space, asset metadata, and a full primary configuration snapshot. Configuration snapshots include the persisted configuration row ID and update time, but redact `master_password_hash`; each later configuration write emits another full snapshot. Other resources use matching incremental updates plus heartbeats.

`/v1/deployment/log-search` streams historical run logs as typed `LogLine` protobuf frames. It scans existing `.logbin` files for the requested deployment and time range; it does not tail the currently active log file.

`/v1/deployment/prepare-output` streams raw prepare/build output chunks for a deployment config version. A request with `version=0` resolves to the latest known prepare status and tails while preparation is still active.

### Cluster
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/cluster/status` | — | `ClusterStatusResponse` | ANY_OF default |
| POST | `/v1/cluster/rename` | `NodeRenameRequest` | `ClusterNode` | ANY_OF default |
| GET | `/v1/cluster/github-credentials` | — | `GithubCredentials` | NO_AUTH over mTLS cluster listener |
| GET | `/v1/cluster/asset?asset_id=<id>` | query params | raw asset bytes with `X-Opsagent-Asset-*` headers | NO_AUTH over mTLS cluster listener |
| GET | `/v1/cluster/secrets` | `ClusterSecretsRequest` | `ClusterSecretsResponse` | NO_AUTH over mTLS cluster listener |
| GET | `/v1/cluster/configs` | `ClusterConfigsRequest` | `ClusterConfigsResponse` | NO_AUTH over mTLS cluster listener |

Cluster secrets/configs requests carry immutable row IDs. The primary authorizes those IDs against the deployment refs allowed for the requesting worker, decrypts/fetches only those rows, and the worker keeps the plaintext values in memory.

### Settings, Secrets, Configs, Assets, Spaces
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/settings` | — | `Settings` | ANY_OF default |
| PUT | `/v1/settings` | `Settings` | `Settings` | ANY_OF default |
| POST | `/v1/secrets/list` | `EmptyRequest` | `SecretList` | ANY_OF default |
| POST | `/v1/secrets/set` | `SecretSetRequest` | `SecretMeta` | ANY_OF default |
| POST | `/v1/secrets/rename` | `SecretRenameRequest` | `SecretMeta` | ANY_OF default |
| POST | `/v1/secrets/reveal` | `SecretRevealRequest` | `SecretRevealResponse` | ANY_OF default |
| POST | `/v1/secrets/delete` | `SecretDeleteRequest` | — | ANY_OF default |
| POST | `/v1/secrets/status` | `EmptyRequest` | `SecretsStatusResponse` | ANY_OF default |
| POST | `/v1/secrets/generate-recovery-code` | `EmptyRequest` | `SecretRecoveryCodeResponse` | ANY_OF default |
| POST | `/v1/secrets/unlock` | `SecretUnlockRequest` | `SecretsStatusResponse` | ANY_OF default |
| POST | `/v1/user/configs/list` | `EmptyRequest` | `UserConfigList` | ANY_OF default |
| POST | `/v1/user/configs/set` | `UserConfigSetRequest` | `UserConfig` | ANY_OF default |
| POST | `/v1/user/configs/rename` | `UserConfigRenameRequest` | `UserConfig` | ANY_OF default |
| POST | `/v1/user/configs/delete` | `UserConfigDeleteRequest` | — | ANY_OF default |
| POST | `/v1/assets/list` | `EmptyRequest` | `AssetList` | ANY_OF default |
| POST | `/v1/assets/get` | `AssetGetRequest` | `Asset` | ANY_OF default |
| POST | `/v1/assets/set` | `AssetSetRequest` | `Asset` | ANY_OF default |
| POST | `/v1/assets/upload` | raw file body, `name`/`format`/`space_id` query params | `Asset` | ANY_OF default |
| POST | `/v1/assets/rename` | `AssetRenameRequest` | `Asset` | ANY_OF default |
| POST | `/v1/assets/delete` | `AssetDeleteRequest` | — | ANY_OF default |
| POST | `/v1/spaces/create` | `SpaceSetRequest` | `Space` | ANY_OF default |
| POST | `/v1/spaces/update` | `SpaceSetRequest` | `Space` | ANY_OF default |
| POST | `/v1/spaces/delete` | `SpaceDeleteRequest` | — | ANY_OF default |

User-managed configs and encrypted secrets are immutable versioned rows. Saving an existing name appends version `vN` with a new numeric row ID; settings refs and deployment env refs pin exact rows with `ConfigRef.id`, `SecretRef.id`, `EnvVarValue.configId`, and `EnvVarValue.secretId`. Rename changes the display name for all versions of a secret/config group without creating a new version. Delete hard-deletes the whole group and is rejected while any settings or deployment config still references one of its row IDs.

`POST /v1/secrets/reveal` is the only user-facing API that returns decrypted secret plaintext. It accepts `SecretRevealRequest.id` for exact-version reveal; list/state APIs return metadata only.

Assets are versioned plaintext file blobs recorded in `assets`. Setting an existing key creates a new version with a numeric asset id. Rename changes the key for every version without creating a version or changing IDs and rejects an existing destination key. List and state stream APIs expose only `AssetMeta`, not blob content. Blobs up to and including 10 MiB are stored inline. Larger blobs use local primary storage while Backup is disabled and S3 while Backup is enabled; changing Backup starts an asynchronous placement transition. Storage placement is transparent to these asset endpoints. Workers stream required asset blobs on demand over the mTLS cluster asset endpoint during preparation. See [Assets](assets.md) for storage modes, transition status, retention, restore, and compatibility.

### Enrollment
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/enrollment/info` | — | `EnrollmentInfo` | ANY_OF default |
| POST | `/v1/enrollment/request` | stream `EnrollmentWorkerMsg` | stream `EnrollmentPrimaryMsg` | NO_AUTH |
| POST | `/v1/enrollment/list` | — | `EnrollmentRequestList` | ANY_OF default |
| POST | `/v1/enrollment/accept` | `EnrollmentAcceptRequest` | `EnrollmentRequestStatus` | ANY_OF default |

Workers use `EnrollmentV1` only when local cluster CA/cert/key material is missing. The enrollment listener is HTTPS using the primary server certificate. Because workers do not yet have a trust root, secondary installs pin the enrollment listener's `sha256:` SPKI fingerprint from authenticated `GET /v1/enrollment/info`; the worker verifies the presented TLS certificate matches that fingerprint before sending its CSR. In production, the public enrollment listener also applies the same generated-mux middleware approach as the web UI: per-client-IP request admission is limited to a burst of 5 and a refill rate of 0.2 requests/second. Workers generate their private key locally, send a stable generated `requesting_machine_id` plus a PEM CSR, then keep the stream open until an operator accepts the request. The CSR CN, worker certificate CN, and `ClusterNode.identifier` are that stable identifier. Deprecated deployment `identity.machine` metadata is transported and persisted only for compatibility with the current SQLite index; placement, authorization, lookup, and duplicate detection use `node_id`. The operator-selected worker name is mutable display metadata only. Acceptance signs the CSR with the primary's internally stored cluster CA key and returns only the CA certificate and worker certificate; the private key never leaves the worker. By default the worker writes them to `/var/lib/opendeploy/tls/ca.crt`, `/var/lib/opendeploy/tls/node.crt`, and `/var/lib/opendeploy/tls/node.key`, then reconnects to `OpsagentClusterV1` over mTLS. The cert files are written `0644`; the private key is written `0600`.

## Adding new endpoints

1. Add the RPC and any new message types to `api-contract/api.proto`.
2. Run `bash api-contract/proto_generate.sh`.
3. Implement the handler method in the matching package under `backend/app/primary`: `webuihandler`, `clusterhandler`, or `enrollmenthandler`.
4. The JS client method is generated automatically in `frontend/src/capi/capi.js`.
