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
