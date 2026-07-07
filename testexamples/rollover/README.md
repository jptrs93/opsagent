# rollover

Tiny `nixDockerBuild` test app for OpenDeploy rollover and port-forwarding E2E tests.

Environment variables:

- `OPD_ROLLOVER_GENERATION`: text returned by HTTP responses and logs.
- `OPD_ROLLOVER_ADDR`: listen address, default `:8080`.
- `OPD_ROLLOVER_READY_DELAY_MS`: delay before writing `ready\n` to `OPENDEPLOY_READINESS_SOCK_PATH`.
- `OPD_ROLLOVER_BIND_BEFORE_READY`: set to `false` for cooperative host-network rollover candidates that must wait for the old container to release a port.
