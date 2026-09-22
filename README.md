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

This is also one read-only command. It says nothing about the write path, package installation,
IIS/DISM/PowerShell operations, or the other 249 CLI verbs.

The earlier note said: `scripts/compare-with-clio.sh` exists and is the intended evidence:
it runs clio `list-apps --json` and this client against the same environment, normalises both result
sets, and writes only counts, SHA-256 fingerprints and mismatch counts to `evidence/latest.json` —
never raw application data, URLs or credentials. **No environment was reachable when this was written,
so no parity verdict exists yet.** Until that file contains a verdict, this pilot has proven the
protocol is reachable and has NOT proven the output matches.

### Build status

Builds against `github.com/modelcontextprotocol/go-sdk v1.0.0`, Go 1.27.1, darwin/arm64. The resulting
static binary is 11.5 MB and needs no runtime installed — the single-binary property that motivated
choosing Go for the pilot, measured rather than assumed.

This client makes transport, HTTP, HTML-login-page, malformed-JSON and `success:false` failures loud. It
deliberately does not inherit the vendor behaviour documented in clio's knowledge base, where
`RemoteDataProvider` returns `Success=false` with an empty payload instead of throwing, and the consumer
side then drops the flag — turning a rejected read into an empty successful list.

## 3. What this does not prove

This is one read-only command. It says nothing about the write path, package installation, IIS/DISM/PowerShell operations, or clio's other 249 CLI verbs.

## 4. Kill criterion

If `list-apps` cannot be reproduced without a vendor assembly for both authentication modes clio supports (forms authentication and OAuth client credentials), a full rewrite should stop here. **The kill criterion has not been evaluated**, because neither a Go build nor a live Go-versus-clio diff could be produced in this environment.

## Run as an MCP server

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) over stdio and exposes one tool, `list-apps`. Connection details are read only from environment variables; see `.env.example`. The `--list-apps-json` mode exists solely for the comparison harness and returns clio's JSON field names.

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
