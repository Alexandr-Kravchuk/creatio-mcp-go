# creatio-mcp-go or clio MCP

Pros, cons and measured figures for choosing the MCP server an agent uses with Creatio: creatio-mcp-go v0.2.1
(pre-release of 2026-10-23) against clio 8.1.0.142 in its `clio mcp-server` mode. The same content as slides:
[clio-mcp-comparison.html](clio-mcp-comparison.html) (download and open it in a browser). Every figure links to the
file it comes from.

## What it is

| | clio MCP | creatio-mcp-go |
|---|---|---|
| What | the `mcp-server` mode of clio, the .NET CLI for Creatio | a separate MCP server written in Go, MIT, public repository |
| Tools | 235 in clio 8.1.0.142 | 200 of them, with the same names, arguments, contracts and answer shapes |
| Runs on | .NET; installed as a .NET tool | one file, no .NET; `install.sh`, `install.ps1` or Homebrew |
| Environments | registers them (`reg-web-app`) | reads clio's `appsettings.json`; one process serves them all |
| Also does | deploys and restores stands, installs the knowledge library and updates it hourly | — |

Sources: [README](../README.md), [install.md](install.md).

## Pros

| Pro | Figure |
|---|---|
| Faster where clio spends time on itself | start 0.04–0.06 s vs 3.2–3.9 s; `list-pages` 0.14 s vs 4.74 s; reads overall 2.7 times faster ([compare-2026-10-22](status/compare-2026-10-22.md)) |
| Short write steps take under a second | `restart-by-environment-name` without waiting 0.56–0.60 s vs 2.51–3.08 s; where clio's extra time goes was not established (both servers reuse their session for this tool) ([T24](status/t24-write-timing.md)) |
| One file, no .NET | 23 MB on disk and 24–28 MB of memory; clio takes 504 MB plus .NET and 136–170 MB ([compare-2026-10-22](status/compare-2026-10-22.md#process-footprint)) |
| Agents and skills work unchanged | 1025 contracts match clio 8.1.0.142, 0 unexplained; the same 74 prompts and guidance ([clio-comparison-2026-10-20](status/clio-comparison-2026-10-20.md)) |
| Updates without stopping the server | the installer renames the new file over the old one, a running server keeps working; on Windows `dotnet tool update clio` fails while `clio mcp-server` runs ([install.md](install.md#1-install), [research.md](research.md#prototype-1--replacing-the-binary-while-it-runs)) |
| One-command install | `install.sh` and `install.ps1` check `SHA256SUMS` and register the server in Claude Code and Codex ([install.md](install.md#one-command)) |

## Cons

| Con | What it means for you |
|---|---|
| 35 clio tools are not served: deploying and restoring stands, Redis, workspaces, `reg-web-app`, `page-smoke`, `get-mobile-page-conversion-guide` | You still need clio: environments are registered and infrastructure work is done through it. Calling such a tool here returns clio's "unknown tool" refusal. |
| Does not install or update the knowledge library itself; clio does it when `mcp-server` starts and every hour | Until `clio install-knowledge` has run, `get-guidance` answers `guidance-unavailable` and the agent works without `core-rules` and `routing`. Git knowledge sources: clio only. |
| Contracts are recorded from clio 8.1.0.142 and built into the binary | Tools and arguments of later clio versions arrive here only with a new release; until then the agent may get a refusal (between 8.1.0.134 and 8.1.0.142 four tools refused the new arguments until they were ported). |
| `validate-page` runs a declared subset of clio's rules: the parent-container (`known-containers`) check runs, the binding, handler, converter, validator, AST lint, chart, run-process, placement-slot and data-source checks do not | A page defect clio would catch can pass here; the answer warns about it. |
| `set-fsm-mode`: clio retries the mode check and the package loads up to 30 times, 3 s apart; this server tries once | A dropped connection while FSM switches fails the command where clio would have waited. |
| A host that does not answer: login waits 45 s, clio 100 s. A host that disappears mid-session: Go's error text, not clio's | A slow stand is refused sooner; an agent that recognizes errors by clio's texts will not recognize this one. |
| Pre-release, one author and maintainer | Answers may change between releases (v0.2.0 changed the answer of `odata-read`); fixes depend on one person. |
| Not yet checked on a live stand: creating an OAuth app, `execute-sql-script` results, entity connections, `install-application` with a real archive, Windows | These paths may differ from clio in ways nobody has seen yet. |
| The Windows binary is not signed | Windows may block it; the installer unblocks it, a manual install needs `Unblock-File`. |
| Telemetry sends `clio_version` = `creatio-mcp-go/<build>` | clio's statistics show these calls apart from clio; served-content attributes are not sent. |

Sources: [README](../README.md), [install.md](install.md), [release notes v0.2.0](release-notes-v0.2.0.md#known-differences-from-clio),
[v0.2.1](release-notes-v0.2.1.md#known-differences-not-changed), [T23](status/t23-live-write-parity.md#not-verified-live),
[T24](status/t24-write-timing.md#known-differences-not-changed).

## Numbers

### Coverage and matching answers

| Figure | What it is | Source |
|---|---|---|
| 200 of 235 | clio 8.1.0.142 tools served, plus `get-tool-contract`; all 19 resident tools in tools/list, in clio's order | [clio-comparison-2026-10-20](status/clio-comparison-2026-10-20.md) |
| 1025 / 0 | contracts that match clio 8.1.0.142 / unexplained differences | [clio-comparison-2026-10-20](status/clio-comparison-2026-10-20.md) |
| 823 of 883 | read answers matched on a live stand; 41 known differences, 17 identical refusals, 2 mismatches (one fixed in v0.2.1, the other matched on a repeat) | [compare-2026-10-22](status/compare-2026-10-22.md#read-run) |
| 0 | mismatches in the live write scenarios, except a package compile whose answer depends on who compiles first | [T23](status/t23-live-write-parity.md) |

### Reads (2026-10-22, clio called first in every case)

| What was measured | clio | creatio-mcp-go |
|---|---:|---:|
| Process start to first answer | 3.2–3.9 s | 0.04–0.06 s |
| Process memory | 136–170 MB | 24–28 MB |
| Size on disk | 504 MB + .NET | 23 MB, one file |
| Call without a stand (`list-environments`), median | 5.4–6.6 ms | 0.7–1.0 ms |
| 864 read calls on a stand, total | 365 s | 135 s |
| Reads on a stand, 90th percentile | 0.60 s | 0.21 s |

Heavy reads with the order alternated, median of calls 2–4:

| Tool | clio | creatio-mcp-go |
|---|---:|---:|
| `list-pages` | 4.74 s | 0.14 s |
| `list-apps` | 0.30 s | 0.03 s |
| `get-page` | 2.64 s | 2.35 s |
| `get-page-hierarchy` | 1.93 s | 1.82 s |
| `get-entity-schema-properties` | 0.04 s | 0.03 s |

Where the stand does the work (`get-page`, `get-page-hierarchy`) the difference is 6–11%; the large gaps are process
start, the first call, `list-pages`, `list-apps` and `compile-status`. Source:
[compare-2026-10-22](status/compare-2026-10-22.md); the footprint was re-checked on 2026-10-23 with the same order of
magnitude.

### Writes (T24, 2026-10-23, medians in seconds, 3 rounds per order)

| Step | Go first: clio / Go | clio first: clio / Go |
|---|---:|---:|
| `set-fsm-mode off`, FSM already off | 0.63 / 0.10 | 0.60 / 0.10 |
| `restart-by-environment-name` without waiting | 3.08 / 0.56 | 2.51 / 0.60 |
| `set-fsm-mode off` from on, first side | Go 14.91 | clio 14.56 |
| `create-app` | 53.45 / 55.76 | 62.86 / 64.95 |

Short steps: the median of the 239 T23 write steps under 30 s is 0.62 s on clio and 0.20 s here (clio called first).
In T23 this server looked slower on `set-fsm-mode`, `restart` and `create-app`; with the order alternated it is not,
and the FSM probe timeout is fixed in v0.2.1. Sources: [T24](status/t24-write-timing.md),
[release notes v0.2.1](release-notes-v0.2.1.md), [compare-2026-10-22](status/compare-2026-10-22.md#write-steps-of-the-t23-run).

## When to use which

| creatio-mcp-go | clio MCP |
|---|---|
| everyday agent work with pages, schemas, data, applications and processes | deploying, restoring and stopping stands, registering environments, Redis, workspaces |
| many short calls where response time matters | `page-smoke` and the mobile page guide |
| a machine without .NET | installing and updating the knowledge library, Git knowledge sources |
| updates without stopping the server | a clio newer than 8.1.0.142 and its new tools |
| | an unstable stand where clio's retries during an FSM switch are needed |

Together: keep clio installed for environments, knowledge and infrastructure, and route the agent's everyday calls
through creatio-mcp-go. creatio-mcp-go reads the environments from clio's `appsettings.json` and the knowledge
library from clio's store, and both servers can run at the same time on one clio home.
