# Migration plan: replacing clio's MCP server with creatio-mcp-go

Goal: an agent (Claude Code, Codex, the CAADT skills) does every Creatio task it does today through
`clio mcp-server`, but through this server, with the same tool names, arguments and answers.

Measured on 2026-10-03 against clio master `914dab286`:

| | clio | this server |
|---|---|---|
| MCP tools | 219 in clio 8.1.0.134 (218 in the get-tool-contract index plus `get-tool-contract`), 19 resident; exact list in [clio-inventory.md](clio-inventory.md) (T5) | see the appendix; clio's resident tools that are served here are listed with clio's contracts (T5) |
| MCP prompts and resources | 74 prompts, 170 resources, 5 resource templates (clio 8.1.0.134 with its installed knowledge bundles) | 74 prompts, 5 resource templates, the knowledge catalog (T6) |
| Environments per process | any, per call (`environment-name`) | any, per call, from clio's `appsettings.json` (T1); `CREATIO_*` as the default |
| Live parity | — | 302 calls, 0 data differences (clio 8.1.0.134); after T1 302 cases per stand, and 632 calls with both stands served by one process (incl. 14 environment cases), 0 mismatches |

## What "done" means

1. Every clio MCP tool, prompt and resource the CAADT skills and the clio guidance reference exists here
   with the same name, input schema and answer shape, or is deliberately dropped with a reason recorded in
   this file.
2. `scripts/compare-mcp.py` covers every tool, including writes, on a disposable stand, with 0 mismatches.
3. The CAADT plugin and the clio skills run against this server unchanged.
4. Releases are built, signed and published by a repeatable process, and installed the way testers install
   clio today.

## Workstreams

### W1. Multiple environments per process (foundation)
clio resolves `environment-name` per call against its `appsettings.json`; every tool here rejects it.
Read clio's settings file (same locations, same keys, forms and OAuth), keep one authenticated client per
environment, make `environment-name` accepted where clio accepts it, keep `CREATIO_*` as the default
environment when no name is given. Add `list-environments` (read-only) since it is the entry point agents
use. Touches every tool's argument handling, so it goes first and alone.

**Done (T1, 2026-10-03).** Every tool takes `environment-name` (`get-fsm-mode`: `environmentName`), the
tools whose clio counterpart takes `uri`/`login`/`password` (and `describe-environment` the OAuth trio)
build a one-off connection the way clio's `EnvironmentSettings.Fill` does, and each tool reports an
unknown name in its own clio envelope with clio's text. `list-environments` matches clio byte for byte.
Live parity with `--go-env-mode=name` (no `CREATIO_*`): on-premises forms stand 302 cases, cloud OAuth
stand 302 cases, and one Go process serving both stands in alternation 632 calls — 0 mismatches each;
the remaining known differences are `validate-page` warnings and one page's `modifiedOn`. Left as they
are, on purpose or for later tasks:
- a call without `environment-name` goes to `CREATIO_*`, then clio's active environment; clio refuses it;
- `get-fsm-mode` also honours `environment-name`, so a caller using the common spelling is never served
  by another environment (clio ignores that key and then refuses for the missing `environmentName`);
- tools that clio checks for a missing `environment-name` ("environment-name is required...") use the
  default target instead; `compile-status`/`restart-status` without a name answer not-found;
- order of checks: where clio validates its own arguments before resolving (e.g. `get-app-info`'s "exactly
  one identifier", the inspect tools' `action`), a call with both problems reports the environment first;
- unknown argument keys are listed sorted, clio lists them in request order (Go maps keep none);
- `start-creatio` still reads the settings through `internal/hosttools`, its own reader (T16);
- the not-found text advises `clio-run reg-web-app`, which this server does not offer yet.

### W2. Shared infrastructure for the remaining tools
- Error-text redaction like clio's `SensitiveErrorTextRedactor`, applied in one place to every failure.
- One type for clio's command envelope (`exit-code` + `execution-log-messages`); three copies exist now.
- Write safety like clio: destructive tools behind the same confirmation contract (`clio-run-destructive`,
  tool annotations), so an agent cannot delete by accident.
- Long-running operations: progress notifications, polling and the operation registry that
  `compile-status` / `restart-status` read, so those two return real states.
- One HTTP helper for `rest/...` service routes (today some tools call internal helpers directly).

**Done (T4, 2026-10-03).** How to use all of it: [writing-tools.md](writing-tools.md).
- Redaction: `internal/redact` ports `SensitiveErrorTextRedactor.Redact` rule for rule (regexp2, the .NET
  engine, for its lookbehinds and backreferences); 1072 input/output pairs produced by clio's own redactor
  match byte for byte. Applied where clio applies it: every raised failure, and every failed result
  (`isError`, `success:false` or a top-level `error` string) of a call through the executors or of a direct
  call to a tool clio does not list, clio's `RedactFailureContent`; a tool clio lists answers a direct call
  with only its own redaction. The
  command envelope stays unredacted, as in clio (measured: clio-run returns `[EnvironmentResolutionException]
  ... "password":"<password>"` as is). The theming tools' partial redactor is gone.
- `creatio.CommandResult` / `LogMessage` replace the three envelope copies; output unchanged.
- Write safety, measured against clio 8.1.0.134: `clio-run` and `clio-run-destructive` are one executor and
  **both run any tool** — clio no longer refuses a destructive tool through `clio-run` — recording
  `_meta["clio-run"] = {dispatchedTool, destructive}`. The refusal clio does have is for a direct call by raw
  name to a hidden tool that is not read-only: `confirmation-required`, pointing at `clio-run`; reproduced.
  Hints come from clio's inventory (resident tools, index `destructive`); `readOnly` of a hidden tool, which
  clio does not publish, from `registerTool(..., withAnnotations(...))`. Parity cases for such tools run
  through `clio-run` on both servers.
- Operations: `runLongOperation` (progress per stage plus a 15 s heartbeat, clio's 150 s response deadline
  with the work continuing, cancellation), and the operation registry `compile-status` / `restart-status`
  read (clio's states, fields, 5 min / 50 records retention, per-environment scoping).
- HTTP: `callService` / `serviceRequest` for `rest/`, `ServiceModel/`, `DataService/` routes; the direct
  callers (record rights, user tasks, inspect, process describe, theming), Data Forge and ClioGate use it.
- `scripts/compare-mcp-writes.py` takes `--go-env-mode` (default `name`).
- Live parity after T4 (rebased on T5/T6): 706 calls on both stands served by one Go process, 657 match, 22 both-failed, 27 known-diff, 0 mismatches (an earlier run had one `dataforge-context` score differing on the cloud stand, where scores vary between calls; alone it matched 3 of 3); before the rebase 632 calls, 0 mismatches; contract comparison 160 match, 0 unexplained.

### W3. Parity harness for writes
Writes cannot be compared by calling both servers with the same arguments. Each case creates its object
under a per-server name (`{side}`), reads it back through both servers, compares the read-backs, then
deletes it. A run against a non-disposable stand is refused. Without this, write tools cannot be verified.
Done (T2): `scripts/compare-mcp-writes.py`, scenarios in `scripts/write-scenarios/`, usage and format in
[parity.md](parity.md).

### W4. Tool contract and resident list
`tools/list`, `get-tool-contract` descriptions and input schemas must match clio, because agents call
`get-tool-contract` before acting. Generate a diff from clio's live answers and close it.

**Done (T5, 2026-10-03), against clio 8.1.0.134.** `scripts/clio-inventory.py` records clio's live
tools/list, prompts, resources, resource templates, get-tool-contract index and every tool's contract in
`docs/clio-inventory.json` (summary: [clio-inventory.md](clio-inventory.md), which also lists how a build of
clio master `914dab286` differs). `go generate ./internal/cliocontract` embeds the tool part; tools/list and
get-tool-contract answer from it, so every served tool carries clio's description, input schema,
annotations, index entry and contract (how it works: [implementation.md](implementation.md#tool-contracts-t5)).
`scripts/compare-contracts.py` compares both servers live: 0 unexplained differences; known are the
not-ported tools (4 resident, 148 indexed) and the suggestions for an unknown name. The read parity run on
`s16123120` stays at 0 mismatches (353 cases with the T6 knowledge cases, incl. 4 that call Go with `{"args": {...}}`). Changes in passing:
- tools/list now lists the 15 resident tools served here, in clio's order (before: 4, sorted by name), and
  every tool accepts clio's published `{"args": {...}}` shape besides the flat one;
- get-tool-contract takes clio's `tool-names`/`detail` (flat or wrapped; the older `name` still works) and
  answers in clio's shape (`index`, `tools`, `not-found`), text only; `list-environments` is in the index;
- the `environment-name` property that T1 added to the hand-written schemas is gone with them: the schema
  agents read is clio's;
- list-apps refuses an unknown flat key with clio's text, and ignores it inside `args`, as clio does.
Not matched on purpose or left for later:
- the contract texts describe clio's behaviour; where a Go tool still differs (for example
  `get-entity-schema-properties` refuses `package-name`), the contract is the target, not the description;
- clio master changes 45 tools' contracts (10 of them served here) and adds 8 tools compared with 8.1.0.134 (listed in clio-inventory.md);
  rerun the inventory after the next clio release;
- the inventory records clio's prompts and resources for reference only; T6 serves them from the
  clio-knowledge bundles, and its `knowledgeClioToolCatalog` is a separate copy of the same tool index.

### W5. Guidance, prompts and resources
`get-guidance` (core-rules, routing, per-area guides), `docs://help/command/{name}` resources, MCP prompts,
knowledge tools (`list-knowledge-sources`, `info-knowledge`, ...), `get-component-info`, `get-request-info`,
`merge-creatio-artifact`, `get-mobile-page-conversion-guide`, telemetry consent tools. Decision needed: serve
the same texts (bundled from clio-knowledge at build time) or leave these tools to clio running alongside.

T6 status (2026-10-03). Done: `internal/knowledge` reads and verifies the bundles clio installed (same cache,
same trust stores, signature and digest checks, compatibility selection, fallback to the previous
generation, topic pins and priorities); `get-guidance`, the `initialize` instructions, the 74 prompts,
`resources/list` / `resources/templates/list` / `resources/read`, `list-knowledge-sources`,
`info-knowledge`, `list-knowledge-examples`, `get-knowledge-feedback-policy`, `get-telemetry-consent`.
Live, against clio 8.1.0.134 and the installed curated library 1.15.95: `scripts/parity-cases/knowledge.json`
33 cases, 31 match, 2 known differences, 0 mismatches; the full case set 349 cases, 0 mismatches; every
prompt rendered for every captured argument combination equals clio's text.

Remaining, in clio only for now:
- Install, update and bootstrap of the curated library (`install-knowledge`, `update-knowledge`, the
  GitHub-release / NuGet / Git transports), `delete-knowledge`, and the source tools `add-`, `remove-`,
  `enable-`, `disable-knowledge-source`, `configure-knowledge-feedback-policy`. They write clio's
  `appsettings.json` and the installation store under clio's file locks (`.locks`, high-water marks,
  staging, pruning), which clio's hourly autoupdate also writes; a partial port risks the user's cache.
- `send-telemetry`, `withdraw-telemetry-consent` (clio's TelemetryService: outbox, flush, consent writes).
- `get-component-info(-to-file)`, `export-component-registry`, `get-request-info(-to-file)`,
  `merge-creatio-artifact`, `get-mobile-page-conversion-guide`: none of them reads the knowledge runtime
  (they use clio's component registry client, embedded catalogs and Creatio probes), so they belong with the
  Creatio-side tasks, not here. `delete-toolkit` and `experimental` are toolkit/feature commands (W7).

Known differences, each with its reason:
- clio 8.1.0.134 run as `dotnet clio.dll` answers every `docs://help/command/*` with its no-documentation
  fallback and `docs://help/restart|flushdb` with an exception text; this server serves the help text of
  the installed clio tool.
- clio prints both paths of a failed `info-knowledge` as `[redacted-path]` (shared redaction, T4);
  `knowledgeUntrusted` fences and flattens repository-controlled diagnostics like clio but does not run
  clio's secret-scrubbing rules (T4).
- `info-knowledge checkUpdates: true` reports `unknown` instead of contacting the publisher.
- Git-type knowledge sources are not read.
- An activation marker that lags the library's accepted sequence is reported, not reconciled (clio
  rewrites `current.json` and prunes generations; a reader must not).
- clio binds a bool prompt argument only from a JSON boolean, which the Go SDK's string-typed prompt
  arguments cannot carry, so `restart-by-credentials` (required `isNetCore`) cannot be rendered here.
- This server answers bundle compatibility as clio `8.1.0` and declares clio's 218-tool catalog as its
  capability set (`knowledge_tool_catalog.go`), because that catalog is what the migration targets; both
  follow the clio release used as the parity baseline.

### W6. Write tools by area (after W1–W3)
| Area | Tools |
|---|---|
| Applications | `create-app`, `create-app-section`, `update-app-section`, `delete-app-section`, `delete-app`, `install-application` |
| Entity and other schemas | `create-entity-schema`, `create-lookup`, `update-entity-schema`, `set-entity-schema-properties`, `modify-entity-schema-column`, `create-schema`, `update-schema`, `delete-schema`, `sync-schemas`, `create-sql-schema`, `update-sql-schema`, `install-sql-schema`, `export-schema`, `import-schema` |
| Pages | `create-page`, `update-page`, `sync-pages`, `localize-page`, `create-client-unit-schema`, `update-client-unit-schema`, `create-related-page-addon`, `create-user-task-page` |
| Business rules | `create-/update-/delete-entity-business-rules`, `create-/update-/delete-page-business-rules` |
| Data | `odata-create`, `odata-update`, `odata-delete`, `execute-dataservice-batch`, `execute-sql-script`, `create-data-binding`, `add-/remove-data-binding-row`, `create-data-binding-db`, `upsert-/remove-data-binding-row-db`, `run-process`, file variants (`execute-esq-to-file`, `odata-read-to-file`, ...) |
| Settings, users, access | `create-/update-sys-setting`, `download-sys-setting-file`, `set-fsm-mode`, `manage-user`, `manage-role`, `manage-license`, `manage-access`, `set-record-rights`, `create-server-to-server-oauth-app`, `create-oauth-technical-user` |
| Processes | `create-business-process`, `modify-business-process`, `modify-business-process-as-new-version`, `set-active-business-process-version`, `generate-process-model`, `create-user-task`, `modify-user-task-parameters`, `register-process-element`, `enroll-sequence-participants`, `validate-process-graph` |
| Themes and branding | `create-/update-/delete-theme`, `build-theme`, `set-user-theme`, `clear-themes-cache`, `set-logo`, `set-background-image`, `upload-image`, `advise-theme-palette` |
| Packages, compile, restart | `create-package`, `add-/remove-package-dependency`, `compile-creatio`, `watch-compilation`, `restart-by-environment-name`, `restart-by-credentials`, `pkg-to-db`, `pkg-to-file-system`, `install-gate`, `unlock-for-hotfix`, `finish-hotfix`, `download-configuration-*`, `add-custom-logging`, `install-process-builder`, `install-dashboards-migrator`, DataForge `initialize`/`update` |

### W7. Local machine, infrastructure and workspace tools
Windows/IIS/database/Redis/identity: `deploy-creatio`, `uninstall-creatio`, `restore-db-*`, `clear-redis-*`,
`deploy-identity`, `uninstall-identity`, `get-identity-service-config`, `assert-infrastructure`,
`show-passing-infrastructure`, `check-settings-health`, `list-creatio-builds`, `list-db-templates`,
`prune-db-templates`. Workspace: `create-workspace`, `push-workspace`, `restore-workspace`, `add-package`,
`link-from-repository-*`, `new-ui-project`, `new-integration-test-project`, `generate-source-code`,
`add-item-model`. Browser session tools, toolkit/skill management, `experimental`. These need a Windows host
for live checks; several may be deliberately left to clio.

### W8. Delivery and rollout
- CI on GitHub: build, `go vet`, tests, Windows and Linux builds on every push.
- Release automation: tag → build → macOS signing and notarization (secrets stay local or move to CI
  secrets) → GitHub release.
- Distribution that matches how people install clio (download, Homebrew tap, or `dotnet tool`-like
  updater) and an update path that works while the server runs (see the binary-replacement prototype).
- CAADT plugin: an option to point its MCP config at this server; a pilot group; then the default switch.
- Exit criteria per release: parity run on two stands (on-premises .NET Framework, cloud with OAuth), and
  one .NET Core stand once available.

## Recovery checkpoint (2026-10-03)

Claude session `ba9331a3-c45a-44dd-aaab-caa021018c34` stopped on its weekly API
limit during Stage 3. Work resumed in the original
`.claude/worktrees/clio-mcp-new-implementation-f9a6b6` at `ece7fe5`; partial source
from five sibling worktrees was preserved and integrated. This checkpoint adds
30 implemented tool registrations to the previous 72, for **102 total** (including
`get-tool-contract`). No commit, push or release was made.

| Task | Implemented in this checkpoint | Remaining task tools | Details |
|---|---:|---:|---|
| T7 applications | 1 | 5 | [T7/T8 status](status/t7-t8-t10-status.md) |
| T8 schemas | 8 | 6 | [T7/T8 status](status/t7-t8-t10-status.md) |
| T9 pages | 2 | 5, plus the T6 component/merge leftovers | [T9/T11/T12 status](status/t9-t11-t12-status.md) |
| T10 business rules | 2, then 4 in the second round (all 6 done; live success parity is window-only) | 0 | [T10 status](status/t10-status.md) |
| T11 data | 3 | 9 | [T9/T11/T12 status](status/t9-t11-t12-status.md) |
| T12 settings/access | 1 | 12 | [T9/T11/T12 status](status/t9-t11-t12-status.md) |
| T13 processes | 1 | 10 | [T13/T14/T15 status](status/t13-t14-t15-status.md) |
| T14 themes/email | 8 | 3 | [T13/T14/T15 status](status/t13-t14-t15-status.md) |
| T15 packages/operations | 4 | 12 | [T13/T14/T15 status](status/t13-t14-t15-status.md) |

Final contract comparison: **220 match, 0 unexplained differences**; remaining
not-ported tools and unknown-name suggestions are explicitly classified. The combined
local-validation comparison is **28 match, 0 mismatches**.

Shared verification passes: `go test -race ./...`, `go vet ./...`, Windows amd64
build, formatting/diff checks and all 21 Python harness tests. HTTP integration
fixtures and MCP tests exercise success/refusal paths, payload preservation,
confirmation/executor routing and failure semantics. Validation parity is separate
from successful live write parity; area reports record exact coverage and known
compatibility limits, including palette floating-point rounding and the installed
clio email argument-binding failure.

Connectivity recovered after the owner logged in on 2026-10-03. Live read parity:
11 matches, eight expected both-failed cases, zero mismatches. Own theme create,
update and delete match; read-back differences are limited to CSS length and the
per-theme cache hash. All explicit content expectations pass after correcting the
scenario's neutralized name casing. Own Contact OData create/update/read also
match. OData DELETE receives the same IIS HTTP 405 on both implementations;
DataService cleanup successfully removed both contacts. The two tentative schemas
from the DNS outage were independently confirmed absent by authenticated
SourceCodeSchemaManager lookups. No stand-wide compile, restart or cache refresh
was run. Stage 3 remains **partial**, not ready for T17 release verification.

Next: run schema/client-unit scenarios in a dedicated owned package, then port
remaining Stage-3 tools. Global-operation scenarios remain in a dedicated
verification window. The appendix below is the generated baseline task allocation;
this checkpoint table and linked reports describe current implementation status.

## Tasks and dependencies

| ID | Task | Depends on | Stage |
|---|---|---|---|
| T1 | Multiple environments per process (W1) — **done** 2026-10-03 | — | 1 |
| T2 | Write-parity harness (W3) — **done**, see [parity.md](parity.md) | — | 1 |
| T3 | CI on GitHub and release automation (W8, first half) — **done**, see [releasing.md](releasing.md) | — | 1 |
| T4 | Shared infrastructure: redaction, envelope type, write safety, long-running operations, `rest/` helper (W2) — **done** 2026-10-03, see W2 | T1 | 2 |
| T5 | Contract and resident-list parity (W4) — **done** 2026-10-03, see W4 | T1 | 2 |
| T6 | Guidance, prompts, resources, knowledge tools from clio-knowledge bundles (W5, D2) — **read side done**, see below | — (rebase after T1) | 2, started early |
| T7–T15 | Write tools, one task per W6 area — **partial**, 30 tools added in the recovery checkpoint | T1, T2, T4, decision D1 | 3 (in parallel) |
| T16 | Local machine, infrastructure and workspace tools (W7) — deferred (D3) | T1, T4 | later |
| T17 | Full parity run, docs, release `v0.2.0` | T5–T16 | 4 |
| T18 | CAADT plugin switch, pilot, default switch (W8, second half) | T17 | 5 |

Stage 1 runs in parallel because T1 changes Go code, T2 only the comparison scripts and T3 only CI files.
Stage 2 waits for T1 because it touches the same argument handling. Stage 3 is the bulk of the work and
splits into independent areas once the write harness and shared infrastructure exist.

## Decisions (2026-10-03)

- **D1. Stand for write tests: `s16123120`** (on-premises, owned by the project owner). Write scenarios
  create and delete only objects named with the `UsrParity{run}{side}` pattern; leftovers from a failed
  cleanup stay there until the next `--cleanup-ledger` run.
- **D2. Guidance comes from clio-knowledge, the same way clio gets it.** clio does not bundle guidance texts:
  its `Knowledge/` runtime installs and updates a versioned knowledge bundle (Git source, GitHub release or
  NuGet), verifies it against a trust store, caches it and selects a compatible version. This server
  implements the same runtime and reads the same bundles, so a guidance fix in clio-knowledge reaches both
  servers without a binary release.
- **D3. Local machine, infrastructure and workspace tools (W7, T16) are deferred.** They stay in clio for
  now and are outside "done" until revisited.

## Appendix: every clio tool, by task

218 clio tools in the get-tool-contract index of clio 8.1.0.134 (plus `get-tool-contract` itself, which the index leaves out), 19 of them resident in tools/list. Generated by `scripts/clio-inventory.py` from clio's live answers (`docs/clio-inventory.json`, summary in [clio-inventory.md](clio-inventory.md)); do not edit by hand, change `TASKS` in the script and rerun it.

| Task | Count | Tools |
|---|---|---|
| done (served by creatio-mcp-go) | 71 | `check-theming-access`, `clio-run`, `clio-run-destructive`, `compile-status`, `dataforge-context`, `dataforge-find-lookups`, `dataforge-find-tables`, `dataforge-get-relations`, `dataforge-get-table-columns`, `dataforge-status`, `describe-business-process`, `describe-environment`, `execute-esq`, `find-app`, `find-empty-iis-port`, `find-entity-schema`, `get-app-info`, `get-classic-list-columns`, `get-classic-page-sources`, `get-client-unit-schema`, `get-email-template`, `get-entity-schema-column-properties`, `get-entity-schema-properties`, `get-fsm-mode`, `get-guidance`, `get-knowledge-feedback-policy`, `get-package-file`, `get-page`, `get-page-hierarchy`, `get-process-page-facts`, `get-process-signature`, `get-record-rights`, `get-related-page-addon`, `get-schema`, `get-schema-name-prefix`, `get-sequence-context`, `get-sql-schema`, `get-sys-setting`, `get-target-package`, `get-telemetry-consent`, `get-theme`, `get-user-culture`, `info-knowledge`, `inspect-access`, `inspect-license`, `inspect-role`, `inspect-user`, `last-compilation-log`, `list-app-sections`, `list-apps`, `list-entity-client-schemas`, `list-environments`, `list-knowledge-examples`, `list-knowledge-sources`, `list-package-files`, `list-packages`, `list-page-templates`, `list-pages`, `list-printables`, `list-sys-settings`, `list-themes`, `list-user-tasks`, `odata-read`, `read-data-binding-db`, `read-entity-business-rules`, `read-page-business-rules`, `resolve-oauth-system-user`, `restart-status`, `start-creatio`, `validate-page`, `verify-oauth-app` |
| T6 guidance and knowledge | 18 | `add-knowledge-source`, `configure-knowledge-feedback-policy`, `delete-knowledge`, `delete-toolkit`, `disable-knowledge-source`, `enable-knowledge-source`, `experimental`, `export-component-registry`, `get-component-info`, `get-request-info`, `install-knowledge`, `install-toolkit`, `merge-creatio-artifact`, `remove-knowledge-source`, `send-telemetry`, `update-knowledge`, `update-toolkit`, `withdraw-telemetry-consent` |
| T7 applications | 6 | `create-app`, `create-app-section`, `delete-app`, `delete-app-section`, `install-application`, `update-app-section` |
| T8 schemas | 14 | `create-entity-schema`, `create-lookup`, `create-schema`, `create-sql-schema`, `delete-schema`, `export-schema`, `import-schema`, `install-sql-schema`, `modify-entity-schema-column`, `set-entity-schema-properties`, `sync-schemas`, `update-entity-schema`, `update-schema`, `update-sql-schema` |
| T9 pages | 7 | `create-client-unit-schema`, `create-page`, `create-related-page-addon`, `create-user-task-page`, `sync-pages`, `update-client-unit-schema`, `update-page` |
| T10 business rules | 6 | `create-entity-business-rules`, `create-page-business-rules`, `delete-entity-business-rules`, `delete-page-business-rules`, `update-entity-business-rules`, `update-page-business-rules` |
| T11 data | 12 | `add-data-binding-row`, `create-data-binding`, `create-data-binding-db`, `execute-dataservice-batch`, `execute-sql-script`, `odata-create`, `odata-delete`, `odata-update`, `remove-data-binding-row`, `remove-data-binding-row-db`, `run-process`, `upsert-data-binding-row-db` |
| T12 settings, users, access | 13 | `create-server-to-server-oauth-app`, `create-sys-setting`, `download-sys-setting-file`, `get-identity-assertion`, `get-identity-public-jwk`, `manage-access`, `manage-license`, `manage-role`, `manage-user`, `regenerate-identity-signing-key`, `set-fsm-mode`, `set-record-rights`, `update-sys-setting` |
| T13 processes | 11 | `create-business-process`, `create-user-task`, `enroll-sequence-participants`, `generate-process-model`, `install-process-builder`, `modify-business-process`, `modify-business-process-as-new-version`, `modify-user-task-parameters`, `register-process-element`, `set-active-business-process-version`, `validate-process-graph` |
| T14 themes, branding, email | 11 | `advise-theme-palette`, `build-theme`, `clear-themes-cache`, `create-theme`, `delete-theme`, `set-background-image`, `set-logo`, `set-user-theme`, `update-email-template`, `update-theme`, `upload-image` |
| T15 packages, compile, restart | 16 | `add-custom-logging`, `add-package-dependency`, `compile-creatio`, `dataforge-initialize`, `dataforge-update`, `download-configuration-by-build`, `download-configuration-by-environment`, `finish-hotfix`, `install-dashboards-migrator`, `install-gate`, `pkg-to-db`, `pkg-to-file-system`, `remove-package-dependency`, `restart-by-credentials`, `restart-by-environment-name`, `unlock-for-hotfix` |
| T16 local, infrastructure, workspace | 33 | `StopAllCreatio`, `add-item-model`, `add-package`, `assert-infrastructure`, `check-auth-code-flow`, `check-settings-health`, `clear-browser-session`, `clear-redis-db-by-credentials`, `clear-redis-db-by-environment`, `create-workspace`, `deploy-creatio`, `generate-source-code`, `get-browser-session`, `get-identity-service-config`, `link-from-repository-by-env-package-path`, `link-from-repository-by-environment`, `link-from-repository-unlocked`, `list-creatio-builds`, `list-db-templates`, `new-integration-test-project`, `new-test-project`, `new-ui-project`, `prune-db-templates`, `push-workspace`, `reg-web-app`, `restore-db-by-credentials`, `restore-db-by-environment`, `restore-db-to-local-server`, `restore-workspace`, `show-passing-infrastructure`, `stop-all-creatio`, `stop-creatio`, `uninstall-creatio` |
