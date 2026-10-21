# T23: first full live write parity run against clio 8.1.0.142

Date: 2026-10-20 … 2026-10-21. Stand: the disposable write stand `s16123120` (decision D1, listed in
`~/.config/creatio-mcp-go/write-stands`; the owner approved the run). Reference: clio 8.1.0.142 (`clio.dll` from a
`--tool-path` install). This server: built from this branch after each fix. No other session wrote to the stand.

Evidence: `evidence/t23/*.json` (one file per run listed below; `evidence/mcp-writes-latest.json` is the final
top-level run and is not tracked). They carry verdicts, timings and differing JSON paths only — no values, URLs,
stand or object names; `scripts/check-public-safety.sh` passes.

## Result

Every scenario file ran. In its final run no scenario ends in `mismatch` except the package compile of
`window/t15.json`, which is the order of the two sides on one stand, not a server difference (see "What is left").
Nothing of the runs is left on the stand: the ledger is empty, and a read of `SysSchema`, `SysPackage`,
`SysInstalledApp`, `Lookup`, `SysAdminUnit`, `SysSettings`, `Contact`, `VwSysProcess` for `UsrParity*` / `Parity*`
returns no rows.

`both-failed` means both servers gave the same refusal; it is not a tested write. `known-diff` means every
difference is a listed path with a `why` (per-side GUIDs, timings, the order two sides act on one stand-wide
setting).

### Top-level scenarios (`evidence/t23/top-level.json`, one run of all files)

| File | match | known-diff | both-failed | mismatch | skipped |
|---|---:|---:|---:|---:|---:|
| example-sys-setting.json | 2 | | 1 | | |
| t11-odata.json | 1 | | | | |
| t11.json | 2 | 1 | | | |
| t12-admin.json | | 2 | | | |
| t12-oauth.json | | | 1 | | |
| t14.json | | 1 | | | |
| t19-rights.json | | 1 | | | |
| t7.json | | 1 | | | |
| t8-schema-writes.json | | 2 | | | |
| t9-client-unit.json | | 1 | | | |
| t9-pages.json | | 1 | | | |

### Window scenarios (one file at a time)

| File | Evidence | match | known-diff | both-failed | mismatch |
|---|---|---:|---:|---:|---:|
| window/t7.json | window-t7.json | | 1 | | |
| window/t8.json | window-t8.json | | 3 | | |
| window/t9.json | window-t9.json | | 1 | | |
| window/t10.json | window-t10.json | | 3 | | |
| window/t11.json | window-t11.json | | 1 | 1 | |
| window/t12.json | window-t12-all.json + window-t12-fsm-rerun.json | 1 | 2 | | |
| window/t13.json | window-t13-install.json + window-t13-process.json | | 2 | | |
| window/t14.json | window-t14.json | | 2 | | |
| window/t15.json | window-t15-all.json + window-t15-compile-rerun.json + window-t15-package-restart-rerun.json | 2 | 3 | 1 | 1 (see below) |

For t12, t13 and t15 the scenarios that failed were rerun alone after the fix; the table counts each scenario's last
run. t12's FSM scenario: `window-t12-fsm-rerun.json`; the other two: `window-t12-all.json`. t13: install from
`window-t13-install.json`, the process chain from `window-t13-process.json`. t15: install-gate, dashboards migrator,
Data Forge and pkg-to-* from `window-t15-all.json`; the full compile from `window-t15-compile-rerun.json`; the
package compile and both restarts from `window-t15-package-restart-rerun.json`.

## What was fixed

### (a) regressions in this server

| Fix | Commit | Test |
|---|---|---|
| `delete-schema` refused on the stand: `GetWorkspaceItems` answers more than the 4 MiB default response limit; now read with the 128 MiB bound the section delete uses | b594913 | `TestSchemaDeleteReadsWorkspaceItemsLargerThanDefaultLimit` |
| Package compile polling sent the `CompilationHistory` date filter unquoted; DataService answers HTTP 500 (`ArgumentNullException`, reproduced with `execute-esq`), so every polling round failed and the compile was reported unmonitored | f6f4b66 | `TestPkgWriteHistoryQueryQuotesTheDate` |
| `set-fsm-mode` left out the empty line clio's `LoadPackagesTo*Command.Load` writes after the load | 29b1b62 | `adminwrite_fsm_test.go` (three cases) |

### (b) clio 8.1.0.142 changes that were not ported

| Fix | Commit | Test |
|---|---|---|
| `create-page` with `entity-schema-name` binds a data source to the entity and reports `dataSourceName` / `primaryDataSourceName` (GH-1839) | aa783cb | `pagewrite_142_test.go` |
| `update-page` append: `appendProjection` operation counts, refusals of an empty fragment and of a section without markers, an empty stored body (GH-1839) | aa783cb | `pagewrite_142_test.go`, `pagewrite_test.go` |
| `update-page` and `sync-pages` return the write digest (ENG-103023) | aa783cb | `pagewrite_142_test.go` |
| `create-business-process`, `modify-business-process`, `modify-business-process-as-new-version`, `set-active-business-process-version` return the process write digest; CrtProcessBuilder floor 1.6.6.103 and the new install wording (ENG-103023, ENG-102116) | 611dbd1 | `processwrite_digest_test.go` and updated process tests |

### Rig (the comparison was not of the same input)

- `MCPGO_CLIO_TOOLS_DIR` (d16f40d): this server read the CrtProcessBuilder, cliogate and dashboards-migrator archives
  of the newest clio in `~/.dotnet/tools` (8.1.0.134) and refused the reinstall as a downgrade. The harnesses now
  pass the tool path of the compared clio. Tested in Go and in the harness.

### (d) scenario and harness defects

- Stale `go-tool-missing` keys on `t8-schema-writes.json`, `window/t8.json`, `window/t10.json` (create-app,
  create-package, create-entity-schema and create-page are ported); `window/t10.json` takes its package from an
  application so `delete-app` removes it (000c0d2).
- Missing `retry-while` on an application write after another scenario's OData rebuild; per-side package,
  application, page and schema ids not captured; captions that carried the side name (the harness neutralizes only
  object names) (a9f83d5, a113a78, 9f14e13, 7ccd9c6).
- `get-schema` / `get-sql-schema` (write-capable in 8.1.0.142) and `get-fsm-mode` / `get-identity-public-jwk` (flat
  arguments in clio) are read through `clio-run` (a9f83d5, febb648).
- The T19 lookup's `Lookups` row is deleted in cleanup — neither `delete-schema` nor `delete-app` removes it; two
  such rows of the first runs were removed by hand (6463b00).
- 8.1.0.142 refusals of old descriptors: formula elements need a body and a result target, element types
  `start`/`terminate` and parameter direction `Input` are gone (566217b, b8697d6).
- Timing: settle before each side's CrtProcessBuilder install and full compile, 120 s before the second side's FSM
  `off` (the first side's toggle recycles the application pool) (566217b, 8d90227, dd41ed9).
- Harness: per-call publish durations are masked; an `expect` matches free text with the run id; a known
  difference covers the paths under it; a read-back marked `right-after-write` runs on each side directly after
  that side's write, for `compile-status` / `restart-status`, whose record lives 5 idle minutes (9f14e13, b7d21e8,
  d71c0f0, dd41ed9). All tested in `scripts/test_compare_mcp*.py`; documented in `docs/parity.md`.
- Two whole-log known differences hid a real difference (the FSM empty line); `set-fsm-mode on` now lists only the
  line that the order of the two sides changes (b813475).

### (c) recorded as known, with the reason in the scenario

Per-side GUIDs the stand creates (pages, entities, binding schemas, own columns, process schemas, record-rights
process ids, Lookups rows), `modifiedOn`, operation ids and start/finish times, temporary directories and the
chunking of streamed installation logs, and the second side finding a stand-wide setting already switched
(`set-fsm-mode`). Process write logs were compared by hand with every GUID and `modifiedOn` masked: identical.

## What is left, with the reason

| Scenario | Verdict | Reason |
|---|---|---|
| window/t15 package compile | mismatch (status `failed` on Go) | Order on one stand: the first side's `RebuildPackage` rebuilds every changed package, including the second side's new one, so the second compile writes no `CompilationHistory` and ends with "wrote no compilation history within 600 s". clio gives the same answer for a package with nothing new (checked: `compile-creatio Custom` through clio, 2026-10-21). Not a server difference; the scenario would need each side's application created right before its own compile. |
| example-sys-setting integer "refused update" | both-failed | By design: both refuse the non-integer value identically. |
| t12-oauth | both-failed | The stand has no Identity Service URL; `AddClient` returns no credentials on both. |
| t19 entity connections | both-failed (steps) | The stand answers "EntityConnections is not registered" on both. |
| window/t11 execute-sql-script | both-failed | The stand denies `CustomQuery.ExecuteReader` by its security settings. |
| window/t15 pkg-to-file-system / pkg-to-db | both-failed | The stand is not in file design mode; both refuse identically. |

Known differences that are server differences, not stand values:

- `update-page` / `sync-pages` warnings: this server adds `PageWriteValidationGapWarning` (it runs a declared subset
  of clio's page validation; see `clio-comparison-2026-10-20.md`).
- `newModifiedOn` / `conflictDetails.modifiedOn`: clio formats the date through .NET in the host culture.

Not ported, found while porting the digests (no live scenario reaches them): the process-parameter mapping warnings
of create/modify (ENG-103081) and the refusal of keys the server does not declare (ENG-103085).

Hypothesis, not reproduced: `set-fsm-mode off` probes `GetApplicationInfo` with this server's 45 s client timeout;
sent right after the other side's toggle it timed out while the application pool recycled. clio's timeout for that
call is the Creatio client default, which was not established.

## Not verified live

- `modify-business-process`, `-as-new-version` and `set-active-business-process-version` digests: verified in the
  final t13 run (masked GUIDs), but only on the simple Start → Formula → End chain.
- The OAuth server-to-server success path, `execute-sql-script` results, entity connections, FSM `on` / `off`
  with packages actually exported and imported in both directions by this server first (it always ran second).
- `install-application` of a real application archive (the scenario installs an empty package folder).

## Left on the stand

No `UsrParity*` object of these runs. Stand-wide state the window scenarios changed and did not restore:
CrtProcessBuilder 1.6.6.104 installed (t13), cliogate and CrtDashboardsMigratorApp reinstalled (t15), the logo and
background-image settings set to the fixture image (t14), the identity signing key regenerated (t12), two full
compilations and three restarts (t15). File system mode is off, as before the run.
