#!/usr/bin/env bash
set -euo pipefail

# This is a deliberately conservative pre-push check. It rejects the two easiest accidental
# disclosure classes; a human must still inspect the staged diff before public publication.
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
git diff --cached --check
secret_matches=$(git ls-files -z | xargs -0 rg -n -i \
  '(password|client[_-]?secret|access[_-]?token)\s*[:=]\s*[^${[:space:]#][^[:space:]]{7,}' \
  --glob '!.env.example' --glob '!scripts/check-public-safety.sh' || true)
secret_matches=$(printf '%s\n' "$secret_matches" | rg -v '(replace-me|example-user|\$\{|firstEnv\()' || true)
if [[ -n "$secret_matches" ]]; then
  printf '%s\n' "$secret_matches" >&2
  echo "possible committed secret found" >&2
  exit 1
fi
host_matches=$(git ls-files -z | xargs -0 rg -n -i -o -P \
  '([a-z0-9-]+\.)+(internal|local|corp|lan|test|dev|rnd|stage|preprod)\b(?!-[a-z])' \
  --glob '!scripts/check-public-safety.sh' || true)
# clio's knowledge resources are named like reference.<topic>.test-patterns; they are not hostnames.
host_matches=$(printf '%s\n' "$host_matches" | rg -v -i '([a-z0-9-]+\.)+(test)-[a-z]' || true)
# The encoding/xml local-name property is a Go member access. Match only the
# complete reported token at its known source location; other tokens still fail.
host_matches=$(printf '%s\n' "$host_matches" | rg -v '^internal/creatio/odatawrite\.go:[0-9]+:(element|attr)\.Name\.Local$' || true)
if [[ -n "$host_matches" ]]; then
  printf '%s\n' "$host_matches" >&2
  echo "possible non-public hostname found" >&2
  exit 1
fi
echo "public-safety check passed: no obvious secrets or non-public hostnames in tracked files"
