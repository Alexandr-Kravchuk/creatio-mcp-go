#!/usr/bin/env bash
set -euo pipefail

# Persists counts and hashes only: never application values, URLs, credentials, tokens, or raw output.
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
clio_root=${CLIO_ROOT:?Set CLIO_ROOT to a local clio checkout.}
go_bin=${GO_BIN:-go}
clio_dll=${CLIO_DLL:-"$clio_root/clio/bin/Debug/net10.0/clio.dll"}
evidence_path=${EVIDENCE_PATH:-"$repo_root/evidence/latest.json"}
base_url=${CREATIO_URL:-${CREATIO_MCP_BASE_URL:-}}
login=${CREATIO_LOGIN:-${CREATIO_MCP_LOGIN:-}}
password=${CREATIO_PASSWORD:-${CREATIO_MCP_PASSWORD:-}}
client_id=${CREATIO_CLIENT_ID:-}
client_secret=${CREATIO_CLIENT_SECRET:-}
auth_app_uri=${CREATIO_AUTH_APP_URI:-${CREATIO_TOKEN_URL:-}}
if [[ -z "$base_url" ]]; then echo "CREATIO_URL is required" >&2; exit 2; fi
forms=false; oauth=false
if [[ -n "$login" || -n "$password" ]]; then forms=true; fi
if [[ -n "$client_id" || -n "$client_secret" || -n "$auth_app_uri" ]]; then oauth=true; fi
if [[ "$forms" == true && "$oauth" == true ]]; then echo "choose forms or OAuth, not both" >&2; exit 2; fi
if [[ "$forms" == true && ( -z "$login" || -z "$password" ) ]]; then echo "forms comparison requires CREATIO_LOGIN and CREATIO_PASSWORD" >&2; exit 2; fi
if [[ "$oauth" == true && ( -z "$client_id" || -z "$client_secret" || -z "$auth_app_uri" ) ]]; then echo "OAuth comparison requires CREATIO_CLIENT_ID, CREATIO_CLIENT_SECRET, and CREATIO_AUTH_APP_URI" >&2; exit 2; fi
if [[ "$forms" == false && "$oauth" == false ]]; then echo "configure forms or OAuth credentials" >&2; exit 2; fi
if [[ ! -f "$clio_dll" ]]; then echo "clio DLL not found; build clio first or set CLIO_DLL" >&2; exit 2; fi
temp_dir=$(mktemp -d); trap 'rm -rf "$temp_dir"' EXIT
# A direct-credential comparison needs no registered clio environment. Isolate clio's runtime files
# so a locked or pre-existing user profile cannot affect the evidence run.
export CLIO_HOME=${CLIO_HOME:-"$temp_dir/clio-home"}
mkdir -p "$CLIO_HOME"
clio_args=(list-apps --json --uri "$base_url")
if [[ "$forms" == true ]]; then
  clio_args+=(--login "$login" --password "$password")
else
  clio_args+=(--client-id "$client_id" --client-secret "$client_secret" --auth-app-uri "$auth_app_uri")
fi
if [[ ${CREATIO_IS_NET_CORE:-false} == true ]]; then clio_args+=(--IsNetCore true); fi
dotnet "$clio_dll" "${clio_args[@]}" >"$temp_dir/clio.json"
(cd "$repo_root" && "$go_bin" run ./cmd/creatio-mcp-go --list-apps-json) >"$temp_dir/go.json"
python3 - "$temp_dir/clio.json" "$temp_dir/go.json" "$evidence_path" <<'PY'
import hashlib, json, pathlib, sys
def normalized(path):
    rows = json.load(open(path, encoding="utf-8")); required = ("id", "name", "code", "version", "description")
    return sorted([{key: row.get(key, "") for key in required} for row in rows], key=lambda row: tuple(row[key].casefold() for key in required))
left, right = normalized(sys.argv[1]), normalized(sys.argv[2])
fingerprint = lambda rows: hashlib.sha256(json.dumps(rows, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
left_set = {json.dumps(row, sort_keys=True, separators=(",", ":"), ensure_ascii=False) for row in left}; right_set = {json.dumps(row, sort_keys=True, separators=(",", ":"), ensure_ascii=False) for row in right}
result = {"schema": 1, "result": "match" if left == right else "mismatch", "clio_count": len(left), "go_count": len(right), "clio_sha256": fingerprint(left), "go_sha256": fingerprint(right), "only_in_clio_count": len(left_set - right_set), "only_in_go_count": len(right_set - left_set)}
path = pathlib.Path(sys.argv[3]); path.parent.mkdir(parents=True, exist_ok=True); path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8"); print(json.dumps(result, indent=2)); sys.exit(0 if result["result"] == "match" else 1)
PY
