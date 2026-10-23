# T25: environment-name refusal, odata-read limits, validate-page parents, clio watch

2026-10-23. Four of the remaining differences from clio 8.1.0.142 listed in
[clio-mcp-comparison.md](../clio-mcp-comparison.md). Every comparison below ran both servers without a
stand: `dotnet clio.dll mcp-server` (8.1.0.142) and the Go build, each with a temporary `HOME` and
`CLIO_HOME`; where a Creatio answer was needed, a local fake HTTP server answered.

## 1. A call without `environment-name` is refused

Before: a call that named no environment went to the `CREATIO_*` variables, then to clio's active
environment (`ActiveEnvironmentKey`). clio's `ToolCommandResolver` never does that: it refuses with
"Either a configured environment name or an explicit URI is required for MCP command execution. …", and
many clio tools check the name themselves first, with their own text and envelope.

Now (`cmd/creatio-mcp-go/environments.go`): a call without a name or `uri` is refused with clio's text;
the active environment is never used. One exception: `CREATIO_*` are set and clio's `appsettings.json`
does not exist (checked at every call), so the variables are the only target. `get-fsm-mode` answers
clio's texts for an absent (`The arguments dictionary is missing a value for the required parameter
'environmentName'. (Parameter 'arguments')`), null (`invalid-parameter-type: … Received a JSON null, and
the argument is required.`) and blank (`Environment name is required. (Parameter 'environmentName')`)
`environmentName`, and no longer reads `environment-name`, which clio's binder ignores.

How it was compared: every case of `scripts/mcp-parity-cases.json` and `scripts/parity-cases/*.json` that
names a tool (866 cases, 152 tools) was sent to both servers with `environment-name` removed, both reading
one `appsettings.json` whose active environment points at a closed local port (so a fallback shows as a
connection failure instead of a refusal). Answers were compared after `compare_mcp.normalize`, with two
refusals counted as equal only when their texts are equal too.

| Build | equal | different |
|---|---|---|
| before (`b152743`) | 335 | 531 |
| resolver change only (`6337022`) | 701 | 165 |
| with each tool's own check, merged with main `14e791d` (890 cases) | 808 | 82 |

The same cases with `environment-name: ""`: 832 equal, 58 different; with `"  "`: 834 equal, 56 different.
The remaining differences are not about the environment (validate-page gap warning, get-guidance
diagnostics, merge-creatio-artifact `resolver-version`, `documentationSource`, odata-* diagnostics, …).
`get-theme` (name checked before `id`) and `list-user-tasks` (exit -1, clio's own text) were changed after
the merge with main, where another session had already ported their version gates. A `{}` sweep of all 232
tools through `clio-run` changed no tool from equal to different. Tools now follow clio: their own
"environment-name is required…" texts where clio has them (list-themes, check-theming-access,
get-target-package, dataforge-*, process tools, read-page-business-rules, sequence tools,
compile-status / restart-status), other arguments checked before the environment where clio does
(get-app-info, list-page-templates, page facts, related pages, inspect-*, package files, update-page
syntax), the resolver text where clio has no own check (install-gate, compile-creatio, pkg-to-*,
add-custom-logging …), and clio's "what this tool needs plus a minimal valid call" suffix
(`internal/creatio/minicontract.go`) on create-app, create-business-process, modify-business-process and
create-entity-schema. In the single-environment exception (`CREATIO_*`, no settings file) these own
checks let an unnamed call through to that environment. Tests: `cmd/creatio-mcp-go/tool_noenv_test.go`
(about 45 cases, absent / "" / "  ", through clio-run and the raw name).

`compare-mcp.py --go-env-mode=variables` (and the write harness, which shares it) now gives the Go child an
empty `CLIO_HOME`, so the variables stay its only target.

## 2. odata-read limits

clio's inline `odata-read` reads the answer with `ExecuteGetRequest` → `HttpClient.ReadAsStringAsync`;
the only bound is .NET's default `MaxResponseContentBufferSize`, int.MaxValue bytes (Creatio.Client 2.1.0
does not set it; read in the decompiled package). This server capped the inline read at 4 MiB; it now
reads up to int.MaxValue bytes too. The wording past that bound is this server's own: clio's for a 2 GiB
answer was not measured.

`odata-read-to-file` refuses past 64 MiB in both. clio's number in "OData response is at least N bytes"
is Creatio.Client's `written + read` from its 81920-byte copy loop: the bytes read up to and including
the read that crossed the limit. This server reported limit + 1 (67108865); it now reads with the same
loop. The number depends on how the socket delivers the body, in both servers.

Measured against a local fake stand serving a 70 MB and a 5 MiB OData answer:

| | clio 8.1.0.142 | before | now |
|---|---|---|---|
| inline, 5 MiB | success, 1 row | `response exceeds 4194304 bytes` | success, 1 row |
| to-file, 70 MB (3 calls) | 67178282, 67125196, 67178282 | 67108865 | 67141632, 67141580, 67190784 |

## 3. validate-page: the known-containers check

Port of clio's `PageParentNameValidation` (`internal/creatio/pageparents.go`): every `insert` and `move`
`parentName` in `SCHEMA_VIEW_CONFIG_DIFF` (else `SCHEMA_DIFF`) must name an element the diff inserts (or
names inside an insert's `values`, other than its own `name` key) or one of `known-containers`, minus
elements a `remove` without `properties` drops. Without `known-containers` an unresolved parent warns
(with clio's hint to supply them); with them, even `[]`, it fails and `content-ok` turns false. A `move`
of a removed element, and with `known-containers` a `move` of an element not known, is skipped. The
closest-name hint is clio's: Levenshtein over the first 256 UTF-16 units of the first 512 known names in
.NET `HashSet` order (a removed name's slot is reused by the next one added), ties in ordinal order. A
container where clio's reader expects a name raises clio's Newtonsoft cast text. It runs, as in clio,
only when syntax, markers and content passed. The gap warning now comes after clio's own warnings.

How it was compared: 24 bodies in `scripts/parity-cases/validate-page-parents.json` (warn, fail, pass,
nested names, removals, moves, hint ties, the child skipped by the hint, a nameless insert, `merge` not
checked, numeric parent, null and null-item `known-containers`, container casts) and the 20 earlier
`validate-page` cases, sent to both servers; Go's gap warning removed before the comparison. 41 of 44
are equal. The 3 others are checks this server still does not run (a syntax error clio's parser finds
inside a section, the handler vendor-prefix check, mobile bodies). The unit tests repeat clio's answers
for a 12-operation diff measured the same way.

Still not run here (named in the gap warning): field and column binding, widget caption resources,
localizable text, converter, handler, validator, schema-deps and context-await checks, the AST lint, the
chart-widget, run-process-button and placement-slot (insert/move `propertyName`) checks, data-source
binding warnings, mobile bodies. Two consequences for the parent check: clio skips it when one of those
checks fails, this server does not; and a numeric element `name` makes one of those clio checks raise
("The requested operation requires an element of type 'String' …") before the parent check runs.

## 4. Weekly clio watch

`.github/workflows/clio-watch.yml` (Mondays 06:00 UTC and on demand, optional `clio-version`) runs
`scripts/clio-watch.sh`: newest stable clio on NuGet (numeric version order), installed into a temporary
tool path with an isolated `HOME`; Go server built; `clio-inventory.py` into a temporary directory;
`scripts/clio_watch_diff.py` compares it with `docs/clio-inventory.json` on clio's own content (tools,
schemas, annotations, contract index and contracts, prompts, resource templates) and ignores the date,
the version string, the served marks and the order of resources. The knowledge library resources
(`docs://knowledge/*`, downloaded by clio at start and released on their own) are listed in the body but
do not count as a change. When clio changed: the inventory, `docs/clio-inventory.md`, the
`migration-plan.md` appendix and `internal/cliocontract` are regenerated, `compare-contracts.py` runs
(its exit code does not fail the job), and the branch `clio-<version>-inventory` is force-pushed with a
PR listing new and removed tools, changed input schemas and descriptions, and the `unexplained` rows.
Only `GITHUB_TOKEN`; never merged automatically.

The repository setting "Allow GitHub Actions to create and approve pull requests" is off
(`can_approve_pull_request_reviews: false`), so GitHub refuses the PR; the script then opens or updates
an issue with the same text and a link to open the PR by hand. A PR opened with `GITHUB_TOKEN` does not
start `ci.yml`.

Dry runs (`scripts/clio-watch.sh --dry-run`): against the installed 8.1.0.142 — "no change"; against
NuGet's newest — clio **8.1.0.143** is published and its inventory changed: `place-section-in-workplace`
gained the `workplace-type` argument (its aliases, examples and output contract changed too) and the
descriptions of `create-business-process`, `describe-business-process`, `list-user-tasks`,
`modify-business-process`, `modify-business-process-as-new-version` and `validate-process-graph`
changed; compare-contracts reports 20 unexplained rows, all on those seven tools.

## Found, not changed

- clio refuses every environment tool with "clio settings bootstrap is broken. Repair <path>. Explicit
  uri/login/password remains available only as an emergency fallback." when its `appsettings.json` has
  `Environments` but no `ActiveEnvironmentKey`; this server serves the named environment. Seen with a
  hand-written settings file; clio's bootstrap rules (`SettingsBootstrapService`) were not ported.
- A direct call of a hidden tool that fails: clio appends "[clio] Attempted '<tool>' directly; it is not
  advertised in tools/list. Prefer the advertised executor next time: clio-run …" and prefixes
  "Error: tool '<tool>' failed:", this server prefixes "MCP tool '<tool>' failed:". Seen on
  `get-fsm-mode`; the parity cases call these tools through `clio-run`.
- clio does not unwrap `{"args": {...}}` for `get-fsm-mode` (a scalar-parameter tool), so
  `{"args": {"environmentName": "x"}}` is "missing a value for the required parameter"; this server
  unwraps it for every tool.

## Checks

On the final tree: `gofmt -l` empty, `go vet ./...`, `go test -race ./...`,
`GOOS=windows GOARCH=amd64 go build ./...`, `python3 -m pytest -q scripts` (82 passed, 8 skipped),
`scripts/check-public-safety.sh` — all pass; `compare-contracts.py`: 1025 match, 70 known:not-ported,
1 known:fit-multi-name, 0 unexplained. `docs/clio-inventory.*` and `internal/cliocontract` are not
regenerated here.
