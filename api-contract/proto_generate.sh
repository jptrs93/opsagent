#!/bin/sh
# go install github.com/jptrs93/cleanproto/cmd/cleanproto@latest
set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(dirname "$SCRIPT_DIR")

cd "$REPO_ROOT"

cleanproto \
  -go.out ./backend/apigen \
  -js.out ./frontend/src/capi \
  -go.ctxtype Context \
  ./api-contract/api.proto
