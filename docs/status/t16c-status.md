# T16c: workspace and code-generation tools — status

Branch `t16c-workspace-1dd9c7`, started 2026-10-23 from `cb64392`. Decision D3 (keep W7 in clio) was
reversed by the project owner on 2026-10-23: all 35 tools clio 8.1.0.142 serves and this server does not are
to be ported. T16c owns 12 of them.

## State: paused before any code

Stopped at the scoping step: the 5-hour usage limit read 87% (the stop rule is 85%). No Go code, generator
script or test has been written yet. Next session starts from "Plan" below.

## Scope read so far (clio 8.1.0.142 source, `~/.cache/creatio-mcp-go/clio-8.1.0.142/clio`)

| Tool | MCP surface (`Command/McpServer/Tools/`) | Command behind it | Annotations (RO/Destr/Idem/Open) | Reaches Creatio |
|---|---|---|---|---|
| `create-workspace` | `CreateWorkspaceTool.cs` (args `workspaceName`, `directory`; wrapped in `args`) | `CreateWorkspaceCommand.cs` (182 lines), `Empty = true` | F/F/F/F | no |
| `push-workspace` | `WorkspaceSyncTool.cs` (`environment-name`, `workspace-path`, `skip-backup`) | `PushWorkspaceCommand.cs` (194), `UseApplicationInstaller = true` | F/T/F/F | yes: package pack + application installer |
| `restore-workspace` | `WorkspaceSyncTool.cs` (`environment-name`, `workspace-path`) | `RestoreWorkspaceCommand.cs` (144) | F/T/F/F | yes: package download |
| `add-package` | `WorkspacePackageTool.cs` (`name`, `workspace-path`, `environment-name`, `as-app`, `build-zip-path`, `schema-name-prefix`) | `AddPackageCommand.cs` (131) | F/F/F/F | optional: SchemaNamePrefix, configuration download |
| `new-test-project` | `WorkspacePackageTool.cs` (`package-name`, `workspace-path`, `environment-name` required) | `CreateTestProjectCommand.cs` (345) | F/F/F/F | no (writes templates + `.slnx`) |
| `new-integration-test-project` | `WorkspacePackageTool.cs` (`package-name`, `workspace-path`, `target-framework` = `net10.0`) | `CreateIntegrationTestProjectCommand.cs` (122) | F/F/F/F | no |
| `new-ui-project` | `CreateUiProjectTool.cs` (`workspaceDirectory`, `projectName`, `packageName`, `vendorPrefix`, `empty`, `creatioVersion`) | `CreateUiProjectCommand.cs` (134); template by Creatio version (8.0.3 / 8.0.8 / 8.0.10 / current) | F/F/F/F | no |
| `link-from-repository-by-environment` | `LinkFromRepositoryTool.cs` (flat args `repoPath`, `packages`, `environmentName`, `dryRun`, `skipPreparation`) | `Link4RepoCommand.cs` (763) | F/T/F/F | yes: preparation (maintainer, unlock, 2fs) |
| `link-from-repository-by-env-package-path` | same file (`envPkgPath`, `repoPath`, `packages`, `dryRun`, `skipPreparation`) | `Link4RepoCommand.cs` | F/T/F/F | only without `skipPreparation` |
| `link-from-repository-unlocked` | same file (`repoPath`, `environmentName`, `dryRun`) | `Link4RepoCommand.cs`, `Unlocked = true` | F/T/F/F | yes: unlocked-package query |
| `generate-source-code` | `GenerateSourceCodeTool.cs` (`environment-name`, `modified`, `required`, `background`, `timeout` 1..10800000; legacy aliases `timeOut`, `time_out`, `timeoutMs`, `timeout-ms` plus env-name aliases refused with a rename hint; `MaxAttempts = 1`) | `GenerateSourceCodeCommand.cs` (103) | F/F/T/F | yes |
| `add-item-model` | `AddItemModelTool.cs` (`namespace`, `folder`, `environment-name`; folder must be local absolute, created if missing; output compacted to `Generated N models; requested filter: none.`) | `AddItemCommand.cs` (194), `ItemType = model`, `CreateAll` | F/T/F/F | yes: entity schemas |

Validation texts seen in the MCP layer (to reproduce verbatim): `Workspace path is required.`,
`Workspace path must be a local absolute path: …`, `Workspace path must be absolute: …`,
`Workspace path not found: …` (push/restore, via `WorkspaceCommandToolBase.ExecuteInWorkspace`); the
`workspaceDirectory …` / `packageName must be a simple identifier …` set in `CreateUiProjectTool`;
`environment-name is required for <tool> outside credential passthrough.` (link by-environment, unlocked);
`generate-source-code arguments are invalid: … Nothing was generated.` and the timeout range text;
`Folder is required.` / `Folder path must be …` / `Failed to create output folder '…': …` (add-item-model).

## Plan for the next session

1. Generator script (pattern: `scripts/generate-prompts.py`) that extracts clio's embedded template
   resources from `~/.cache/creatio-mcp-go/clio142/.store/clio/8.1.0.142/clio/8.1.0.142/tools/net10.0/any/clio.dll`
   (workspace layout and `.clio` files, `tpl/` test-project, integration-test and UI-project templates,
   `.slnx` skeletons, model templates) into an embedded Go directory with a manifest of names and SHA-256.
2. Local-only tools first (no Creatio): `create-workspace`, `new-test-project`,
   `new-integration-test-project`, `new-ui-project`, `add-package` without environment. Compare file by file
   with `dotnet clio.dll` 142 run on the same inputs in temp dirs with a temp `HOME`/`CLIO_HOME`.
3. `link-from-repository-*`: symlinks on macOS/Linux, junctions on Windows (windows CI job);
   `skipPreparation=true` + `dryRun` paths first, preparation against a fake stand.
4. Creatio-reaching tools against HTTP fixtures / `scripts/fake-stands`: `generate-source-code`,
   `add-item-model` (entity-schema fixtures), `restore-workspace` (package download), `push-workspace`
   (pack format byte-equal with clio's archiver, installer requests compared with clio's).
5. Long-operation progress where clio's tool runs in a worker (`Location = Worker`): push, restore,
   add-package, link, generate-source-code, add-item-model.

Files not to touch (other sessions): `environments.go`, `internal/creatio/client.go`, `internal/knowledge`,
`internal/hosttools`, T16a/T16b files, page-smoke.

## Not verified

Nothing is implemented, so nothing is verified. Checks (`check-public-safety.sh`, `go vet`, `go test -race`,
windows build, pytest, `compare-contracts.py`) were not run for this commit: it changes only this file.
