# T13–T15 recovery checkpoint (2026-10-03)

Recovered Claude's untracked source files from the three original sibling worktrees into
`clio-mcp-new-implementation-f9a6b6`, preserving the original files. No commit or push.

Implemented and registered in this checkpoint:

- T13: `generate-process-model`: process resolution, metadata/localization parsing, CLR types,
  collections and C# model file output; original recovered implementation completed and tested.
- T14: `create-theme`, `update-theme`, `delete-theme`, `clear-themes-cache`; the recovered
  `BuildThemeForCreate` stub was replaced by a functional embedded-template builder.
- T14: `build-theme`, all eight operations of `advise-theme-palette`, `set-user-theme` and
  `update-email-template`. Theme application resolves id/class/caption in that order, rejects ambiguity,
  writes the theme ID and verifies the profile. Email updates check confirmation and the optimistic
  checksum before writing and derive the receipt from the persisted row; supports Beefree and legacy
  variants, default languages and message-template translations.
- T15: `add-package-dependency`, `remove-package-dependency`, `unlock-for-hotfix`, `finish-hotfix`.
  Dependency edits preserve server extension properties and avoid duplicate dependency IDs;
  removing a missing dependency performs no save.

Verification:

- Focused tests cover deterministic embedded CSS/descriptor output and invalid inputs; fixed theme
  service routes; package read/modify/save preservation and hotfix request GUID; user-theme application,
  reset and silently ignored writes; stale-email refusal, confirmation and persisted checksums;
  model localization and explicit output paths; MCP executor argument validation.
- Offline comparison against installed clio 8.1.0.134: 14 calls, 12 exact matches, one floating-point
  difference, one failed clio binding. CSS and descriptor bytes of a fixed-ID `build-theme` matched.
  `accent-suggest` selects identical colors/candidate verdicts but some metrics differ by about
  1e-16 (Go versus .NET math functions); strict equality remains a known parity gap.
- The installed clio `update-email-template` failed argument binding before executing for both flat
  and nested executor payloads: `invalid-parameter-type: argument 'args' ... must be an object`.
  Native Go email behavior is tested with mock HTTP integration, not verified live against this binary.
- `scripts/write-scenarios/t14.json` recovered and its CSS/class expectations corrected to the lowercase
  class actually derived from the caption. It has not run live in this checkpoint.

Live writes were not attempted: the disposable stand is currently inaccessible through DNS.
Global theme/cache, branding, package installation, compile and restart operations must not be run
against the stand while other workstreams are active. Mocks cover the implemented transport paths.

Remaining tools (not registered as stubs):

- T13: none since the third round (2026-10-10, below); at this checkpoint all ten listed there remained.
- T14: `upload-image`, `set-logo`, `set-background-image`.
- T15: none since the third round (see "T15 third round" below).

Additional limits requiring follow-up parity coverage: custom-font probing currently performs bounded
per-call probes without clio's process cache; descriptor JSON uses Go's escaping convention for special
characters; process model full remote resolution/output and the theme workspace-write mode require more
fixture and live cases. Argument-type failure wording for the new email handler is not yet measured.

## Live follow-up (2026-10-03)
Own theme creation, update and deletion match the baseline. Caption, CSS class
and CSS content expectations pass. Read-back differs only in declared CSS length
and platform cache-hash path. Both test themes were removed in each run. Scenario
expectations now use the canonical neutralized name casing.

## T14 third round (2026-10-10)

All 11 T14 tools are served. New in this round: `upload-image`, `set-logo`, `set-background-image`
(clio master 914dab286; the texts match clio 8.1.0.134). The uploader came from the `codex-t14-t15`
partial work and was checked against clio's `SysImageUploader` and Creatio.Client's
`CreateImageUploadRequest` (raw body, `Content-Type`, `Content-Range`, `Content-Disposition`, then a
byte-for-byte read-back).

- `upload-image`: live on `s16123120`. One fixture PNG uploaded through each server, both envelopes
  `{success, image-id}`, both rows read back as `image/png`, both removed through clio's
  `execute-dataservice-batch` and confirmed absent. Not expressible as a harness scenario: the object is
  a `SysImage` row with a random id, not a `{run}{side}` name, so the check was run by hand.
- `set-logo`, `set-background-image`: window-only (stand-wide branding for all users, no revert).
  Implemented and mock-tested (request order, payloads, refusals, partial failures, binding collisions);
  scenarios in `scripts/write-scenarios/window/t14.json` bind into an app package the run creates and
  remove it with `delete-app`. The logo/background settings themselves stay changed after such a run.
- Read parity, `scripts/parity-cases/t14.json`: 58 cases, 58 match against clio 8.1.0.134 — every
  refusal the three branding tools give before a write, 24 `advise-theme-palette` cases over all eight
  operations, 7 `update-email-template` refusals.

Closed gaps:
- `update-email-template` "binding failure" was a probe error, not a clio defect: clio's
  `EmailTemplateUpdateArgs` has `required` members (email-id, environment-name, format,
  expected-checksum), and a call without one fails binding. Go now gives the same refusal and omits
  null fields from the answer, as clio does. With the members present, clio and Go match.
- `advise-theme-palette` float differences: clio rounds nothing in its output path (raw doubles,
  serializer defaults). 144 ad-hoc cases over all operations and 18 colours matched exactly; the
  earlier ~1e-16 difference was not reproduced. If it reappears, it is .NET libm `Math.Pow`/`Cbrt`
  against Go's `math` package, not rounding.

Known gaps (branding):
- The package resolver of `set-logo` / `set-background-image` follows clio master: the
  `CurrentPackageId` value is matched on `SysPackage.UId` and the advice reads "Ask the user which
  package should receive the change...". clio 8.1.0.134 (and this server's `get-target-package`, which
  was measured against it) match on `Id` and advise "Name the package explicitly...".
- clio master reads the file-system development mode after each binding save to log a warning; the
  warning never reaches the MCP result, so Go does not make that read (8.1.0.134 does not either).
- `UsePanelIconBackground` is turned off with one DataService `BatchQuery` per ATF save; ATF's exact
  query shape (all model columns) and its error texts are approximated. The same holds for the OData
  error-envelope detection used by the gallery lookups.
- Transport failures that clio raises as .NET exceptions surface with Go's error text.
- The sys-setting write used by branding is a minimal, `themeWrite`-prefixed slice of clio's
  `SysSettingsCommand` / `SysSettingsManager` (Binary from a file with the file-security policy,
  Boolean, text). T12's `update-sys-setting` port should absorb it.

## T15 third round (2026-10-10)

All 16 T15 tools are served. New in this round (clio master; the comparison binary is 8.1.0.134):
`compile-creatio`, `restart-by-environment-name`, `restart-by-credentials`, `pkg-to-db`,
`pkg-to-file-system`, `install-gate`, `install-dashboards-migrator`, `download-configuration-by-build`,
`download-configuration-by-environment`, `add-custom-logging`, `dataforge-initialize`, `dataforge-update`.
From the `codex-t14-t15` partial work only the T15 files were taken; the sync and Data Forge parts were
kept after review, the compile part was rewritten (it posted `BuildPackage`, read one response and had
no observation of the build).

- `compile-creatio`: master's argument record (`process-name` included) and every scope refusal before
  anything is tracked (legacy or unknown key, package list, blank or explicit-null scope, both scopes).
  Full compile: `Rebuild` sent once, then the build is observed (compilation-history rows, the reload
  seen by an anonymous probe of `GetLastCompilationResult`, quiet activity) and the verdict read from
  `GetLastCompilationResult` when the request never answers. Package compile: `RebuildPackage` waited
  as `compile-package --wait 600` (answer plus settled history). Process compile: CrtProcessBuilder
  1.6.6.33+ gate, then `ProcessDesignService/CompileProcess` with clio's fenced error listing. One
  compile per target at a time (shared with `install-dashboards-migrator`), recorded for
  `compile-status`; in-progress after the response deadline with master's text and `started-utc`.
- `restart-by-*`: the operation is recorded before the request; the request and the readiness wait
  (health ping, then an authenticated `SelectQuery` round-trip; two rejected logins end it) run inside
  the deadline race; `restart-status` reports `ready`, `timedout` or `requestfailed` (shared change:
  `operationStore.finishAs` in `operations.go`). Master's accepted/pending in-progress texts.
- `pkg-to-db` / `pkg-to-file-system`: FSM probe, then the load; clio's 30 attempts with 3 s delay;
  the empty line clio's command writes after the loader is kept.
- `install-gate` / `install-dashboards-migrator`: PackageInstaller (`PackageInstallerService`
  `InstallPackage` with the archive name, `CreateBackup`), developer mode forced off, restart on
  .NET Core; the gate then restarts the application; the migrator checks the shipped version
  (`Files/app-descriptor.json` in clio's .gz) against the installed one, waits for the liveness ping
  and pings `DashboardsMigratorPingService`. This server ships no archives: they are read from the
  newest clio installed as a .NET tool (`~/.dotnet/tools/.store/clio/...`, as `docs://help/command`
  already does). T13's `install-process-builder` needs the same locator (`pkgWriteBundledArchive`).
- `add-custom-logging`: FluentValidation's every-rule report, then the NLog edit of the environment's
  `EnvironmentPath` (encoding, BOM and line endings kept, backup, verify, rollback), optional restart.
- `download-configuration-*`: workspace checks first; by-build copies from a zip or an extracted
  directory; by-environment copies from a local `EnvironmentPath` or downloads through cliogate
  (CoreBin, Lib, ConfigurationBin, each workspace package's Autogenerated folder, referenced dlls).
  The workspace path is passed in instead of changing the process working directory.
- `dataforge-initialize` / `-update`: one proxy POST after the platform version check; "Scheduled".

Verification:
- Unit tests against `httptest` stands for every tool (request shapes, routes, bodies, success and
  failure paths, timeouts, retries); MCP-level tests through `clio-run` / `clio-run-destructive`, the
  raw-name gate, binding failures, refusals that must not reach Creatio, `compile-status` /
  `restart-status` records.
- Read parity, `scripts/parity-cases/t15.json`: 29 cases, 29 match against clio 8.1.0.134 (every
  refusal that is safe on the shared stand: unknown environments, binding failures, scope and path
  refusals, validation reports). Contracts: `compare-contracts.py` 297 match, 81 known not ported.
- Live, `download-configuration-by-environment` on `s16123120` into one workspace per side (reads the
  stand, writes local files only): both sides wrote the same 1944 files byte for byte; the answers
  differ only in the temporary folder name and the order of the parallel "Run download" lines (clio
  downloads in parallel too). Run as an ad-hoc `compare-mcp.py` case with `{side}` workspaces
  (`.clio/workspaceSettings.json` with no packages), not kept in the case file because it needs them.

Window-only (implemented and mock-tested, not run live): `compile-creatio`, `restart-by-*`,
`pkg-to-db`, `pkg-to-file-system`, `install-gate`, `install-dashboards-migrator`, `dataforge-*`,
`add-custom-logging` (also needs a local installation; `s16123120` has no `EnvironmentPath`).
Scenarios: `scripts/write-scenarios/window/t15.json` (compile of a run-named app package and a full
compile, both read back through `compile-status`; restart with and without the wait; FSM loads;
install-gate and the migrator with a `list-packages` read-back; Data Forge). `restart-by-credentials`
has no scenario: it would need the stand's credentials in the file. No stand objects were created in
this round; the write ledger holds no T15 entries.

Known gaps (T15):
- Deliberate: a blank environment name is refused by `pkg-to-*`, `compile-creatio`, `install-*` and
  `download-configuration-by-environment`, where clio resolves the ACTIVE environment and would run the
  tool there.
- The response deadline of `compile-creatio`, the restart tools and the migrator is master's 45 s
  (`CLIO_MCP_RESPONSE_DEADLINE_SECONDS` overrides it); 8.1.0.134 waits 150 s, as the shared default
  still does. `restart-status`'s not-found note is 8.1.0.134's, not master's.
- 8.1.0.134 has no `process-name`, no alias refusal and no `started-utc`; its binder turns an unknown
  or blank scope into a FULL compile, so those cases are not in the parity file.
- The deprecated alias `restart-by-environmentName` is not registered.
- Not reproduced: clio's bundled-package convergence refusal on the process compile; the stack-trace
  line the install commands' catch prints; `correlation-id`; .NET exception texts (transport failures
  carry Go's error text); the readiness waiter's own log lines in the restart answer (clio does not
  capture them either, so only their absence is unmeasured).
- `install-gate` and the migrator log the environment URI as this server stores it (no trailing slash).
- `add-custom-logging` restarts with the restart tools' 30 s request timeout (clio: 100 s).
- `watch-compilation` and `create-package` are not in the 8.1.0.134 index and were not ported; T10's
  window scenarios still name `create-package`.

## T13 third round (2026-10-10)

All 11 T13 tools are served (clio master 914dab286). New in this round: `create-business-process`,
`modify-business-process`, `modify-business-process-as-new-version`, `set-active-business-process-version`,
`validate-process-graph`, `register-process-element`, `enroll-sequence-participants`, `create-user-task`,
`modify-user-task-parameters`, `install-process-builder`. The `codex-t13` partial work did not build and
was largely rewritten (wrong exit codes, string-only documents, the presence wording instead of the
version gate, no pre-call log line, alphabetical descriptor JSON, a struct decode that turned missing
enrollment counts into zeros); its change to the shared `internal/creatio/command.go` was dropped (the
compile note lives in the area's own envelope type, `ProcessWriteResult`). No shared file was changed.

What the stand can and cannot show. `s16123120` has no CrtProcessBuilder, so the four
ProcessDesignService writes and `validate-process-graph` refuse there at the package gate; the
user-task tools need a local workspace linked to a stand package, which the write harness cannot stage;
`install-process-builder` is a stand-wide build and restart. Hence:

- Read parity, `scripts/parity-cases/t13.json`: 51 cases (every argument refusal, the package gates,
  binder refusals, an enrollment with random ids) — 44 match, 5 both-failed and 1 known-diff, all six
  where clio master (served here) differs from clio 8.1.0.134 (below), 0 mismatches. The enrollment
  case POSTs to the native `SequenceParticipantBulkAddService` with random sequence and contact ids:
  it enrolls nobody (added 0) and creates nothing, but it is a write endpoint.
- Success and failure paths against a canned Creatio, `scripts/fake-stands/t13` (how to run it is in
  `fake_creatio.py`): 48 cases through both servers — every ProcessDesignService answer shape (success,
  failure texts, layout-change relay, version report, activation read-back, UNKNOWN / empty / shape
  texts), the post-write block verification of create and modify (unverified, refused, garbled and
  described read-backs: missing accessRights/email/approval blocks, unresolved elements, lossy reads,
  absent/conditionless/undecodable record filters, unlanded templates, unresolved body macros,
  describe-shaped approval blocks, absent/different/uncleared/UId-addressed flow labels, addElement
  rights), the validator on rule-heavy graphs, enrollment outcomes, user-task create/modify with
  lookups, composite items and save refusals — 47 match, 1 known-diff (compile note, below). The
  request bodies the two servers sent are the same on all 13 routes (parsed JSON, generated parameter
  uIds blanked).
- `register-process-element` (offline): run against a fixture workspace with clio 8.1.0.134 and Go; the
  SQL scripts are byte-identical, the descriptors differ only in the random UId and the timestamp.
- Unit and MCP-level tests per tool (request shapes, success, failure, binding, both executors, raw-name
  confirmation, annotations); the validator carries clio's ProcessGraphValidatorTests cases.
- Write scenarios: `scripts/write-scenarios/window/t13.json` — install-process-builder, then the
  process designer chain in an own app (validate, build, describe, edit, new version, activate,
  describe; delete-app removes the process). There is no non-window T13 scenario: no T13 tool can
  create a stand object outside the window. The write harness was not run, so the ledger is untouched
  and nothing is left on the stand.

clio master versus 8.1.0.134 (measured; this server follows master):
- The build/modify gate asks for CrtProcessBuilder 1.6.6.77; 8.1.0.134 names 1.6.2.1.
- `descriptor` / `operations` take a JSON object/array or a string holding one; 8.1.0.134 binds a
  string only. A missing document is exit 1 (FromValidationError); 8.1.0.134 gives -1.
- The compile note is left out when a message says a compile is needed (`MentionsCompileRequirement`);
  8.1.0.134 adds it anyway.
- One deliberate exception: `register-process-element` writes its SQL with CRLF lines as the released
  8.1.0.134 does (its raw string literal is compiled from a CRLF checkout), not with master's LF source;
  clio compares an existing script with line endings normalized, so either form is accepted on a re-run.

The post-write verification (clio's `WarnOnDiscardedConfigurationBlocks` with BlockExpectationIntent,
the AccessRights / Approval / Email / FlowLabel expectations and BlockExpectationReporter) is ported:
after a successful build or edit that sent an `accessRights`, `email` or `approval` block, a flow
`label` or (modify) a `clearFilter`, the process is read back once (DescribeProcess, 5 s, no version
facts) and every block that did not land becomes a warning; modify also warns about `accessRights` sent
with `addElement`.

Known gaps:
- The verification reads the described graph as JSON rather than through clio's typed model, so a
  DescribeProcess answer with a field of the wrong JSON type (which clio's deserializer refuses as
  "could not parse server response") is read leniently.
- A `descriptor` / `operations` sent as a JSON object reaches Go as a map, so it is forwarded with its
  keys in alphabetical order; clio keeps the caller's order. A string-held document keeps its order.
  The server reads both the same.
- Failure texts that clio takes from .NET exceptions (an HTTP transport failure, a JSON reader fault
  past the first character, a missing file) carry Go's or an approximated wording.
- No request retries: clio's user-task designer calls retry 3 times, its file-design-mode calls 30 times,
  the CrtProcessBuilder Ping 3 times (the Ping retry is ported).
- User tasks: an absent `workspace-path` walks up from the server's working directory to a `.clio`
  folder, as clio does from its own; an absent `package-name` is treated as empty (clio raises .NET's
  null-argument text).
- `install-process-builder`: the archive is the newest installed clio's (`~/.dotnet/tools/.store/clio`);
  without one the refusal names a placeholder path. The one-build-per-target reservation is this tool's
  own (clio shares it with compile-creatio, not served yet). The health wait polls the health route
  anonymously like clio's HealthCheckCommand, with an approximated certificate/timeout behaviour. The
  same installed archive also feeds the gates' convergence check ("This clio carries ..."); without an
  installed clio there is no convergence refusal.
- `install-process-builder` installs the package through T15's PackageInstaller path
  (`pkgWriteInstallPackageFile`: `PackageInstallerService` `CreateBackup` / `InstallPackage`), as clio does,
  not through the application installer (`InstallAppFromFile`).
- The lookup of the newest installed clio in `~/.dotnet/tools/.store/clio` and the reading of its bundled
  archive now exist twice: here and in T15 (`pkgWriteBundledArchive`). They should be merged into one helper.
