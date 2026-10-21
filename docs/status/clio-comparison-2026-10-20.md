# creatio-mcp-go vs clio: comparison (2026-10-20)

## Current state after the second wave (2026-10-20, `caa2eb0`)

Both waves of the 8.1.0.142 work are merged. First wave (streams A-D, up to `fdc3aa4`): contracts and
prompts moved to the 8.1.0.142 baseline (`0bb560b`, `674b18d`), T19 rights and connections
([status](t19-rights-connections-status.md)), T20 new arguments ([status](t20-new-args-status.md)) and the
T6 write side ([status](t6-write-status.md)). Second wave (streams a and b, T21): see below. Measured at
`caa2eb0` without a Creatio stand:

| | clio 8.1.0.142 | creatio-mcp-go |
|---|---:|---:|
| Tools in the get-tool-contract index (+ `get-tool-contract`) | 235 + 1 | 200 + 1 |
| Resident in tools/list | 19 | 19 |
| MCP prompts | 74 | 74 |
| Not served | — | 35: 33 T16 (decision D3) + `page-smoke` and `get-mobile-page-conversion-guide` (T21a, not ported) |

How the server side was counted: `get-tool-contract` with `detail=index-entries` and tools/list of the
built binary (`scripts/clio-inventory.py --go-bin`, recorded in the `tasks` map of
`docs/clio-inventory.json`); clio's side from `docs/clio-inventory.json`.

Contract comparison, `python3 scripts/compare-contracts.py --clio-dll <clio 8.1.0.142 clio.dll> --go-bin
<binary built at caa2eb0>` (the evidence file `evidence/contracts-latest.json` is not committed):
`{"match": 1025, "known:not-ported": 70, "known:fit-multi-name": 1}`, 0 unexplained. The 70 are the 35
not-served tools, each counted on two surfaces; the one `fit-multi-name` is the two-tool batch
`get-page,update-page` that clio shortens to 18 KB. After the first wave (`fdc3aa4`) it was 980 match,
88 not-ported (44 tools), 1 fit-multi-name, 0 unexplained.

Of the next steps at the end of this report, 1-4 are done: both scripts read the new `get-tool-contract`
shape, the inventory and contracts are on 8.1.0.142, the four tools that refused the new arguments accept
them (T20), and the 17 new tools are handled (6 ported in T19, 9 ported in T21, 2 not ported in T21a).
Step 5 is decided: the 15 T6 tools are ported, T16 stays deferred. Step 6: the window run and the full write
run on `s16123120` are done in T23 ([status](t23-live-write-parity.md)); the parity run on a second stand is
still open.

Still different from clio, each with its reason in the area report: a call without `environment-name`
goes to this server's default target, where clio 8.1.0.142 refuses it; `git`-type knowledge sources are not
installed or updated here; `validate-page` runs a declared subset of clio's rules (the `known-containers`
check is among those not run).

### Second wave (streams a and b)

T21a ([status](t21a-page-package-status.md)) ported `create-package`, `place-section-in-workplace`,
`localize-page` and `trigger-smoke`. T21b ([status](t21b-to-file-status.md)) ported the five `-to-file`
tools and moved inline `odata-read` and `execute-esq` onto the same code. Compared with clio 8.1.0.142's
own MCP server, refusal paths against an unreachable environment and the rest against fake stands
(`scripts/fake-stands/t21a`, `scripts/fake-stands/t21b`):

| Tool | No stand / refusal paths | Fake stand |
|---|---|---|
| `create-package` | 11: 10 match, 1 both failed | 18: 8 match, 10 differ only in `package-uid` |
| `place-section-in-workplace` | 21: 20 match, 1 both failed | 21: 13 match, 8 differ only in generated ids |
| `localize-page` | 22: 21 match, 1 both failed | 23: 23 match |
| `trigger-smoke` | 25: 25 match | 14: 14 match |
| `execute-esq-to-file` | 17 match, 3 known | 12 match, 5 files byte-identical |
| `odata-read-to-file` | 36 match, 4 known | 17 match, 6 files byte-identical |
| `get-component-info-to-file` | — | 15: 15 match, 5 files byte-identical |
| `get-request-info-to-file` | — | 9: 9 match, 3 files byte-identical |
| `list-entity-client-schemas-to-file` | 7 match, 2 known | 3 match, 2 files byte-identical |
| `odata-read` (inline) | — | 15 match |
| `execute-esq` (inline) | — | 5 match, 1 known |

**Breaking for v0.1.x callers:** `odata-read` now takes clio 8.1.0.142's arguments (`filters`, `expand`,
`order-by`, ...) and answers in its shape (`success`, `count`, `value`, `next-link`, ...); the old
`{rows, count}` answer is gone and the old `orderBy` key is refused with clio's rename hint. `execute-esq`
answers as clio 8.1.0.142's `ExecuteEsqTool` (SQL and flat-call hints, `suggested-query`, `warnings`).

Not ported, with the reason in the [t21a status](t21a-page-package-status.md#not-ported): `page-smoke`
(needs a headless Chromium driven over the DevTools protocol, with browser discovery, download and consent)
and `get-mobile-page-conversion-guide` (needs clio's ~15,800-line web-to-mobile analysis engine).

Second-wave differences, per tool in the two status files: the answer text of the four T21a tools uses
System.Text.Json's default spelling; `create-package` and `place-section-in-workplace` send
`sysSettingsNameCollection` in a different case; `trigger-smoke` sorts the create body; the two
documentation twins depend on a warm documentation cache for `documentationSource`; the five `-to-file`
tools do not detect a symbolic link swapped into a parent directory during the write; inline `odata-read`
reads at most 4 MiB.

The sections below are the report as written earlier on 2026-10-20, before the first wave merged: they
describe `cc1462a` against the 8.1.0.134 contracts and are kept as recorded.


State of `main` at `cc1462a` ("Make the stage-3 tests pass on Linux and Windows") compared with clio
**8.1.0.134** (the parity baseline in `docs/clio-inventory.json`) and with clio **8.1.0.142** (latest release,
2026-10-19).

What was measured today and what is only carried over from earlier reports:

| Source | Facts |
|---|---|
| Measured 2026-10-20 | tool counts of this server and of clio 8.1.0.142; tools/list and get-tool-contract of both servers without an environment; whether this server accepts the new arguments (calls with an unknown environment); `go vet ./...`, `go test ./...`, 24 Python harness tests — all pass |
| Carried over (2026-10-03 … 10-10, not rerun) | live read/write parity on stand `s16123120`, the per-area "awaits the window run" lists, the 220 match / 0 unexplained contract result against 8.1.0.134 |

No Creatio stand was called today.

## Tool coverage

| | clio 8.1.0.134 | clio 8.1.0.142 | creatio-mcp-go |
|---|---:|---:|---:|
| Tools in the get-tool-contract index (+ `get-tool-contract`) | 218 + 1 | 235 + 1 | — |
| Served by this server | 170 of 219 | 170 of 236 | 170 |
| Resident in tools/list | 19 | 19 (same set) | 19 |
| Not served | 49 | 66 | — |

The 170 are 166 tools in the registry plus `clio-run`, `clio-run-destructive`, `list-apps`,
`list-environments` in `main.go`. Every T7–T15 area is fully registered; T1–T6 done as recorded in
[migration-plan.md](../migration-plan.md).

### Not served

| Group | Count | Tools | Status |
|---|---:|---|---|
| T16 local machine, infrastructure, workspace | 33 | `deploy-creatio`, `restore-db-*`, `clear-redis-*`, `create-/push-/restore-workspace`, `reg-web-app`, `stop-creatio`, … (full list in the plan appendix) | deferred by decision D3 |
| T6 knowledge and toolkit management, telemetry writes | 15 | `install-/update-/delete-knowledge`, `add-/remove-/enable-/disable-knowledge-source`, `configure-knowledge-feedback-policy`, `install-/update-/delete-toolkit`, `experimental`, `export-component-registry`, `send-telemetry`, `withdraw-telemetry-consent` | not planned yet; they write clio's `appsettings.json` and knowledge store |
| New in clio 8.1.0.135–142 | 17 | see below | not in any task |

New tools in 8.1.0.142 (none removed):

| Area | Tools |
|---|---|
| Access rights | `get-object-rights`, `set-object-rights`, `apply-default-record-rights`, `set-default-record-rights` |
| Entity connections | `get-entity-connections`, `update-entity-connections` |
| Pages | `localize-page`, `page-smoke`, `trigger-smoke` |
| Applications, packages | `place-section-in-workplace`, `create-package` |
| Write the answer to a file | `execute-esq-to-file`, `odata-read-to-file`, `get-component-info-to-file`, `get-request-info-to-file`, `list-entity-client-schemas-to-file` |
| Guides | `get-mobile-page-conversion-guide` |

## Contract drift between 8.1.0.134 and 8.1.0.142

The contracts this server serves are generated from the 8.1.0.134 inventory. Against 8.1.0.142, 169 of the
170 served tools return a different `get-tool-contract` entry. By cause:

1. **Answer shape of `get-tool-contract` changed.** The call without arguments now returns `tool-index`
   (one text line per tool) and a `legend`; the object list moved to `detail=index-entries`. Every contract
   gained `detail` and `full-contract-bytes` (127 tools). Consequence: `scripts/clio-inventory.py` and
   `scripts/compare-contracts.py` stop with `get-tool-contract returned no index` / `KeyError: 'index'`, so
   the inventory cannot be refreshed and the contract comparison cannot run until both scripts read the new
   shape. An agent that calls `get-tool-contract` without arguments gets a different answer from each
   server.
2. **Texts only.** `error-contract` differs in 150 tools with the same error codes and changed messages;
   for 82 tools the input schema differs only in descriptions.
3. **New arguments — the part that changes behaviour.** 10 tools gained arguments in their 8.1.0.142 input
   schema. Each was called through `clio-run` on this server with the new key and an unknown environment:
   a call that reaches the environment lookup accepts the key, a call refused before it does not.

   | Tool | New arguments | This server |
   |---|---|---|
   | `odata-create` | `rows-file` (`rows` no longer required) | accepts |
   | `get-page`, `update-page` | `include-operations` | accepts |
   | `validate-page` | `known-containers` | accepts |
   | `update-app-section` | `caption-culture` | accepts |
   | `compile-creatio` | `process-name` | accepts |
   | `odata-update` | `rows-file` (`data` no longer required) | refuses: `invalid-parameter-type: argument 'args' ... must be an object` |
   | `odata-delete` | `ids`, `cascade`, `cascade-plan-hash`, `dry-run` (`id`, `confirm` no longer required) | refuses: `ids` as above, `dry-run` / `cascade` with `json: unknown field` |
   | `run-process` | `wait`, `wait-timeout`, `watch`, `cleanup` | refuses: `Unknown args: 'wait'` |
   | `describe-business-process` | `element`, `mode` | refuses: `Unknown args: 'mode'` |

   The accepted ones were ported from a clio master newer than 8.1.0.134; their contracts here still show
   the 8.1.0.134 schema. An agent that follows the 8.1.0.142 contract for the last four tools gets a
   refusal instead of the operation.

4. **tools/list.** 7 of the 19 resident tools differ: `get-page` (annotations, input schema),
   `get-tool-contract`, `validate-page`, `get-guidance` (description, input schema), `list-packages`
   (description), `clio-run`, `clio-run-destructive` (annotations).

`knowledge_tool_catalog.go` still declares clio's 218-tool catalog as this server's capability set.

## Behaviour verified only by earlier reports

From [migration-plan.md](../migration-plan.md#recovery-checkpoint-2026-10-03) and the area reports in this
folder, not rerun today:

- Read parity against 8.1.0.134: 706 calls on two stands served by one process, 0 mismatches (T4); 349
  cases including knowledge and prompts, 0 mismatches (T6).
- Write parity: paired live scenarios passed for T7 applications, own themes (T14) and Contact OData
  create/update (T11). Most other write tools are covered by HTTP fixtures and a canned Creatio only;
  compile, restart, package installs, FSM loads, `install-application`, `install-process-builder`,
  `set-logo` / `set-background-image`, `run-process` launches and Data Forge writes wait for a dedicated
  "window run" on the stand.
- Known permanent differences (each with its reason in the area reports): floating-point last digits in
  `advise-theme-palette`, Go wording of .NET exception texts, no request retries in the user-task designer
  calls, JSON object key order of `descriptor` / `operations`.

## What the docs do not say yet

- `migration-plan.md` "Recovery checkpoint" still says 102 registered tools and Stage 3 partial; the code
  has 170 and all T7–T15 tools.
- `clio-inventory.json` / `.md` and the generated contracts are pinned to 8.1.0.134.
- The lookup of the newest installed clio archive exists twice (T13 and T15), noted in
  [t13-t14-t15-status.md](t13-t14-t15-status.md).

## Next steps

1. Teach `clio-inventory.py` and `compare-contracts.py` the new `get-tool-contract` shape
   (`detail=index-entries`, `tool-index`, `legend`).
2. Re-inventory on 8.1.0.142, `go generate ./internal/cliocontract`, `go test ./...`; classify the new
   differences.
3. Accept the new arguments of `odata-update`, `odata-delete`, `run-process`, `describe-business-process`.
4. Assign the 17 new tools to tasks (rights and entity connections are the largest).
5. Decide on the 15 T6 management tools and keep T16 deferred or not.
6. T17: the window run of the pending writes and a full parity run on two stands, then `v0.2.0`
   (the only release so far is `v0.1.0`, 2026-09-28, read-only preview).
