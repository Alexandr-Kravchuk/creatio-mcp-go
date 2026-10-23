# creatio-mcp-go v0.2.1 — release notes

A fix release on top of [v0.2.0](release-notes-v0.2.0.md): what a side-by-side test against clio 8.1.0.142 found
after v0.2.0, and a closer look at three write steps that looked slower than clio's.

## Fixed

- **A stand that cannot be reached is no longer reported as wrong credentials.** When the login request itself got
  no answer (the host does not resolve, the connection is refused or reset, the deadline ran out while another call
  was logging in), every tool answered "Authentication error … check credentials". The login failure now keeps its
  transport category, and the text is the one clio gives: for the system-setting reads a `ProviderFailure` naming
  the `SysSettings` schema, for `create-sys-setting` and `get-schema-name-prefix` a `Network` failure, for the other
  tools .NET's wording (`<sentence> (host:port)`, the exception chain of an exit -1 envelope,
  `MCP tool 'x' failed: <sentence>`). A rejected login (bad credentials, HTTP 401/403, an OAuth token refusal) is
  still an authentication failure. Measured against clio with a temporary clio home on 110 tools: identical answers
  for a host that does not resolve went from 41 to 105, for a closed port from 41 to 105, for a rejected login from
  45 to 46, with no tool getting worse.
- **`list-user-tasks` writes clio's `Reading user task catalog from '<env>'...` Info line** before the catalog read,
  as clio does, both before the task list and before the error of a failed read.
- **`set-fsm-mode` no longer gives up on a restarting site after 45 s.** Its state probe (`GetApplicationInfo`)
  used the client's 45 s timeout; clio waits for it with no limit. Sent while the site restarted after the previous
  toggle, the probe timed out and the command ended with "Could not determine current FSM state". The probe now has
  the 30-minute bound the same command's package loads use; `get-fsm-mode` keeps 45 s. Reasoned from clio's source
  and the failure of the v0.2.0 live write run, covered by a unit test against a slow fake site; not reproduced on a
  real restarting site.

- **`delete-app` and `get-fsm-mode` wait for a slow site as clio does.** `delete-app` sent `UninstallApp` with the
  client's 45 s and one attempt; clio's `uninstall-app-remote` waits 100 s and tries three times, 1 s apart. On the
  timing stand an uninstall took 48–64 s: `delete-app` failed 7 of 7 on this server (the application was removed
  anyway) and succeeded 7 of 7 on clio. `get-fsm-mode` failed 3 of 3 at 45 s as the first request after a restart,
  where clio waited 61–70 s and answered; it now waits up to 30 minutes, as `set-fsm-mode`'s probe. Checked live
  after the fix: `get-fsm-mode` answered after 61 s as the first request after a restart, and `delete-app`
  succeeded.

## Write timing: three steps that looked slower

In the v0.2.0 live write run clio always called first. Measured again on a fresh stand with the order alternated
(`compare-mcp-writes.py --order alternate`, the scenarios in `scripts/write-scenarios/timing/`, 3 rounds per order
after a warm-up; medians, seconds; details in [status/t24-write-timing.md](status/t24-write-timing.md)):

| Step | v0.2.0 run: clio / Go | Go first: clio / Go | clio first: clio / Go | Verdict |
|---|---|---|---|---|
| `set-fsm-mode off`, already off | – | 0.63 / 0.10 | 0.60 / 0.10 | not slower |
| `set-fsm-mode off` from on, first side | 4.6 / 14.8 and 59.5 (failed) | Go 14.9 (n=6) | clio 14.6 (n=9) | same: the time is the stand's package import; 59.5 s was the probe timeout, fixed |
| `set-fsm-mode off` from on, second side | – | clio 70.5 | Go 66.1 | same: both wait for the recycled site |
| `restart-by-environment-name` without waiting | 1.4–1.6 / 14.0–14.8 | 3.08 / 0.56 | 2.51 / 0.60 | Go faster; v0.2.0's 14 s was order (its request started the unloaded site) |
| `create-app` | 16.5 / 32.7 | 53.5 / 55.8 | 62.9 / 65.0 | same; v0.2.0's 32.7 s was order and stand state |

The fresh stand is a different, slower host than v0.2.0's, so its absolute numbers are not comparable with the
v0.2.0 column. Early in the session 4 of Go's 8 `set-fsm-mode off` imports were slow (26.7–132.7 s, the slowest right
after the cliogate install); later runs imported in 13.2–16.5 s, as clio.

## Known differences, not changed

- On a host that accepts connections but never answers, clio's login waits 100 s and this server's 45 s, and the
  failure texts differ. Timeouts and TLS failures during login are transport failures now, but keep Go's wording.
- A host that stops resolving while this server holds a session for it: the request fails as a transport failure,
  as on clio, but with Go's wording (for example `get-fsm-mode`: `... dial tcp: lookup <host>: no such host`, where
  clio says `nodename nor servname provided, or not known`). clio logs in before every call, so it always reports
  the login's text; this server reuses its session and reports the request's.
- `GetIsFileDesignMode` and the package loads of `set-fsm-mode`: clio retries 30 times 3 s apart, this server tries
  once.
- A refused connection on Windows keeps Go's text (still a transport failure); the Linux and Windows host-not-found
  sentences are .NET's, not measured on those systems.

## Verification

`go vet`, `go test -race`, the Windows build, the script tests and the public-safety check pass on macOS; CI runs
the Linux and Windows jobs. Contract comparison against clio 8.1.0.142: 1025 match, 0 unexplained.
