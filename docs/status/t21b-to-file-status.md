# T21b: the five *-to-file tools of clio 8.1.0.142

Ported from the 8.1.0.142 sources and compared with that version's own MCP server
(`dotnet clio.dll mcp-server`), never against a live stand: refusal paths on an unreachable environment
(`validation-cases.json`), everything else against the canned Creatio in `scripts/fake-stands/t21b`
(`fake_creatio.py`, `rules.json`, `cases.json`). `scripts/fake-stands/t21b/run_cases.py` calls both servers
through `clio-run`, compares the answers strictly (a refusal worded differently is a mismatch) and then
compares every file a case names byte for byte (sha256), both servers writing into one fresh workspace.

| Tool | State |
|---|---|
| execute-esq-to-file | done |
| odata-read-to-file | done |
| get-component-info-to-file | done |
| get-request-info-to-file | done |
| list-entity-client-schemas-to-file | done |
| odata-read (inline) | brought to clio 142's arguments and answers (coordinator request), shares the twin's code |
| execute-esq (inline) | brought to clio 142's answers, shares the twin's code (separate commit, revertible on its own) |

## What was ported

- **Output file** (`internal/creatio/tofile.go`): clio's `McpOutputFileWriter` / `ODataFileContract` path
  rules. A blank path is `output-file is required.`; the symlink-followed path must lie in the workspace
  anchor (the nearest `.clio/workspaceSettings.json` above the current directory, else the current directory
  unless it is the bare home) or the OS temp directory, never inside clio's own configuration directory
  (`CLIO_HOME`); an existing file or directory is refused (`output-file '<as given>' already exists; refusing
  to overwrite it. ...`). The answer reports the canonical path (`/private/var/...` for a macOS temp path, as
  clio). The write creates missing parent directories owner-only, completes the content in an owner-only
  (0600) staging file and publishes it with a hard link, so a file created meanwhile is never replaced (clio's
  linkat) and a failed write leaves nothing. Content is written as bytes, no BOM, no newline translation:
  Windows and Unix write the same bytes. Path checks compare case-insensitively on Windows and macOS.
- **Failure texts** are produced as clio produces them and then redacted over System.Text.Json-escaped text,
  which is why a refused path reads `output-file '[redacted-path] resolves ...` (the closing quote is eaten by
  the path placeholder in clio too).
- **execute-esq-to-file** (`internal/creatio/esq142.go`, `esqhints.go`, `cmd/.../tool_esq142.go`): clio 142's
  `ExecuteEsqTool.Run` with `ExecuteEsqQueryHints` (SQL text, write-SQL refusal, flat call, SelectQuery shape
  refusals and the `suggested-query` translation, unknown-member warnings), timeout clamp 1000-120000 ms,
  64 MiB ceiling (inline: 200 000 bytes plus the pointer to the twin), DataService failure shapes,
  unknown-column check. The file holds the `rows` array exactly as it arrived (the whole body when it has no
  `rows` array); the answer drops `rows` for `output-file` and keeps `count` and `warnings`.
- **odata-read-to-file** (`internal/creatio/odataread142.go`, `odataread_classify.go`, `odataread_explain.go`,
  `cmd/.../tool_odata_read142.go`): clio 142's `ODataReadTool` argument binding (filters/all/any/conditions,
  select/expand as array or comma string, order-by, top 1-100, skip, count, aliases `outputFile`, `orderBy`,
  `limit`, ...), validation texts, `$filter` builder with `Uri.EscapeDataString` escaping, the identity checks
  of `@odata.context`, the error classification (`entity-not-found`, `invalid-query`, `server-reported-error`,
  `non-json-response`, `incomplete-response`, `transport`, `argument`), the structured-detail sentence, the
  invalid-query explanation from `$metadata` (`unknown-members`, `valid-column-count`), and
  `ODataFileContract`: the response bytes go to the file unchanged, the answer carries `count`, `row-count`,
  `column-sizes` (UTF-8 bytes of each column's raw JSON, `@odata.*` skipped), `next-link`, `total-count`,
  `correlation-id`. Order of checks as clio: arguments, target, blank output-file, environment, path, fetch.
  64 MiB ceiling while reading. The CSDL reader of the OData write tools now also records navigation
  properties (read side only).
- **get-component-info-to-file / get-request-info-to-file**: the inline tools' handlers became
  `componentInfoAnswer` / `requestInfoAnswer`, which both the inline tool and the twin call; the twin projects
  the answer as `DocumentationFileProjection` does (removes `documentation`, appends `documentationFile` and
  `documentationSections`, the ATX headings outside fenced code; writes nothing when there is no
  documentation).
- **list-entity-client-schemas-to-file**: the inline lookup's answer is written as clio's MCP serializer
  writes it (compact, nulls omitted, `sections`/`editPages` always present on success,
  McpResultJsonEncoder escaping); the answer carries `{total, classic, freedom, unknown}` counts.
- **odata-read (inline)**: now the same code as the twin, so a call built from clio 142's
  get-tool-contract (`filters`, `expand`, `order-by`, ...) is served; answers are clio's
  (`success`, `count`, `value`, `next-link`, `total-count`, `error-code`, `correlation-id`, ...). The old
  `{rows, count}` answer and the raised failures are gone.
- **execute-esq (inline)**: same code as the twin (last commit `Serve execute-esq through clio 8.1.0.142's
  shared run ...`). The superseded `internal/creatio/esq.go` and `odata.go` are removed.
- Registration: all five twins are hidden tools with clio's annotations (not read-only, not destructive, not
  idempotent): a direct call by raw name answers `confirmation-required`, `clio-run` runs them.
  `docs/clio-inventory.json` tasks map: the five marked served.

## Evidence, per tool

Runs of 2026-10-20 against clio 8.1.0.142; `evidence/t21b-validation-latest.json` and
`evidence/t21b-fake-latest.json` (verdicts, JSON paths, sha256 of written files; no values).

- **execute-esq-to-file**: refusal paths 17 match, 3 known (JSON parser wording, transport wording twice);
  fake stand 12 match, 5 files written, identical bytes on both sides (rows as sent with their spacing, rows
  from a query given as a string, empty rows, a body without rows array, a call with an unknown-member
  warning). Responses for responseStatus / errorInfo / unmarked / array / blank bodies and the unknown column
  match. Unit tests: `TestESQPrepareRefusesAsClio142`, `TestESQExecuteReadsTheResponseAsClio142`.
- **odata-read-to-file**: refusal paths 36 match, 4 known (key order once, transport wording three times);
  fake stand 17 match, 6 files written with identical bytes (collection, nested directory under the OS temp
  directory, filtered query with count, single entity), the second write to the same path refused on both,
  and every rejected body (wrong entity set, HTML 404, outage page, routing miss, proxy body, empty and
  non-JSON body, missing @odata.count, invalid query with the $metadata explanation, server error) writes
  nothing on both sides. The request log of the fake stand (method, route, decoded query, rule) is identical
  per side: 31 keys, 0 different. Unit tests: `TestODataReadRequestPathMatchesClio` (the decoded query clio
  sent), `TestODataReadToFileWritesTheResponseBytes`.
- **get-component-info-to-file**: 15 cases, 15 match, 5 documentation files byte-identical (detail, detail into
  a new nested directory, composite, mobile detail, unreachable environment with the version fallback); list,
  not-found and both-selectors answers unchanged, no file. Documentation comes from clio's CDN on both sides
  (network). Unit tests: `TestToFileExtractHeadingsFollowsClio`, `TestToFileProjectDocumentation`.
- **get-request-info-to-file**: 9 cases, 9 match, 3 documentation files byte-identical (two web requests, one
  mobile); list and not-found unchanged, no file.
- **list-entity-client-schemas-to-file**: refusal paths 7 match, 2 known (transport wording); fake stand 3
  match, 2 files byte-identical (a graph with sections and an edit page, an empty graph), unknown entity
  refused without a file.
- **odata-read**: fake stand 15 match (every body above, filters/select/expand/order-by/count, the old
  `orderBy` key answered with clio's rename); `TestODataReadServesClio142Arguments`.
- **execute-esq**: fake stand 5 match, 1 known (below).
- Contracts: `scripts/compare-contracts.py` against clio 142: 1005 match, 78 not-ported, 1 known
  (fit-multi-name), 0 unexplained; each of the five twins and odata-read / execute-esq 5 of 5 rows match.
- `gofmt -l .` empty, `go vet ./...`, `go test -race ./...`, `GOOS=windows go build ./...`,
  `python3 -m pytest scripts -q` (30 passed), `scripts/check-public-safety.sh`: all pass.

## Known differences

- Transport failures read in Go's words (`dial tcp ...: connect: connection refused`) instead of .NET's
  (`Connection refused (...)`); same for the JSON parser message of an unparsable query string and of an
  unparsable SelectQuery response.
- Arguments arrive in Go as a map, so where clio names keys or members in request order this server uses
  sorted order: unknown/renamed argument keys, the members of a `query` object (warnings, shape problems) and
  the `filterN` numbering of a suggested query built from a flat `filters` object. Measured once each.
- A call without `environment-name`: clio 142 refuses the binding; this server resolves its default target,
  as for every tool.
- `documentationSource` (`cdn` / `cache`) depends on whether the documentation cache was warm; with a warm
  cache on both sides it matches (a cold first run showed `cdn` for clio and `cache` for Go).
- The publish refuses an existing name with a hard link (clio: `linkat` from an owner-only staging directory;
  Go: an owner-only staging file beside the target). Neither leaves anything on failure; a symbolic link
  swapped into a parent directory between the check and the write is not detected by Go (clio's descent with
  directory descriptors is not ported).
- The 64 MiB refusal of odata-read-to-file reports the bytes read when the ceiling was passed (limit + 1);
  clio reports its transport's count, which may be larger. Not measured.
- Inline odata-read reads at most the client's 4 MiB response ceiling and answers a larger body as a
  transport failure; clio's inline read has no ceiling. Not measured.
- Cancellation of a running read is not reported with clio's "no output file was written" text (clio says it
  never observes MCP cancellation in practice either).

## Not ported / left

- Nothing of the five tools. The 78 not-ported contract rows belong to other tasks (stream a tools, T16,
  T6 management).
- `docs/implementation.md`, `README.md` and `docs/install.md` still describe the old odata-read /
  execute-esq, and the generated `docs/clio-inventory.md` still labels the five tools "T19 new in clio
  8.1.0.142" (regenerating it needs a live clio run and rewrites the migration-plan appendix); left to the
  coordinator.
- Live parity cases: `scripts/mcp-parity-cases.json` has one execute-esq case, a valid 142 SelectQuery; no
  case uses odata-read, so nothing there relies on the pre-142 shapes.
- The execute-esq switch commit was reverted on a scratch tree: the result builds and passes `go vet`.
