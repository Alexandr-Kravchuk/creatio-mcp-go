# T9, T11, T12 recovery checkpoint (2026-10-03)

Worktree: `.claude/worktrees/clio-mcp-new-implementation-f9a6b6`. No commit or push.

Recovered `internal/creatio/datawrite.go` and `odatawrite.go` from Claude's
`agent-ab5ae331b441c49a6` sibling; original files preserved. Fixed embedded BOM literals,
missing sort helper, added MCP handlers and meaningful tests. Writes explicitly refuse
HTTP redirects as well as avoiding session-reauthentication replay: a 307 must not
silently repeat an insert.

## Implemented checkpoint

| Tool | Evidence | Remaining verification |
|---|---|---|
| odata-create | Mock POST payload, numeric returned Id, partial batch, stop-on-error, unknown side effect on 401, no replay on 307. MCP both executors and raw write gate. Three safe validation parity cases match. | Live own-entity insert/readback/cleanup after DNS recovery; file payload and advanced date/CSDL/foreign-key cases need wider parity. |
| odata-update | Mock $metadata unknown-field refusal, valid PATCH and success, confirmation gate. MCP both executors and raw gate. Three safe validation parity cases match. | Live own-entity update/readback; inherited CSDL, select-probe fallback, date offset cases. |
| odata-delete | Mock DELETE, explicit confirmation before remote call. MCP both executors and raw gate. Three safe validation parity cases match. | Live own-entity cleanup; failure/readback side-effect diagnosis. |
| create-client-unit-schema | Mock package/name lookups, exact ClientUnit save structure/culture, success and provider refusal. MCP both executors and raw gate. Invalid-name parity matches. | Prepared `scripts/write-scenarios/t9-client-unit.json`, plan validation passes; not executed. Profile-culture and script guard use T8 helpers. |
| update-client-unit-schema | Mock deterministic most-derived package layer, load/full-save preserving other metadata, UTF-16 body length. MCP both executors and raw gate. Empty-body parity matches. | Same prepared write scenario. Additional body-file and save failure parity. |
| set-record-rights | Mock grant/revoke payloads, invalid grantee refusal and non-JSON response refusal. MCP both executors and raw gate. Invalid operation/grantee parity match. | Live grant/revoke only on an own UsrParity record/schema, persisted readback; currently follows clio's tolerant valid-JSON ApplyChanges response contract. |

Read parity evidence (outside repository, no secrets):
- `/private/tmp/cmcp-pages-data/read-parity.json`: 9 match, 0 mismatches.
- `/private/tmp/cmcp-pages-data/page-rights-parity.json`: 4 match, 0 mismatches.

Focused tests pass with the race detector for this checkpoint. Full shared-repository
checks belong to the root agent after all owners finish editing. A single exploratory
case which submitted a valid row to nonexistent `UsrParityNonexistent` failed at DNS on
both servers and exposed different transport wording; it was replaced with a local
empty-row refusal in the validation suite. No remote creation was confirmed, and this
agent ran no write harness or ledger-producing run.

## Exact remaining scope

T9: see "T9 pages, second round" below.

T11 unimplemented: `execute-dataservice-batch`, `execute-sql-script`, `run-process`,
`create-data-binding`, `add-data-binding-row`, `remove-data-binding-row`,
`create-data-binding-db`, `upsert-data-binding-row-db`, `remove-data-binding-row-db`.

T12 unimplemented: `create-sys-setting`, `update-sys-setting`,
`download-sys-setting-file`, `set-fsm-mode`, `manage-user`, `manage-role`,
`manage-license`, `manage-access`, `create-server-to-server-oauth-app`,
`get-identity-assertion`, `get-identity-public-jwk`, `regenerate-identity-signing-key`.
The W6 narrative additionally mentions `create-oauth-technical-user`, which is outside
the captured T12 inventory and is not added here.

No stand-global operation is implemented or run as part of this checkpoint. Future
`set-fsm-mode` verification must stay in the dedicated window; changes to existing
settings/users/roles/licenses are forbidden by the Stage-3 brief. T9/T11/T12 are **not
complete**. The root agent requested a cohesive checkpoint after the stand remained
unreachable, rather than starting another large unverified area.

Known compatibility limits: the recovered OData backend follows master diagnostic
extensions while the baseline is clio 8.1.0.134; transport-error wording remains
unverified against a reachable stand. Some unbound/legacy aliases currently receive
strict Go decoding errors rather than master's rename-specific answer. Client-unit
creation uses the shared guarded designer helper on empty/non-JSON saves, so this
failure wording is more specific than the original creation command's raw JSON parse
exception; success/provider failure paths and tested local validation match.

## Live follow-up (2026-10-03)
Connectivity recovered. Own Contact OData create/update and ESQ read-back exactly
match the installed baseline; updated Notes was verified. OData DELETE is blocked
by IIS HTTP 405 on both servers. Authenticated read-back confirmed the records
remained before native DataService deletion, which succeeded for both. The new
`t11-odata.json` scenario uses exact-name lookup plus captured ID for baseline
DataService cleanup. This does not verify successful OData deletion on this stand.

## T9 pages, second round (2026-10-03)

All seven remaining T9 tools are served; `merge-creatio-artifact` without clio's semantic
resolver (see gaps). Code: `internal/creatio/pagewrite_*.go`, `cmd/creatio-mcp-go/tool_page_*.go`,
`tool_related_page_addon_write.go`, `tool_user_task_page.go`, `tool_merge_creatio_artifact.go`.

| Tool | Read cases (`scripts/parity-cases/t9.json`) | Live writes | Verdict |
|---|---|---|---|
| create-page | 5 match | `t9-pages.json`: two pages per side in an app the run creates (plain, and with entity, description, caption culture, optional properties) — match | done |
| update-page | 6 match | `t9-pages.json`: replace with resources, append, dry-run append projection, stale pinned checksum (conflict), and saves on the other server's `.clio-pages` baseline in both directions — known-diff only (checksums, modifiedOn text, body length by side name, Go's gap warning) | done, validation partial |
| sync-pages | 2 match | `t9-pages.json`: sync with verify (read-back body and meta written) — known-diff only | done, validation partial |
| create-related-page-addon | 10 match | window only: `scripts/write-scenarios/window/t9.json` (BuildConfiguration rebuilds static content for the whole stand) | done, not run live |
| create-user-task-page | 6 match | offline (workspace files): a golden test compares every file with clio 8.1.0.134's scaffold of the same workspace | done |
| merge-creatio-artifact | 19 cases: 2 match, 17 known-diff (resolver-version; one also the unported merge) | read-only | classification only |
| get-component-info | `t9-component-info.json`: 20 match, 6 known-diff (documentation cache source), 0 mismatch | read-only | done |

`t9-client-unit.json` now creates an app (through clio on both sides) and writes its helper into
that app's package instead of `Custom`; live run: every step matches.

Harness changes (shared, separate commits): a write/read-back step may set `server: clio` (both
sides' objects made through clio, used for create-app until this server has it) and
`retry-while`/`retry-seconds` (a second create-app is refused while Creatio rebuilds its OData
library).

`.clio-pages` interop: get-page now records the call's environment name (and direct URI) in the
baseline exactly as clio does — before, it recorded only the configured URL, so clio's
update-page never armed its conflict check from a baseline this server wrote. The live scenario
saves through each server on a baseline the other one captured, both directions, and a stale
pinned checksum is refused identically.

Not ported, with reasons:
- update-page / sync-pages content validation beyond the structural floor: clio's
  SchemaValidationService (5,900 lines), the Acornima AST linter, chart-widget and mobile
  component catalogs, run-process signature checks, inserted-widget caption gate,
  insert-downgrade and inert-operation warnings. The JavaScript syntax gate is the structural
  reader of `classicpagejs.go` (Acornima's line/column can differ). A validated save carries
  `PageWriteValidationGapWarning` naming what was not checked; failures do not.
- Designer Presence notification after update-page (best effort in clio; no answer change).
- merge-creatio-artifact semantic merge (clio's Creatio.ConflictResolver, ~9,500 lines): a shape
  clio merges is answered `not-implemented` here; `resolver-version` names this server.
- caption-culture validity is checked by shape (.NET checks the ICU culture list); transport
  failure wording of SelectQuery differs from clio's classified texts (as elsewhere).

Stand side effects: update-page and sync-pages call `WorkplaceService/ResetScriptCache` after a
save, as clio and the page designer do; the live scenario ran it (both servers). No compile,
restart or static-content rebuild was run. Ledger: every object of this agent's runs is removed
(two app creations refused during the OData rebuild never existed and were marked removed after
checking the stand).

## T9 verification after rebasing onto main (2026-10-03)

The page read cases returned 31 matches, 17 documented merge differences and no
unexpected mismatches. `get-component-info` returned 20 matches, six documentation
cache-source differences and no unexpected mismatches. The own-app `t9-pages.json`
scenario passed every create, read-back, update, sync, cross-server baseline and cleanup
step; its overall verdict is `known-diff` for the documented per-side checksums, times,
body lengths and validation warning. The own-app `t9-client-unit.json` scenario also
passed every write, read-back and cleanup step; its only differences are in the clio
`create-app` setup response for each side's own app. Both runs' ledger entries are all
removed (10 objects created, zero leftovers). The related-page add-on scenario remains
window-only because it calls `BuildConfiguration` for the whole stand.

Checks after rebase: `gofmt -l` empty, `go vet ./...`, `go test -race ./...`, Windows
build, 23 Python script tests, contract comparison (256 matches, zero unexplained),
and the public-safety script all pass. The semantic merge, full page content validation,
and best-effort Designer Presence notification remain the explicit gaps above.
