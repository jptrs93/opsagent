#!/usr/bin/env bash
#
# Verifies that toolchain versions (Go, Node, pnpm) are consistent between
# flake.nix (local dev source of truth) and .github/workflows/build.yml (CI).
# Fails with a non-zero exit code if any version drifts.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FLAKE="$REPO_ROOT/flake.nix"
WF="$REPO_ROOT/.github/workflows/build.yml"

for f in "$FLAKE" "$WF"; do
    if [ ! -f "$f" ]; then
        echo "ERROR: missing $f" >&2
        exit 1
    fi
done

# --- flake.nix: attribute names like go_1_25, nodejs_22, pnpm_10 ---
flake_go=$(grep -oE 'go_[0-9]+_[0-9]+' "$FLAKE" | head -n1 | sed -E 's/go_([0-9]+)_([0-9]+)/\1.\2/')
flake_node=$(grep -oE 'nodejs_[0-9]+' "$FLAKE" | head -n1 | sed -E 's/nodejs_//')
flake_pnpm=$(grep -oE 'pnpm_[0-9]+' "$FLAKE" | head -n1 | sed -E 's/pnpm_//')

# --- build.yml: scoped to the relevant setup action blocks ---
wf_go=$(awk '/actions\/setup-go/{f=1} f && /go-version:/{gsub(/[^0-9.]/,""); print; exit}' "$WF")
wf_node=$(awk '/actions\/setup-node/{f=1} f && /node-version:/{gsub(/[^0-9.]/,""); print; exit}' "$WF")
wf_pnpm=$(awk '/pnpm\/action-setup/{f=1} f && /version:/{gsub(/[^0-9.]/,""); print; exit}' "$WF")

fail=0
check() {
    local name="$1" flake_v="$2" wf_v="$3"
    if [ -z "$flake_v" ] || [ -z "$wf_v" ]; then
        printf '  %-6s FAIL (could not parse: flake=%q workflow=%q)\n' "$name" "$flake_v" "$wf_v"
        fail=1
    elif [ "$flake_v" = "$wf_v" ]; then
        printf '  %-6s OK   (%s)\n' "$name" "$flake_v"
    else
        printf '  %-6s DRIFT (flake=%s workflow=%s)\n' "$name" "$flake_v" "$wf_v"
        fail=1
    fi
}

echo "Toolchain sync check:"
check "go"   "$flake_go"   "$wf_go"
check "node" "$flake_node" "$wf_node"
check "pnpm" "$flake_pnpm" "$wf_pnpm"

if [ "$fail" -ne 0 ]; then
    echo ""
    echo "Toolchain versions drifted between flake.nix and build.yml." >&2
    echo "Update whichever is stale so both match." >&2
    exit 1
fi
