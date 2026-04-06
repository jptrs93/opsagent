#!/usr/bin/env bash
#
# Generate a private CA and per-node certificates for opsagent cluster mTLS.
#
# Usage: ./generate_certs.sh <machine-name> [machine-name ...]
#
# Example:
#   ./generate_certs.sh primary worker-1 worker-2
#
# Produces (in the current directory):
#   ca.crt, ca.key                — root CA (keep ca.key offline after setup)
#   <name>.crt, <name>.key        — per-node cert + key (deploy to each node)
#
# Each node cert has CN=<name> and is valid for 10 years.
# The CA is self-signed and valid for 20 years.
#
set -euo pipefail

if [ $# -lt 1 ]; then
    echo "Usage: $0 <machine-name> [machine-name ...]" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

DAYS_CA=7300    # ~20 years
DAYS_NODE=3650  # ~10 years

# --- Root CA ---
if [ ! -f ca.key ]; then
    echo "==> Generating root CA"
    openssl ecparam -genkey -name prime256v1 -noout -out ca.key
    openssl req -new -x509 -key ca.key -out ca.crt -days "$DAYS_CA" \
        -subj "/CN=opsagent-ca" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
        -addext "keyUsage=critical,keyCertSign,cRLSign"
    echo "    ca.key  ca.crt"
else
    echo "==> Root CA already exists (ca.key), skipping"
fi

# --- Per-node certs ---
for name in "$@"; do
    if [ -f "${name}.key" ]; then
        echo "==> ${name}.key already exists, skipping"
        continue
    fi

    echo "==> Generating cert for: ${name}"
    openssl ecparam -genkey -name prime256v1 -noout -out "${name}.key"
    openssl req -new -key "${name}.key" -out "${name}.csr" \
        -subj "/CN=${name}"

    openssl x509 -req -in "${name}.csr" \
        -CA ca.crt -CAkey ca.key -CAcreateserial \
        -out "${name}.crt" -days "$DAYS_NODE" \
        -extfile <(printf "basicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth,clientAuth\nsubjectAltName=DNS:%s" "$name")

    rm -f "${name}.csr"
    echo "    ${name}.key  ${name}.crt"
done

rm -f ca.srl

echo ""
echo "Done. Deploy to each node:"
echo "  - ca.crt          → all nodes (OPSAGENT_CLUSTER_CA)"
echo "  - <name>.crt/key  → that node (OPSAGENT_CLUSTER_CERT / OPSAGENT_CLUSTER_KEY)"
echo ""
echo "Keep ca.key offline — it's only needed to issue new node certs."
