# Library app

A deliberately small, unprotected combined backend/frontend application for
OpenDeploy examples. It mirrors the main repository's layout and build flow:

- `api-contract/` defines a binary protobuf HTTP API and generates Go and JS.
- `backend/` connects to PostgreSQL, initializes the schema, serves the API,
  and embeds the compiled frontend.
- `frontend/` is a one-page VanJS/Vite interface.
- `flake.nix` builds the frontend, embeds it in the Go executable, and exposes
  a streamed container image as its default package for OpenDeploy.

The app creates `authors`, `genres`, and `books` tables. The page lists every
record and provides forms for adding each type. **Add a random shelf** inserts
a linked author, genre, and book in one PostgreSQL transaction.

## Configuration

The backend uses the standard PostgreSQL environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `PGHOST` | `127.0.0.1` | PostgreSQL hostname or OpenDeploy network address |
| `PGPORT` | `5432` | PostgreSQL port |
| `PGDATABASE` | `postgres` | Database name |
| `PGUSER` | `postgres` | Database user |
| `PGPASSWORD` | empty | Database password |
| `PGSSLMODE` | `prefer` | PostgreSQL TLS mode |
| `HTTP_ADDR` | `:8080` | HTTP listen address |

For OpenDeploy, configure those values as deployment environment variables,
using a secret reference for `PGPASSWORD`. Give the app network access to the
PostgreSQL deployment and expose container port `8080` through the desired
ingress or port-forward setting. The app retries its PostgreSQL connection until
the database is available.

## Development

Regenerate both API clients after editing the contract:

```sh
sh api-contract/proto_generate.sh
```

Start the backend (after PostgreSQL is available):

```sh
cd backend
go generate ./...
go run .
```

For frontend development with API requests proxied to port 8080:

```sh
cd frontend
pnpm install
pnpm run dev
```

Build the same default streamed container image consumed by OpenDeploy:

```sh
nix build
```
