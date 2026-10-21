#!/usr/bin/env bash
set -euo pipefail

# Regenerates the Homebrew formula for a release in the tap repository, commits and pushes it.
# scripts/release.sh runs it as its last step; run it by hand to retry a tap update that failed after
# the release was already published. The procedure is described in docs/releasing.md.
#
# Usage: scripts/update-homebrew-tap.sh [--dry-run] <tag> <SHA256SUMS>
#   scripts/update-homebrew-tap.sh v0.2.0 dist/SHA256SUMS
#
# HOMEBREW_TAP_REPO (default Alexandr-Kravchuk/homebrew-tap) names the tap; HOMEBREW_TAP_URL (default
# https://github.com/$HOMEBREW_TAP_REPO.git) is the clone and push URL, for example an SSH URL.
# The push uses git's own credentials for that URL (for HTTPS on github.com: gh auth setup-git).
# --dry-run checks that the tap is reachable, prints the formula and pushes nothing.
# Exit codes: 0 updated or already current, 1 failure, 2 usage, 3 the tap is not reachable.

usage() { sed -n '8,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2; }

dry_run=0; positional=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage ;;
    -*) echo "unknown option: $1" >&2; usage ;;
    *) positional+=("$1"); shift ;;
  esac
done
[[ ${#positional[@]} -eq 2 ]] || usage
tag=${positional[0]}; sums=${positional[1]}
version=${tag#v}

tap_repo=${HOMEBREW_TAP_REPO:-Alexandr-Kravchuk/homebrew-tap}
tap_url=${HOMEBREW_TAP_URL:-"https://github.com/$tap_repo.git"}
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

fail() { echo "homebrew-tap: $*" >&2; exit 1; }
[[ -s $sums ]] || fail "SHA256SUMS is missing or empty: $sums"
command -v python3 >/dev/null || fail "python3 is not installed"

GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code "$tap_url" HEAD >/dev/null 2>&1 || {
  echo "homebrew-tap: $tap_url is not reachable (or empty); the tap is not updated" >&2
  exit 3
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [[ $dry_run == 1 ]]; then
  python3 "$script_dir/homebrew-formula.py" --version "$tag" --sums "$sums" --tap "$tap_repo" \
    --out "$work/tap" --readme >/dev/null
  echo "would clone $tap_url, write this Formula/creatio-mcp-go.rb and README.md,"
  echo "commit 'creatio-mcp-go $version' and push:"
  cat "$work/tap/Formula/creatio-mcp-go.rb"
  exit 0
fi

GIT_TERMINAL_PROMPT=0 git clone --quiet --depth 1 "$tap_url" "$work/tap"
python3 "$script_dir/homebrew-formula.py" --version "$tag" --sums "$sums" --tap "$tap_repo" \
  --out "$work/tap" --readme
git -C "$work/tap" add -A
if git -C "$work/tap" diff --cached --quiet; then
  echo "tap $tap_repo already has creatio-mcp-go $version; nothing to push"
  exit 0
fi
git -C "$work/tap" commit --quiet -m "creatio-mcp-go $version"
GIT_TERMINAL_PROMPT=0 git -C "$work/tap" push --quiet origin HEAD
echo "tap $tap_repo updated to creatio-mcp-go $version ($(git -C "$work/tap" rev-parse --short HEAD))"
