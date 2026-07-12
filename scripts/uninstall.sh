#!/usr/bin/env bash
set -euo pipefail

REPO="${OPENDEPLOY_REPO:-jptrs93/opsagent}"
INSTALLED_BIN="${OPENDEPLOY_BIN:-/var/lib/opendeploy/bin/opendeploy}"
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

run_as_root() {
    if [ "${EUID:-$(id -u)}" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

verify_checksum() {
    local uninstall_checksum_bin_path="$1"
    local uninstall_checksum_sums_path="$2"
    local uninstall_checksum_bin_name="$3"
    local uninstall_checksum_want

    uninstall_checksum_want="$(awk -v name="$uninstall_checksum_bin_name" '$2 == name { print $1 }' "$uninstall_checksum_sums_path")"
    if [ -z "$uninstall_checksum_want" ]; then
        printf 'No checksum for %s in sha256sums.txt\n' "$uninstall_checksum_bin_name" >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$(dirname "$uninstall_checksum_bin_path")" && printf '%s  %s\n' "$uninstall_checksum_want" "$uninstall_checksum_bin_name" | sha256sum -c - >/dev/null)
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        local uninstall_checksum_got
        uninstall_checksum_got="$(shasum -a 256 "$uninstall_checksum_bin_path" | awk '{ print $1 }')"
        if [ "$uninstall_checksum_got" = "$uninstall_checksum_want" ]; then
            return
        fi
        printf 'Checksum mismatch for %s\n' "$uninstall_checksum_bin_name" >&2
        exit 1
    fi

    printf 'sha256sum or shasum is required to verify %s\n' "$uninstall_checksum_bin_name" >&2
    exit 1
}

download_opendeploy() {
    local uninstall_download_version="$1"
    local uninstall_download_arch="$2"
    local uninstall_download_tmp="$3"
    local uninstall_download_base_url uninstall_download_bin_name uninstall_download_bin_path uninstall_download_sums_path

    if [ "$uninstall_download_version" = "latest" ]; then
        uninstall_download_base_url="https://github.com/${REPO}/releases/latest/download"
    else
        uninstall_download_base_url="https://github.com/${REPO}/releases/download/${uninstall_download_version}"
    fi

    uninstall_download_bin_name="opendeploy-linux-${uninstall_download_arch}"
    uninstall_download_bin_path="${uninstall_download_tmp}/${uninstall_download_bin_name}"
    uninstall_download_sums_path="${uninstall_download_tmp}/sha256sums.txt"

    log "downloading ${uninstall_download_version} ${uninstall_download_bin_name}"
    curl -fsSL "${uninstall_download_base_url}/${uninstall_download_bin_name}" -o "$uninstall_download_bin_path"
    curl -fsSL "${uninstall_download_base_url}/sha256sums.txt" -o "$uninstall_download_sums_path"
    verify_checksum "$uninstall_download_bin_path" "$uninstall_download_sums_path" "$uninstall_download_bin_name"
    chmod +x "$uninstall_download_bin_path"

    printf '%s\n' "$uninstall_download_bin_path"
}

main() {
    if [ -x "$INSTALLED_BIN" ]; then
        run_as_root "$INSTALLED_BIN" uninstall "$@"
        return
    fi

    local uninstall_version uninstall_arch uninstall_bin
    uninstall_version="${OPENDEPLOY_VERSION:-latest}"
    uninstall_arch="$(detect_arch)"
    OPENDEPLOY_DOWNLOAD_TMP="$(mktemp -d)"
    trap 'rm -rf -- "$OPENDEPLOY_DOWNLOAD_TMP"' EXIT

    uninstall_bin="$(download_opendeploy "$uninstall_version" "$uninstall_arch" "$OPENDEPLOY_DOWNLOAD_TMP")"
    run_as_root "$uninstall_bin" uninstall "$@"
}

main "$@"
