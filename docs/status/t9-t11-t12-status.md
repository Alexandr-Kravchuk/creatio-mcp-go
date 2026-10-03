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

T9 unimplemented: `create-page`, `update-page`, `sync-pages`,
`create-related-page-addon`, `create-user-task-page`; component-registry/request/merge
items mentioned in T6 remain unimplemented by this checkpoint.

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
