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
    local secondary_requested_version="${OPENDEPLOY_VERSION:-v0.0.202}"
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --version=*)
                secondary_requested_version="${1#*=}"
                ;;
            --version)
                if [ "$#" -gt 1 ]; then
                    secondary_requested_version="$2"
                fi
                ;;
        esac
        shift
    done
    printf '%s\n' "$secondary_requested_version"
}

run_as_root() {
    if [ "${EUID:-$(id -u)}" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

verify_checksum() {
    local secondary_checksum_bin_path="$1"
    local secondary_checksum_sums_path="$2"
    local secondary_checksum_bin_name="$3"
    local secondary_checksum_want

    secondary_checksum_want="$(awk -v name="$secondary_checksum_bin_name" '$2 == name { print $1 }' "$secondary_checksum_sums_path")"
    if [ -z "$secondary_checksum_want" ]; then
        printf 'No checksum for %s in sha256sums.txt\n' "$secondary_checksum_bin_name" >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$(dirname "$secondary_checksum_bin_path")" && printf '%s  %s\n' "$secondary_checksum_want" "$secondary_checksum_bin_name" | sha256sum -c - >/dev/null)
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        local secondary_checksum_got
        secondary_checksum_got="$(shasum -a 256 "$secondary_checksum_bin_path" | awk '{ print $1 }')"
        if [ "$secondary_checksum_got" = "$secondary_checksum_want" ]; then
            return
        fi
        printf 'Checksum mismatch for %s\n' "$secondary_checksum_bin_name" >&2
        exit 1
    fi

    printf 'sha256sum or shasum is required to verify %s\n' "$secondary_checksum_bin_name" >&2
    exit 1
}

download_opendeploy() {
    local secondary_download_version="$1"
    local secondary_download_arch="$2"
    local secondary_download_tmp="$3"
    local secondary_download_base_url secondary_download_bin_name secondary_download_bin_path secondary_download_sums_path

    if [ "$secondary_download_version" = "latest" ]; then
        secondary_download_base_url="https://github.com/${REPO}/releases/latest/download"
    else
        secondary_download_base_url="https://github.com/${REPO}/releases/download/${secondary_download_version}"
    fi

    secondary_download_bin_name="opendeploy-linux-${secondary_download_arch}"
    secondary_download_bin_path="${secondary_download_tmp}/${secondary_download_bin_name}"
    secondary_download_sums_path="${secondary_download_tmp}/sha256sums.txt"

    log "downloading ${secondary_download_version} ${secondary_download_bin_name}"
    curl -fsSL "${secondary_download_base_url}/${secondary_download_bin_name}" -o "$secondary_download_bin_path"
    curl -fsSL "${secondary_download_base_url}/sha256sums.txt" -o "$secondary_download_sums_path"
    verify_checksum "$secondary_download_bin_path" "$secondary_download_sums_path" "$secondary_download_bin_name"
    chmod +x "$secondary_download_bin_path"

    printf '%s\n' "$secondary_download_bin_path"
}

main() {
    local secondary_version secondary_arch secondary_bin
    secondary_version="$(requested_version "$@")"
    secondary_arch="$(detect_arch)"
    OPENDEPLOY_DOWNLOAD_TMP="$(mktemp -d)"
    trap 'rm -rf -- "$OPENDEPLOY_DOWNLOAD_TMP"' EXIT

    secondary_bin="$(download_opendeploy "$secondary_version" "$secondary_arch" "$OPENDEPLOY_DOWNLOAD_TMP")"
    run_as_root "$secondary_bin" install secondary "$@"
}

main "$@"
