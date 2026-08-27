#!/bin/sh
go install github.com/jptrs93/cleanproto/cmd/cleanproto@v1.19.0
set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(dirname "$SCRIPT_DIR")

cd "$REPO_ROOT"

COMBINED_PROTO=$(mktemp api-contract/.combined.XXXXXX.proto)
trap 'rm -f "$COMBINED_PROTO"' EXIT
{
  printf '%s\n\n' 'syntax = "proto3";' 'package opsagent.v1;'
  printf '%s\n' 'import "api-contract/options.proto";' 'import "google/protobuf/timestamp.proto";'
  printf '%s\n\n' 'option go_package = "github.com/jptrs93/opsagent/backend/apigen";'
  for proto in api-contract/model/deployments.proto api-contract/model/scheduled_instances.proto \
               api-contract/model_deployments_operations.proto \
               api-contract/model/logs.proto api-contract/model_logs_operations.proto \
               api-contract/model/secrets.proto api-contract/model_secrets_operations.proto \
               api-contract/model/configs.proto api-contract/model_configs_operations.proto \
               api-contract/model/assets.proto api-contract/model_assets_operations.proto \
               api-contract/model_directories_operations.proto \
               api-contract/model/spaces.proto api-contract/model_spaces_operations.proto \
               api-contract/model/auth.proto api-contract/model_auth_operations.proto \
               api-contract/model/sessions.proto api-contract/model_sessions_operations.proto \
               api-contract/model/authz.proto api-contract/model_authz_operations.proto \
               api-contract/model/nodes.proto api-contract/model_nodes_operations.proto \
               api-contract/model/networking.proto \
               api-contract/model/network_policies.proto api-contract/model_network_policies_operations.proto \
               api-contract/model_cluster_operations.proto \
               api-contract/model/enrollment.proto api-contract/model_enrollment_operations.proto \
               api-contract/model/primary_config.proto api-contract/model/backup.proto \
               api-contract/model_global_operations.proto \
               api-contract/api_service.proto api-contract/cluster_service.proto api-contract/enrollment_service.proto; do
    sed '/^syntax = /d; /^package /d; /^import /d; /^option go_package = /d' "$proto"
  done
} > "$COMBINED_PROTO"

cleanproto \
  -go.out ./backend/apigen \
  -js.out ./frontend/src/capi \
  -go.ctxtype Context \
  -go.client \
  -go.json \
  "$COMBINED_PROTO"
