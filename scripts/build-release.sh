#!/usr/bin/env bash

set -euo pipefail

version="${1:-}"
dist_dir="${2:-dist}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-PRERELEASE] [DIST_DIR]" >&2
  exit 2
fi

release_version="${version#v}"
if [[ -e "$dist_dir" ]] && [[ -n "$(find "$dist_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "release output directory must be empty: $dist_dir" >&2
  exit 2
fi
mkdir -p "$dist_dir"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/fcp-release.XXXXXX")"
trap 'find "$work_dir" -depth -delete' EXIT

for target_os in darwin linux; do
  for target_arch in amd64 arm64; do
    archive="fcp_${release_version}_${target_os}_${target_arch}"
    archive_dir="$work_dir/$archive"
    mkdir -p "$archive_dir"
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags="-s -w -X main.version=$version" \
      -o "$archive_dir/fcp" ./cmd/fcp
    cp LICENSE README.md "$archive_dir/"
    tar -C "$work_dir" -czf "$work_dir/$archive.tar.gz" "$archive"
    rm -r "$archive_dir"
  done
done

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "$work_dir"
    sha256sum ./*.tar.gz >checksums.txt
  )
else
  (
    cd "$work_dir"
    shasum -a 256 ./*.tar.gz >checksums.txt
  )
fi

mv "$work_dir"/* "$dist_dir"/
echo "built release assets for $version in $dist_dir"
