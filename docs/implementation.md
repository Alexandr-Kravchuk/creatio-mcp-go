# Implementation notes

How each tool reaches Creatio and what it deliberately does not do. Evidence of parity with clio is
in [research.md](research.md).

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) over stdio. The protocol probes establish progress-token correlation, response `_meta`, cancellation, and hidden-name dispatch.

## Tool contracts (T5)

tools/list and `get-tool-contract` answer with clio's own data, not hand-written text:

1. `scripts/clio-inventory.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go>` asks a live `clio mcp-server`
   for tools/list, prompts, resources, the get-tool-contract index and every tool's contract, and writes
   `docs/clio-inventory.json`, `docs/clio-inventory.md` and the appendix of `docs/migration-plan.md`.
2. `go generate ./internal/cliocontract` copies the tool part of that file into
   `internal/cliocontract/contracts.json`, which is embedded in the binary. `TestEmbeddedContractsMatchTheInventory`
   fails when the two differ.
3. At run time the server lists clio's resident tools that it implements, in clio's order, with clio's
   description, input schema and annotations, and `get-tool-contract` serves clio's index entries and
   contracts for the tools it implements (`cmd/creatio-mcp-go/contracts.go`). A tool registered with
   `registerTool` gets its contract from the inventory by name; the contract map passed to `registerTool`
   is used only for a tool clio does not have, and `TestEveryServedToolHasClioContract` keeps that set
   empty.
4. `scripts/compare-contracts.py` diffs both servers live; see [parity.md](parity.md).

clio publishes its resident schemas wrapped in `args`, so every tool here also accepts `{"args": {...}}`
(a wrapper next to other top-level keys is refused with clio's "ambiguous argument shape" text); the
flat shape keeps working as before. The Go SDK omits a false `readOnlyHint`/`idempotentHint`, which MCP
reads as false, the same value clio writes.

`odata-read` replaces the narrow `IApplicationClient` OData read path with direct HTTP: it supports entity, projection, ordering, pagination and count; filters and expands are still rejected. R1's `find-empty-iis-port` and `start-creatio` use built-in `appcmd.exe`/`netstat.exe` and have no `creatio.client`, `Microsoft.Web.Administration`, WMI, PowerShell, or .NET helper dependency. `start-creatio` launches `dotnet Terrasoft.WebHost.dll` where appropriate; that is the Creatio application's runtime, not a .NET library linked into this MCP server. The Windows commands have mocked coverage and a Windows cross-build, but no live Windows run.

R2's `execute-esq` sends the caller's raw SelectQuery JSON unchanged to `DataService/.../SelectQuery`, so relation paths, filters, sorting, and paging are accepted without an ESQ builder. It enforces the same 200,000-byte response ceiling and detects aliases silently dropped by Creatio. `get-entity-schema-properties` reads the merged runtime schema through `RuntimeEntitySchemaRequest`; the 2,049-column test fixture exceeds 200 KB. It does not implement package-scoped designer reads. Unlike Clio's per-call named environments, this prototype has one process-wide target configured by `CREATIO_URL`; that is an architectural difference to preserve in any timing comparison. Both R2 tools have mocked coverage; `get-entity-schema-properties` and `execute-esq` are also covered by the live MCP comparison in [research.md](research.md#live-mcp-to-mcp-comparison-2026-09-28). Collectively these probes still do not establish parity for Clio's full 202-tool catalog. Creatio-client connection details are read only from environment variables; see `.env.example`. The local `start-creatio` tool separately reads Clio's `appsettings.json`. The `--list-apps-json` mode exists solely for the comparison harness and returns clio's JSON field names.

Stage 2 breadth is in progress; six of the plan's ten representative reads are now implemented: `list-packages` (SysPackage query plus case-insensitive filtering and local paging), `list-app-sections` (application lookup plus ApplicationSection query), `list-pages` (Freedom UI SysSchema query, including primary-package resolution, bounded empty-result cross-check, and total/truncated reporting), `get-sql-schema` (unique-name resolution plus the native SQL schema designer endpoint), and `list-package-files` / `get-package-file` (ClioGate package-file reads). `list-pages` also calls Creatio's `ApplicationPackagesService` when given an application code; package and section metadata are read through DataService. The package-file pair calls the existing `/rest/CreatioApiGateway/GetPackageFilesDirectoryContent` and `GetPackageFileContent` routes, so it has no linked .NET or `creatio.client` dependency, but requires ClioGate 2.0.0.47+ installed in the target Creatio environment. File reads retain the 10 MiB source limit and reject rooted/traversing paths. `get-sql-schema` returns its body inline and intentionally does not implement Clio's optional local `output-file` write. These tools preserve the main MCP response shapes but, like the other Go probes, target the one process-configured instance instead of accepting Clio's per-call `environment-name`. Beyond mocked wire-level coverage, they are covered by the live MCP comparison in [research.md](research.md#live-mcp-to-mcp-comparison-2026-09-28). The Stage 2 target remains 10–15 representative reads; `get-page`, `describe-environment`, `list-entity-client-schemas`, and `get-target-package` are not implemented yet.

Environments (migration task T1, 2026-10-03): the single process-wide target described above is gone. `cmd/creatio-mcp-go/environments.go` resolves every call's `environment-name` against clio's `appsettings.json` (`internal/creatio/cliosettings.go`: the same file location rule, `CLIO_HOME` override, case-insensitive names, forms/OAuth/bearer selection and `EnvironmentSettings.Fill` overrides as clio), re-reads the file when its size or modification time changes, and keeps one authenticated `creatio.Client` per resolved connection. `CREATIO_*` variables remain as the target of calls that name no environment, then clio's active environment. Each tool reports a resolution failure in its own clio envelope; `list-environments` is ported from clio's `ShowWebAppListTool`.
