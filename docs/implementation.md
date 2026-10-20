# Implementation notes

How each tool reaches Creatio and what it deliberately does not do. Evidence of parity with clio is
in [research.md](research.md), [parity.md](parity.md) and the per-area reports in [status](status).

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) over stdio. The protocol tests in `cmd/creatio-mcp-go/main_test.go` establish progress-token correlation, response `_meta`, cancellation, and hidden-name dispatch.

## Tool contracts (T5)

tools/list and `get-tool-contract` answer with clio's own data, not hand-written text. The baseline is clio
**8.1.0.142** since `0bb560b` (2026-10-20; before that 8.1.0.134):

1. `scripts/clio-inventory.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go>` asks a live `clio mcp-server`
   for tools/list, prompts, resources, the get-tool-contract index and every tool's contract, and writes
   `docs/clio-inventory.json`, `docs/clio-inventory.md` and the appendix of `docs/migration-plan.md`. It reads
   8.1.0.142's `get-tool-contract` shape: the default one-line `tool-index` with its `legend`, the object index
   from `detail=index-entries`, and each contract in its default, `full` and `short` detail.
2. `go generate ./internal/cliocontract` copies the tool part of that file into
   `internal/cliocontract/contracts.json`, which is embedded in the binary: tools/list (19 entries), `legend`,
   `toolIndex` and `index` (235 tools each), and the default, short and full contracts.
   `TestEmbeddedContractsMatchTheInventory` fails when the two differ.
3. At run time the server lists clio's resident tools that it implements — all 19 of 8.1.0.142's — in clio's
   order, with clio's description, input schema and annotations, and `get-tool-contract` answers in clio
   8.1.0.142's shape (`tool-index` and `legend` without arguments; `detail=index-entries`, `full`, `short`;
   `tool-names` for contracts, `not-found` for unknown names) with the entries of the tools it implements
   only (`cmd/creatio-mcp-go/contracts.go`). A tool registered with `registerTool` gets its contract from
   the inventory by name; the contract map passed to `registerTool` is a fallback used only for a tool the
   inventory does not have, and `TestEveryServedToolHasClioContract` keeps that set empty. The generated
   `tool_object_rights_contract.go`, `tool_default_record_rights_contract.go` and
   `tool_entity_connections_contract.go` (T19, written before the inventory moved to 8.1.0.142) are such
   fallbacks; the inventory has those tools, so their answers come from it. `knowledge_tool_catalog.go`
   reads the same index (235 tools) as the capability set a knowledge bundle is checked against.
4. `scripts/compare-contracts.py` diffs both servers live; see [parity.md](parity.md). Last run against
   8.1.0.142 at `caa2eb0`: 1025 match, 0 unexplained; known are the 35 tools not served (70 rows: each on two
   surfaces) and one multi-name batch clio shortens
   ([comparison report](status/clio-comparison-2026-10-20.md)).

clio publishes its resident schemas wrapped in `args`, so every tool here also accepts `{"args": {...}}`
(a wrapper next to other top-level keys is refused with clio's "ambiguous argument shape" text); the
flat shape keeps working as before. The Go SDK omits a false `readOnlyHint`/`idempotentHint`, which MCP
reads as false, the same value clio writes.

Environments (migration task T1, 2026-10-03): every call names its target with `environment-name`, as in
clio. `cmd/creatio-mcp-go/environments.go` resolves it against clio's `appsettings.json`
(`internal/creatio/cliosettings.go`: the same file location rule, `CLIO_HOME` override, case-insensitive
names, forms/OAuth/bearer selection and `EnvironmentSettings.Fill` overrides as clio), re-reads the file
when its size or modification time changes, and keeps one authenticated `creatio.Client` per resolved
connection. The `CREATIO_*` variables (see `.env.example`) are optional: they are the target of calls that
name no environment, and clio's active environment is used after them. Each tool reports a resolution failure in its own
clio envelope; `list-environments` is ported from clio's `ShowWebAppListTool`. The first reads below
predate T1; they take `environment-name` like every other tool.

`odata-read` and `odata-read-to-file` run one port of clio 8.1.0.142's `ODataReadTool`
(`internal/creatio/odataread142.go`, `odataread_classify.go`, `odataread_explain.go`; T21b): clio's
arguments (`filters`, `select`, `expand`, `order-by`, `top`, `skip`, `count` and its legacy aliases, which
are refused with clio's rename hint, among them `orderBy`), its `$filter` builder, its response
classification and its answer (`success`, `count`, `value`, `next-link`, `total-count`, `error-code`,
`correlation-id`, …). The pre-142 client (`internal/creatio/odata.go`, which rejected filters and expands,
took `orderBy` and answered `{rows, count}`) is removed. The hand-written `odata-read` contract in
`hiddenToolContracts` (`cmd/creatio-mcp-go/main.go`) still describes that old client; it is only the
fallback for a tool the inventory lacks, and the inventory has `odata-read`, so it is never served.
Inline `odata-read` reads at most the client's 4 MiB response ceiling; clio's inline read has none
([t21b status](status/t21b-to-file-status.md#known-differences)). R1's `find-empty-iis-port` and `start-creatio` (`internal/hosttools`) use built-in `appcmd.exe`/`netstat.exe` and have no `creatio.client`, `Microsoft.Web.Administration`, WMI, PowerShell, or .NET helper dependency. `start-creatio` reads clio's `appsettings.json` and launches `dotnet Terrasoft.WebHost.dll` where appropriate; that is the Creatio application's runtime, not a .NET library linked into this MCP server. The Windows host tools have mocked coverage, which the Windows CI job runs (`go test ./...`, see [releasing.md](releasing.md)), but no live Windows run.

`execute-esq` and `execute-esq-to-file` run one port of clio 8.1.0.142's `ExecuteEsqTool`
(`internal/creatio/esq142.go`, `esqhints.go`; T21b): the SelectQuery goes to
`DataService/json/SyncReply/SelectQuery`, a call carrying SQL text or a flat query is answered with clio's
hint (and a `suggested-query` where clio builds one), SelectQuery shape problems are refused as clio refuses
them, unknown members are reported in `warnings`, and requested columns missing from the rows are reported as
unknown columns. The inline ceiling is 200,000 bytes and its refusal points to `execute-esq-to-file`
(64 MiB). The pre-142 `internal/creatio/esq.go` is removed (`a296050`). `get-entity-schema-properties` reads the merged runtime schema through `RuntimeEntitySchemaRequest`; the 2,049-column test fixture exceeds 200 KB. It does not implement package-scoped designer reads (`internal/creatio/schema.go`). Both R2 tools have mocked coverage; the live MCP comparison in [research.md](research.md#live-mcp-to-mcp-comparison-2026-09-28) covered `get-entity-schema-properties` and the pre-142 `execute-esq`. The `--list-apps-json` mode exists solely for the comparison harness and returns clio's JSON field names.

The first representative reads: `list-packages` (SysPackage query plus case-insensitive filtering and local paging), `list-app-sections` (application lookup plus ApplicationSection query), `list-pages` (Freedom UI SysSchema query, including primary-package resolution, bounded empty-result cross-check, and total/truncated reporting), `get-sql-schema` (unique-name resolution plus the native SQL schema designer endpoint), and `list-package-files` / `get-package-file` (ClioGate package-file reads). `list-pages` also calls Creatio's `ApplicationPackagesService` when given an application code; package and section metadata are read through DataService. The package-file pair calls the existing `/rest/CreatioApiGateway/GetPackageFilesDirectoryContent` and `GetPackageFileContent` routes, so it has no linked .NET or `creatio.client` dependency, but requires ClioGate 2.0.0.47+ installed in the target Creatio environment. File reads retain the 10 MiB source limit and reject rooted/traversing paths (`internal/creatio/packagefiles.go`). `get-sql-schema` returns its body inline or, with `output-file`, writes it to a new file inside the workspace or the OS temp directory and leaves it out of the answer, as clio 8.1.0.142 does ([t22 status](status/t22-live-parity-142-status.md)). Beyond mocked wire-level coverage, these tools are covered by the live MCP comparison in [research.md](research.md#live-mcp-to-mcp-comparison-2026-09-28), except a uniquely named SQL script for `get-sql-schema`, which that run did not find.

The tools ported since then — among them `get-page`, `describe-environment`, `list-entity-client-schemas` and `get-target-package`, and the write areas — are described per task in [migration-plan.md](migration-plan.md) and [status](status). Which tools of clio 8.1.0.142's `get-tool-contract` index (235 tools, see T5 above) this server serves and which it does not is in the [comparison report](status/clio-comparison-2026-10-20.md); the local-machine, infrastructure and workspace tools are left to clio (decision D3 in the migration plan).
