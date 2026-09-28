# creatio-mcp-go

An MCP server for the [Creatio](https://www.creatio.com) low-code platform, written in Go. It
reimplements part of [clio](https://github.com/Advance-Technologies-Foundation/clio)'s MCP server:
the same tool names and response shapes, but as one static binary that needs no .NET runtime and
talks to Creatio over its documented HTTP endpoints.

**Status: preview.** It covers a set of read tools, and on live Creatio environments they return the
same data as clio (see [Answer and evidence](#2-answer-and-evidence)). It is published so people can
try it and report where it answers differently from clio.

## Install

### 1. Download

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

### 2. Pick the connection settings

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

### 3. Claude Code

```bash
claude mcp add creatio-go --env CREATIO_URL=https://your-site.creatio.com --env CREATIO_LOGIN=example-user --env CREATIO_PASSWORD=replace-me -- /path/to/creatio-mcp-go
```

This registers the server for the current project only. Add `--scope user` to make it available in
every project, or `--scope project` to write it to `.mcp.json` for the whole team (then keep
credentials out of the file you commit). With OAuth, replace the two login variables with
`--env CREATIO_CLIENT_ID=… --env CREATIO_CLIENT_SECRET=… --env CREATIO_AUTH_APP_URI=…`.

Check it: `claude mcp list` must show `creatio-go: … ✔ Connected`. Remove it with
`claude mcp remove creatio-go`.

### 4. Codex

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

### 5. Try a call

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
| `find-empty-iis-port`, `start-creatio` | Local machine: free IIS port, start a local Creatio |

Nothing here writes to Creatio. `start-creatio` starts a local process; the `-write-probe` command-line
flag inserts and deletes a test record and is not meant for everyday use.

Found an answer that differs from clio's for the same call? Open an issue with the tool name and the
arguments. To compare many calls at once, run `scripts/compare-mcp.py` against an environment
registered in clio. Release archives are built with `scripts/build-release.sh <tag>`.

## Background: the research behind it

This repository started as a pilot that answered whether clio's MCP behaviour can be reproduced
without clio's .NET dependencies. The sections below keep that record.

### 1. Question

Can clio's `list-apps` behaviour be reproduced from Go, without `ATF.Repository` or `creatio.client`, with output matching clio?

### 2. Answer and evidence

**Answered: yes, with full parity for both forms authentication and OAuth client credentials.**

#### What is established, by decompiling the vendor assembly

`ATF.Repository` encapsulates no private protocol. Decompiled from
`ATF.Repository.dll` 2.0.3.5 (`netstandard2.0`) with `ilspycmd`, `RemoteDataProvider` declares five
plain HTTP endpoints and nothing else:

| Member | Endpoint |
|---|---|
| `SelectEndpointUri` | `/DataService/json/SyncReply/SelectQuery` (`/0/…` on .NET Framework) |
| `BatchEndpointUrl` | `/DataService/json/SyncReply/BatchQuery` |
| `SysSettingEndpointUrl` | `/DataService/json/SyncReply/QuerySysSettings` |
| `FeatureEndpointUrl` | `/rest/FeatureService/GetFeatureState` |
| `RunProcessEndpointUrl` | `/ServiceModel/ProcessEngineService.svc/RunProcess` |

`GetItems(ISelectQuery)` serialises the query and calls `ExecutePostRequest(url, requestData, 1800000)`
against the first of them. The same routes are already registered independently in clio's own
`ServiceUrlBuilder` (`KnownRoute.Select`, `KnownRoute.BatchQuery`).

**Therefore the vendor assembly is a query builder and serialiser over documented HTTP endpoints, not a
carrier of unreachable behaviour.** Reproducing it from another language is work — translating an
expression tree into the `SelectQuery` JSON shape — not an access problem. The kill criterion stated in
section 4 was **not** hit.

Note the provenance correction: an earlier draft of this file attributed the endpoint choice to "the C#
reference". It cannot come from there — `InstalledApplicationQueryService.cs` only calls
`ATF.Repository`. The endpoints above come from the decompiled assembly, which is why they are stated
with a version and a tool.

#### The live comparison — full parity

Against a Creatio 10.1.725.0 environment (`IsNetCore=false`), forms auth:

| | clio | Go |
|---|---|---|
| rows | 65 | 65 |
| rows only on this side | 0 | 0 |
| rows matching exactly | 65 of 65 | |
| field mismatches (`name`, `code`, `version`, `description`) | none | |
| SHA-256 of the canonicalised set | `7d85ed7d3d4b2843` | `7d85ed7d3d4b2843` |

The two fingerprints are equal. See `evidence/latest.json`.

#### Two things a naive reimplementation gets wrong, both found by hitting them

Reaching parity took two corrections, and neither is guessable from the C# — both were confirmed by
decompiling the vendor assemblies:

1. **The authentication route is a site-root exception.** Every DataService route takes a `/0/` prefix on
   .NET Framework; `/ServiceModel/AuthService.svc/Login` does **not**. Applying the prefix uniformly —
   the obvious thing to do — answers **HTTP 401** and says nothing about why.
2. **Forms-authenticated DataService requires the `BPMCSRF` header.** The vendor client reads the
   `BPMCSRF` cookie from the login response and echoes it on every subsequent request. Without it the
   server answers **HTTP 403**, again with no indication of what is missing.

That is the real cost a rewrite pays: not the protocol, which is plain HTTP, but a set of undocumented
conventions that are only discoverable by decompiling or by failing.

#### What is still NOT established

The first live list-apps parity above was forms-auth only. OAuth client credentials were exercised
later, on a cloud environment with its own identity service, through a server-to-server client created
with clio `create-server-to-server-oauth-app`: `compare-with-clio.sh` reported a match (42 of 42 rows)
and the MCP comparison below found no data differences. .NET Core routing (no `/0/` prefix) is still
covered only by mocked tests.

#### Live MCP-to-MCP comparison (2026-09-28)

`scripts/compare-mcp.py` starts clio `mcp-server` and this server over stdio, sends both the calls in
`scripts/mcp-parity-cases.json` against one registered clio environment, and compares the answers after
normalising serializer conventions (key case, `-`/`_`, absent versus empty values). It prints and writes
to `evidence/mcp-latest.json` only verdicts, timings and differing JSON paths — never values, URLs,
environment names or credentials.

Run against clio 8.1.0.134, all environments on .NET Framework, 19 cases each:

| | on-premises, ClioGate, forms | on-premises, no ClioGate, forms | cloud, no ClioGate, forms | same cloud, OAuth |
|---|---|---|---|---|
| same data (`match`) | 12 | 11 | 11 | 11 |
| both refused, wording differs (`both-failed`) | 6 | 8 | 8 | 8 |
| same data, a nested diagnostic worded differently (`error-text`) | 1 | 0 | 0 | 0 |
| different data (`mismatch`) | 0 | 0 | 0 | 0 |

Every read that returned data — `list-apps`, `list-packages`, `list-app-sections`, `list-pages`,
`get-entity-schema-properties`, `execute-esq`, `list-package-files`, `get-package-file` — returned the
same data from both servers. Four differences found on the way were fixed rather than normalised away:
the MCP `list-apps` now returns clio's `{success, applications}` envelope with an empty version kept
empty, in clio's `StringComparer.OrdinalIgnoreCase` order — lower-casing put `Custom_…` before
`Customer 360`, because `_` sorts before lower-case letters but after upper-case ones (the CLI `--list-apps-json` mode keeps `"none"`); a missing or failed ClioGate route
now names the cause as clio does instead of reporting a bare HTTP status; and `get-sql-schema` always
carries `bodyLength`. Single-run timings, not a benchmark: clio starts in 0.6 s, this server in 0.01 s.
The first call and every `list-app-sections`, `list-pages` and `execute-esq` call are faster here
(1.3–8 s versus 0.03–0.3 s). Repeated small reads are not: after its first call clio answers
`list-packages` and `get-entity-schema-properties` in 0.04–0.07 s, and this server takes 0.03–0.12 s,
because it re-reads every SysPackage row on each call while clio appears to keep results in-process.
The comparison used the global clio tool; a newer clio can be compared by pointing `--clio-dll` at it.

`list-packages` is the exception: clio orders it with the culture-sensitive default comparer, which the
lower-cased order matched on all four runs but is not guaranteed to match for every name.

Still not covered by a live run: .NET Core routing (no such environment was available), a
uniquely named SQL script (every candidate was reported as ambiguous by both servers), the Windows host
tools, and `odata-read`, which clio does not expose as an MCP tool.

`scripts/compare-with-clio.sh` is the reproducible CLI evidence harness: it runs clio `list-apps --json`
and this client against the same environment, normalises both result sets (key case and `null` versus
empty, which clio 8.1 changed), and writes only counts, SHA-256 fingerprints and mismatch counts to
`evidence/latest.json` — never raw application data, URLs or credentials. The file is not committed;
rerun the harness before using this result against another Creatio version or authentication
configuration.

#### Build status

Builds against `github.com/modelcontextprotocol/go-sdk v1.0.0`, Go 1.27.1, darwin/arm64. The resulting
static binary is 11.5 MB and needs no runtime installed — the single-binary property that motivated
choosing Go for the pilot, measured rather than assumed.

This client makes transport, HTTP, HTML-login-page, malformed-JSON and `success:false` failures loud. It
deliberately does not inherit the vendor behaviour documented in clio's knowledge base, where
`RemoteDataProvider` returns `Success=false` with an empty payload instead of throwing, and the consumer
side then drops the flag — turning a rejected read into an empty successful list.

### 3. What this does not prove

This is still a small read-only slice. It says nothing about write parity, package installation, most
IIS/DISM/PowerShell operations, or parity for clio's 202 MCP tools.

Authentication uses one session per process and reauthenticates once after an authentication refusal.
Each structured tool response includes one JSON text block alongside its structured content.

### 4. Kill criterion

If `list-apps` cannot be reproduced without a vendor assembly for both authentication modes clio supports (forms authentication and OAuth client credentials), a full rewrite should stop here. **It has passed for both: forms authentication on-premises and in the cloud, and OAuth client credentials in the cloud** (see "Live MCP-to-MCP comparison"). The criterion is met; it does not by itself justify a rewrite, which still depends on write parity and the remaining tool catalog.

## Implementation notes

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) over stdio. Its resident list is `list-apps`, `clio-run`, and `get-tool-contract`; `odata-read`, `find-empty-iis-port`, `start-creatio`, `execute-esq`, `get-entity-schema-properties`, `get-package-file`, `get-sql-schema`, `list-app-sections`, `list-package-files`, `list-packages`, and `list-pages` are available through `clio-run` or by raw tool name, and their schemas are returned on demand by `get-tool-contract`. The protocol probes establish progress-token correlation, response `_meta`, cancellation, and hidden-name dispatch.

`odata-read` replaces the narrow `IApplicationClient` OData read path with direct HTTP: it supports entity, projection, ordering, pagination and count; filters and expands are still rejected. R1's `find-empty-iis-port` and `start-creatio` use built-in `appcmd.exe`/`netstat.exe` and have no `creatio.client`, `Microsoft.Web.Administration`, WMI, PowerShell, or .NET helper dependency. `start-creatio` launches `dotnet Terrasoft.WebHost.dll` where appropriate; that is the Creatio application's runtime, not a .NET library linked into this MCP server. The Windows commands have mocked coverage and a Windows cross-build, but no live Windows run.

R2's `execute-esq` sends the caller's raw SelectQuery JSON unchanged to `DataService/.../SelectQuery`, so relation paths, filters, sorting, and paging are accepted without an ESQ builder. It enforces the same 200,000-byte response ceiling and detects aliases silently dropped by Creatio. `get-entity-schema-properties` reads the merged runtime schema through `RuntimeEntitySchemaRequest`; the 2,049-column test fixture exceeds 200 KB. It does not implement package-scoped designer reads. Unlike Clio's per-call named environments, this prototype has one process-wide target configured by `CREATIO_URL`; that is an architectural difference to preserve in any timing comparison. Both R2 tools have mocked coverage; `get-entity-schema-properties` and `execute-esq` are also covered by the live MCP comparison above. Collectively these probes still do not establish parity for Clio's full 202-tool catalog. Creatio-client connection details are read only from environment variables; see `.env.example`. The local `start-creatio` tool separately reads Clio's `appsettings.json`. The `--list-apps-json` mode exists solely for the comparison harness and returns clio's JSON field names.

Stage 2 breadth is in progress; six of the plan's ten representative reads are now implemented: `list-packages` (SysPackage query plus case-insensitive filtering and local paging), `list-app-sections` (application lookup plus ApplicationSection query), `list-pages` (Freedom UI SysSchema query, including primary-package resolution, bounded empty-result cross-check, and total/truncated reporting), `get-sql-schema` (unique-name resolution plus the native SQL schema designer endpoint), and `list-package-files` / `get-package-file` (ClioGate package-file reads). `list-pages` also calls Creatio's `ApplicationPackagesService` when given an application code; package and section metadata are read through DataService. The package-file pair calls the existing `/rest/CreatioApiGateway/GetPackageFilesDirectoryContent` and `GetPackageFileContent` routes, so it has no linked .NET or `creatio.client` dependency, but requires ClioGate 2.0.0.47+ installed in the target Creatio environment. File reads retain the 10 MiB source limit and reject rooted/traversing paths. `get-sql-schema` returns its body inline and intentionally does not implement Clio's optional local `output-file` write. These tools preserve the main MCP response shapes but, like the other Go probes, target the one process-configured instance instead of accepting Clio's per-call `environment-name`. Beyond mocked wire-level coverage, they are covered by the live MCP comparison above. The Stage 2 target remains 10–15 representative reads; `get-page`, `describe-environment`, `list-entity-client-schemas`, and `get-target-package` are not implemented yet.

## Related work

[`CRACKISH/mcp-creatio`](https://github.com/CRACKISH/mcp-creatio) and its `mcp-creatio` npm package are related OData v4 work. They are useful evidence that a non-.NET read path exists, but they do not answer this pilot's `ATF.Repository` model-mapping and SelectQuery-translation question.

## Reference behaviour

The pilot references, but does not copy, these paths in the clio checkout:

- `clio/Command/ListInstalledApplications.cs`
- `clio/Command/InstalledApplicationQueryService.cs`
- `clio/CreatioModel/SysInstalledApp.cs`
- `clio/Common/ICreatioClientTransport.cs`
- `clio/Common/ServiceUrlBuilder.cs`
- `clio/Common/ClassifyingDataProvider.cs`
- `docs/knowledge/Common/remotedataprovider-swallows-every-failure-into-success-false.md`

## Prototype 1 — replacing the binary while it runs

**Question.** On Windows, `dotnet tool update clio` fails while `clio mcp-server` runs: Windows will not
delete the versioned store directory that holds a mapped image. A single Go binary has no such
directory. Does the lock go away?

**Measured on Windows 10.0.26200, with a live resident process holding the image:**

| Operation | Result |
|---|---|
| Overwrite the file directly | **refused** — `The process cannot access the file because it is being used by another process` |
| Rename it aside, then place the new build | **permitted** |
| Resident process afterwards | still `build-A` |
| New launch from the same path | `build-B` |

**Verdict: the lock is the same; the escape is not.** Windows refuses to overwrite or delete a mapped
image either way — Go changes nothing about that. What changes is that a *single file* can be renamed
aside, and the replacement takes its path immediately. `dotnet tool update` cannot use that escape,
because its update path must **delete the versioned store directory**, and deletion is exactly what
Windows refuses.

So packaging does not remove the lock. It decides whether the standard Windows workaround is available
at all. And note what this still does not do: the resident process keeps running the old build. Nothing
here updates a process that is already running — which was the conclusion of the earlier distribution
research and remains true.

**One discarded run, recorded because it looked like a pass.** The first attempt used `select{}` to hold
the process open. The Go runtime detects that every goroutine is asleep and panics with `all goroutines
are asleep - deadlock!`, so the process died immediately and the overwrite then succeeded against a file
nothing was holding — reported as "overwrite PERMITTED". The resident mode now sleeps instead, and the
script fails loudly if the process is not alive when the overwrite is attempted.

Reproduce: `scripts/replace-while-running.ps1` (needs two builds stamped with
`-ldflags "-X main.buildID=..."`).

## Prototype 2 — a write path that tells the truth

**Question.** clio's knowledge base records that `ATF.Repository`'s `RemoteDataProvider` catches its own
exceptions and returns `Success=false` with an **empty payload**, and that the consumer side then drops
the flag — so a refused operation can surface as an empty success. clio needed a dedicated
`ClassifyingDataProvider` wrapper to stop that. Does a client that does not use the vendor assembly need
one?

**Measured against a live Creatio 10.1.725.0 environment, forms auth:**

| Case | Result |
|---|---|
| Insert a record | succeeded, 1 row, id returned |
| Delete it again (cleanup) | succeeded, 1 row — nothing left behind |
| Insert with a column that does not exist | **refused**, HTTP 500, `ItemNotFoundException`, server's message preserved |
| Insert into a restricted schema | **refused**, HTTP 500, `SecurityException: Current user does not have permissions for the "SysSchema" object` |

Neither refusal was reported as success. Both carry a failure class and the server's own words.

**What this means for the rewrite question.** clio reaches the same truthfulness, but only because it
wraps the vendor provider in `ClassifyingDataProvider` to undo a behaviour it did not choose. A client
built directly on the documented endpoints never acquires that behaviour, so it needs no patch to
correct it. That is a real, if modest, argument for a rewrite — not a language preference.

**What was NOT exercised, stated plainly.** Both refusals arrived as HTTP 500. An HTTP 200 body carrying
`success:false` — the exact shape the vendor provider swallows — was not produced by either case. The
client handles it and there is code for it, but that branch is **unproven**.

**Two payload details that are not guessable**, both found by being refused:

1. A `columnValues` item is `{expressionType, parameter}` **directly**. Wrapping it in an `expression`
   object — the obvious symmetry with `SelectQuery`'s column expressions — makes the server answer
   HTTP 500 `NullReferenceException`, naming no field. The correct shape is visible in clio's own
   `DataServiceBatchCommand`.
2. Go's resolver does not apply the DNS search domain the way `host` does, so a short corporate
   hostname fails with `no such host` where other tools succeed.

Reproduce: `-write-probe`. Case 2 and 3 are safe by construction — they are supposed to fail.
