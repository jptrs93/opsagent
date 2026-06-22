# Local Repo Container

This container is a local GitHub-shaped test fixture for install and Playwright
flows. It serves Git smart HTTP, the GitHub release API subset OpenDeploy uses,
OpenDeploy release binaries, and the pinned containerd/runc artifacts.

Start it before running local tests:

```sh
bash testing/localrepocontainer/run.sh
OPENDEPLOY_LOCAL_TEST=true bash testing/e2e/run.sh
```

OpenDeploy hardcodes local-test traffic to `http://opendeploy-local-repo:8080`
when `OPENDEPLOY_LOCAL_TEST=true`, so the container must be attached to the same
Docker network as the install-test containers.

The image downloads upstream artifacts at build time and serves them locally at
test time. Override the cached OpenDeploy releases if needed:

```sh
docker build \
  --build-arg OPD_RELEASES="v0.0.160 v0.0.222" \
  --build-arg OPD_LATEST_RELEASE="v0.0.222" \
  -t opendeploy-local-repo testing/localrepocontainer
```
