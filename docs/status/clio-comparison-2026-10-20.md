# creatio-mcp-go vs clio: comparison (2026-10-20)

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
