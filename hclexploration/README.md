# OpenDeploy HCL exploration

A standalone VanJS + Vite prototype for authoring OpenDeploy deployment
configuration as HCL. It does not communicate with the OpenDeploy API.

```sh
pnpm install
pnpm run dev
```

The app provides HCL syntax highlighting, schema-aware completion, structural
validation, examples, formatting, and a schema reference. The Container, Nix,
and blank scratch tabs retain independent drafts in browser local storage.

`src/mockStateStream.js` mirrors the snapshot fields emitted by
`PostV1StateStream`. Completion catalogs are derived from its spaces, nodes,
deployments, secret references, user-config references, and asset metadata.
Space-owned references are filtered using the `identity.space` value in the
document, with the mock production space as the fallback for a blank document.
References such as `secret("database.password")` resolve to immutable API IDs
only when saved; an omitted asset, secret, or config version resolves to the
latest version, while `{ version = number }` pins one explicitly. Every
symbolic resource uses a typed function with a quoted
name, such as `space("production")`, `node("worker_03")`, and
`asset("application.license")`.

Deployment name and space use an explicit singleton identity block. Node
placement is a root `deployment` attribute. Repeatable `container`
blocks directly own source, process, environment, mounts, resources, upgrade,
and version configuration, anticipating future pod-like multi-container
deployments.

Runtime storage is a list of composable expressions. `mount` combines a
`default_volume()`, `asset("name")`, or `deployment("name")` source with a
container path and optional settings.

Networking has an explicit `mode`. Its `ingress` attribute is a list of
`port_forward(...)` and `tls_passthrough(...)` expressions. A port forward's
host port defaults to its container port and can be overridden with
`{ host_port = number }`. Deployment intent
is the root-level boolean `desired_running`.

See [SCHEMA.md](SCHEMA.md) for the proposed mapping to the current protobuf API.
