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
- Protobuf encoding/decoding uses `protobufjs/minimal`.

## Error handling

- UI errors are `ApiErr` with a `display_err` and `code`.
- `HandleReqErr` logs and writes a binary error body.
- The JS client surfaces the display error via `handleErr()`.

## Endpoints

### Auth
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/auth/master` | `MasterPasswordRequest` | `LoginResponse` | NO_AUTH |
| GET | `/v1/auth/current/session` | — | `LoginResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/register/start` | `EmptyRequest` | `WebAuthNOptionsResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/register/finish` | `WebAuthNFinishRequest` | `LoginResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/login/start` | `EmptyRequest` | `WebAuthNOptionsResponse` | NO_AUTH |
| POST | `/v1/auth/passkey/login/finish` | `WebAuthNFinishRequest` | `LoginResponse` | NO_AUTH |

### Config
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| PUT | `/v1/config` | `PutConfigRequest` | `UserConfigVersion` | ANY_OF default |
| GET | `/v1/config/history` | — | `UserConfigHistory` | ANY_OF default |

### Deployments
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/state/stream` | — | stream `State` | ANY_OF default |
| POST | `/v1/deployment/update` | `DeploymentUpdateRequest` | `DesiredState` | ANY_OF default |
| POST | `/v1/deployment/history` | `DeploymentHistoryRequest` | `DeploymentHistory` | ANY_OF default |
| POST | `/v1/deployment/logs` | `DeploymentLogRequest` | text stream | ANY_OF default |
| POST | `/v1/list/scopes` | `ListScopesRequest` | `ListScopesResponse` | ANY_OF default |
| POST | `/v1/list/versions` | `ListVersionsRequest` | `ListVersionsResponse` | ANY_OF default |

### Cluster
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/cluster/status` | — | `ClusterStatusResponse` | ANY_OF default |

## Adding new endpoints

1. Add the RPC and any new message types to `api-contract/api.proto`.
2. Run `bash api-contract/proto_generate.sh`.
3. Implement the handler method in `backend/handler/*.go`.
4. The JS client method is generated automatically in `frontend/src/capi/capi.js`.
