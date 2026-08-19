#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p build
if [ "$#" -gt 0 ]; then
  cases=("$@")
else
  cases=()
  for d in cases/*/; do
    cases+=("$(basename "$d")")
  done
fi
status=0
for c in "${cases[@]}"; do
  out="build/$c.fizz"
  : > "$out"
  cat "cases/$c/frontmatter.yaml" >> "$out"
  echo "" >> "$out"
  while IFS= read -r part; do
    if [ -n "$part" ]; then
      cat "common/$part" >> "$out"
      echo "" >> "$out"
    fi
  done < "cases/$c/parts"
  cat "cases/$c/case.fizz" >> "$out"
  echo "== $c =="
  args=()
  if [ -f "cases/$c/bounds.cfg" ]; then
    args+=(--preinit-hook-file "cases/$c/bounds.cfg")
  fi
  if ! fizz ${args[@]+"${args[@]}"} "$out"; then
    status=1
  fi
done
exit $status
