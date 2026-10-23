# T24: are three write steps really slower on this server?

Date: 2026-10-23. Stand: the disposable write stand of T23 (decision D1, .NET Framework on IIS, reached from macOS).
Reference: clio 8.1.0.142 (`clio.dll`, `mcp-server`). This server: `e45af4d` plus this branch.

## Question

In the T23 write run three steps took longer on this server than on clio. In that run clio always called first,
so the second side may have paid for the state the first side left:

| Step (T23 evidence) | clio s | Go s |
|---|---:|---:|
| `set-fsm-mode off` (`window-t12-all.json`, `window-t12-fsm-rerun.json`) | 4.58, 4.64 | 59.51 (failed), 14.76 |
| `restart-by-environment-name` `waitReady:false` (`window-t15-all.json`, `window-t15-package-restart-rerun.json`) | 1.43, 1.62 | 14.83, 14.02 |
| `create-app` before a compile (`window-t15-all.json`) | 16.45 | 32.65 |

## Method

- `compare-mcp-writes.py --order alternate` (new, 68f49d5): the side that calls first flips with every scenario
  that runs; each scenario's evidence records `first`, and a retried step records its attempts. The measured value
  is the `tools/call` duration of the step on each server (`clio_seconds` / `go_seconds`); settle times before a
  call are not part of it. Tested in `scripts/test_compare_mcp_writes.py` (go-first, alternate, default, attempts).
- Timing scenarios, window-only, in `scripts/write-scenarios/timing/` (the default run leaves them out). Each file
  has 7 rounds: round 0 warms both servers up and is not counted; rounds 1–6 are measured, Go first in 1, 3, 5 and
  clio first in 2, 4, 6. Both servers call the tools through `clio-run`, as the window scenarios do.
  - `fsm-off-while-off.json`: FSM already off; both read the mode, then `off` (nothing to import or write, no
    recycle). Steady state.
  - `fsm-off-from-on.json`: clio switches FSM on (setup); after 30 s both read the mode; the first side's `off`
    imports and writes the configuration (the site recycles), the second side's `off` is sent at once, the first
    request after that recycle, as in the first T23 run.
  - `restart-no-wait.json`: both read the mode; each side sends the restart request alone 120 s after the previous
    call, as `window/t15.json` does: the first side's request reaches a running site, the second side's is the
    first request after the first side's restart.
  - `create-app.json`: 120 s settle, then both sides create their own application back to back (as `window/t15.json`
    before a compile); `delete-app` removes them.

## Live result

Not measured: the stand's host stopped answering before the first round and did not come back during the window
(see "Stand outage"). The only live run is `evidence/t24/host-unresponsive-fsm-off-while-off.json`: every call
failed on both servers, which measures the two servers' timeouts, not the step.

| Series | rounds (Go first / clio first) | clio median s | Go median s |
|---|---|---:|---:|
| `set-fsm-mode off` while off, host not answering | 3 / 3 | 100.01 | 45.00 |

All 14 calls of that run (7 reads, 7 `off`) failed after 100.0–100.1 s on clio and 45.0 s on Go, in both orders:
clio's login has a 100 s timeout (`CreatioClient.SendAsync` → `EnsureAuthenticatedAsync(100000)`), this server's 45 s
(the login client takes the client's timeout).

## Verdict per step (code of both servers and the T23 numbers)

### `set-fsm-mode off`: one real difference, fixed; the rest is order

Both servers take the same path on this stand: `GetIsFileDesignMode` → (import with `LoadPackagesToDB` when it is
on) → `GetApplicationInfo` probe → cliogate `SetFileDesignMode`, after which IIS recycles the site. No extra probe,
wait or restart on this server: the 90 s post-restart login loop runs only for .NET Core and only for `on`, as in
clio.

- T23 first run, 59.51 s and a failure: the probe. clio's `FsmModeStatusService.GetStatus` posts with
  `Timeout.Infinite` on a client whose `HttpClient.Timeout` is infinite; this server sent it with the client's 45 s.
  Sent at once after clio's `off`, the probe reached the recycling site, timed out at 45 s and the command ended
  with "Could not determine current FSM state". **Fix 6faaf0c**: the probe has the 30-minute bound the package loads
  of the same command already use; `get-fsm-mode` keeps 45 s. Test `TestSetFsmModeProbeOutlastsTheClientTimeout`
  (fake stand answering after three client timeouts; fails without the change with T23's message).
- T23 rerun, 14.76 s: order. Go called 120 s after clio's `off`, and its first request started the recycled
  site (the host's health ping took 22 s cold the same night). clio's 4.64 s came after Go's `on`, a 30 s settle
  and a mode read, on a running site. Not measured with the order flipped (outage).

### `restart-by-environment-name` without waiting: order (code identical)

Both post `{}` to `AppInstallerService.svc/UnloadAppDomain` with a 30 s timeout, 3 attempts, 1 s apart
(clio: `RestartTool` sets `TimeOut = 30_000`, `RemoteCommandOptions` `MaxAttempts` 3, `RetryDelay` 1;
this server: `RestartApplication`); neither probes or polls after the answer. In T23 Go's request came 120 s after
clio's unload with no call between, so it was the request that started the unloaded site (~13 s on top of the
~1.5 s clio needed on a running one). Not measured with the order flipped (outage).

### `create-app` before a compile: order and stand state (code identical)

Both hold the request behind the same OData build gate before and after `CreateApp` (`IsODataBuildRunning`, 10 s,
one attempt; while a build runs, every 3 s up to 30 times) and read the same settings and icon list. Over all 22
create-app pairs of T23 (Go always second) the median is clio 13.51 s, Go 10.65 s; this server was faster in 20 of them.
The three slow ones each follow a heavy operation, one of them on clio: Go 32.65 (`window-t15-all`, first scenario
of that file), Go 38.56 (`window-t13-install`, right after the CrtProcessBuilder install), clio 40.14
(`window-t15-compile-rerun`). The second side starts while the first side's OData build may still run and waits
for it at the gate. Not measured with the order flipped (outage).

## Known differences, not changed

| Difference | clio 8.1.0.142 | This server | Why not changed here |
|---|---|---|---|
| Login timeout | 100 s | 45 s (client timeout) | `client.go` login path belongs to another stream; seen only on an unresponsive host |
| `GetIsFileDesignMode`, `LoadPackagesToDB/FileSystem` | `Timeout.Infinite`, 30 attempts 3 s apart | 30 min, one attempt | No live case where the retry mattered; a failure would be immediate, not slower |
| `get-fsm-mode` read | `Timeout.Infinite` | 45 s | Read tool, outside the three steps |

## Stand outage

At 00:25 (local time) the stand still answered: its login page in 0.08 s, its health ping after 22 s (a cold
start). The harness started at 00:27 and its very first call already went unanswered. From then on every HTTP
request to every site of the stand's IIS and the SSH banner of the host timed out, while TCP connect and ping still
answered and other corporate hosts answered through the same VPN tunnel, so the tunnel was not the cause. The host
was only probed, not changed (other stands live there). Polled every 50 s, it was still not answering at 03:01,
when the window ended; it needs someone at the host.

## Not verified live

- All three steps with the order alternated: the scenario files and the harness option are in place; the run is
  `compare-mcp-writes.py --order alternate --scenarios scripts/write-scenarios/timing/<file>.json` once the stand
  answers (fsm-off-while-off, fsm-off-from-on, restart-no-wait, then create-app).
- Fix 6faaf0c against a recycling site (unit-tested only).

## Cleanup

No object was created: the only run failed before any write reached the stand, and the `create-app` series never
started. The write ledger has no unremoved objects. A read of `SysSchema`, `SysPackage`, `SysInstalledApp`,
`Lookup`, `SysSettings` for `UsrParity*` / `Parity*` and the FSM state could not be made (stand not answering); the
last state seen was T23's: FSM off, no `UsrParity*` objects.
