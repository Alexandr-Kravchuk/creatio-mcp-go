# Installation and usage

The short version is in the [README](../README.md). This page covers each step in full.

## 1. Download

Take the archive for your platform from [Releases](https://github.com/Alexandr-Kravchuk/creatio-mcp-go/releases):
macOS (arm64, amd64), Linux (amd64, arm64) or Windows (amd64). Check it against `SHA256SUMS` from the
same release, unpack it, and note the full path to `creatio-mcp-go` (`creatio-mcp-go.exe` on Windows).

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
tar -xzf creatio-mcp-go_0.1.0_darwin_arm64.tar.gz
```

On Windows, compare `(Get-FileHash .\creatio-mcp-go_0.1.0_windows_amd64.zip).Hash` with the line in
`SHA256SUMS`, then unpack the zip.

The macOS binaries are signed with a Developer ID certificate and notarized by Apple, so they start
as downloaded; macOS checks the notarization online on the first launch. The Windows binary is not
signed: if Windows marks it as blocked, run `Unblock-File .\creatio-mcp-go.exe` in PowerShell.

## 2. Pick the connection settings

One server process serves every environment registered in clio, the same way clio does: each call
names its target with `environment-name` (`get-fsm-mode` takes `environmentName`, as in clio).

**clio's settings file.** The server reads clio's `appsettings.json` and never writes it:

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

tools/list carries the same resident tools as clio's, in clio's order and with clio's descriptions,
input schemas and annotations — 15 of clio's 19 for now (`get-component-info`, `get-request-info`,
`merge-creatio-artifact` and `clio-run-destructive` are not ported yet). The rest
are called by name, directly or through `clio-run`, and `get-tool-contract` returns clio's contract for
each tool served here. Every tool takes its arguments flat or wrapped as `{"args": {...}}`, the shape
tools/list publishes:

| Tool | What it reads |
|---|---|
| `list-environments` | Environments registered in clio's `appsettings.json`, secrets masked |
| `list-apps` | Installed applications |
| `list-packages` | Packages, with name filter and paging |
| `list-app-sections` | Sections of one application |
| `list-pages` | Freedom UI pages by package, application or name |
| `get-entity-schema-properties` | Columns of an entity schema |
| `execute-esq` | Any DataService SelectQuery (ESQ) passed as JSON |
| `get-sql-schema` | Body of an SQL script schema |
| `list-package-files`, `get-package-file` | Package files; need cliogate 2.0.0.47+ on the site |
| `odata-read` | OData entity reads with projection, ordering and paging |
| `get-app-info`, `find-app` | One application's package, entities and pages; search applications |
| `find-entity-schema`, `get-entity-schema-column-properties` | Find entity schemas; one column's properties |
| `list-entity-client-schemas` | Client (page) schemas built on an entity |
| `get-page` | A Freedom UI page, written to `.clio-pages/<schema>/` as `body.js`, `bundle.json`, `meta.json` |
| `read-entity-business-rules`, `read-page-business-rules` | Business rules of an entity or a page |
| `get-record-rights` | Access rights on one record |
| `get-process-signature` | Parameters of a business process |
| `get-sys-setting`, `list-sys-settings` | System setting values |
| `get-user-culture`, `get-schema-name-prefix`, `get-target-package` | Current user culture, schema name prefix, package new schemas go to |
| `describe-environment` | Creatio version, runtime, current user and cliogate state |
| `list-themes`, `list-printables`, `list-user-tasks`, `list-page-templates` | Themes, printables, process user tasks, page templates |
| `get-page-hierarchy`, `get-client-unit-schema`, `get-schema` | Page layer chain; client and other schema bodies, optionally to `output-file` |
| `validate-page` | Offline check of a Freedom UI page body; covers only part of clio's rules and says which |
| `get-classic-page-sources`, `get-classic-list-columns` | Classic UI page sources; Classic section list columns |
| `describe-business-process`, `get-process-page-facts` | Process graph; what a page offers to a process |
| `get-sequence-context`, `read-data-binding-db`, `get-email-template`, `get-related-page-addon` | Sequences, package data bindings, email templates, related-page add-ons |
| `inspect-user`, `inspect-role`, `inspect-license`, `inspect-access` | Users, roles, licenses, operation rights |
| `check-theming-access`, `get-theme` | Theming rights; one theme's CSS |
| `last-compilation-log`, `get-fsm-mode` | Last compilation result; file-system mode |
| `compile-status`, `restart-status` | Status of jobs this server started; it starts none, so these answer not-found |
| `resolve-oauth-system-user`, `verify-oauth-app` | OAuth technical user; check an OAuth client can get a token |
| `dataforge-status`, `dataforge-context`, `dataforge-find-tables`, `dataforge-find-lookups`, `dataforge-get-relations`, `dataforge-get-table-columns` | Data Forge state and lookups through Creatio |
| `find-empty-iis-port`, `start-creatio` | Local machine: free IIS port, start a local Creatio |

Nothing here writes to Creatio. `get-page` writes local files, the way clio does, under
`output-directory` or the workspace root; tools with `output-file` write only that file and never
overwrite one. `start-creatio` starts a local process; the `-write-probe`
command-line flag inserts and deletes a test record and is not meant for everyday use.

Found an answer that differs from clio's for the same call? Open an issue with the tool name and the
arguments. To compare many calls at once, run `scripts/compare-mcp.py` against an environment
registered in clio; write tools are compared with `scripts/compare-mcp-writes.py` on an allow-listed
disposable stand (see [parity.md](parity.md)). How releases are built, signed and published:
[releasing.md](releasing.md).

## 6. Guidance, prompts and resources

The server sends clio's `initialize` instructions, which tell the agent to read `get-guidance`
`core-rules` and `routing` before any operation. The guidance comes from the clio-knowledge bundle that
**clio** installed; this server reads the same cache and never downloads, updates, repairs or prunes it.

- Where: clio's home — `CLIO_HOME`, else `~/creatio/clio` (macOS, Linux) or `%LOCALAPPDATA%\creatio\clio`
  (Windows) — its `appsettings.json` section `knowledge` (sources, `root-path`, `topic-pins`) and the
  installed generations under `knowledge/sources/`.
- Prerequisite: run clio once so it installs the curated library — start `clio mcp-server` or run
  `clio install-knowledge`. Until then `get-guidance` answers `success: false` with
  `errorCode: guidance-unavailable` and a `diagnostics` text saying why. `clio update-knowledge` brings a
  newer generation; this server picks it up on the next call, without a restart.
- Trust: every generation is verified on activation exactly as clio verifies it — the ECDSA P-256 manifest
  signature (the built-in `com.creatio.clio` library only with the key pinned in clio, other libraries
  with the key file their source configures), the SHA-256 and length of every file, no unsigned files,
  the compatibility range (this server answers it as clio `8.1.0`, MCP tool contract `1.1.0`) and the
  required tools. A refused generation falls back to the previous one, as in clio.
- Not read: Git-type sources (clio reads their checkout directly). `info-knowledge` with
  `checkUpdates: true` reports `unknown`; use `clio info-knowledge --check-updates`.

Served from the bundle: `get-guidance`, the `docs://knowledge/{libraryId}/{itemId}` and legacy
`docs://mcp/guides/...`, `docs://mcp/references/...` resources (`resources/list` pages them 100 at a
time, as clio does), `list-knowledge-sources`, `info-knowledge`, `list-knowledge-examples`,
`get-knowledge-feedback-policy`. `get-telemetry-consent` reads clio's `telemetry/consent.json`. These
tools ignore `environment-name`: they read only local clio state.

The 74 MCP prompts are clio's, with the same names, arguments and text. `docs://help/command/{name}` is
clio's CLI help: it is read from `help/en` of the newest clio installed as a .NET tool
(`~/.dotnet/tools/.store/clio/...`); without one the answer is "`<name>` command does not provide
documentation.".

Still only in clio: installing, updating and deleting knowledge (`install-knowledge`, `update-knowledge`,
`delete-knowledge`), managing sources (`add-`, `remove-`, `enable-`, `disable-knowledge-source`),
`configure-knowledge-feedback-policy`, `send-telemetry`, `withdraw-telemetry-consent`.
