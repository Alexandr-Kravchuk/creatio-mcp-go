#!/usr/bin/env bash
set -euo pipefail

# Local end-to-end release: checks, signed build, annotated tag, GitHub pre-release, and a check of
# what was actually published. The whole procedure is described in docs/releasing.md.
#
# Usage: scripts/release.sh [--dry-run] [--title <title>] [--no-homebrew] [--[no-]homebrew-prerelease] <tag> <notes-file>
#   scripts/release.sh v0.2.0 notes.md
#   scripts/release.sh --title "v0.2.0 — multi-environment" v0.2.0 notes.md
#   scripts/release.sh --dry-run v0.2.0 notes.md
#
# --dry-run does every check and the build, verifies dist/ locally, and stops before the tag, the push
# and the release. In dry-run the branch and up-to-date checks only warn, and the build is unsigned
# (no signing secrets needed) unless DRY_RUN_SIGNED=1 is set.
# A real run reads the signing secrets through scripts/build-release.sh and refuses UNSIGNED=1.
#
# Last step, after the published release is verified: scripts/update-homebrew-tap.sh regenerates
# Formula/creatio-mcp-go.rb in the tap HOMEBREW_TAP_REPO (default Alexandr-Kravchuk/homebrew-tap),
# commits "creatio-mcp-go <version>" and pushes. It is skipped with a warning when the tap is not
# reachable, and with --no-homebrew. --homebrew-prerelease (the default) updates the tap for a
# pre-release too; this script always publishes a pre-release, so --no-homebrew-prerelease currently
# means the tap is not updated. A dry run prints the formula it would push.

usage() { sed -n '7,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2; }

dry_run=0; title=""; homebrew=1; homebrew_prerelease=1; positional=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run) dry_run=1; shift ;;
    --title) [[ $# -ge 2 ]] || usage; title=$2; shift 2 ;;
    --no-homebrew) homebrew=0; shift ;;
    --homebrew-prerelease) homebrew_prerelease=1; shift ;;
    --no-homebrew-prerelease) homebrew_prerelease=0; shift ;;
    -h|--help) usage ;;
    -*) echo "unknown option: $1" >&2; usage ;;
    *) positional+=("$1"); shift ;;
  esac
done
[[ ${#positional[@]} -eq 2 ]] || usage
tag=${positional[0]}; notes_file=${positional[1]}
title=${title:-$tag}

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
dist="$repo_root/dist"

fail() { echo "release: $*" >&2; exit 1; }
warn() { echo "release: WARNING: $*" >&2; }
step() { echo; echo "==> $*"; }
# Fails in a real run; only warns in a dry run.
require() { if [[ $dry_run == 1 ]]; then warn "$* (ignored in --dry-run)"; else fail "$*"; fi; }

step "Preconditions"
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "not a release tag: $tag (expected vX.Y.Z or vX.Y.Z-suffix)"
[[ -s $notes_file ]] || fail "release notes file is missing or empty: $notes_file"
notes_file=$(cd "$(dirname "$notes_file")" && pwd)/$(basename "$notes_file")
for tool in go gh git shasum rg; do command -v "$tool" >/dev/null || fail "$tool is not installed"; done
if [[ $dry_run == 0 && ${UNSIGNED:-0} == 1 ]]; then fail "UNSIGNED=1 is only allowed with --dry-run"; fi
gh auth status >/dev/null 2>&1 || fail "gh is not logged in (gh auth login)"

[[ -z $(git status --porcelain) ]] || fail "the working tree is not clean; commit or remove the changes first"
git fetch --quiet --tags origin
branch=$(git rev-parse --abbrev-ref HEAD)
[[ $branch == main ]] || require "releases are cut from main, current branch is $branch"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || require "HEAD is not origin/main; pull or push first"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then fail "tag $tag already exists locally"; fi
if git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then fail "tag $tag already exists on origin"; fi
if gh release view "$tag" >/dev/null 2>&1; then fail "a GitHub release for $tag already exists"; fi
echo "tag $tag at $(git rev-parse --short HEAD) on $branch"

# The Homebrew tap is optional: an unreachable tap is reported here, before the tag, and skipped.
# gh release create below always passes --prerelease.
prerelease=1
tap_repo=${HOMEBREW_TAP_REPO:-Alexandr-Kravchuk/homebrew-tap}
tap_url=${HOMEBREW_TAP_URL:-"https://github.com/$tap_repo.git"}
if [[ $homebrew == 0 ]]; then
  echo "homebrew tap: skipped (--no-homebrew)"
elif [[ $prerelease == 1 && $homebrew_prerelease == 0 ]]; then
  homebrew=0; echo "homebrew tap: skipped for a pre-release (--no-homebrew-prerelease)"
elif ! command -v python3 >/dev/null; then
  homebrew=0; warn "python3 is not installed: the homebrew tap will not be updated"
elif ! GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code "$tap_url" HEAD >/dev/null 2>&1; then
  homebrew=0; warn "homebrew tap $tap_url is not reachable (or empty): it will not be updated"
else
  echo "homebrew tap: $tap_repo will be updated after the release"
fi

step "gofmt, go vet, go test, public-safety check"
unformatted=$(git ls-files -z -- '*.go' | xargs -0 gofmt -l)
[[ -z $unformatted ]] || fail "these files need gofmt: $unformatted"
go vet ./...
go test ./...
bash scripts/check-public-safety.sh

step "Build archives"
signed=1
if [[ $dry_run == 1 && ${DRY_RUN_SIGNED:-0} != 1 ]]; then signed=0; fi
if [[ $signed == 1 ]]; then
  UNSIGNED=0 scripts/build-release.sh "$tag"
else
  UNSIGNED=1 scripts/build-release.sh "$tag"
fi

version=${tag#v}
expected=(
  "creatio-mcp-go_${version}_darwin_amd64.tar.gz" "creatio-mcp-go_${version}_darwin_arm64.tar.gz"
  "creatio-mcp-go_${version}_linux_amd64.tar.gz" "creatio-mcp-go_${version}_linux_arm64.tar.gz"
  "creatio-mcp-go_${version}_windows_amd64.zip"
)

# Checks a directory of archives: every expected file is there and listed, and every hash matches.
# With signed=1 on macOS, both darwin binaries must be accepted by Gatekeeper as notarized.
verify_archives() {
  local dir=$1 name work
  for name in "${expected[@]}"; do
    [[ -f $dir/$name ]] || fail "$name is missing in $dir"
    grep -q "  $name\$" "$dir/SHA256SUMS" || fail "$name is not listed in SHA256SUMS"
  done
  [[ $(wc -l < "$dir/SHA256SUMS") -eq ${#expected[@]} ]] || fail "SHA256SUMS lists unexpected files"
  (cd "$dir" && shasum -a 256 -c SHA256SUMS)
  if [[ $signed == 1 && $(uname -s) == Darwin ]]; then
    work=$(mktemp -d)
    for name in "${expected[@]}"; do
      [[ $name == *_darwin_* ]] || continue
      tar -xzf "$dir/$name" -C "$work"
      # "-t install" is the assessment that applies to a bare command-line binary; "-t execute"
      # rejects any non-app code. The output must name the notarization, not only the signature.
      spctl -a -t install -vv "$work/${name%.tar.gz}/creatio-mcp-go" 2>&1 | tee "$work/spctl.log"
      grep -q "source=Notarized Developer ID" "$work/spctl.log" || fail "$name: binary is not notarized"
    done
    rm -rf "$work"
  elif [[ $signed == 0 ]]; then
    echo "unsigned build: Gatekeeper check skipped"
  else
    warn "not on macOS: Gatekeeper check skipped; run spctl on a Mac before announcing the release"
  fi
}

step "Verify dist/"
verify_archives "$dist"

if [[ $dry_run == 1 ]]; then
  step "Dry run finished"
  echo "would run: git tag -a $tag -m 'creatio-mcp-go $tag'; git push origin $tag"
  echo "would run: gh release create $tag --prerelease --verify-tag --title '$title' --notes-file $notes_file <archives> SHA256SUMS"
  echo "then download the release back and verify SHA256SUMS and the notarization"
  if [[ $homebrew == 1 ]]; then
    echo "then update the homebrew tap (the sha256 below are of this local build, not of a published release):"
    HOMEBREW_TAP_REPO=$tap_repo HOMEBREW_TAP_URL=$tap_url \
      scripts/update-homebrew-tap.sh --dry-run "$tag" "$dist/SHA256SUMS"
  fi
  exit 0
fi

step "Tag and push"
git tag -a "$tag" -m "creatio-mcp-go $tag"
git push origin "refs/tags/$tag"

step "Create the GitHub pre-release"
assets=()
for name in "${expected[@]}"; do assets+=("$dist/$name"); done
gh release create "$tag" --prerelease --verify-tag --title "$title" --notes-file "$notes_file" \
  "${assets[@]}" "$dist/SHA256SUMS"

step "Download the published release and verify it"
download=$(mktemp -d)
gh release download "$tag" --dir "$download"
diff "$download/SHA256SUMS" "$dist/SHA256SUMS" || fail "published SHA256SUMS differs from the local one"
verify_archives "$download"
rm -rf "$download"

if [[ $homebrew == 1 ]]; then
  step "Update the Homebrew tap"
  # dist/SHA256SUMS was just compared with the published one, so the formula carries the published hashes.
  HOMEBREW_TAP_REPO=$tap_repo HOMEBREW_TAP_URL=$tap_url \
    scripts/update-homebrew-tap.sh "$tag" "$dist/SHA256SUMS" ||
    fail "$tag is published, but the tap update failed; retry: scripts/update-homebrew-tap.sh $tag dist/SHA256SUMS"
fi

step "Released $tag"
gh release view "$tag" --json url --jq .url
