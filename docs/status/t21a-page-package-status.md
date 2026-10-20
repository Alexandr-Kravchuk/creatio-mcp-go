# T21a: pages, packages and smoke checks (clio 8.1.0.142)

Six tools clio 8.1.0.142 added. Four are ported from the 8.1.0.142 source; two are not, with the reason below.

| Tool | Status | Go | Annotations (clio's `[McpServerTool]`) |
|---|---|---|---|
| `create-package` | ported | `tool_create_package.go`, `internal/creatio/createpackage.go` | destructive, not idempotent |
| `place-section-in-workplace` | ported | `tool_place_section_in_workplace.go`, `internal/creatio/workplace.go` | destructive, idempotent |
| `localize-page` | ported | `tool_localize_page.go`, `internal/creatio/localizepage.go` | destructive, idempotent |
| `trigger-smoke` | ported | `tool_trigger_smoke.go`, `internal/creatio/triggersmoke.go` | destructive, not idempotent |
| `page-smoke` | not ported | — | — |
| `get-mobile-page-conversion-guide` | not ported | — | — |

Shared argument code: `cmd/creatio-mcp-go/tool_t21a_args.go` (clio's binder refusals, `BuildLegacyAliasError` with each
tool's own alias map and comparer, the typed-response environment resolution, and the answer text). Contracts come from
`docs/clio-inventory.json` (already at 8.1.0.142); the `tasks` map labels the four ported tools as served and the two
others as not ported.

## What is ported

- **create-package** (`CreatePackageTool`, `CreatePackageCommand`, `RemotePackageCreator`): binder refusals, the Ordinal
  alias map, the required checks, then the environment. The `SchemaNamePrefix` is prepended unless the name already
  starts with it (ignoring case); the taken name, every missing dependency and an unknown application are refused before
  anything is written; the create (`PackageService.svc/CreatePackage`, or `ApplicationPackagesService.svc/CreatePackageInApp`
  with `application-code`) is sent once, never replayed after a re-login; a transport failure is followed by the
  existence check by the UId sent (`package-created` false or null); dependencies are saved through the existing
  `add-package-dependency` code; the read-back reports maintainer, description, dependencies, install type and editable.
- **place-section-in-workplace** (`PlaceSectionInWorkplaceTool`, `WorkplacePlacementService`, `WorkplaceRepository`,
  clio 8.1.0.142's `PackageTargetResolver` with `requireEditable`): every input is resolved before the first write; mode
  `none` / `existing` / `new`; My applications found by name or through the `SysWorkplace_MyApps` binding; the move
  refusals (no role sees the target, another package ships the moved row); a workplace another package ships is used
  but not bound, and a position change for it is refused; workplace, grants and placement are written through the
  existing DB-first binding code (`create-data-binding-db` / `upsert-data-binding-row-db`), read back, and the navigation
  cache is reset. A failure after the first write answers `writes-started` and `written`.
- **localize-page** (`LocalizePageTool`, `LocalizePageCommand`): the default-culture refusal, `resources` parsed as
  Newtonsoft's `Dictionary<string,string>` (numbers and booleans become strings, null and blank values refused), the
  unstorable-character checks, the culture resolved against `SysCulture` (inactive culture: warning), the editable schema
  resolved as the page designer does (never a replacing schema), unknown keys refused with the available ones and the
  data-source-bound hint, inherited keys written as a page override holding only the target culture, the caption,
  coverage (`missing`, `sameAsDefault`, `captionSameAsDefault`, `captionInherited`), the save, the script cache reset, the
  `.clio-pages` baseline refresh (`RefreshAfterSave`: unproven, stale, dropped, refreshed) and the read-back mismatch.
- **trigger-smoke** (`TriggerSmokeTool`, `TriggerSmokeRunner`) on top of the run verification T20 ported for
  `run-process` (watcher, monitor, provenance, cleaner): SysProcessLog baseline first, the OData create sent once, the
  wait (minimum 6 s, 3 s quiet, parked runs), attribution to the caller and to `process` (code, caption or the root of a
  sub-process), the verdict, and the cleanup of only what is tied to the trigger, cancelling a running run that holds
  one of those records.

## Verification

Measured against `dotnet clio.dll mcp-server` of clio 8.1.0.142, no Creatio stand.

### create-package

- No stand (`scripts/fake-stands/t21a/validation-cases.json`, environment on a closed port and a Safe one): 11 cases,
  10 match, 1 both-failed (unknown-argument order, below). Missing environment-name, direct raw-name call and the Safe
  refusal were compared by hand with the same texts; they are in `tool_t21a_test.go`.
- Fake stand (`scripts/fake-stands/t21a/fake_creatio.py`, `cases.json`): 18 cases — prefix added and kept, duplicate (two
  spellings), dependencies (duplicates, blanks, null), missing dependencies, in-application create, unknown application,
  server refusal with and without details, failed dependency save, failed read-back, null description, no install type,
  blank application code, a connection dropped after and before the store, an existing package. 8 match, 10 differ only in
  `package-uid` (a fresh GUID on each side). Requests identical on every route (GUIDs masked) except the body key casing
  of `QuerySysSettings` (below).
- Unit tests: `internal/creatio/createpackage_test.go`.

### place-section-in-workplace

- No stand: 21 cases, 20 match, 1 both-failed (unknown-argument order).
- Fake stand: 21 cases — mode none, unknown and ambiguous section, unknown and locked package, unknown, ambiguous and
  unknown-id role, mode existing/new refusals, the move refusals, a foreign workplace with and without position, create
  and move, idempotent re-run, an existing unbound workplace into the CurrentPackageId package, mobile client type, two
  My applications placements, and a failure after the first write. 13 match, 8 differ only in ids generated on each side
  (`workplace.id`, `grant-id`, `placement-id`, `written[].id`). Requests identical on every route (GUIDs masked),
  `GetIsFileDesignMode` once per call included, except the body key casing of `QuerySysSettings` (below).
- Unit tests: `internal/creatio/workplace_test.go`.

### localize-page

- No stand: 23 cases, 22 match, 1 both-failed (malformed `resources` JSON wording, below).
- Fake stand: 23 cases — report-only, inactive and absent culture, missing page, no editable schema, hierarchy refused,
  unknown keys with the data-source hint, own and inherited keys with caption, idempotent re-run, a value equal to the
  inherited one, coverage after a write, numbers and booleans, a replacing schema, save refused, save not stored, save
  connection dropped, inherited caption, a page without localizableStrings, trailing comma, root string/number, array
  value, duplicate keys. All 23 match. Requests on all 7 routes identical (new entry uIds masked).
- Unit tests: `internal/creatio/localizepage_test.go` (parsing, unstorable characters, save with baseline refresh,
  stale baseline left unchanged, inherited caption). The baseline refresh is covered by these tests only: the fake-stand
  run has no `.clio-pages` workspace.

### trigger-smoke

- No stand: 25 cases, all match (every validation branch, wrong types, alias, unknown and Safe environment, closed port).
- Fake stand: 14 cases — a run that completes and is cleaned up, a failed run, no run, only another user's run, a parked
  run cancelled before the delete, a record with no lookup to the trigger (skipped), `process` by code and by caption with
  a sub-process, a scheduled run of the same login and another user's run, no `process`, cleanup off, create refused,
  create without Id, the trigger entity watched, an unknown watched entity. All 14 match; requests identical on every
  route (OData create and deletes, CancelExecution, SelectQuery, RuntimeEntitySchemaRequest).
- Unit tests: `internal/creatio/triggersmoke_test.go`.

### All tools

- `tool_t21a_test.go`: annotations, contracts, raw-name gate, 35 refusal texts as clio answers them.
- `scripts/compare-contracts.py` against 8.1.0.142: 1000 match, 80 known not-ported rows (`page-smoke` and
  `get-mobile-page-conversion-guide` among them, 2 rows each), 1 known fit-multi-name, 0 unexplained. Each ported tool
  matches on all 5 of its rows.

## Known differences

- Unknown argument names are listed sorted; clio lists them in request order (Go maps keep no order). Shared behaviour.
- A call with no `environment-name` (localize-page also without `uri`): clio refuses; this server uses its default
  target. Shared behaviour. create-package, place-section-in-workplace and trigger-smoke refuse a blank name themselves,
  as clio does.
- localize-page: malformed `resources` JSON is refused with the Go decoder's wording after clio's prefix; clio quotes
  Newtonsoft. Root arrays and scalars, object/array values, nulls and blanks use Newtonsoft's texts. A lone surrogate
  half arrives decoded as U+FFFD and is not refused.
- The answer text of these four tools is written in System.Text.Json's default spelling (`'`, `→`); clio writes
  it relaxed. The JSON values are the same. clio redacts a failed answer in the default spelling (a `d:'POST'` in the
  navigation note becomes a path), so this keeps the redaction identical; `failure_redaction.go` does not re-spell for
  other tools (open question 1).
- trigger-smoke's create body lists the values sorted and re-encodes numbers (`1.50` is sent as `1.5`); clio keeps the
  caller's order and literals. When several `values` keys are not plain names, the first sorted one is named.
- `QuerySysSettings` is sent with `sysSettingsNameCollection`; clio's ATF provider writes `SysSettingsNameCollection`.
  Creatio reads both; the request is the existing shared code of get-schema-name-prefix.
- Transport failures other than a refused or dropped connection keep Go's error text.

## Not ported

- **page-smoke**: opens a Freedom UI page in a headless Chromium and drives it over the DevTools protocol (pipe
  transport on Unix and Windows), with browser discovery, a pinned headless-shell download with resume, a consent
  question remembered in settings, the Shell readiness scripts and click handling — about 4,000 lines in
  `Common/BrowserSession` plus 1,300 in `PageSmokeRunner`, `FreedomUiPageScripts` and `PageEventCollector`. Registering
  only the argument checks would answer without ever running a check, so the tool stays unregistered.
- **get-mobile-page-conversion-guide**: the guide is computed by clio's web-to-mobile analysis engine
  (`WebToMobileAnalysisService` 7,400 lines, models, rules catalog and probes — about 15,800 lines). Nothing short of the
  engine produces its answer.

## Changes to shared code

- `internal/creatio/pageget.go`: hierarchy layers also keep `caption` (localize-page reads ancestor titles).
- `internal/creatio/datawrite_process_verify.go`: `resolveSeeded` (provenance with seed ids; `resolve` calls it), and the
  fenced error text names a refused connection as clio does ("Connection refused (host:port)") — run-process's watch
  warnings now read the same way.
- `docs/clio-inventory.json`: six lines of the `tasks` map. `docs/clio-inventory.md` regenerates from
  `scripts/clio-inventory.py` and was not edited by hand.

## How to rerun

```
python3 scripts/fake-stands/t21a/fake_creatio.py serve 18921 <dir> &
python3 scripts/compare-mcp.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> --clio-env t21afake \
    --clio-settings <dir>/appsettings.json --cases scripts/fake-stands/t21a/cases.json
python3 scripts/fake-stands/t21a/fake_creatio.py compare <dir>/requests.jsonl
```

`validation-cases.json` needs no fake: a settings file that registers `dummy` at `http://127.0.0.1:9` and `dummysafe`
(the same, `Safe: true`), then `--clio-env dummy --cases scripts/fake-stands/t21a/validation-cases.json`.

## Open questions

1. clio redacts every failed hidden-tool answer in System.Text.Json's default spelling and writes it back relaxed
   (`ClioRunTool.RedactSerializedText`). Porting that into `failure_redaction.go` would align every tool; T21a only
   spells its own answers that way.
2. page-smoke needs a decision: drive Chromium from Go (CDP over pipes, the headless-shell download and the consent
   store), or leave it to clio.
