# Applications and schemas recovery status

2026-10-03, recovered from Claude worktree `agent-a3eb540b63e4d1005` into the shared migration worktree. T10 is owned by the root agent; this report covers T7/T8 only.

## Implemented tools

- T8 `create-schema`, `update-schema`: resolve package/schema, load/create designer schema, preserve the designer document, apply captions in the profile culture, validate caption script, set body, save. Body-file takes precedence, UTF-16 body lengths, dry-run, and ambiguous/unknown save outcomes follow the reference commands.
- T8 `create-sql-schema`, `update-sql-schema`, `install-sql-schema`: native SQL script designer payloads, database engine detection/explicit override, installation phase (default 1), unique schema name resolution, save verification after an unanswerable create, and `InstallSqlScripts` by schema UId.
- T8 `delete-schema`: workspace ownership/ambiguity checks or explicit remote mode, platform `GetWorkspaceItems` metadata carried to `Delete`, requires positive rowsAffected before reporting success.
- T8 `export-schema`, `import-schema`: minimum ClioGate 2.0.0.46 guard, verbatim authoritative payload bundles, descriptor identity consistency, projection files, no overwrite, explicit destination confinement, create/replace/new-layer planning, dry-run, identity mismatch and new-layer refusal.
- T7 `delete-app`: resolve application name/code or accept GUID, pass the scalar JSON application ID to AppInstallerService.UninstallApp.

All nine registrations use clio annotations, environment resolution, structured envelopes, and executor gating. No stubs were registered.

## Verification

- Live validation/refusal parity: `scripts/parity-cases/t8-schema-writes.json`, **11 match, 0 mismatches**, evidence `/tmp/t8-schema-parity-final.json`.
- HTTP mock tests cover code/SQL updates, create and SQL installation success/refusal, preservation of designer metadata, UTF-16 length, dry-run, unknown absence, deletion's platform type/positive affected-row requirement, export files/no-overwrite, import planning/dry-run/identity/new-layer refusal, and application deletion scalar payload/HTTP failure.
- MCP tests cover all nine tools by raw name (confirmation gate), `clio-run`, and `clio-run-destructive` (validation/refusal).
- Targeted internal/creatio and MCP tests passed; root runs the full repository checks.
- Write parity was attempted before unit checks with the C# scenario in `scripts/write-scenarios/t8-schema-writes.json`. The registered disposable stand fails DNS resolution. Clio rejected creation before any server object was created; update/read-back were skipped. Cleanup and a separate `--cleanup-ledger` retry also failed DNS. This is not successful write parity.
- Tentative ledger entries remain: `UsrParitytmbymjclioSource`, `UsrParitytmbymjgoSource`. No creation was confirmed. Retry cleanup-ledger when the registered stand answers; do not clear these records without checking.

## Remaining work and differences

T8 still lacks `create-entity-schema`, `create-lookup`, `modify-entity-schema-column`, `set-entity-schema-properties`, `sync-schemas`, and `update-entity-schema`. T7 still lacks `create-app`, `create-app-section`, `delete-app-section`, `install-application`, and `update-app-section`.

Successful live writes/read-backs remain unverified for every implemented tool. Export/import require ClioGate 2.0.0.46 and are only covered with mock services; no live bundle transfer was attempted. SQL installation and application deletion were not run live. No global operations were run.

Known implementation limits to close with live parity:

- Designer/transfer HTTP parser and transport exception wording uses Go errors where the reference embeds .NET exception class/parser text.
- Workspace deletion does not yet reproduce intermediate progress log lines, and remote deletion's failure diagnostics differ from clio's detailed endpoint/body text. The success path and affected-row check are covered.
- Export projection JSON indentation/escaping and provenance timestamp/version formatting have not been compared byte-for-byte; unsafe culture filenames are skipped. The authoritative schema-data.json is preserved exactly.
- Export projection I/O diagnostics currently use shorter warnings than clio; no partial authoritative bundle is left after its writes fail.
- Application uninstall failure text uses Go transport errors rather than the reference command's exception/log decoration. The reference command ignores service JSON on a successful HTTP response, which the port preserves.
- The contract inventory may lag master for create-schema body/body-file and SQL engine/installation-phase keys; handlers support those keys without changing the shared contract inventory.
