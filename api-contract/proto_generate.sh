#!/bin/sh
set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(dirname "$SCRIPT_DIR")

cd "$REPO_ROOT"

CLEANPROTO_BRANCH=feat/go-presence-and-oneofs
CLEANPROTO_COMMIT=92995203ecc3d672ca61b5692796a90827b32e89
CLEANPROTO_DIR=${CLEANPROTO_DIR:-$REPO_ROOT/../cleanproto}
if [ -d "$CLEANPROTO_DIR" ]; then
  echo "cleanproto: building from $CLEANPROTO_DIR" >&2
  CLEANPROTO="go run -C $CLEANPROTO_DIR ./cmd/cleanproto"
else
  CLEANPROTO="go run github.com/jptrs93/cleanproto/cmd/cleanproto@$CLEANPROTO_COMMIT"
fi

COMBINED_PROTO=$(mktemp api-contract/.combined.XXXXXX.proto)
trap 'rm -f "$COMBINED_PROTO"' EXIT
{
  printf '%s\n\n' 'syntax = "proto3";' 'package opsagent.v1;'
  printf '%s\n' 'import "api-contract/options.proto";' 'import "buf/validate/validate.proto";' 'import "google/protobuf/timestamp.proto";'
  printf '%s\n\n' 'option go_package = "github.com/jptrs93/opsagent/backend/apigen";'
  for proto in api-contract/model/supporting.proto api-contract/model/spaces.proto \
               api-contract/model/deployments.proto api-contract/model/scheduled_instances.proto \
               api-contract/model_deployments_operations.proto \
               api-contract/model/logs.proto api-contract/model_logs_operations.proto \
               api-contract/model/metrics.proto api-contract/model_metrics_operations.proto \
               api-contract/model/secrets.proto api-contract/model_secrets_operations.proto \
               api-contract/model/configs.proto api-contract/model_configs_operations.proto \
               api-contract/model/assets.proto api-contract/model_assets_operations.proto \
               api-contract/model_directories_operations.proto \
               api-contract/model_spaces_operations.proto \
               api-contract/model/auth.proto api-contract/model_auth_operations.proto \
               api-contract/model/sessions.proto api-contract/model_sessions_operations.proto \
               api-contract/model/authz.proto api-contract/model_authz_operations.proto \
               api-contract/model/nodes.proto api-contract/model_nodes_operations.proto \
               api-contract/model/networking.proto \
               api-contract/model/network_policies.proto api-contract/model_network_policies_operations.proto \
               api-contract/model_cluster_operations.proto \
               api-contract/model_enrollment_operations.proto \
               api-contract/model/system_config.proto api-contract/model/backup.proto \
               api-contract/model/events.proto \
               api-contract/api_service.proto api-contract/cluster_service.proto api-contract/enrollment_service.proto; do
    sed '/^syntax = /d; /^package /d; /^import /d; /^option go_package = /d' "$proto"
  done
} > "$COMBINED_PROTO"

$CLEANPROTO \
  -proto_path "$REPO_ROOT" \
  -proto_path "$REPO_ROOT/api-contract" \
  -go.out "$REPO_ROOT/backend/apigen" \
  -js.out "$REPO_ROOT/frontend/src/capi" \
  -go.ctxtype Context \
  -go.client \
  -go.json \
  "$COMBINED_PROTO"
