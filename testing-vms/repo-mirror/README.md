# Repo Mirror

VM-native test fixture used by `testing-vms/run.sh` when `OPD_REMOTE=mock`.

It serves:

- Git smart HTTP for mirrored test repositories.
- A GitHub-shaped releases API and release asset downloads.
- OpenDeploy, containerd, and runc release artifacts.
- A local TLS OCI registry populated with the container images used by E2E.

The harness maps `github.com`, `api.github.com`, and the registry host to the repo mirror VM inside `/etc/hosts`, so OpenDeploy uses production URLs while tests avoid remote artifact downloads during the run.

`prepare.sh` fails closed for missing release/runtime artifacts and OCI images unless `OPD_REPO_MIRROR_REFRESH=true` is set. That refresh mode is the explicit online seeding path; normal mock runs only use assets already present in `/srv/releases` and `/srv/registry`.
