#!/bin/sh
set -eu

go install github.com/jptrs93/cleanproto/cmd/cleanproto@v1.16.3

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
APP_ROOT=$(dirname "$SCRIPT_DIR")

cd "$APP_ROOT"
cleanproto \
  -go.out ./backend/apigen \
  -js.out ./frontend/src/capi \
  -go.ctxtype Context \
  -go.client \
  api-contract/api.proto
