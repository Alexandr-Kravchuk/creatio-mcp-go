# creatio-mcp-go

## 1. Question

Can clio's `list-apps` behaviour be reproduced from Go, without `ATF.Repository` or `creatio.client`, with output matching clio?

## 2. Answer and evidence

**Answered: yes, with full parity on the forms-auth path. OAuth remains untested.**

### What is established, by decompiling the vendor assembly

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

### The live comparison — full parity

Against a Creatio 10.1.725.0 environment (`IsNetCore=false`), forms auth:

| | clio | Go |
|---|---|---|
| rows | 65 | 65 |
| rows only on this side | 0 | 0 |
| rows matching exactly | 65 of 65 | |
| field mismatches (`name`, `code`, `version`, `description`) | none | |
| SHA-256 of the canonicalised set | `7d85ed7d3d4b2843` | `7d85ed7d3d4b2843` |

The two fingerprints are equal. See `evidence/latest.json`.

### Two things a naive reimplementation gets wrong, both found by hitting them

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

### What is still NOT established

**OAuth client-credentials is untested.** The environment used carries no client id, secret or
authorization-app URI, so that half of the kill criterion was never exercised. It is not evidence of
success.

The live list-apps parity above is evidence for that one command and forms-auth mode. The newer
R0/R1/R2 prototype probes below have unit and wire-shape coverage, but are not yet live comparisons
against the same Creatio instance and clio process.

`scripts/compare-with-clio.sh` is the reproducible evidence harness: it runs clio `list-apps --json`
and this client against the same environment, normalises both result sets, and writes only counts,
SHA-256 fingerprints and mismatch counts to `evidence/latest.json` — never raw application data, URLs
or credentials. The committed evidence is the forms-auth run reported above; rerun the harness before
using this result against another Creatio version or authentication configuration.

### Build status

Builds against `github.com/modelcontextprotocol/go-sdk v1.0.0`, Go 1.27.1, darwin/arm64. The resulting
static binary is 11.5 MB and needs no runtime installed — the single-binary property that motivated
choosing Go for the pilot, measured rather than assumed.

This client makes transport, HTTP, HTML-login-page, malformed-JSON and `success:false` failures loud. It
deliberately does not inherit the vendor behaviour documented in clio's knowledge base, where
`RemoteDataProvider` returns `Success=false` with an empty payload instead of throwing, and the consumer
side then drops the flag — turning a rejected read into an empty successful list.

## 3. What this does not prove

This is still a small read-only slice. It says nothing about write parity, package installation, most
IIS/DISM/PowerShell operations, or parity for clio's 202 MCP tools.

## 4. Kill criterion

If `list-apps` cannot be reproduced without a vendor assembly for both authentication modes clio supports (forms authentication and OAuth client credentials), a full rewrite should stop here. **It has passed for forms authentication and remains open for OAuth client credentials.** A full rewrite must not be approved until OAuth is exercised against the same clio-versus-Go harness.

## Run as an MCP server

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) over stdio. Its resident list is `list-apps`, `clio-run`, and `get-tool-contract`; `odata-read`, `find-empty-iis-port`, `start-creatio`, `execute-esq`, `get-entity-schema-properties`, `get-package-file`, `get-sql-schema`, `list-app-sections`, `list-package-files`, `list-packages`, and `list-pages` are available through `clio-run` or by raw tool name, and their schemas are returned on demand by `get-tool-contract`. The protocol probes establish progress-token correlation, response `_meta`, cancellation, and hidden-name dispatch.

`odata-read` replaces the narrow `IApplicationClient` OData read path with direct HTTP: it supports entity, projection, ordering, pagination and count; filters and expands are still rejected. R1's `find-empty-iis-port` and `start-creatio` use built-in `appcmd.exe`/`netstat.exe` and have no `creatio.client`, `Microsoft.Web.Administration`, WMI, PowerShell, or .NET helper dependency. `start-creatio` launches `dotnet Terrasoft.WebHost.dll` where appropriate; that is the Creatio application's runtime, not a .NET library linked into this MCP server. The Windows commands have mocked coverage and a Windows cross-build, but no live Windows run.

R2's `execute-esq` sends the caller's raw SelectQuery JSON unchanged to `DataService/.../SelectQuery`, so relation paths, filters, sorting, and paging are accepted without an ESQ builder. It enforces the same 200,000-byte response ceiling and detects aliases silently dropped by Creatio. `get-entity-schema-properties` reads the merged runtime schema through `RuntimeEntitySchemaRequest`; the 2,049-column test fixture exceeds 200 KB. It does not implement package-scoped designer reads. Unlike Clio's per-call named environments, this prototype has one process-wide target configured by `CREATIO_URL`; that is an architectural difference to preserve in any timing comparison. Both R2 tools are tested against mocked HTTP responses, not yet against a live Creatio instance. Collectively these probes still do not establish parity for Clio's full 202-tool catalog. Creatio-client connection details are read only from environment variables; see `.env.example`. The local `start-creatio` tool separately reads Clio's `appsettings.json`. The `--list-apps-json` mode exists solely for the comparison harness and returns clio's JSON field names.

Stage 2 breadth is in progress; six of the plan's ten representative reads are now implemented: `list-packages` (SysPackage query plus case-insensitive filtering and local paging), `list-app-sections` (application lookup plus ApplicationSection query), `list-pages` (Freedom UI SysSchema query, including primary-package resolution, bounded empty-result cross-check, and total/truncated reporting), `get-sql-schema` (unique-name resolution plus the native SQL schema designer endpoint), and `list-package-files` / `get-package-file` (ClioGate package-file reads). `list-pages` also calls Creatio's `ApplicationPackagesService` when given an application code; package and section metadata are read through DataService. The package-file pair calls the existing `/rest/CreatioApiGateway/GetPackageFilesDirectoryContent` and `GetPackageFileContent` routes, so it has no linked .NET or `creatio.client` dependency, but requires ClioGate 2.0.0.47+ installed in the target Creatio environment. File reads retain the 10 MiB source limit and reject rooted/traversing paths. `get-sql-schema` returns its body inline and intentionally does not implement Clio's optional local `output-file` write. These tools preserve the main MCP response shapes but, like the other Go probes, target the one process-configured instance instead of accepting Clio's per-call `environment-name`. Their behavior has mocked wire-level coverage only; no live comparison has yet been run. The Stage 2 target remains 10–15 representative reads; `get-page`, `describe-environment`, `list-entity-client-schemas`, and `get-target-package` are not implemented yet.

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
