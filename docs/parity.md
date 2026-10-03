# Parity with clio's MCP server

Two scripts call the same tools on `clio mcp-server` and on this server against one Creatio environment
and compare the answers. Both print and save only verdicts, timings and the JSON paths that differ:
no values, URLs, environment names, object names or credentials.

Verdicts: `match`, `both-failed` (both refused; the wording may differ), `error-text` (only a nested error
text differs), `known-diff` (listed in the case file with `known-differences` and a `why`), `mismatch`.
The write script adds `skipped` for a scenario whose Go tool does not exist yet.

## Read tools: `scripts/compare-mcp.py`

```sh
python3 scripts/compare-mcp.py --clio-dll <path>/clio.dll --go-bin ./creatio-mcp-go --clio-env <name>
```

Cases live in `scripts/mcp-parity-cases.json` and `scripts/parity-cases/*.json`; evidence goes to
`evidence/mcp-latest.json`.

The Go server starts without `CREATIO_*` variables and gets `environment-name` on every call, as clio
does (`--go-env-mode=name`, the default). `--go-env-mode=variables` keeps the older mode: the named
environment is copied into `CREATIO_*` variables and the calls carry no name. `--clio-env a,b` runs every
case once per name against the same two processes, so one Go process serves both environments in
alternation; the evidence numbers the environments instead of naming them. The write script below takes
the same `--go-env-mode` with the same default.

A case with `"clio-run": true` reaches both servers through `clio-run`. Use it for a tool that is not
read-only: called directly by raw name, both servers answer `confirmation-required` instead of running it
(see [writing-tools.md](writing-tools.md)).

A case with `method` instead of `tool` sends that MCP method (`resources/list`, `resources/read`,
`resources/templates/list`, `prompts/list`, `prompts/get`) with its `params` unchanged to both servers: no
`args` wrapper, no environment name. `"method": "initialize"` compares the answers both servers gave at
startup. `fields` narrows the result to the named keys (for `initialize`: `instructions`,
`capabilities`, because `serverInfo` names the server). `unordered` lists result keys compared without
regard to order: clio lists resource templates and its two static help resources from a hash set, so
their order changes between clio processes. A protocol error is compared as
`{success: false, code, error}`. The cases are in `scripts/parity-cases/knowledge.json`.

### Prompt table

`cmd/creatio-mcp-go/knowledge_prompt_table.go` reproduces clio's MCP prompts and is generated, not edited.
After a clio update, regenerate it from that clio and a clio checkout at the same version (the checkout
supplies which prompt arguments are bool or int):

```sh
python3 scripts/generate-prompts.py --clio-dll <path>/clio.dll --clio-source <clio checkout>
```

The script renders every prompt through `clio mcp-server` with a placeholder in each argument and with
each optional argument absent, empty or blank (and every pair absent), derives what each state changes
in the text, and writes the file only when that model reproduces every captured render, errors
included. It needs no Creatio environment. Then run `go test ./...` and the `prompts/*` cases of
`scripts/parity-cases/knowledge.json`.

A case with `"go-wrapped": true` calls the Go server with `{"args": {...}}`, the shape clio's tools/list
publishes (`scripts/parity-cases/wrapped-args.json`); every other case calls it flat.

## Tool contracts: `scripts/compare-contracts.py`

```sh
python3 scripts/compare-contracts.py --clio-dll <path>/clio.dll --go-bin ./creatio-mcp-go
```

Starts both servers without a Creatio environment and compares tools/list (resident set, order,
description, input schema, annotations), the get-tool-contract index, every served tool's full contract,
`detail=full` and the answer for an unknown name. Verdicts: `match`, `known:<class>` (the reason is in
`KNOWN_DIFFERENCES` in the script and in the evidence) and `unexplained`; the exit code is non-zero on any
`unexplained`. `known:not-ported` rows carry the task that ports the tool, read from the `tasks` map of
`docs/clio-inventory.json`. Evidence goes to `evidence/contracts-latest.json`.

A clio upgrade shows up here as `unexplained`: rerun `scripts/clio-inventory.py` against the new clio, then
`go generate ./internal/cliocontract`, then `go test ./...`.

## Write tools: `scripts/compare-mcp-writes.py`

A write cannot be compared by sending both servers the same arguments. Each scenario creates one object
per server, reads each one back through its own server, compares the two read-backs and removes both.

Before the first run, list the disposable stand in `~/.config/creatio-mcp-go/write-stands` (one clio
environment name per line, `#` starts a comment). Without that line, or without
`--i-understand-this-writes`, the script refuses and starts nothing.

```sh
# Check the scenario files and print the plan; starts no server and writes nothing.
python3 scripts/compare-mcp-writes.py --clio-env <name> --plan-only

# Run every scenario in scripts/write-scenarios/*.json.
python3 scripts/compare-mcp-writes.py --clio-dll <path>/clio.dll --go-bin ./creatio-mcp-go \
  --clio-env <name> --i-understand-this-writes

# Remove objects that an interrupted run left behind.
python3 scripts/compare-mcp-writes.py --clio-dll <path>/clio.dll --go-bin ./creatio-mcp-go \
  --clio-env <name> --i-understand-this-writes --cleanup-ledger
```

Evidence goes to `evidence/mcp-writes-latest.json`. Every object is recorded in
`~/.cache/creatio-mcp-go/write-ledger.jsonl` before it is created, together with the calls that remove it;
a `removed` record follows once it is gone. The exit code is non-zero on any `mismatch` and when an object
of this run is still on the stand.

### Scenario format

A file is a list of scenarios: `{"label", "why"?, "steps": [...]}`. Placeholders in any string:
`{side}` is `clio` or `go`, `{run}` is an alphanumeric id of the run (Creatio codes and schema names reject
`-`), and a name from `capture` (for example `{id}`) is the value captured on that side.

| Step `kind` | Fields | What happens |
|---|---|---|
| `write` | `tool`, `args`, `creates`, `label`, `known-differences`, `why`, `clio-run`, `clio-environment-key`, `go-tool-missing`, `capture` | Calls the tool on both servers with each side's names. `creates` is the template of the object it makes and must contain `{run}` and `{side}`. The answers are compared too. If either side fails, the remaining steps are skipped and cleanup runs. |
| `read-back` | `tool`, `args`, `label`, `known-differences`, `why`, `capture` | Calls a read tool on both servers for their own objects. Each side's concrete names and captured ids are replaced with their template (case-insensitive) before comparison. |
| `expect` | `step` (label of an earlier step), `path` (for example `$.value`), `equals`, `side` (`both`, `clio`, `go`) | Checks one field of the step's answer on each side. Paths use normalized keys (lowercase, no `-`/`_`); write names in `equals` as templates. |
| `cleanup` | `for` (a `creates` template of an earlier write), `server` (`own` or `clio`), `calls` (`tool`, `args`, `capture`, `clio-run`) | Always runs, also after a failure, for every object whose write was attempted. Cleanup steps run in reverse order (last created, first removed); the calls inside one step run in order, so a lookup can precede the delete that uses its result. `server: clio` removes both sides' objects through clio, for when this server has no delete tool yet. A `capture` that finds nothing means the object is already gone. |

`settle-seconds: N` on a write or read-back step (or on one call of a cleanup step) waits N seconds before each
side's call. Use it after a write that starts background work on the stand: an application or section write
starts an OData rebuild (90-120 s) that refuses the next application write with "Creatio is currently
rebuilding the OData library" on either server, and this stand cannot report the rebuild
(`IsODataBuildRunning` answers an HTML page).

`go-tool-missing: "<reason>"` on a write or read-back step marks the scenario `skipped`: nothing runs until
the Go tool exists and the key is removed.

Destructive tools (`delete-*`, `remove-*`, `uninstall-*`, `clear-*`, `prune-*`, `restore-db*`,
`odata-delete`, `clio-run-destructive`, `execute-sql-script`, or any call with `"destructive": true`) run
only when their `target` (or the cleanup's `for`) is an object this run created, and their arguments lead
to it: they contain its name, or a value captured by a call whose arguments contained it. Anything else is
refused before it is sent.

`scripts/write-scenarios/example-sys-setting.json` is the reference: create a system setting, compare
`get-sys-setting`, check two fields, remove the row through clio's `odata-read` + `odata-delete`.

Self-tests (no network; two fake stdio servers share one fake stand):

```sh
python3 -m unittest            # from the repository root
```
