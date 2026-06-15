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
- Each route decodes the request body, calls the corresponding `handler.Handler` method, and writes a binary response.
- Auth is enforced by `handler.VerifyAuth` before handlers run.
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

`/v1/state/stream` is the UI's live state source. Its initial `State` message includes deployment, user, connected-machine, and enrollment snapshots; subsequent messages carry deployment, user, machine, and enrollment updates plus heartbeats.

`/v1/deployment/log-search` streams historical run logs as typed `LogLine` protobuf frames. It scans existing `.logbin` files for the requested deployment and time range; it does not tail the currently active log file.

`/v1/deployment/prepare-output` streams raw prepare/build output chunks for a deployment config version. A request with `version=0` resolves to the latest known prepare status and tails while preparation is still active.

### Cluster
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/cluster/status` | — | `ClusterStatusResponse` | ANY_OF default |
| GET | `/v1/cluster/github-credentials` | — | `GithubCredentials` | NO_AUTH over mTLS cluster listener |
| GET | `/v1/cluster/asset` | `ClusterAssetRequest` | `ClusterAssetBlob` | NO_AUTH over mTLS cluster listener |
| GET | `/v1/cluster/secrets` | `ClusterSecretsRequest` | `ClusterSecretsResponse` | NO_AUTH over mTLS cluster listener |
| GET | `/v1/cluster/configs` | `ClusterConfigsRequest` | `ClusterConfigsResponse` | NO_AUTH over mTLS cluster listener |

### Config
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/config` | — | `DynamicConfiguration` | ANY_OF default |
| POST | `/v1/config/update` | `ConfigUpdateRequest` | `DynamicConfiguration` | ANY_OF default |
| POST | `/v1/secret/value/reveal` | `SecretValue` | `SecretRevealResponse` | ANY_OF default |
| POST | `/v1/user/configs/list` | `EmptyRequest` | `UserConfigList` | ANY_OF default |
| POST | `/v1/user/configs/set` | `UserConfigSetRequest` | `UserConfig` | ANY_OF default |
| POST | `/v1/user/configs/delete` | `UserConfigDeleteRequest` | — | ANY_OF default |
| POST | `/v1/assets/list` | `EmptyRequest` | `AssetList` | ANY_OF default |
| POST | `/v1/assets/get` | `AssetGetRequest` | `Asset` | ANY_OF default |
| POST | `/v1/assets/set` | `AssetSetRequest` | `Asset` | ANY_OF default |
| POST | `/v1/assets/delete` | `AssetDeleteRequest` | — | ANY_OF default |

User-managed configs are plaintext values stored in `user_configs` and referenced from deployment env as `${c:name}`. Encrypted secrets are referenced as `${s:name}`. Deployment preparation batches referenced secret and config keys; secondaries fetch them over the mTLS cluster secrets/configs endpoints into memory only.

Assets are versioned plaintext file blobs stored in `assets`. Rows are immutable; setting an existing key creates a new version with a numeric asset id. The first implementation stores blobs inline up to 10 MiB and reserves `location` for future filesystem/S3-backed assets. Workers download required asset blobs on demand over the mTLS cluster asset endpoint during preparation.

### Enrollment
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/enrollment/request` | stream `EnrollmentWorkerMsg` | stream `EnrollmentPrimaryMsg` | NO_AUTH |
| POST | `/v1/enrollment/list` | — | `EnrollmentRequestList` | ANY_OF default |
| POST | `/v1/enrollment/accept` | `EnrollmentAcceptRequest` | `EnrollmentRequestStatus` | ANY_OF default |

Workers use `EnrollmentV1` only when local cluster CA/cert/key material is missing. The enrollment listener is HTTPS using the primary server certificate, but workers skip server verification during bootstrap because they do not yet have a trust root. Workers generate their private key locally, send a stable generated `requesting_machine_id` plus a PEM CSR, then keep the stream open until an operator accepts the request. Acceptance signs the CSR with the primary's internally stored cluster CA key and returns only the CA certificate and worker certificate; the private key never leaves the worker. By default the worker writes them to `/var/lib/opendeploy/tls/ca.crt`, `/var/lib/opendeploy/tls/node.crt`, and `/var/lib/opendeploy/tls/node.key`, then reconnects to `OpsagentClusterV1` over mTLS. The cert files are written `0644`; the private key is written `0600`.

## Adding new endpoints

1. Add the RPC and any new message types to `api-contract/api.proto`.
2. Run `bash api-contract/proto_generate.sh`.
3. Implement the handler method in `backend/handler/*.go`.
4. The JS client method is generated automatically in `frontend/src/capi/capi.js`.
