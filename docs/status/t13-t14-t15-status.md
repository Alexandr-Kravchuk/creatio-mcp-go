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
