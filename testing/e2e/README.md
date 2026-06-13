# OpenDeploy E2E Flows

Run browser flows from a separate Playwright container against the local
install-test deployment.

```sh
bash testing/e2e/run.sh
```

By default the runner resets the local primary/secondary containers with
`testing/run.sh`, builds the Playwright image, and runs:

```sh
FLOWS=bootstrap-enroll-nixdocker
```

Select one or more flow files with a comma-separated list:

```sh
FLOWS=bootstrap-enroll-nixdocker RESET=false bash testing/e2e/run.sh
```

To test local backend/frontend changes without publishing a release first, pass
`USE_SELF=true`. The install harness builds the local checkout, copies it into
the test containers, and installs it as `v0.0.0` with `opendeploy install
--use-self`:

```sh
USE_SELF=true bash testing/e2e/run.sh
```

If `OPENDEPLOY_GITHUB_TOKEN` is set in the host environment, the install harness
adds it to both test containers' `/etc/opendeploy/env` files. This keeps source
validation and Nix/GitHub preparers on authenticated GitHub API limits.

Flows run in the primary container's network namespace by default and target
`http://localhost:8080`. This keeps WebAuthn on the same origin the HTTP-only
server allows for passkeys.

Override the Docker network mode or base URL if needed:

```sh
NETWORK_MODE=opendeploy-install-test OPD_BASE_URL=http://opendeploy-install-primary:8080 bash testing/e2e/run.sh
```
