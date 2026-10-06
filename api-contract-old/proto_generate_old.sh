#!/bin/sh
set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(dirname "$SCRIPT_DIR")

cd "$REPO_ROOT"

COMBINED_PROTO=$(mktemp api-contract-old/.combined.XXXXXX.proto)
trap 'rm -f "$COMBINED_PROTO"' EXIT
{
  printf '%s\n\n' 'syntax = "proto3";' 'package opsagent.v1;'
  printf '%s\n' 'import "api-contract-old/options.proto";' 'import "google/protobuf/timestamp.proto";'
  printf '%s\n\n' 'option go_package = "github.com/jptrs93/opsagent/backend/apigenold";'
  for proto in api-contract-old/model/deployments.proto api-contract-old/model/scheduled_instances.proto \
               api-contract-old/model_deployments_operations.proto \
               api-contract-old/model/logs.proto api-contract-old/model_logs_operations.proto \
               api-contract-old/model/metrics.proto api-contract-old/model_metrics_operations.proto \
               api-contract-old/model/secrets.proto api-contract-old/model_secrets_operations.proto \
               api-contract-old/model/configs.proto api-contract-old/model_configs_operations.proto \
               api-contract-old/model/assets.proto api-contract-old/model_assets_operations.proto \
               api-contract-old/model_directories_operations.proto \
               api-contract-old/model/spaces.proto api-contract-old/model_spaces_operations.proto \
               api-contract-old/model/auth.proto api-contract-old/model_auth_operations.proto \
               api-contract-old/model/sessions.proto api-contract-old/model_sessions_operations.proto \
               api-contract-old/model/authz.proto api-contract-old/model_authz_operations.proto \
               api-contract-old/model/nodes.proto api-contract-old/model_nodes_operations.proto \
               api-contract-old/model/networking.proto \
               api-contract-old/model/network_policies.proto api-contract-old/model_network_policies_operations.proto \
               api-contract-old/model_cluster_operations.proto \
               api-contract-old/model_enrollment_operations.proto \
               api-contract-old/model/system_config.proto api-contract-old/model/backup.proto \
               api-contract-old/model/events.proto; do
    sed '/^syntax = /d; /^package /d; /^import /d; /^option go_package = /d' "$proto"
  done
} > "$COMBINED_PROTO"

go run github.com/jptrs93/cleanproto/cmd/cleanproto@v1.25.1 \
  -go.out ./backend/apigenold \
  -go.server=false \
  "$COMBINED_PROTO"
