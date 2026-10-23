# T16c: workspace and code-generation tools — status

Branch `t16c-workspace-1dd9c7`, from `cb64392` (2026-10-23). Decision D3 (keep W7 in clio) was reversed by
the project owner on 2026-10-23; T16c ports 12 of the 35 tools clio 8.1.0.142 serves and this server did
not.

## Done in this branch

| Tool | Where | Parity (scripts/compare-workspace-tools.py against clio 8.1.0.142) |
|---|---|---|
| `create-workspace` | `internal/workspace/create.go`, `workspace.go`, `nuget.go` | 12 calls, 126 files: match |
| `add-package` | `internal/workspace/package.go` | 3 scenarios, 20 calls, 281 files: match; one known request difference (below) |
| `new-test-project` | `internal/workspace/testproject.go` | 17 calls (with the integration tool), 71 files: match |
| `new-integration-test-project` | `internal/workspace/testproject.go` | in the scenario above: match |
| `new-ui-project` | `internal/workspace/uiproject.go` | 2 scenarios, 27 calls, 397 files: match |

MCP side: `cmd/creatio-mcp-go/tool_workspace.go` (clio's argument records bound case-insensitively, clio's
annotations ReadOnly/Destructive/Idempotent/OpenWorld = false/false/false/false, so a direct call by raw
name is gated and `clio-run` runs them). Answers are clio's command envelope.

How the generated files are kept byte-equal:
- **Templates** are not hand-copied: `scripts/generate-workspace-templates.py --clio-dll <clio.dll>` copies
  the 262 files the ported commands read from clio's `tpl/` folder (they are plain files next to clio.dll,
  not embedded resources) into `internal/workspace/templates/templates.zip` with `manifest.json` (path,
  size, SHA-256, Unix mode). The zip is embedded; every file is checked against the manifest when loaded.
  A zip keeps dotfiles embeddable and stops Git from changing line endings on Windows (`.gitattributes`).
- **.NET writers** reproduced in `internal/workspace/dotnet.go` / `xmldoc.go`: Newtonsoft and
  System.Text.Json indented JSON with each one's escaping, `XmlDocument.Save` (two-space indent, ` />`,
  no final newline), `File.WriteAllText` (no BOM, an existing file keeps its mode), the host newline, and
  LINQ's culture-aware `OrderBy` (ICU order: punctuation before digits before letters, lower case first —
  checked against clio with 15 package names, scenario `solution-order`).
- **File modes**: templates keep clio's 0744 (File.Copy keeps the mode on Unix); `*.sh` under `.solution`
  and `tasks` get `chmod +x` as clio's ExecutablePermissionsActualizer does.
- **create-workspace reads api.nuget.org** for the newest CreatioSDK version (clio binds
  `CreatioSdkOnline`, 15 s timeout); an unreachable feed is logged and the version written as null, as in
  clio. Tests point `workspace.NuGetBaseURL` at a local server.

The harness (`scripts/compare-workspace-tools.py`, scenarios in `scripts/workspace-scenarios/*.json`) runs
both servers in separate temp homes (`CLIO_HOME`, `HOME`, `TMPDIR`) on the same calls through `clio-run`,
and compares answers (JSON as data, home path and clio's `correlation-id` removed), every file under the
scenario's base folder (bytes and mode; GUIDs numbered in order of appearance, Microsoft JSON dates
replaced) and, with a fake Creatio site per side (`"stand"` rules), the requests both sent. Evidence:
`evidence/t16c-workspace.json`.

## Known differences

- `add-package` with `as-app` reading the prefix from an environment: this server's existing
  `GetSchemaNamePrefix` client sends `{"sysSettingsNameCollection": [...]}`, clio
  `{"SysSettingsNameCollection": [...]}`. Creatio accepts both (get-schema-name-prefix is in the live T22
  parity run with 0 mismatches); the same difference exists for `get-schema-name-prefix` itself.
- Like every command tool here, no `correlation-id` in the envelope and no `notifications/message` log
  stream (clio 142 adds both).
- `new-ui-project`: clio asks Creatio for its package list (`FindExistingPackage`) only to offer a download
  that a silent MCP call always declines; this server skips the request. clio's environment-less call made
  none against the fake stand either.
- The per-tenant container clio caches can keep a workspace root from an earlier call; this server takes
  the root from each call's arguments.

## In progress (parallel worktrees, merged when done)

`link-from-repository-*` (branch `t16c-link`), `generate-source-code` / `add-item-model`
(`t16c-codegen`), `push-workspace` / `restore-workspace` (`t16c-sync`).

## Not verified

- Windows: the tools build for Windows and the unit tests are written OS-neutral, but nothing ran on
  Windows (CRLF output, backslash paths in .slnx, `IsPathFullyQualified` with drive letters).
- `add-package` follow-up download (DconfChainItem) was compared only against a fake site answering an empty
  package list; its download logic is the existing `download-configuration-*` port.
