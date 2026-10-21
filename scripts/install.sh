#!/bin/sh
# Installs or updates creatio-mcp-go on macOS and Linux and registers it in Claude Code and Codex.
#
#   curl -fsSL https://raw.githubusercontent.com/Alexandr-Kravchuk/creatio-mcp-go/main/scripts/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.2.0 --dir "$HOME/bin" --name creatio-go --no-register
#
# Options (each also has an environment variable):
#   --version <tag>   release to install (CREATIO_MCP_GO_VERSION); default: the newest release,
#                     pre-releases included
#   --dir <path>      install directory (CREATIO_MCP_GO_INSTALL_DIR); default: ~/.local/bin
#   --name <name>     MCP server name in claude/codex (CREATIO_MCP_GO_NAME); default: creatio-go
#   --no-register     do not touch the claude/codex configuration (CREATIO_MCP_GO_NO_REGISTER=1)
#
# Re-running updates the binary in place. The new file is written next to the old one and renamed
# over it, so a server that is already running keeps its file and the next launch takes the new
# build. Nothing is replaced unless the archive matches the release's SHA256SUMS.
#
# CREATIO_MCP_GO_RELEASES_API and CREATIO_MCP_GO_DOWNLOAD_BASE point at another release source
# (used by scripts/test_install_sh.py).
set -eu

repo="Alexandr-Kravchuk/creatio-mcp-go"
releases_api=${CREATIO_MCP_GO_RELEASES_API:-"https://api.github.com/repos/$repo/releases?per_page=10"}
download_base=${CREATIO_MCP_GO_DOWNLOAD_BASE:-"https://github.com/$repo/releases/download"}
version=${CREATIO_MCP_GO_VERSION:-}
install_dir=${CREATIO_MCP_GO_INSTALL_DIR:-"$HOME/.local/bin"}
name=${CREATIO_MCP_GO_NAME:-creatio-go}
register=1
case ${CREATIO_MCP_GO_NO_REGISTER:-} in 1 | true | yes) register=0 ;; esac

say() { printf '%s\n' "$*"; }
fail() {
  printf 'creatio-mcp-go install: %s\n' "$*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || fail "--version needs a value"; version=$2; shift 2 ;;
    --version=*) version=${1#*=}; shift ;;
    --dir) [ $# -ge 2 ] || fail "--dir needs a value"; install_dir=$2; shift 2 ;;
    --dir=*) install_dir=${1#*=}; shift ;;
    --name) [ $# -ge 2 ] || fail "--name needs a value"; name=$2; shift 2 ;;
    --name=*) name=${1#*=}; shift ;;
    --no-register) register=0; shift ;;
    -h | --help) say "usage: install.sh [--version <tag>] [--dir <path>] [--name <mcp-name>] [--no-register]"; exit 0 ;;
    *) fail "unknown option: $1 (see --help)" ;;
  esac
done
[ -n "$name" ] || fail "--name is empty"

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported OS: $(uname -s). Release archives exist for macOS, Linux and Windows (use scripts/install.ps1 there)." ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU architecture: $(uname -m). Release archives exist for amd64 and arm64." ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 2 -o "$2" "$1"; }
  fetch_stdout() { curl -fsSL --retry 2 "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  fetch_stdout() { wget -q -O - "$1"; }
else
  fail "neither curl nor wget is installed"
fi

if command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
elif command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
else
  fail "neither shasum nor sha256sum is installed, so the download cannot be verified"
fi

if [ -z "$version" ]; then
  # The releases list is newest first and includes pre-releases; /releases/latest skips them.
  version=$(fetch_stdout "$releases_api" | tr ',' '\n' | grep '"tag_name"' | head -n 1 |
    sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/') ||
    fail "could not read the release list from $releases_api (network?)"
  [ -n "$version" ] || fail "no release found at $releases_api (network, or GitHub API rate limit?)"
fi
case $version in v*) ;; *) version="v$version" ;; esac

asset="creatio-mcp-go_${version#v}_${os}_${arch}"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/creatio-mcp-go-install.XXXXXX") || fail "cannot create a temporary directory"
staged=""
cleanup() {
  [ -n "$staged" ] && rm -f "$staged"
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

say "Installing creatio-mcp-go $version ($os/$arch)"
fetch "$download_base/$version/$asset.tar.gz" "$tmp/$asset.tar.gz" ||
  fail "download failed: $download_base/$version/$asset.tar.gz (wrong version, or network?)"
fetch "$download_base/$version/SHA256SUMS" "$tmp/SHA256SUMS" ||
  fail "download failed: $download_base/$version/SHA256SUMS"

expected=$(awk -v f="$asset.tar.gz" '$2 == f || $2 == "*" f {print $1; exit}' "$tmp/SHA256SUMS")
[ -n "$expected" ] || fail "SHA256SUMS of $version has no line for $asset.tar.gz"
actual=$(sha256 "$tmp/$asset.tar.gz")
[ "$actual" = "$expected" ] ||
  fail "checksum mismatch for $asset.tar.gz: expected $expected, got $actual. Nothing was installed."

tar -xzf "$tmp/$asset.tar.gz" -C "$tmp" "$asset/creatio-mcp-go" ||
  fail "the archive does not contain $asset/creatio-mcp-go"

mkdir -p "$install_dir" || fail "cannot create $install_dir"
install_dir=$(cd "$install_dir" && pwd) || fail "cannot enter $install_dir"
target="$install_dir/creatio-mcp-go"
# Staged in the target directory so the final mv is a rename on one filesystem.
staged="$install_dir/.creatio-mcp-go.new.$$"
cp "$tmp/$asset/creatio-mcp-go" "$staged" || fail "cannot write to $install_dir"
chmod 755 "$staged"
installed_version=$("$staged" --version 2>/dev/null </dev/null) ||
  fail "the downloaded binary does not start on this machine ($os/$arch). Nothing was replaced."
mv -f "$staged" "$target" || fail "cannot replace $target"
staged=""
[ -n "$installed_version" ] || installed_version=$version
say "Installed $target ($installed_version)"

case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *) say "Note: $install_dir is not on PATH. Add it to your shell profile, for example:"
     say "  export PATH=\"$install_dir:\$PATH\"" ;;
esac

# Client commands get </dev/null: under `curl | sh` stdin is the rest of this script.
# Each client is registered once per name: the same path is left alone, another path is replaced.
register_claude() {
  current=$(claude mcp get "$name" 2>/dev/null </dev/null) || current=""
  current_command=$(printf '%s\n' "$current" | sed -n 's/^[[:space:]]*Command:[[:space:]]*//p' | head -n 1)
  if [ -n "$current" ] && [ "$current_command" = "$target" ] &&
    printf '%s\n' "$current" | grep -q 'Scope:.*User'; then
    say "Claude Code: $name is already registered with $target (user scope); left as is"
    return 0
  fi
  if [ -n "$current" ]; then
    say "Claude Code: $name points at ${current_command:-another command}; registering it again with $target"
    say "  (any --env values of the old entry are not carried over; add them again if you used them)"
    claude mcp remove "$name" --scope user >/dev/null 2>&1 </dev/null || true
  fi
  if claude mcp add --scope user "$name" -- "$target" >/dev/null </dev/null; then
    say "Claude Code: registered $name -> $target (user scope)"
  else
    say "Claude Code: registration failed; run: claude mcp add --scope user $name -- $target" >&2
  fi
}

register_codex() {
  current=$(codex mcp get "$name" 2>/dev/null </dev/null) || current=""
  current_command=$(printf '%s\n' "$current" | sed -n 's/^[[:space:]]*command:[[:space:]]*//p' | head -n 1)
  if [ -n "$current" ] && [ "$current_command" = "$target" ]; then
    say "Codex: $name is already registered with $target; left as is"
    return 0
  fi
  if [ -n "$current" ]; then
    say "Codex: $name points at ${current_command:-another command}; registering it again with $target"
    say "  (any env values of the old entry are not carried over; add them again if you used them)"
    codex mcp remove "$name" >/dev/null 2>&1 </dev/null || true
  fi
  if codex mcp add "$name" -- "$target" >/dev/null </dev/null; then
    say "Codex: registered $name -> $target"
  else
    say "Codex: registration failed; run: codex mcp add $name -- $target" >&2
  fi
}

if [ "$register" = 1 ]; then
  found=0
  if command -v claude >/dev/null 2>&1; then found=1; register_claude; fi
  if command -v codex >/dev/null 2>&1; then found=1; register_codex; fi
  [ "$found" = 1 ] || say "Neither claude nor codex was found; register the server yourself (docs/install.md, sections 3 and 4)."
else
  say "Registration skipped (--no-register)."
fi
say "Done: creatio-mcp-go $installed_version"
