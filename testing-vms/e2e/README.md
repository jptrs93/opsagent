# OpenDeploy Playwright Flows

This directory contains the Playwright specs and helpers used by the Lima VM E2E
harness. Run them through the VM harness:

```sh
bash testing-vms/run.sh
```

The VM harness copies this suite into the Playwright VM, points it at the HTTPS test
origin, and copies `test-results` / `playwright-report` back after the run.

Select one or more flow files with a comma-separated list:

```sh
FLOWS=bootstrap-enroll-nixdocker bash testing-vms/run.sh
```
