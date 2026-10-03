# Writing a tool

How to port one clio MCP tool, read or write. The rule throughout: the same name, arguments and answer as
clio. Read clio's tool first (`git -C <clio> show origin/master:clio/Command/McpServer/Tools/<Tool>.cs`)
and measure its answers live (`scripts/compare-mcp.py`) instead of guessing.

## Where the code goes

- `cmd/creatio-mcp-go/tool_<name>.go`: the MCP side — contract, argument handling, environment, envelope.
  The file registers the tool from `init()`, so tools are added without touching shared code.
- `internal/creatio/<area>.go`: the Creatio side — HTTP calls, parsing, clio's answer types.

```go
func init() {
	registerTool(map[string]any{
		"name":        "get-record-rights",
		"description": "<clio's description>",
		"inputSchema": map[string]any{"type": "object", "required": []string{"entity"}, "properties": map[string]any{...}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct{ Entity string `json:"entity"` }
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
			return nil, fmt.Errorf("decode get-record-rights arguments: %w", err)
		}
		client, failure, err := envs.resolve("get-record-rights", args, scopeName)
		if err != nil {
			return nil, err // argument binding failure (invalid-parameter-type), raised before the tool runs
		}
		if failure != nil {
			return structuredToolResult(creatio.RecordRightsResponse{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.GetRecordRights(ctx, input.Entity)), nil
	}, withAnnotations(readOnlyAnnotations))
}
```

The contract agents read is clio's own, served from `internal/cliocontract` (generated from
`docs/clio-inventory.json`); the map passed to `registerTool` is used only for a tool clio's inventory does
not know. Every tool accepts flat arguments and clio's `{"args": {...}}` wrapper (unwrapped centrally); a
tool whose own argument is named `args` needs its own handler, as `clio-run` / `clio-run-destructive` have.
The tool is callable through `clio-run`, `clio-run-destructive` and, when read-only, by its raw name.

## Annotations and write safety

Where the hints come from (`annotationsOf` in `write_safety.go`):

- a tool in clio's `tools/list`: all four of clio's published hints, from the inventory;
- any other tool: `destructive` from clio's `get-tool-contract` index (inventory), and the rest from the
  registration. clio does not publish `readOnly` for a hidden tool, so pass
  `withAnnotations(toolAnnotations{ReadOnly, Destructive, Idempotent, OpenWorld})` with the values of clio's
  `[McpServerTool(...)]` attribute. Without the option a tool is read-only (`ReadOnly`, `Idempotent`);
  `localWriteAnnotations` is clio's shape for a read that may also write a local file (`get-schema`,
  `get-theme`, `get-sql-schema`). A test fails when a registration contradicts clio's index.

What the hints do, as in clio (measured against clio 8.1.0.134):

- `tools/list` and the `get-tool-contract` index carry them as clio does.
- A tool that is **not read-only**, called directly by its raw name, is not run. The answer is clio's
  `confirmation-required` (isError, `code`, `destructive`, `write-capable`, `argument-names`, `retry` pointing
  at `clio-run`). The gate keys on `ReadOnly`, not on `Destructive`, so additive writes are gated too.
  Tools clio lists in `tools/list` (`isClioResident`) are never gated.
- `clio-run` and `clio-run-destructive` are one executor under two names, both annotated destructive so the
  host asks before running them. **Both run any tool, read or write**: clio removed the refusal of a
  destructive tool through `clio-run`, because agents looped on it. Each answer records
  `_meta["clio-run"] = {dispatchedTool, destructive}`.
- A confirmation argument some clio tools take (for example `confirm: true` on `odata-update` /
  `odata-delete`, "refuses without any remote call") belongs to that tool's own contract: port it with the
  tool.

## Arguments and environment

- `decodeStrictArgs(withoutEnvironmentArgs(args, scope), &input)` refuses unknown keys; where clio words that
  refusal itself, use `unknownArgumentError(args, valid...)` and `optionalStringArg` (clio's
  `invalid-parameter-type` text).
- `scopeName` reads only `environment-name`; `scopeDirect` adds clio's `uri`/`login`/`password`;
  `scopeDirectOAuth` adds `client-id`/`client-secret`/`auth-app-uri`. Use the scope of clio's argument record.
- `envs.resolve(tool, args, scope)` returns the client, or `failure` (unknown name, unusable registration,
  Safe environment) for the tool to report in its own envelope, or `err` for a binding failure.
  `envs.target` is the same for tools that let the failure raise.
- Command-style tools (clio's `CommandExecutionResult`) report a failed resolution with
  `resolverFailureEnvelope(failure)`.

## Answers

- Return `structuredToolResult(value)`: the JSON text and the same value as structured content.
- clio's answer type goes into `internal/creatio` with clio's JSON names. Command-style tools use
  `creatio.CommandResult` (`exit-code` + `execution-log-messages`): `NewCommandResult`, `CommandFailure`
  (exit 1, one Error), `CommandInfo` (exit 0, one Info: an in-progress notice).
- A failure that clio raises (its tool throws): `return nil, err`. The dispatcher reports it as
  `MCP tool '<name>' failed: <message>` (direct) or `Error: tool '<name>' failed: <message>` (clio-run).

## Redaction

One rule, `internal/redact` (a port of clio's `SensitiveErrorTextRedactor`, checked against clio's own
output), applied where clio applies it:

- every raised failure (`return nil, err`) — by the dispatcher;
- every result of a hidden tool that signals failure — `isError`, top-level `success: false` or a non-empty
  top-level string `error` — by the dispatcher (`failure_redaction.go`, clio's `RedactFailureContent`);
- not a success, and not clio's command envelope, which signals no failure that way: clio leaves its
  messages unredacted, and so does this server.

A tool only redacts itself where clio's tool does and the dispatcher would not see it, for example a nested
`error` object: `redacted(err)` or `redact.Text(text)`.

## HTTP

`c.callService(ctx, serviceCall{Route: "rest/Service/Method", Body: body, Timeout: t, Limit: n})` is the one
authenticated helper: it accepts only `rest/`, `ServiceModel/` and `DataService/` routes, re-logs in once on
a login page or 401/403, caps the body at `Limit` and fails on a transport error, a non-2xx status or an
HTML body. `c.serviceRequest` returns status and body whatever they are, for a service whose clio client
reads failures from the body (ClioGate, Data Forge). Routes are constants; an argument never becomes a route.

## Long-running operations

```go
op := compileOperations.begin(envs.tenantKey(name), name, operationDetails{PackageName: pkg})
result, finished, err := runLongOperation(ctx, "compile-creatio", 0,
	func(ctx context.Context, stage func(string)) creatio.CommandResult {
		stage("Compiling")
		result := client.Compile(ctx, pkg)
		compileOperations.finish(op.ID, result.ExitCode, result.Messages)
		return result
	})
if err != nil {
	return nil, err // the caller cancelled; the work's context was cancelled too
}
if !finished {
	return structuredToolResult(creatio.CommandInfo("<clio's in-progress text naming op.ID>")), nil
}
```

`runLongOperation` sends `notifications/progress` on the caller's token (a stage line per `stage`, and a
heartbeat every 15 s), answers after clio's 150 s response deadline while the work goes on, and cancels the
work when the call is cancelled first. `compileOperations` / `restartOperations` are what `compile-status` /
`restart-status` read: clio's states (`running`, `succeeded`/`failed`, `ready`/`timedout`), fields and
retention (5 minutes after finishing, 50 records, running ones kept). `progressFrom(ctx)` gives a tool the
raw progress reporter.

## Tests and parity

- Unit tests next to the code, against `httptest` servers. To reach a tool the way callers do, loop over
  `callPaths(name, args)` (clio-run always, raw name when read-only).
- Read parity: add cases to `scripts/parity-cases/<area>.json` (`tool`, `args`, `label`; `"clio-run": true`
  for a tool that is not read-only) and run `scripts/compare-mcp.py` on both stands; 0 mismatches.
- Write parity: a scenario in `scripts/write-scenarios/` (create per side, read back, compare, clean up) run
  with `scripts/compare-mcp-writes.py --clio-env s16123120 --i-understand-this-writes` — only on the
  disposable stand (decision D1). Steps for a tool that is not read-only carry `"clio-run": true`. See
  [parity.md](parity.md).
- Before a commit: `gofmt -l .` empty, `go vet ./...`, `go test -race ./...`, `GOOS=windows go build ./...`,
  `python3 -m unittest discover -s scripts`, `bash scripts/check-public-safety.sh`.
