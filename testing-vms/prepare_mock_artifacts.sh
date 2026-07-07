#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
source ./lib.sh

require_cmd curl
require_cmd git
require_cmd shasum
if ! command -v skopeo >/dev/null 2>&1 && ! command -v docker >/dev/null 2>&1; then
  die "skopeo or docker is required to prepare mock OCI artifacts"
fi

download_file() {
  local url=$1
  local dst=$2
  mkdir -p "$(dirname "$dst")"
  if [[ -s "$dst" ]]; then
    return
  fi
  log "Downloading $url"
  curl -fL --retry 3 --retry-delay 2 "$url" -o "$dst"
}

mirror_git_repo() {
  local owner_repo=$1
  local url=$2
  local dst="$MOCK_ARTIFACT_DIR/git/${owner_repo}.git"
  mkdir -p "$(dirname "$dst")"
  if [[ -d "$dst" ]]; then
    log "Refreshing git mirror $owner_repo"
    git -C "$dst" remote update --prune
  else
    log "Cloning git mirror $owner_repo"
    git clone --mirror "$url" "$dst"
  fi
  git -C "$dst" update-server-info
}

prepare_release() {
  local version=$1
  local arch release_dir bin sum_file
  release_dir="$MOCK_ARTIFACT_DIR/releases/$RELEASE_REPO/$version"
  mkdir -p "$release_dir"
  for arch in amd64 arm64; do
    bin="$release_dir/opendeploy-linux-$arch"
    download_file "https://github.com/$RELEASE_REPO/releases/download/$version/opendeploy-linux-$arch" "$bin"
    chmod 0755 "$bin"
  done
  sum_file="$release_dir/sha256sums.txt"
  download_file "https://github.com/$RELEASE_REPO/releases/download/$version/sha256sums.txt" "$sum_file"
}

prepare_runtime() {
  local arch
  for arch in amd64 arm64; do
    download_file "https://github.com/containerd/containerd/releases/download/v$CONTAINERD_VERSION/containerd-$CONTAINERD_VERSION-linux-$arch.tar.gz" \
      "$MOCK_ARTIFACT_DIR/releases/containerd/containerd/v$CONTAINERD_VERSION/containerd-$CONTAINERD_VERSION-linux-$arch.tar.gz"
    download_file "https://github.com/opencontainers/runc/releases/download/v$RUNC_VERSION/runc.$arch" \
      "$MOCK_ARTIFACT_DIR/releases/opencontainers/runc/v$RUNC_VERSION/runc.$arch"
  done
}

prepare_oci() {
  local spec src archive
  for spec in $REPO_MIRROR_OCI_IMAGES; do
    src=${spec%%=*}
    archive=$(mock_artifact_oci_archive "$src")
    if [[ -s "$archive" ]]; then
      continue
    fi
    mkdir -p "$(dirname "$archive")"
    if command -v skopeo >/dev/null 2>&1; then
      log "Saving OCI image $src with skopeo"
      skopeo copy --all "docker://$src" "oci-archive:$archive:$src"
    else
      log "Saving OCI image $src with docker"
      docker pull --platform "linux/$GOARCH" "$src"
      docker save "$src" -o "$archive"
    fi
  done
}

prepare_lima_images() {
  download_file "$LIMA_IMAGE_ARM64_URL" "$LIMA_IMAGE_ARM64"
  download_file "$LIMA_IMAGE_AMD64_URL" "$LIMA_IMAGE_AMD64"
}

mkdir -p "$MOCK_ARTIFACT_DIR"
mirror_git_repo jptrs93/opsagent https://github.com/jptrs93/opsagent.git
mirror_git_repo jptrs93/jnotes https://github.com/jptrs93/jnotes.git

for version in $REPO_MIRROR_RELEASES; do
  [[ -n "$version" ]] || continue
  prepare_release "$version"
done
mkdir -p "$MOCK_ARTIFACT_DIR/releases/$RELEASE_REPO"
printf '%s\n' "$REPO_MIRROR_LATEST" > "$MOCK_ARTIFACT_DIR/releases/$RELEASE_REPO/latest"

prepare_runtime
prepare_oci
prepare_lima_images

log "Mock artifacts ready in $MOCK_ARTIFACT_DIR"
