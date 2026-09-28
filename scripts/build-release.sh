#!/usr/bin/env bash
set -euo pipefail

# Cross-compiles static release archives for the platforms testers use, plus SHA256SUMS.
# macOS binaries are signed with the Developer ID certificate (hardened runtime, secure timestamp)
# and notarized by Apple, so Gatekeeper starts them without a quarantine workaround.
#
# Usage: scripts/build-release.sh v0.1.1              (writes to dist/)
#        UNSIGNED=1 scripts/build-release.sh v0.1.1   (local test build, macOS binaries unsigned)
#
# Signing reads RELEASE_ENV (default ~/secrets/adac-release.env), which exports
#   MAC_CERT_P12, MAC_CERT_PASSWORD, APPLE_API_KEY (.p8 path), APPLE_API_KEY_ID, APPLE_API_ISSUER
# It must live outside the repository and is never printed.
version=${1:?Pass the release tag, for example v0.1.1}
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dist="$repo_root/dist"
targets=(darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64)

stage_name() { echo "creatio-mcp-go_${version#v}_${1%/*}_${1#*/}"; }

rm -rf "$dist"; mkdir -p "$dist"
for target in "${targets[@]}"; do
  goos=${target%/*}; goarch=${target#*/}
  stage="$dist/$(stage_name "$target")"; mkdir -p "$stage"
  binary=creatio-mcp-go; [[ $goos == windows ]] && binary+=.exe
  (cd "$repo_root" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
    -ldflags "-s -w -X main.buildID=$version" -o "$stage/$binary" ./cmd/creatio-mcp-go)
  cp "$repo_root/LICENSE" "$repo_root/README.md" "$repo_root/.env.example" "$stage/"
done

darwin_binaries=()
for target in "${targets[@]}"; do
  [[ $target == darwin/* ]] && darwin_binaries+=("$dist/$(stage_name "$target")/creatio-mcp-go")
done

if [[ ${UNSIGNED:-0} == 1 ]]; then
  echo "UNSIGNED=1: macOS binaries are NOT signed; do not publish this build" >&2
else
  release_env=${RELEASE_ENV:-"$HOME/secrets/adac-release.env"}
  [[ -r $release_env ]] || { echo "signing secrets not found: $release_env (or pass UNSIGNED=1)" >&2; exit 2; }
  set -a; source "$release_env"; set +a

  # Import the certificate into a throwaway keychain so the result does not depend on the login keychain.
  keychain="$HOME/Library/Keychains/creatio-mcp-go-release-$$.keychain-db"
  keychain_password="rel-$$-$RANDOM"
  original_keychains=$(security list-keychains -d user | sed -E 's/^[[:space:]]*"//; s/"$//')
  notarize_zip="$dist/notarize.zip"
  cleanup() {
    security list-keychains -d user -s $original_keychains >/dev/null 2>&1 || true
    security delete-keychain "$keychain" >/dev/null 2>&1 || true
    rm -f "$notarize_zip"
  }
  trap cleanup EXIT
  security create-keychain -p "$keychain_password" "$keychain"
  security set-keychain-settings "$keychain"
  security unlock-keychain -p "$keychain_password" "$keychain"
  security import "$MAC_CERT_P12" -k "$keychain" -P "$MAC_CERT_PASSWORD" -T /usr/bin/codesign >/dev/null
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain" >/dev/null
  security list-keychains -d user -s "$keychain" $original_keychains >/dev/null
  identity=$(security find-identity -v -p codesigning "$keychain" | grep "Developer ID Application" | head -1 | sed -E 's/.*"(.*)"$/\1/')
  [[ -n $identity ]] || { echo "no Developer ID Application identity in the certificate" >&2; exit 1; }
  echo "signing identity: $identity"

  for binary in "${darwin_binaries[@]}"; do
    codesign --force --options runtime --timestamp --identifier com.creatio.creatio-mcp-go \
      --sign "$identity" "$binary"
    codesign --verify --strict --verbose=2 "$binary"
  done

  # A bare executable cannot carry a stapled ticket; Gatekeeper fetches the notarization online.
  staging_notary="$dist/notary"; mkdir -p "$staging_notary"
  for binary in "${darwin_binaries[@]}"; do
    arch_dir="$staging_notary/$(basename "${binary%/*}")"; mkdir -p "$arch_dir"; cp -p "$binary" "$arch_dir/"
  done
  ditto -c -k "$staging_notary" "$notarize_zip"
  rm -rf "$staging_notary"
  echo "submitting macOS binaries to the Apple notary service (waits for the result)..."
  xcrun notarytool submit "$notarize_zip" \
    --key "$APPLE_API_KEY" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER" --wait \
    | tee "$dist/notary.log"
  grep -q "status: Accepted" "$dist/notary.log" || { echo "notarization was not accepted" >&2; exit 1; }
  rm -f "$dist/notary.log" "$notarize_zip"
fi

for target in "${targets[@]}"; do
  name=$(stage_name "$target")
  if [[ $target == windows/* ]]; then
    (cd "$dist" && zip -qr "$name.zip" "$name")
  else
    # A Mach-O signature is embedded in the file itself, so tar preserves it.
    tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
  fi
  rm -rf "${dist:?}/$name"
done
(cd "$dist" && shasum -a 256 ./*.tar.gz ./*.zip | sed 's# \./# #' > SHA256SUMS)
ls -l "$dist"
