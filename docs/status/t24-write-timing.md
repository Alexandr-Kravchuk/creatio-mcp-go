# T24: are three write steps really slower on this server?

Date: 2026-10-23. Reference: clio 8.1.0.142 (`clio.dll`, `mcp-server`). This server: built from the merged
v0.2.1 branch at `cee6d5e` (stream 1's login classification, this branch's harness option and the set-fsm-mode probe
fix 6faaf0c). Stand: **not T23's**. T23's stand stopped answering (see "First attempt: stand outage"); the live run
used a second disposable stand given for this testing: .NET Framework on IIS, a datacenter dev server, reached from
macOS. cliogate was not installed there; it was installed with clio 8.1.0.142 (`install-gate`) before the run, since
the remote FSM toggle needs it. File system mode was off at the start and is off at the end.

## Question

In the T23 write run three steps took longer on this server than on clio. In that run clio always called first,
so the second side may have paid for the state the first side left:

| Step (T23 evidence) | clio s | Go s |
|---|---:|---:|
| `set-fsm-mode off` (`window-t12-all.json`, `window-t12-fsm-rerun.json`) | 4.58, 4.64 | 59.51 (failed), 14.76 |
| `restart-by-environment-name` `waitReady:false` (`window-t15-all.json`, `window-t15-package-restart-rerun.json`) | 1.43, 1.62 | 14.83, 14.02 |
| `create-app` before a compile (`window-t15-all.json`) | 16.45 | 32.65 |

## Method

- `compare-mcp-writes.py --order alternate` (68f49d5): the side that calls first flips with every scenario that
  runs; each scenario's evidence records `first`, and a retried step records its attempts. The measured value is the
  `tools/call` duration of the step on each server (`clio_seconds` / `go_seconds`); settle times are not part of it.
- Timing scenarios, window-only, in `scripts/write-scenarios/timing/`. Each file has 7 rounds: round 0 warms both
  servers up and is not counted; rounds 1–6 are measured, Go first in 1, 3, 5 and clio first in 2, 4, 6. Both servers
  call the tools through `clio-run`, as the window scenarios do.
  - `fsm-off-while-off.json`: FSM already off; both read the mode, then `off` (nothing to import or write).
  - `fsm-off-from-on.json`: clio switches FSM on (setup, both calls through clio); after 30 s both read the mode; the
    first side's `off` is the whole path (import with `LoadPackagesToDB`, then cliogate's `SetFileDesignMode`, after
    which IIS recycles the site); the second side's `off` is sent at once, the first request after that recycle, as
    in the first T23 run. Run three times with `--order alternate` (the spread was large) and once with
    `--order go-first`.
  - `restart-no-wait.json`: both read the mode; each side sends the restart request alone 120 s after the previous
    call, as `window/t15.json` does.
  - `create-app.json`: 120 s settle, then both sides create their own application back to back; `delete-app`
    removes them.
- Every run after the first FSM run went through a local logging HTTP proxy (`HTTP_PROXY`; clio needs
  `CLIO_MCP_RESPECT_AMBIENT_PROXY=true`, since its MCP mode clears the ambient proxy). It records each request's
  route, status, duration and size, and the bodies of the package loads. It opens a new upstream connection per
  request, the same hop for both sides. The proxy logs carry the site path and are not part of the evidence.

Evidence (timings, verdicts and differing paths only): `evidence/t24/fsm-off-while-off.json`,
`fsm-off-from-on-1.json` … `-3.json`, `fsm-off-from-on-go-first.json`, `restart-no-wait.json`, `create-app.json`.

## Live result

Seconds per call. "Go first" rounds are 1, 3, 5 of a run; "clio first" rounds 2, 4, 6. Raw values in round order.

| Step | Order | n | clio median | Go median | clio raw | Go raw |
|---|---|---:|---:|---:|---|---|
| `set-fsm-mode off`, FSM already off | Go first | 3 | 0.63 | 0.10 | 0.57, 0.74, 0.63 | 0.10, 0.09, 0.11 |
| | clio first | 3 | 0.60 | 0.10 | 0.60, 0.56, 1.12 | 0.08, 0.10, 0.18 |
| `set-fsm-mode off` from on, **first side** (whole path) | clio first (runs 1–3) | 9 | 14.56 | – | 18.42, 18.72, 16.87, 15.03, 14.56, 14.34, 14.53, 13.96, 14.46 | – |
| | Go first (runs 1–3) | 8 | – | 22.91 | – | 132.66, 26.73, 15.22, 19.09, 35.65, 31.12, 13.18, 13.83 |
| | Go first (go-first run) | 6 | – | 14.91 | – | 14.16, 13.64, 14.65, 16.45, 15.18, 25.96 |
| `set-fsm-mode off` from on, **second side** (first request after the recycle) | Go second (runs 1–3) | 9 | – | 66.13 | – | 84.48, 66.13, 65.60, 63.78, 71.89, 66.62, 64.98, 66.33, 65.24 |
| | clio second (runs 1–3) | 8 | 70.45 | – | 89.97, 67.39, 84.60, 73.52, 65.70, 81.14, 65.17, 67.10 | – |
| | clio second (go-first run) | 6 | 77.65 | – | 71.15, 67.05, 68.35, 90.49, 84.15, 172.07 | – |
| `restart-by-environment-name` `waitReady:false` | Go first | 3 | 3.08 | 0.56 | 1.66, 3.08, 3.85 | 0.44, 0.56, 0.84 |
| | clio first | 3 | 2.51 | 0.60 | 2.29, 2.51, 2.99 | 0.57, 0.70, 0.60 |
| `create-app` | Go first | 3 | 53.45 | 55.76 | 53.45ᵃ, 69.20ᵃ, 53.19ᵃ | 55.76, 56.79, 52.70 |
| | clio first | 3 | 62.86 | 64.95 | 72.95, 62.86, 58.60 | 94.20ᵃ, 64.95ᵃ, 56.01ᵃ |

ᵃ The second side's create was refused twice with "rebuilding the OData library" and retried by the harness 30 s
apart; the value is the third, successful call. Run 3 of `fsm-off-from-on` lost its round 1: clio's setup `on` got
a connection reset from the recycling site after 190 s, so that round's `off` was skipped.

## Verdict per step

### `set-fsm-mode off`: not slower; T23 was order and stand state

- FSM already off: Go 0.10 s, clio 0.60 s, both orders.
- First side, whole path: the time is the stand's `LoadPackagesToDB`. From the proxy log of run 2, outside that
  request both servers add under 1 s (clio 15.03 s for a 14.23 s import, Go 19.09 s for an 18.69 s import), and the
  two requests are the same (`POST` with an empty body; the headers differ only in CSRF header name, charset and Go's
  `Accept-Encoding: gzip`). In runs 1–2 four of Go's eight imports were slow (26.7–132.7 s; the 132.66 s round came
  right after the cliogate install's compile, when clio's own `on` setup took 126 s too); in run 2 the two slow ones
  answered with a longer body (464 bytes against the usual 58), as clio's own export did once (484 bytes, 74.6 s):
  the stand found items to synchronize. Run 3, with the bodies captured, and the go-first run (Go first in all six
  rounds) gave Go 13.2–16.5 s with the empty answer `{"errorInfo":null,"success":true,"changes":[],"errors":[]}`
  in all but one round (25.96 s, also with the empty answer; clio's second `off` took 172 s in that round): Go median
  14.91 s against clio's 14.56 s. Not a server difference.
- Second side, sent at once after the first side's recycle: both wait in `GetIsFileDesignMode` until the recycled
  site answers (62–72 s on this host), then probe and find FSM off: Go 66.13 s, clio 70.45 s. T23's 14.76 s and
  this 66 s are the same effect (the first request after a recycle pays the restart); whoever calls second pays it.
- T23's 59.51 s failure: the `GetApplicationInfo` probe's 45 s cap, fixed in 6faaf0c (unit-tested). Not exercised
  live here: on this host `GetIsFileDesignMode` absorbed every recycle, so the probe always answered in under 0.5 s.

### `restart-by-environment-name` without waiting: not slower; T23 was order

Go 0.56–0.60 s, clio 2.51–3.08 s in both orders. Where clio's extra ~2 s goes was not established: both servers reuse
their session for this tool (a later measurement, 2026-10-23, showed clio logs in again on every call only for 15
tools, restart not among them). On this host the unload
answers in under 0.5 s even 120 s after the other side's unload; the restart cost moves to the next request (the
read after it: 60–70 s). On T23's host the restart request itself paid it when it was the first request after the other side's unload: 14 s on Go, which always went second there.

A check of the fixed binary later the same day (789e539, by hand, one pair per order, 120 s apart) found the stand in
that state too: the second side's restart request paid the site start itself. clio first, Go second: Go 64.34 s,
succeeded. Go first, clio second: clio failed after 100.78 s ("The operation was canceled. (WebException: Timeout)")
and the VPN tunnel dropped right after, so that failure is not counted. Two Go failures of an earlier harness run
(92 s, 81 s, Go second) fell inside the same VPN instability and are discarded as well.

### `create-app` before a compile: not slower; T23 was order and stand state

First side: Go 55.76 s, clio 62.86 s; second side (last attempt): Go 64.95 s, clio 53.45 s. `CreateApp` itself takes
48–90 s on this host on both sides. Its `IsODataBuildRunning` answers HTTP 404, so both servers skip the OData gate
and the platform refuses the second side's create twice while the first side's rebuild runs. On T23's host the
second side presumably waited at the gate inside the call instead (T23: 32.65 s on Go, 40.14 s once on clio); the
median of all 22 T23 pairs was clio 13.51 s, Go 10.65 s.

## Found live: this server failed where clio succeeded (fixed in 789e539)

Outside the three steps. Same class as 6faaf0c: a request this server capped at the client's 45 s where clio waits
longer. Fixed after the run: `delete-app` keeps clio's 100 s and three attempts 1 s apart, `get-fsm-mode` waits up
to 30 minutes (tests `TestAppDeleteOutlastsTheClientTimeoutAndRetries`, `TestSetFsmModeProbeOutlastsTheClientTimeout`;
both fail without the change). Live after the fix: one `create-app` round (Go first) on the same stand, both
`delete-app` cleanups succeeded (the harness does not record cleanup durations, so whether this uninstall ran past
45 s is not known); `get-fsm-mode` as the first request after a restart answered after 61.31 s on Go.

| Tool | Live | clio 8.1.0.142 | This server before 789e539 | Fix |
|---|---|---|---|---|
| `delete-app` | 7 of 7 failed on Go: `UninstallApp` took 47.8–63.9 s on this host (clio's 53.4–60.2 s); clio 7 of 7 succeeded | `uninstall-app-remote`: `RemoteCommandOptions` timeout 100 000 ms, `MaxAttempts` 3, `RetryDelay` 1 s | `callService` default 45 s, one attempt (`appwrite_delete.go`) | `Timeout: 100 * time.Second` (and clio's 3 attempts) |
| `get-fsm-mode` | 3 of 3 failed on Go at 45.0 s when it was the first request after an unload; clio waited 60.9–70.4 s and answered | `FsmModeStatusService`: `Timeout.Infinite` | 45 s (`GetFsmMode`) | `getFsmMode(ctx, adminWriteFsmLoadTimeout)` |

Each failed Go `delete-app` still uninstalled its application on the stand (the request finished after the client
gave up): the read below found none of them.

## Known differences, not changed

| Difference | clio 8.1.0.142 | This server | Why not changed here |
|---|---|---|---|
| Login timeout | 100 s | 45 s (client timeout) | Seen only on an unresponsive host (first attempt); this server gives up sooner, and the texts differ |
| `GetIsFileDesignMode`, `LoadPackagesToDB/FileSystem` | `Timeout.Infinite`, 30 attempts 3 s apart | 30 min, one attempt | clio's retry was needed once (connection reset after 190 s in a setup `on`, which only clio ran); no Go call hit it |

## First attempt: stand outage

The first attempt (00:25–03:01 local time) ran against T23's stand. Its host stopped answering HTTP and SSH at
00:27, before the first round, while TCP and ping still answered; it never came back in that window. The one run
there (`evidence/t24/host-unresponsive-fsm-off-while-off.json`) failed every call after 100 s on clio and 45 s on Go,
which are the two servers' login timeouts.

## Cleanup and what is left on the stand

- Write ledger: no unremoved objects. The 7 Go `create-app` objects whose `delete-app` failed were marked removed by
  hand with `"note": "verified absent on the stand; UninstallApp completed after this server's 45 s client timeout"`
  after the read below.
- A read through clio's `execute-esq` of `SysSchema.Name`, `SysPackage.Name`, `SysInstalledApp.Code`/`Name`,
  `Lookup.Name`, `SysSettings.Code` starting with `UsrParity` or `Parity` returns no rows (the same query with
  `Crt` / `cliogate` returns rows, so the filter works).
- File system mode: off (read through clio), as at the start.
- Left on the stand: cliogate 2.0.0.53 (installed for this run); 28 FSM on/off rounds and 15 application restarts
  (14 of the restart series, one by `install-gate`) have run.
