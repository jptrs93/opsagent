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
  for proto in api-contract/deployment_model.proto api-contract/model.proto api-contract/system_config_model.proto \
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
