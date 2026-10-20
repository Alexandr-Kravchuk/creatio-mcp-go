# T22: live read parity against clio 8.1.0.142 — triage and fixes

Date: 2026-10-20. Reference: clio **8.1.0.142** (`clio.dll` from the 8.1.0.142 tool package, source in the local
8.1.0.142 checkout). Stand: the shared read stand used by `scripts/compare-mcp.py` (read cases only; no write
harness was run).

The "0 mismatches" recorded earlier in `docs/migration-plan.md` was measured against clio 8.1.0.134. It does not
carry over to 8.1.0.142: most of the mismatches below are 8.1.0.142 behaviour changes that the read tools had not
followed yet.

## Before and after

| Run | Binary | match | both-failed | error-text | known-diff | mismatch |
|---|---|---:|---:|---:|---:|---:|
| Orchestrator's live run (before) | `caa2eb0` | 795 | 30 | 1 | 37 | 20 |
| Live run after the fixes (once, sequential) | `ca0e5a1` | 815 | 27 | 0 | 41 | 0 |

Of the 21 differing cases, 17 now match and 4 are known differences (812 / 41 expected). Three more cases moved from both-failed to match: `get-guidance unknown name` and `get-guidance reference item` (both servers refuse; the refusal text now is clio's closest-name message) and `list-package-files unknown` (both refuse; ClioGate's failure text now is clio's). No case got worse.

## How each case was classified

1. A baseline binary was built from `fdc3aa4` (the first-wave merge) in a throwaway `git worktree` and the head
   binary from `caa2eb0`. Only the 21 differing cases (20 mismatch + 1 error-text) were rerun against both,
   through a filtered case file.
2. A case that matches on the baseline and differs on head is a regression (a). None did.
3. A case that differs on both was compared with the clio 8.1.0.142 source: a change there that this server did
   not follow is (b).
4. A case that matched on both binaries in the rerun is nondeterminism or stand data (c).

Targeted rerun results: baseline `fdc3aa4` — 1 match, 1 error-text, 19 mismatch; head `caa2eb0` — the same
1 / 1 / 19. After the fixes: 17 match, 4 known-diff, 0 mismatch, 0 error-text.

## Per-case classification

| Case | Class | Cause (clio 8.1.0.142 source) | Result |
|---|---|---|---|
| list-pages search | (c) stand data | Matched on both `fdc3aa4` and `caa2eb0` in the targeted rerun and again after the fixes; the first run likely saw the shared stand's pages change between the two calls | no change; matches |
| get-package-file source file (error-text) | (d) wording | `ShowPackageFileContentCommand.DeserializeResponse`: "ClioGate could not complete /rest/…GetPackageFileContent. … and retry." (route only, leading `/`, trailing period); project error quotes the file with `'…'` | fixed; matches |
| compile-status echoes an unknown environment | (b) | `CompileStatusTool.BuildNotFound` (ENG-102333): new note, `compilation-history-error` for an environment-name that does not resolve | fixed; matches |
| compile-status latest | (b) | Same: a not-found answer reads the environment's `CompilationHistory` (`CompilationHistoryReader`) and lists `checked-utc`, `compilation-history`, `older-failures` | fixed; known-diff (time fields) |
| compile-status unknown operation-id | (b) | Same, plus `NoRecordSentence` ("the operation-id you passed" for a non-GUID id) | fixed; known-diff (time fields) |
| restart-status latest | (b) | `RestartStatusTool.NotFoundNote`: new wording | fixed; matches |
| find-app pattern | (b) | `FindAppCommand.LoadSectionsBatch`: one IN filter on `ApplicationId`; the virtual `ApplicationSection` schema reads only the first filter of an OR group, so the OR query returned one application's sections | fixed; matches |
| find-app all | (b) | Same | fixed; matches |
| find-app alias name | (b) | Same | fixed; matches |
| find-app, wrapped args | (b) | Same | fixed; matches |
| target package current setting | (b) | `PackageTargetResolver.ResolveCurrentPackage`: `CurrentPackageId` holds a package **UId**, so `SysPackage` is filtered on `UId`, not `Id`; refusals end with "Ask the user which package should receive the change…" | fixed; matches |
| page Contacts_FormPage | (b) | `PageGetTool` (ENG-103023): `include-operations` defaults to false, the answer carries `viewConfigDiffOpCounts`; meta.json keeps the list | fixed; matches |
| page Accounts_FormPage | (b) | Same | fixed; matches |
| page with editable schema | (b) | Same; `$.editable.modifiedon` stays the recorded known difference | fixed; known-diff (pre-existing) |
| page mobile | (b) | Same (empty `viewConfigDiffOpCounts: {}`) | fixed; matches |
| get-page Classic page with trailing text after a section | (b) | Same | fixed; matches |
| get-client-unit-schema top layer | (b) | `SchemaDesignerHelper.ExtractCaption`: of several localized captions, the one in the user's profile culture, then en-US, then the first (this server took the first) | fixed; matches |
| get-client-unit-schema full hierarchy | (b) | Same | fixed; matches |
| validate-page comment inside a JSON section | (b) | `SchemaValidationService.TryParseJsonDocument`: `JsonCommentHandling.Skip` and trailing commas | fixed; known-diff (the case's recorded `warnings` difference) |
| initialize instructions and capabilities | (b) | `McpServerInstructions`: "name=list returns the guide catalog." replaces "An unknown name returns availableGuides." | fixed; matches |
| get-guidance missing name | (b) | `GuidanceGetTool`: a missing name, or `list`/`index`/`catalog`/`help` that names no guide, answers `catalog` (one `name: purpose` line per guide, cut to fit 13 000 bytes); an unknown name gets up to five closest names; `availableGuides` is no longer sent | fixed; matches (catalog text byte-identical) |

No case was (a): nothing regressed after `fdc3aa4`.

## What changed

| Area | Files |
|---|---|
| compile-status history read, notes, 40 s bound | `internal/creatio/compilehistory.go`, `cmd/creatio-mcp-go/tool_operation_status.go` |
| restart-status note | `cmd/creatio-mcp-go/tool_operation_status.go` |
| get-page `include-operations` default | `cmd/creatio-mcp-go/tool_get_page.go` (update-page keeps passing its own value through, as clio does) |
| find-app IN filter, section failure fails the call | `internal/creatio/findapp.go` |
| get-target-package UId filter, refusal text | `internal/creatio/targetpackage.go` |
| profile-culture caption (get-client-unit-schema, get-schema, get-sql-schema, get-classic-page-sources) | `internal/creatio/sqlschema.go` (`schemaCaption`), callers in `schemaget.go`, `classicpagesources.go` |
| validate-page JSON comments | `internal/creatio/pagevalidate.go` (`pageValidateCleanJSON`, also used for `resources`, which clio parses the same way) |
| get-guidance catalog, suggestions; instructions | `cmd/creatio-mcp-go/tool_get_guidance.go`, `knowledge_instructions.go`, `internal/knowledge/runtime.go` (`CatalogByName`) |
| get-package-file failure text | `internal/creatio/service.go`, `internal/creatio/packagefiles.go` |
| compare harness: `[*]` in `known-differences` | `scripts/compare-mcp.py`, `scripts/test_compare_mcp.py`, `scripts/parity-cases/group-f.json` |

The two compile-status cases list `$.checkedutc` and `$.compilationhistory[*]` / `$.olderfailures[*]`
`.finishedsecondsago` as known differences: each server stamps `checked-utc` with its own clock when it reads, so
those values differ by the time between the two calls. Every other field of the history matches.

## Orchestrator follow-ups (same branch)

- `get-sql-schema` accepts `output-file` (clio 8.1.0.142's `SqlSchemaGetArgs : SchemaGetBaseArgs`): the path is
  confined before any request (workspace or OS temp directory, no overwrite), the body is written atomically and
  left out of the answer. `internal/creatio/sqlschema.go` (`GetSQLSchemaTo`), `cmd/creatio-mcp-go/main.go`.
  The docs that still say get-sql-schema has no `output-file` (README, install.md, implementation.md, release
  notes) are not edited here; the orchestrator updates them after the merge.
- `main.go`'s hand-written `odata-read` contract (with the old `orderBy`) was dead: `get-tool-contract` answers
  from clio 8.1.0.142's inventory, which carries every built-in hidden tool. The entry is now the name only;
  `TestHiddenToolContractsAreServedFromClioInventory` fails if any built-in hidden tool loses its clio contract.

## Tests (fixtures only, no stand)

- `internal/creatio/compilehistory_test.go`: session-offset conversion, query order, error fencing, file-name cut,
  ten listed rows plus five older failures, unreadable answers (no offset, fractional offset, failed query, row
  without time, zoned time).
- `cmd/creatio-mcp-go/tool_operation_status_test.go`: history listed, unreadable, unresolved environment, read
  abandoned past its budget, restart note.
- `internal/creatio/groupa_test.go`: find-app IN filter and failure.
- `internal/creatio/targetpackage_test.go`: UId filter and ask-for-package text.
- `internal/creatio/schemacaption_test.go`: culture choice and when the profile culture is asked.
- `internal/creatio/grouph_test.go`: commented JSON sections pass, an unterminated comment fails.
- `cmd/creatio-mcp-go/tool_get_guidance_test.go`, `tool_group_c_test.go`: catalog helpers, missing-name refusal,
  instructions, include-operations default.
- `internal/creatio/advanced_read_test.go`, `cmd/creatio-mcp-go/main_test.go`: ClioGate failure text,
  get-sql-schema `output-file`, hidden contracts served from the inventory.

## Checks

`gofmt -l .` empty; `go vet ./...`; `go test -race ./...`; `GOOS=windows go build ./...`;
`python3 -m pytest scripts -q` (32 passed); `scripts/check-public-safety.sh`;
`compare-contracts.py` against 8.1.0.142: `{"match": 1025, "known:not-ported": 70, "known:fit-multi-name": 1}`,
0 unexplained.
