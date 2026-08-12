# OpenDeploy Playwright Flows

This directory contains the Playwright specs and helpers used by the E2E harness.
The harness runs them in Docker against the Lima primary/secondary VM cluster.
For mock remote mode, start the persistent repo mirror first:

```sh
go run ./testing-vms/test-orchestrator repo-mirror-up
```

Then run the main harness:

```sh
bash testing-vms/run.sh
```

The harness copies this suite inside the Playwright Docker container, points it at
the HTTPS test origin, and writes `test-results` / `playwright-report` on the host.

Select one or more flow files with a comma-separated list:

```sh
FLOWS=bootstrap-enroll-nixdocker bash testing-vms/run.sh
```

`bootstrap-enroll-nixdocker` ends with the independent cases in
`cases/postgres-pgbackrest.js`. Those cases deliberately use dedicated resource
names and run serially after the baseline suite in the same authenticated browser
context.

`cases/access-enforcement.js` runs mid-flow, after the asset-backed baseline
content exists. It opens a second browser context with its own virtual
authenticator, creates a second user through the setup-password bootstrap flow,
revokes its automatic cluster_admin grant, and verifies the live state stream
empties that session. The admin then creates the `e2e-restricted` space and
grants space_admin bound to it, and the restricted session verifies scoped
visibility (its space, the node/user directory, no default-space content), the
full create/manage lifecycle for deployments, secrets, configs, folders, and
assets inside its space, and access-denied errors on cluster-level actions
(space creation, grant management, node rename). The restricted deployment and
values are deleted before the flow continues; the extra user and space remain.
