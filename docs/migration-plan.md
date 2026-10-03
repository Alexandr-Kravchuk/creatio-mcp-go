# Migration plan: replacing clio's MCP server with creatio-mcp-go

Goal: an agent (Claude Code, Codex, the CAADT skills) does every Creatio task it does today through
`clio mcp-server`, but through this server, with the same tool names, arguments and answers.

Measured on 2026-10-03 against clio master `914dab286`:

| | clio | this server |
|---|---|---|
| MCP tools | about 217 | 65, all read-only (`list-environments` added in T1) |
| MCP prompts and resources | about 125 attributes (`get-guidance`, `docs://help/...`) | 74 prompts, 5 resource templates, the knowledge catalog (T6) |
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

### W3. Parity harness for writes
Writes cannot be compared by calling both servers with the same arguments. Each case creates its object
under a per-server name (`{side}`), reads it back through both servers, compares the read-backs, then
deletes it. A run against a non-disposable stand is refused. Without this, write tools cannot be verified.
Done (T2): `scripts/compare-mcp-writes.py`, scenarios in `scripts/write-scenarios/`, usage and format in
[parity.md](parity.md).

### W4. Tool contract and resident list
`tools/list`, `get-tool-contract` descriptions and input schemas must match clio, because agents call
`get-tool-contract` before acting. Generate a diff from clio's live answers and close it.

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

## Tasks and dependencies

| ID | Task | Depends on | Stage |
|---|---|---|---|
| T1 | Multiple environments per process (W1) — **done** 2026-10-03 | — | 1 |
| T2 | Write-parity harness (W3) — **done**, see [parity.md](parity.md) | — | 1 |
| T3 | CI on GitHub and release automation (W8, first half) — **done**, see [releasing.md](releasing.md) | — | 1 |
| T4 | Shared infrastructure: redaction, envelope type, write safety, long-running operations, `rest/` helper (W2) | T1 | 2 |
| T5 | Contract and resident-list parity (W4) | T1 | 2 |
| T6 | Guidance, prompts, resources, knowledge tools from clio-knowledge bundles (W5, D2) — **read side done**, see below | — (rebase after T1) | 2, started early |
| T7–T15 | Write tools, one task per W6 area | T1, T2, T4, decision D1 | 3 (in parallel) |
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

## Appendix: every clio tool not yet ported, by task

157 clio tools (clio master `914dab286`), generated from clio's `[McpServerTool]` attributes. The generator under-counts files that declare several tools through same-named constants; the business-rule row was completed by hand, so other rows may still miss a tool — T5 produces the exact list from clio's live `tools/list` and `get-tool-contract`.

| Task | Count | Tools |
|---|---|---|
| T1 environments | 1 | `list-environments` (done) |
| T4 shared infrastructure | 1 | `clio-run-destructive` |
| T6 guidance and knowledge | 25 | `add-knowledge-source`, `configure-knowledge-feedback-policy`, `delete-knowledge`, `delete-toolkit`, `disable-knowledge-source`, `enable-knowledge-source`, `experimental`, `export-component-registry`, `get-component-info`, `get-component-info-to-file`, `get-guidance`, `get-knowledge-feedback-policy`, `get-mobile-page-conversion-guide`, `get-request-info`, `get-request-info-to-file`, `get-telemetry-consent`, `info-knowledge`, `install-knowledge`, `list-knowledge-examples`, `list-knowledge-sources`, `merge-creatio-artifact`, `remove-knowledge-source`, `send-telemetry`, `update-knowledge`, `withdraw-telemetry-consent` |
| T7 applications | 6 | `create-app`, `create-app-section`, `delete-app`, `delete-app-section`, `install-application`, `update-app-section` |
| T8 schemas | 15 | `create-entity-schema`, `create-lookup`, `create-schema`, `create-sql-schema`, `delete-schema`, `export-schema`, `import-schema`, `install-sql-schema`, `list-entity-client-schemas-to-file`, `modify-entity-schema-column`, `set-entity-schema-properties`, `sync-schemas`, `update-entity-schema`, `update-schema`, `update-sql-schema` |
| T9 pages | 8 | `create-client-unit-schema`, `create-page`, `create-related-page-addon`, `create-user-task-page`, `localize-page`, `sync-pages`, `update-client-unit-schema`, `update-page` |
| T10 business rules | 6 | `create-entity-business-rules`, `update-entity-business-rules`, `delete-entity-business-rules`, `create-page-business-rules`, `update-page-business-rules`, `delete-page-business-rules` |
| T11 data | 14 | `add-data-binding-row`, `create-data-binding`, `create-data-binding-db`, `execute-dataservice-batch`, `execute-esq-to-file`, `execute-sql-script`, `odata-create`, `odata-delete`, `odata-read-to-file`, `odata-update`, `remove-data-binding-row`, `remove-data-binding-row-db`, `run-process`, `upsert-data-binding-row-db` |
| T12 settings, users, access | 11 | `create-oauth-technical-user`, `create-server-to-server-oauth-app`, `create-sys-setting`, `download-sys-setting-file`, `manage-access`, `manage-license`, `manage-role`, `manage-user`, `set-fsm-mode`, `set-record-rights`, `update-sys-setting` |
| T13 processes | 11 | `create-business-process`, `create-user-task`, `enroll-sequence-participants`, `generate-process-model`, `install-process-builder`, `modify-business-process`, `modify-business-process-as-new-version`, `modify-user-task-parameters`, `register-process-element`, `set-active-business-process-version`, `validate-process-graph` |
| T14 themes, branding, email | 11 | `advise-theme-palette`, `build-theme`, `clear-themes-cache`, `create-theme`, `delete-theme`, `set-background-image`, `set-logo`, `set-user-theme`, `update-email-template`, `update-theme`, `upload-image` |
| T15 packages, compile, restart | 18 | `add-custom-logging`, `add-package-dependency`, `compile-creatio`, `create-package`, `dataforge-initialize`, `dataforge-update`, `download-configuration-by-build`, `download-configuration-by-environment`, `finish-hotfix`, `install-dashboards-migrator`, `install-gate`, `pkg-to-db`, `pkg-to-file-system`, `remove-package-dependency`, `restart-by-credentials`, `restart-by-environment-name`, `unlock-for-hotfix`, `watch-compilation` |
| T16 local, infrastructure, workspace | 30 | `add-item-model`, `add-package`, `assert-infrastructure`, `check-auth-code-flow`, `check-settings-health`, `clear-browser-session`, `clear-redis-db-by-credentials`, `clear-redis-db-by-environment`, `create-workspace`, `deploy-creatio`, `deploy-identity`, `generate-source-code`, `get-browser-session`, `get-identity-service-config`, `link-from-repository-by-env-package-path`, `link-from-repository-by-environment`, `link-from-repository-unlocked`, `list-creatio-builds`, `list-db-templates`, `new-integration-test-project`, `new-ui-project`, `prune-db-templates`, `push-workspace`, `restore-db-by-credentials`, `restore-db-by-environment`, `restore-db-to-local-server`, `restore-workspace`, `show-passing-infrastructure`, `uninstall-creatio`, `uninstall-identity` |
