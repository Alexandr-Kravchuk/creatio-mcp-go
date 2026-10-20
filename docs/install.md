# Installation and usage

The short version is in the [README](../README.md). This page covers each step in full.

## 1. Download

Take the archive for your platform from [Releases](https://github.com/Alexandr-Kravchuk/creatio-mcp-go/releases):
macOS (arm64, amd64), Linux (amd64, arm64) or Windows (amd64). Check it against `SHA256SUMS` from the
same release, unpack it, and note the full path to `creatio-mcp-go` (`creatio-mcp-go.exe` on Windows).

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
tar -xzf creatio-mcp-go_0.2.0_darwin_arm64.tar.gz
```

On Windows, compare `(Get-FileHash .\creatio-mcp-go_0.2.0_windows_amd64.zip).Hash` with the line in
`SHA256SUMS`, then unpack the zip. Archives are named `creatio-mcp-go_<version>_<os>_<arch>`
(`scripts/build-release.sh`); `creatio-mcp-go --version` prints the release tag the binary was built from
(a local `go build` prints `unstamped`).

The macOS binaries are signed with a Developer ID certificate and notarized by Apple, so they start
as downloaded; macOS checks the notarization online on the first launch. The Windows binary is not
signed: if Windows marks it as blocked, run `Unblock-File .\creatio-mcp-go.exe` in PowerShell.

## 2. Pick the connection settings

One server process serves every environment registered in clio, the same way clio does: each call
names its target with `environment-name` (`get-fsm-mode` takes `environmentName`, as in clio).

**clio's settings file.** The server reads clio's `appsettings.json` for the environments. Only the
knowledge tools and `experimental` write it (section 6), under clio's own file lock:

| Platform | File |
|---|---|
| macOS, Linux | `$HOME/creatio/clio/appsettings.json` |
| Windows | `%LOCALAPPDATA%\creatio\clio\appsettings.json` |
| Any, when `CLIO_HOME` is set | `$CLIO_HOME/appsettings.json` |

Register environments with clio (`clio reg-web-app <name> -u <url> -l <login> -p <password>`, or with
`--client-id`, `--client-secret` and `--auth-app-uri` for OAuth). The server reads `Uri`, `Login`,
`Password`, `ClientId`, `ClientSecret`, `AuthAppUri`, `IsNetCore` and `Safe` of each environment:

- an environment with `ClientId` uses OAuth client credentials, otherwise forms login; for a
  `*.creatio.com` site without `AuthAppUri` the token endpoint is the site's `-is` identity service,
  as in clio;
- names match ignoring case; an unknown name is answered with clio's text, which lists the registered
  names;
- the file is read again when it changes, so an environment registered while the server runs is
  available on the next call;
- each environment gets one authenticated session, reused by every call to it, also by parallel ones;
- an environment marked `Safe` is refused, as clio refuses it without an interactive confirmation.

The `list-environments` tool shows the registered environments, with passwords and client secrets
masked, exactly as clio's tool does. The tools whose clio counterpart accepts `uri`, `login` and
`password` (and `describe-environment` also `client-id`, `client-secret`, `auth-app-uri`) accept them
too, for a one-off connection without a registered name.

**A call without `environment-name`** goes to the default target:

1. the environment set by the `CREATIO_*` variables below, when `CREATIO_URL` is set;
2. otherwise clio's active environment (`ActiveEnvironmentKey` in `appsettings.json`).

clio itself refuses such a call ("Either a configured environment name or an explicit URI is
required..."); this server keeps the default so a registration with `CREATIO_*` variables, as in
earlier releases, keeps working.

| Variable | Value |
|---|---|
| `CREATIO_URL` | Site root, for example `https://your-site.creatio.com` (no `/0`) |
| `CREATIO_LOGIN`, `CREATIO_PASSWORD` | Forms login — or, instead, the three OAuth variables below |
| `CREATIO_CLIENT_ID`, `CREATIO_CLIENT_SECRET` | OAuth client-credentials app of the site |
| `CREATIO_AUTH_APP_URI` | Token endpoint, `https://<identity-service>/connect/token` |
| `CREATIO_IS_NET_CORE` | `true` only for a .NET Core site, whose service URLs have no `/0` prefix |

Set either the login pair or the OAuth triple, not both. An OAuth client for a site can be created
with `clio create-server-to-server-oauth-app -e <environment>`. The commands below put the password
into your shell history and into the client's config file in plain text, the same way clio stores it
in `appsettings.json`.

## 3. Claude Code

With environments registered in clio, no variables are needed:

```bash
claude mcp add creatio-go -- /path/to/creatio-mcp-go
```

With one default environment set by variables:

```bash
claude mcp add creatio-go --env CREATIO_URL=https://your-site.creatio.com --env CREATIO_LOGIN=example-user --env CREATIO_PASSWORD=replace-me -- /path/to/creatio-mcp-go
```

This registers the server for the current project only. Add `--scope user` to make it available in
every project, or `--scope project` to write it to `.mcp.json` for the whole team (then keep
credentials out of the file you commit). With OAuth, replace the two login variables with
`--env CREATIO_CLIENT_ID=… --env CREATIO_CLIENT_SECRET=… --env CREATIO_AUTH_APP_URI=…`.

Check it: `claude mcp list` must show `creatio-go: … ✔ Connected`. Remove it with
`claude mcp remove creatio-go`.

## 4. Codex

```bash
codex mcp add creatio-go --env CREATIO_URL=https://your-site.creatio.com --env CREATIO_LOGIN=example-user --env CREATIO_PASSWORD=replace-me -- /path/to/creatio-mcp-go
```

This adds the server to `~/.codex/config.toml` for every project. The same entry can be written by hand:

```toml
[mcp_servers.creatio-go]
command = "/path/to/creatio-mcp-go"
# Optional: run the tools without asking each time. Codex asks by default, and
# `codex exec` without this line refuses every tool call instead of asking.
default_tools_approval_mode = "approve"

[mcp_servers.creatio-go.env]
CREATIO_URL = "https://your-site.creatio.com"
CREATIO_LOGIN = "example-user"
CREATIO_PASSWORD = "replace-me"
```

Check it: `codex mcp list` must show `creatio-go` as `enabled`. Remove it with
`codex mcp remove creatio-go`.

On Windows, use the full path to the executable in both clients, for example
`C:\Tools\creatio-mcp-go\creatio-mcp-go.exe`.

## 5. Try a call

Ask the agent, for example: *"Call list-environments of creatio-go, then list-apps of the first
environment and tell me how many applications there are."* A working setup answers with
`success: true` and the list. A wrong URL, password or runtime setting does not stop the server from
starting; the first call then fails with the reason, for example `forms login rejected credentials` or
`DataService SelectQuery returned HTTP 404`.

## 6. Which clio this matches

The tool contracts are clio **8.1.0.142**'s: `docs/clio-inventory.json` records that version's live
tools/list, get-tool-contract index and every contract, and the binary embeds it
(`internal/cliocontract`). tools/list carries all 19 of clio's resident tools, in clio's order and with
clio's descriptions, input schemas and annotations. `get-tool-contract` answers in clio 8.1.0.142's shape
(the one-line `tool-index` with its `legend` by default; `detail=index-entries`, `full` or `short`), but
only for the tools served here. Every tool takes its arguments flat or wrapped as `{"args": {...}}`, the
shape tools/list publishes.

clio's settings file and knowledge store are shared with the clio you have installed. For tools that
write them, run a clio of the same version (8.1.0.142) next to this server: the file locks, store layout
and settings keys are 8.1.0.142's.

What is served, by area (the full per-tool list with the task that ported it is in
[clio-inventory.md](clio-inventory.md) and the appendix of [migration-plan.md](migration-plan.md)):

| Area | Examples |
|---|---|
| Environments and contracts | `list-environments`, `describe-environment`, `get-tool-contract`, `clio-run`, `clio-run-destructive` |
| Applications | `list-apps`, `find-app`, `get-app-info`, `create-app`, `create-/update-/delete-app-section`, `delete-app`, `install-application`, `place-section-in-workplace` |
| Entity and other schemas | `find-entity-schema`, `get-entity-schema-properties`, `create-entity-schema`, `create-lookup`, `update-entity-schema`, `modify-entity-schema-column`, `create-/update-/delete-schema`, `create-/update-/install-sql-schema`, `export-/import-schema` |
| Pages | `list-pages`, `get-page`, `create-page`, `update-page`, `sync-pages`, `validate-page` (a declared subset of clio's rules), `create-related-page-addon`, `create-user-task-page`, `get-component-info(-to-file)`, `list-entity-client-schemas(-to-file)`, `localize-page`, `merge-creatio-artifact` |
| Business rules | `read-/create-/update-/delete-entity-business-rules`, the same four for pages |
| Data | `execute-esq(-to-file)`, `odata-read(-to-file)`, `odata-create`, `odata-update`, `odata-delete`, `execute-dataservice-batch`, `execute-sql-script`, data bindings (`create-data-binding(-db)`, `add-/remove-/upsert-…-row`), `run-process`, `get-request-info(-to-file)` |
| Settings, users, access | `get-/list-/create-/update-sys-setting(s)`, `set-fsm-mode`, `manage-user`, `manage-role`, `manage-license`, `manage-access`, `inspect-*`, `get-/set-record-rights`, `get-/set-object-rights`, `set-/apply-default-record-rights`, `get-/update-entity-connections`, OAuth and identity tools |
| Processes | `describe-business-process`, `create-business-process`, `modify-business-process(-as-new-version)`, `set-active-business-process-version`, `generate-process-model`, user tasks, `validate-process-graph`, `install-process-builder`, `trigger-smoke` |
| Themes, branding, email | `list-themes`, `get-/create-/update-/delete-theme`, `build-theme`, `set-logo`, `set-background-image`, `upload-image`, `advise-theme-palette`, email templates |
| Packages, compile, restart | `list-packages`, `create-package`, package files, `add-/remove-package-dependency`, `compile-creatio`, `compile-status`, `restart-by-environment-name`, `restart-by-credentials`, `restart-status`, `pkg-to-db`, `pkg-to-file-system`, `install-gate`, hotfix, `download-configuration-*`, Data Forge |
| Knowledge, toolkit, telemetry | `get-guidance`, `install-/update-/delete-knowledge`, `add-/remove-/enable-/disable-knowledge-source`, `install-/update-/delete-toolkit`, `experimental`, `export-component-registry`, `get-telemetry-consent`, `send-telemetry`, `withdraw-telemetry-consent` |
| Local machine | `find-empty-iis-port`, `start-creatio` |

Not served in this release:

- clio's local-machine, infrastructure and workspace tools (`deploy-creatio`, `uninstall-creatio`,
  `restore-db-*`, `clear-redis-*`, `create-/push-/restore-workspace`, `reg-web-app`, `stop-creatio`,
  `list-creatio-builds`, browser-session tools, …): 33 tools, kept in clio by decision D3
  ([migration-plan.md](migration-plan.md#decisions-2026-10-03));
- `page-smoke` and `get-mobile-page-conversion-guide`, new in clio 8.1.0.142: the first needs a headless
  Chromium driven over the DevTools protocol, the second clio's web-to-mobile analysis engine
  ([status/t21a-page-package-status.md](status/t21a-page-package-status.md#not-ported)).

`odata-read` and `execute-esq` answer as clio 8.1.0.142's: `odata-read` takes its arguments (`filters`,
`expand`, `order-by`, …) and returns its shape (`success`, `count`, `value`, `next-link`, …), not v0.1.x's
`{rows, count}`; the old `orderBy` key is refused with clio's rename hint
([release notes](release-notes-v0.2.0.md#breaking-changes)).

Calling a tool that is not served answers clio's "unknown tool" refusal with suggestions; use clio for it.

## 7. Writes and their safety

The write tools change the Creatio environment the call names, as clio's do. The same rules apply:

- `clio-run` and `clio-run-destructive` run any tool, as in clio, and record which tool they
  ran in `_meta["clio-run"]`;
- a direct call by raw name to a hidden tool that is not read-only is refused with `confirmation-required`
  and a pointer to `clio-run`, as clio refuses it;
- an environment marked `Safe` in clio's settings is refused;
- `get-page` and the tools with `output-file` / `output-directory` write local files the way clio does
  and never overwrite an existing `output-file` (`get-sql-schema` included: the path must be inside the
  workspace or the OS temp directory, and the body is then left out of the answer);
- long operations (compile, restart, installs, process runs) report progress and answer "still running"
  after a response deadline while the work continues; `compile-status` / `restart-status` read the
  operations this process started. `CLIO_MCP_RESPONSE_DEADLINE_SECONDS` overrides the deadline as in clio
  (defaults in `cmd/creatio-mcp-go/operations.go` and `tool_pkg_write_restart.go`).

The `-write-probe` command-line flag inserts and deletes a test record and is not meant for everyday use.

What was compared with clio and how is recorded per area in [status/](status/). Many write success paths
are checked against HTTP fixtures, a canned Creatio or a fake stand rather than a live site; the area
reports say which.

Found an answer that differs from clio's for the same call? Open an issue with the tool name and the
arguments. To compare many calls at once, run `scripts/compare-mcp.py` against an environment
registered in clio; write tools are compared with `scripts/compare-mcp-writes.py` on an allow-listed
disposable stand (see [parity.md](parity.md)). How releases are built, signed and published:
[releasing.md](releasing.md).

## 8. Guidance, prompts and resources

The server sends clio's `initialize` instructions, which tell the agent to read `get-guidance`
`core-rules` and `routing` before any operation. The guidance comes from the clio-knowledge bundles in
clio's knowledge store; this server reads the same store and, through the knowledge tools, installs,
updates and deletes bundles in it the way clio does.

- Where: clio's home — `CLIO_HOME`, else `~/creatio/clio` (macOS, Linux) or `%LOCALAPPDATA%\creatio\clio`
  (Windows) — its `appsettings.json` section `knowledge` (sources, `root-path`, `topic-pins`) and the
  installed generations under `knowledge/sources/`.
- Prerequisite: the curated library must be installed. clio installs it when `clio mcp-server` starts and
  updates it hourly; this server does neither on its own. Run `clio install-knowledge` (or start
  `clio mcp-server` once). This server's `install-knowledge` through `clio-run` does the same, but its
GitHub-release and NuGet transports are tested only against local fixtures, never against the real
publishers ([status/t6-write-status.md](status/t6-write-status.md)), so prefer clio for this. Until then
  `get-guidance` answers `success: false` with `errorCode: guidance-unavailable` and a `diagnostics` text
  saying why. A newer generation installed by either server is picked up on the next call, without a
  restart.
- Trust: every generation is verified on activation exactly as clio verifies it — the ECDSA P-256 manifest
  signature (the built-in `com.creatio.clio` library only with the key pinned in clio, other libraries
  with the key file their source configures), the SHA-256 and length of every file, no unsigned files,
  the compatibility range (this server answers it as clio `8.1.0`, MCP tool contract `1.1.0`) and the
  required tools. A refused generation falls back to the previous one, as in clio.
- Writes: `install-`, `update-`, `delete-knowledge`, `add-`, `remove-`, `enable-`, `disable-knowledge-source`
  and `configure-knowledge-feedback-policy` write clio's store and `appsettings.json` under clio's own
  OS locks, so clio and this server can run at the same time on one home
  ([status/t6-write-status.md](status/t6-write-status.md)). Installing and updating use the GitHub-release
  and NuGet transports. `info-knowledge` with `checkUpdates: true` contacts the publisher.
- Not ported: Git-type sources. They can be added, enabled, disabled, removed and deleted, but
  `install-knowledge` / `update-knowledge` answer with a pointer to `clio install-/update-knowledge`, and
  the runtime does not read their checkout.

Served from the bundle: `get-guidance`, the `docs://knowledge/{libraryId}/{itemId}` and legacy
`docs://mcp/guides/...`, `docs://mcp/references/...` resources (`resources/list` pages them 100 at a
time, as clio does), `list-knowledge-sources`, `info-knowledge`, `list-knowledge-examples`,
`get-knowledge-feedback-policy`. The knowledge and telemetry tools ignore `environment-name`: they work
only on local clio state.

Telemetry: `get-telemetry-consent`, `send-telemetry` and `withdraw-telemetry-consent` use clio's consent
file, outbox and run state. Events carry `creatio-mcp-go/<build>` as `clio_version`.

The 74 MCP prompts are clio 8.1.0.142's, with the same names, arguments and text. `docs://help/command/{name}`
is clio's CLI help: it is read from `help/en` of the newest clio installed as a .NET tool
(`~/.dotnet/tools/.store/clio/...`); without one the answer is "`<name>` command does not provide
documentation.".

Toolkit: `install-`, `update-`, `delete-toolkit` set up clio's AI toolkit for Claude Code, Codex and Cursor
by running the same CLI commands and writing the same files clio writes.
