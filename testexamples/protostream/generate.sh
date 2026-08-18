#!/bin/sh
# Regenerates the Go server code and the JS models used by the e2e suite.
# Run from anywhere; requires cleanproto (see api-contract/proto_generate.sh).
set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(dirname "$(dirname "$SCRIPT_DIR")")

cd "$REPO_ROOT"

cleanproto \
  -go.out testexamples/protostream/gen \
  -js.out testing-vms/e2e/helpers/protostreamgen \
  testexamples/protostream/protostream.proto
