#!/usr/bin/env bash
set -euo pipefail

REPO="${OPENDEPLOY_REPO:-jptrs93/opsagent}"
OPENDEPLOY_DOWNLOAD_TMP=""

log() {
    printf 'opendeploy: %s\n' "$*" >&2
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) printf 'amd64\n' ;;
        aarch64|arm64) printf 'arm64\n' ;;
        *)
            printf 'Unsupported architecture: %s\n' "$(uname -m)" >&2
            exit 1
            ;;
    esac
}

requested_version() {
    local primary_requested_version="${OPENDEPLOY_VERSION:-v0.0.160}"
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --version=*)
                primary_requested_version="${1#*=}"
                ;;
            --version)
                if [ "$#" -gt 1 ]; then
                    primary_requested_version="$2"
                fi
                ;;
        esac
        shift
    done
    printf '%s\n' "$primary_requested_version"
}

run_as_root() {
    if [ "${EUID:-$(id -u)}" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

verify_checksum() {
    local primary_checksum_bin_path="$1"
    local primary_checksum_sums_path="$2"
    local primary_checksum_bin_name="$3"
    local primary_checksum_want

    primary_checksum_want="$(awk -v name="$primary_checksum_bin_name" '$2 == name { print $1 }' "$primary_checksum_sums_path")"
    if [ -z "$primary_checksum_want" ]; then
        printf 'No checksum for %s in sha256sums.txt\n' "$primary_checksum_bin_name" >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$(dirname "$primary_checksum_bin_path")" && printf '%s  %s\n' "$primary_checksum_want" "$primary_checksum_bin_name" | sha256sum -c - >/dev/null)
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        local primary_checksum_got
        primary_checksum_got="$(shasum -a 256 "$primary_checksum_bin_path" | awk '{ print $1 }')"
        if [ "$primary_checksum_got" = "$primary_checksum_want" ]; then
            return
        fi
        printf 'Checksum mismatch for %s\n' "$primary_checksum_bin_name" >&2
        exit 1
    fi

    printf 'sha256sum or shasum is required to verify %s\n' "$primary_checksum_bin_name" >&2
    exit 1
}

download_opendeploy() {
    local primary_download_version="$1"
    local primary_download_arch="$2"
    local primary_download_tmp="$3"
    local primary_download_base_url primary_download_bin_name primary_download_bin_path primary_download_sums_path

    if [ "$primary_download_version" = "latest" ]; then
        primary_download_base_url="https://github.com/${REPO}/releases/latest/download"
    else
        primary_download_base_url="https://github.com/${REPO}/releases/download/${primary_download_version}"
    fi

    primary_download_bin_name="opendeploy-linux-${primary_download_arch}"
    primary_download_bin_path="${primary_download_tmp}/${primary_download_bin_name}"
    primary_download_sums_path="${primary_download_tmp}/sha256sums.txt"

    log "downloading ${primary_download_version} ${primary_download_bin_name}"
    curl -fsSL "${primary_download_base_url}/${primary_download_bin_name}" -o "$primary_download_bin_path"
    curl -fsSL "${primary_download_base_url}/sha256sums.txt" -o "$primary_download_sums_path"
    verify_checksum "$primary_download_bin_path" "$primary_download_sums_path" "$primary_download_bin_name"
    chmod +x "$primary_download_bin_path"

    printf '%s\n' "$primary_download_bin_path"
}

main() {
    local primary_version primary_arch primary_bin
    primary_version="$(requested_version "$@")"
    primary_arch="$(detect_arch)"
    OPENDEPLOY_DOWNLOAD_TMP="$(mktemp -d)"
    trap 'rm -rf -- "$OPENDEPLOY_DOWNLOAD_TMP"' EXIT

    primary_bin="$(download_opendeploy "$primary_version" "$primary_arch" "$OPENDEPLOY_DOWNLOAD_TMP")"
    # The binary owns installer flag parsing; keep this wrapper transparent.
    run_as_root "$primary_bin" install primary "$@"
}

main "$@"
