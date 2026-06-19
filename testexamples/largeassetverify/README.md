# Large Asset Verify

E2E workload that reads the mounted file at `OPENDEPLOY_E2E_ASSET_PATH`, prints its size and SHA-256 hash, and exits non-zero if it does not match `OPENDEPLOY_E2E_ASSET_SHA256`.
