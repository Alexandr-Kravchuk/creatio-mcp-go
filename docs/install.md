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

One server process works with **one** Creatio environment, set through environment variables. Unlike
clio, tools take no `environment-name`; to work with several environments, register the server
several times under different names.

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

Ask the agent, for example: *"Call list-apps of creatio-go and tell me how many applications there
are."* A working setup answers with `success: true` and the list. A wrong URL, password or runtime
setting does not stop the server from starting; the first call then fails with the reason, for
example `forms login rejected credentials` or `DataService SelectQuery returned HTTP 404`.

Only three tools are listed up front — `list-apps`, `clio-run` and `get-tool-contract`, as in clio.
The rest are called by name, directly or through `clio-run`, and `get-tool-contract` returns their
input schemas:

| Tool | What it reads |
|---|---|
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
| `find-empty-iis-port`, `start-creatio` | Local machine: free IIS port, start a local Creatio |

Nothing here writes to Creatio. `get-page` writes local files, the way clio does, under
`output-directory` or the workspace root. `start-creatio` starts a local process; the `-write-probe`
command-line flag inserts and deletes a test record and is not meant for everyday use.

Found an answer that differs from clio's for the same call? Open an issue with the tool name and the
arguments. To compare many calls at once, run `scripts/compare-mcp.py` against an environment
registered in clio. Release archives are built with `scripts/build-release.sh <tag>`.
