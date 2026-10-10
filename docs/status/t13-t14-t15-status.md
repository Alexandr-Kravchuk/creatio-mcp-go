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

- T13: `create-business-process`, `modify-business-process`,
  `modify-business-process-as-new-version`, `set-active-business-process-version`, `create-user-task`,
  `modify-user-task-parameters`, `register-process-element`, `enroll-sequence-participants`,
  `validate-process-graph`, `install-process-builder`.
- T14: `upload-image`, `set-logo`, `set-background-image`.
- T15: `compile-creatio`, `restart-by-environment-name`, `restart-by-credentials`, `pkg-to-db`,
  `pkg-to-file-system`, `install-gate`, `download-configuration-by-build`,
  `download-configuration-by-environment`, `add-custom-logging`, `install-dashboards-migrator`,
  `dataforge-initialize`, `dataforge-update`.

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
