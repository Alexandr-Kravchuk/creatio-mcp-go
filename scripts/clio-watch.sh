#!/usr/bin/env bash
set -euo pipefail

# Weekly check for a new clio release (.github/workflows/clio-watch.yml runs it).
#
# Usage: scripts/clio-watch.sh [--dry-run] [--clio-version <v>] [--clio-dll <path>] [--work-dir <dir>]
#   scripts/clio-watch.sh --dry-run
#   scripts/clio-watch.sh --dry-run --clio-dll <tool-path>/.store/clio/<v>/clio/<v>/tools/net10.0/any/clio.dll
#
# Finds the newest clio on NuGet (or takes --clio-version, or an installed --clio-dll), installs it into a
# temporary tool path, records its MCP inventory with scripts/clio-inventory.py and compares it with the
# committed docs/clio-inventory.json on clio's own content (scripts/clio_watch_diff.py). When clio changed,
# it regenerates docs/ and internal/cliocontract, runs compare-contracts.py, pushes the branch
# clio-<version>-inventory and opens or updates a PR; when GitHub refuses the PR (Actions may not create
# PRs in this repository), it opens or updates an issue with the same text instead.
#
# --dry-run installs nothing when --clio-dll is given, writes only under the work directory and calls no
# git or gh; it prints the decision and the body it would post. A real run is meant for GitHub Actions and
# refuses to run elsewhere unless CLIO_WATCH_ALLOW_LOCAL=1, since it switches the checkout's branch.
#
# clio and the Go server run with HOME set to an empty directory under the work directory, so nothing
# reads or writes the real clio settings.

usage() { sed -n '6,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2; }

dry_run=0; clio_version=""; clio_dll=""; work_dir=""
while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run) dry_run=1; shift ;;
    --clio-version) [[ $# -ge 2 ]] || usage; clio_version=$2; shift 2 ;;
    --clio-dll) [[ $# -ge 2 ]] || usage; clio_dll=$2; shift 2 ;;
    --work-dir) [[ $# -ge 2 ]] || usage; work_dir=$2; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

fail() { echo "clio-watch: $*" >&2; exit 1; }
step() { echo; echo "==> $*"; }

if [[ $dry_run == 0 && ${GITHUB_ACTIONS:-} != true && ${CLIO_WATCH_ALLOW_LOCAL:-0} != 1 ]]; then
  fail "a real run switches branches and pushes; run it in GitHub Actions, use --dry-run, or set CLIO_WATCH_ALLOW_LOCAL=1"
fi
for tool in go python3 dotnet; do command -v "$tool" >/dev/null || fail "$tool is not installed"; done
if [[ -n $clio_dll ]]; then
  [[ -f $clio_dll ]] || fail "no such file: $clio_dll"
  clio_dll=$(cd "$(dirname "$clio_dll")" && pwd)/$(basename "$clio_dll")
fi

if [[ -z $work_dir ]]; then
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/clio-watch.XXXXXX")
  trap 'rm -rf "$work_dir"' EXIT
fi
mkdir -p "$work_dir/home" "$work_dir/inventory"
work_dir=$(cd "$work_dir" && pwd)
# Everything dotnet and clio write goes under the work directory.
isolated=(env "HOME=$work_dir/home" "DOTNET_CLI_HOME=$work_dir/home" "NUGET_PACKAGES=$work_dir/nuget"
          "DOTNET_CLI_TELEMETRY_OPTOUT=1" "DOTNET_NOLOGO=1")

baseline=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["clio"]["version"])' docs/clio-inventory.json)
echo "baseline: clio $baseline (docs/clio-inventory.json)"

if [[ -z $clio_dll ]]; then
  if [[ -z $clio_version ]]; then
    step "Newest clio on NuGet"
    # Highest stable version, compared number by number (8.1.0.142 > 8.1.0.98).
    clio_version=$(curl -fsSL --retry 3 https://api.nuget.org/v3-flatcontainer/clio/index.json | python3 -c '
import json, sys
versions = [v for v in json.load(sys.stdin)["versions"] if "-" not in v and "+" not in v]
print(max(versions, key=lambda v: [int(part) for part in v.split(".")]))')
    echo "newest: clio $clio_version"
    newer=$(python3 -c 'import sys; key = lambda v: [int(p) for p in v.split(".")]; print(int(key(sys.argv[1]) > key(sys.argv[2])))' \
      "$clio_version" "$baseline")
    if [[ $newer == 0 ]]; then
      echo "Decision: no change (no clio newer than the baseline $baseline)"
      [[ -n ${GITHUB_STEP_SUMMARY:-} ]] && echo "No clio newer than the baseline $baseline on NuGet." >>"$GITHUB_STEP_SUMMARY"
      exit 0
    fi
  fi
  step "Install clio $clio_version"
  "${isolated[@]}" dotnet tool install clio --version "$clio_version" --tool-path "$work_dir/tool" >/dev/null
  clio_dll=$(find "$work_dir/tool/.store/clio/$clio_version/clio/$clio_version/tools" -path '*/any/clio.dll' | sort -V | tail -1)
  [[ -n $clio_dll ]] || fail "clio.dll not found under $work_dir/tool/.store"
fi
echo "clio.dll: $clio_dll"

step "Build the Go server"
go_bin=$work_dir/creatio-mcp-go
go build -o "$go_bin" ./cmd/creatio-mcp-go

step "Record the inventory"
new_json=$work_dir/inventory/clio-inventory.json
new_md=$work_dir/inventory/clio-inventory.md
new_plan=$work_dir/inventory/migration-plan.md
cp docs/migration-plan.md "$new_plan"
"${isolated[@]}" python3 scripts/clio-inventory.py --clio-dll "$clio_dll" --go-bin "$go_bin" \
  --json "$new_json" --markdown "$new_md" --plan "$new_plan"
version=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["clio"]["version"])' "$new_json")

step "Compare with the committed inventory"
old_json=$work_dir/committed-inventory.json
cp docs/clio-inventory.json "$old_json"
body=$work_dir/body.md
diff_rc=0
python3 scripts/clio_watch_diff.py "$old_json" "$new_json" --body-out "$body" || diff_rc=$?
case $diff_rc in
  0) changed=0 ;;
  3) changed=1 ;;
  *) fail "clio_watch_diff.py failed (exit $diff_rc)" ;;
esac

if [[ $changed == 0 ]]; then
  echo "Decision: no change (clio $version against the baseline $baseline)"
  cat "$body"
  [[ -n ${GITHUB_STEP_SUMMARY:-} ]] && cat "$body" >>"$GITHUB_STEP_SUMMARY"
  exit 0
fi

tests_note=""
contracts_note="against this server built before the inventory was regenerated"
if [[ $dry_run == 0 ]]; then
  step "Regenerate docs/ and internal/cliocontract"
  cp "$new_json" docs/clio-inventory.json
  cp "$new_md" docs/clio-inventory.md
  cp "$new_plan" docs/migration-plan.md
  go generate ./internal/cliocontract
  test_rc=0
  go test ./internal/cliocontract/... || test_rc=$?
  tests_note="go test ./internal/cliocontract/... exited with $test_rc after go generate."
  go build -o "$go_bin" ./cmd/creatio-mcp-go
  contracts_note="against this server rebuilt from the regenerated contracts"
fi

step "compare-contracts.py"
contracts_rc=0
"${isolated[@]}" python3 scripts/compare-contracts.py --clio-dll "$clio_dll" --go-bin "$go_bin" \
  --inventory "$new_json" --evidence "$work_dir/contracts.json" || contracts_rc=$?
python3 scripts/clio_watch_diff.py "$old_json" "$new_json" --body-out "$body" \
  --contracts "$work_dir/contracts.json" --contracts-exit "$contracts_rc" --contracts-note "$contracts_note" || [[ $? == 3 ]]
{
  echo "### Notes"
  echo
  [[ -n $tests_note ]] && { echo "- $tests_note"; }
  echo "- Written by scripts/clio-watch.sh (.github/workflows/clio-watch.yml). Merge by hand after review."
  echo "- A pull request opened with GITHUB_TOKEN does not start ci.yml: close and reopen the PR, or push a commit to the branch, to run it."
} >>"$body"

branch=clio-$version-inventory
title="clio $version: inventory changed"
if [[ $dry_run == 1 ]]; then
  echo "Decision: changed (clio $version against the baseline $baseline)"
  echo "Would push $branch and open the PR \"$title\" with this body:"
  echo
  cat "$body"
  exit 0
fi

step "Push $branch"
command -v gh >/dev/null || fail "gh is not installed"
git checkout -B "$branch"
git add docs/clio-inventory.json docs/clio-inventory.md docs/migration-plan.md internal/cliocontract/contracts.json
git -c user.name='github-actions[bot]' -c user.email='41898282+github-actions[bot]@users.noreply.github.com' \
  commit -m "clio $version: regenerate the inventory and the embedded contracts"
git push --force origin "HEAD:refs/heads/$branch"

step "Pull request"
pr=$(gh pr list --head "$branch" --state open --json number --jq '.[0].number // empty')
if [[ -n $pr ]]; then
  gh pr edit "$pr" --body-file "$body"
  echo "updated PR #$pr"
elif gh pr create --base main --head "$branch" --title "$title" --body-file "$body"; then
  echo "opened a PR from $branch"
else
  echo "GitHub refused the PR (Actions may not create PRs here); opening an issue instead" >&2
  issue_body=$work_dir/issue.md
  {
    echo "Open a PR from \`$branch\` by hand: ${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:?}/compare/main...$branch?expand=1"
    echo
    cat "$body"
  } >"$issue_body"
  issue=$(gh issue list --state open --limit 200 --json number,title | python3 -c '
import json, sys
print(next((str(i["number"]) for i in json.load(sys.stdin) if i["title"] == sys.argv[1]), ""))' "$title")
  if [[ -n $issue ]]; then
    gh issue edit "$issue" --body-file "$issue_body"
    echo "updated issue #$issue"
  else
    gh issue create --title "$title" --body-file "$issue_body"
  fi
  body=$issue_body
fi
[[ -n ${GITHUB_STEP_SUMMARY:-} ]] && cat "$body" >>"$GITHUB_STEP_SUMMARY"
exit 0
