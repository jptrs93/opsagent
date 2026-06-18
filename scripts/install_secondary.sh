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
    local version="${OPENDEPLOY_VERSION:-v0.0.160}"
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --version=*)
                version="${1#*=}"
                ;;
            --version)
                if [ "$#" -gt 1 ]; then
                    version="$2"
                fi
                ;;
        esac
        shift
    done
    printf '%s\n' "$version"
}

run_as_root() {
    if [ "${EUID:-$(id -u)}" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

verify_checksum() {
    local bin_path="$1"
    local sums_path="$2"
    local bin_name="$3"
    local want

    want="$(awk -v name="$bin_name" '$2 == name { print $1 }' "$sums_path")"
    if [ -z "$want" ]; then
        printf 'No checksum for %s in sha256sums.txt\n' "$bin_name" >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$(dirname "$bin_path")" && printf '%s  %s\n' "$want" "$bin_name" | sha256sum -c - >/dev/null)
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        local got
        got="$(shasum -a 256 "$bin_path" | awk '{ print $1 }')"
        if [ "$got" = "$want" ]; then
            return
        fi
        printf 'Checksum mismatch for %s\n' "$bin_name" >&2
        exit 1
    fi

    printf 'sha256sum or shasum is required to verify %s\n' "$bin_name" >&2
    exit 1
}

download_opendeploy() {
    local version="$1"
    local arch="$2"
    local tmp="$3"
    local base_url bin_name bin_path sums_path

    if [ "$version" = "latest" ]; then
        base_url="https://github.com/${REPO}/releases/latest/download"
    else
        base_url="https://github.com/${REPO}/releases/download/${version}"
    fi

    bin_name="opendeploy-linux-${arch}"
    bin_path="${tmp}/${bin_name}"
    sums_path="${tmp}/sha256sums.txt"

    log "downloading ${version} ${bin_name}"
    curl -fsSL "${base_url}/${bin_name}" -o "$bin_path"
    curl -fsSL "${base_url}/sha256sums.txt" -o "$sums_path"
    verify_checksum "$bin_path" "$sums_path" "$bin_name"
    chmod +x "$bin_path"

    printf '%s\n' "$bin_path"
}

main() {
    local version arch bin
    version="$(requested_version "$@")"
    arch="$(detect_arch)"
    OPENDEPLOY_DOWNLOAD_TMP="$(mktemp -d)"
    trap 'rm -rf -- "$OPENDEPLOY_DOWNLOAD_TMP"' EXIT

    bin="$(download_opendeploy "$version" "$arch" "$OPENDEPLOY_DOWNLOAD_TMP")"
    run_as_root "$bin" install secondary "$@"
}

main "$@"
