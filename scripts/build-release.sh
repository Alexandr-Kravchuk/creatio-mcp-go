#!/usr/bin/env bash
set -euo pipefail

# Cross-compiles static release archives for the platforms testers use, plus SHA256SUMS.
# Usage: scripts/build-release.sh v0.1.0   (writes to dist/)
version=${1:?Pass the release tag, for example v0.1.0}
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dist="$repo_root/dist"
rm -rf "$dist"; mkdir -p "$dist"
targets=(darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64)
for target in "${targets[@]}"; do
  goos=${target%/*}; goarch=${target#*/}
  name="creatio-mcp-go_${version#v}_${goos}_${goarch}"
  stage="$dist/$name"; mkdir -p "$stage"
  binary=creatio-mcp-go; [[ $goos == windows ]] && binary+=.exe
  (cd "$repo_root" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
    -ldflags "-s -w -X main.buildID=$version" -o "$stage/$binary" ./cmd/creatio-mcp-go)
  cp "$repo_root/LICENSE" "$repo_root/README.md" "$repo_root/.env.example" "$stage/"
  if [[ $goos == windows ]]; then
    (cd "$dist" && zip -qr "$name.zip" "$name")
  else
    tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done
(cd "$dist" && shasum -a 256 ./*.tar.gz ./*.zip | sed 's# \./# #' > SHA256SUMS)
ls -l "$dist"
