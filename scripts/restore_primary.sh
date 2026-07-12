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
    local restore_requested_version="${OPENDEPLOY_VERSION:-latest}"
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --version=*)
                restore_requested_version="${1#*=}"
                ;;
            --version)
                if [ "$#" -gt 1 ]; then
                    restore_requested_version="$2"
                fi
                ;;
        esac
        shift
    done
    printf '%s\n' "$restore_requested_version"
}

run_as_root() {
    if [ "${EUID:-$(id -u)}" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

verify_checksum() {
    local restore_checksum_bin_path="$1"
    local restore_checksum_sums_path="$2"
    local restore_checksum_bin_name="$3"
    local restore_checksum_want

    restore_checksum_want="$(awk -v name="$restore_checksum_bin_name" '$2 == name { print $1 }' "$restore_checksum_sums_path")"
    if [ -z "$restore_checksum_want" ]; then
        printf 'No checksum for %s in sha256sums.txt\n' "$restore_checksum_bin_name" >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$(dirname "$restore_checksum_bin_path")" && printf '%s  %s\n' "$restore_checksum_want" "$restore_checksum_bin_name" | sha256sum -c - >/dev/null)
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        local restore_checksum_got
        restore_checksum_got="$(shasum -a 256 "$restore_checksum_bin_path" | awk '{ print $1 }')"
        if [ "$restore_checksum_got" = "$restore_checksum_want" ]; then
            return
        fi
        printf 'Checksum mismatch for %s\n' "$restore_checksum_bin_name" >&2
        exit 1
    fi

    printf 'sha256sum or shasum is required to verify %s\n' "$restore_checksum_bin_name" >&2
    exit 1
}

download_opendeploy() {
    local restore_download_version="$1"
    local restore_download_arch="$2"
    local restore_download_tmp="$3"
    local restore_download_base_url restore_download_bin_name restore_download_bin_path restore_download_sums_path

    if [ "$restore_download_version" = "latest" ]; then
        restore_download_base_url="https://github.com/${REPO}/releases/latest/download"
    else
        restore_download_base_url="https://github.com/${REPO}/releases/download/${restore_download_version}"
    fi

    restore_download_bin_name="opendeploy-linux-${restore_download_arch}"
    restore_download_bin_path="${restore_download_tmp}/${restore_download_bin_name}"
    restore_download_sums_path="${restore_download_tmp}/sha256sums.txt"

    log "downloading ${restore_download_version} ${restore_download_bin_name}"
    curl -fsSL "${restore_download_base_url}/${restore_download_bin_name}" -o "$restore_download_bin_path"
    curl -fsSL "${restore_download_base_url}/sha256sums.txt" -o "$restore_download_sums_path"
    verify_checksum "$restore_download_bin_path" "$restore_download_sums_path" "$restore_download_bin_name"
    chmod +x "$restore_download_bin_path"

    printf '%s\n' "$restore_download_bin_path"
}

main() {
    local restore_version restore_arch restore_bin
    restore_version="$(requested_version "$@")"
    restore_arch="$(detect_arch)"
    OPENDEPLOY_DOWNLOAD_TMP="$(mktemp -d)"
    trap 'rm -rf -- "$OPENDEPLOY_DOWNLOAD_TMP"' EXIT

    restore_bin="$(download_opendeploy "$restore_version" "$restore_arch" "$OPENDEPLOY_DOWNLOAD_TMP")"
    run_as_root "$restore_bin" install primary "$@"
}

main "$@"
