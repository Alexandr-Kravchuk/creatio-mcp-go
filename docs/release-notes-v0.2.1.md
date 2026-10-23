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

## Write timing: three steps that looked slower

In the v0.2.0 live write run clio always called first. Reading both servers' code for the three steps
([status/t24-write-timing.md](status/t24-write-timing.md)):

| Step | v0.2.0 run: clio / Go | Verdict |
|---|---|---|
| `set-fsm-mode off` | 4.6 s / 14.8 s and 59.5 s (failed) | 59.5 s: the probe timeout above, fixed. 14.8 s: order — Go's request was the first after the site restarted |
| `restart-by-environment-name` without waiting | 1.4–1.6 s / 14.0–14.8 s | Order: same requests, timeout and retries on both; Go's request started the unloaded site |
| `create-app` before a compile | 16.5 s / 32.7 s | Order and stand state: over all 22 create-app pairs of that run the median is clio 13.5 s, Go 10.7 s |

The write harness can now alternate which server calls first (`compare-mcp-writes.py --order alternate`), and
`scripts/write-scenarios/timing/` holds the four timing scenarios. **The measurement with the order alternated was
not made:** the stand stopped answering HTTP before the first round. Until it is, the verdicts above come from the
code and the v0.2.0 numbers.

## Known differences, not changed

- On a host that accepts connections but never answers, clio's login waits 100 s and this server's 45 s, and the
  failure texts differ. Timeouts and TLS failures during login are transport failures now, but keep Go's wording.
- `GetIsFileDesignMode` and the package loads of `set-fsm-mode`: clio retries 30 times 3 s apart, this server tries
  once; `get-fsm-mode`: clio has no timeout, this server 45 s.
- A refused connection on Windows keeps Go's text (still a transport failure); the Linux and Windows host-not-found
  sentences are .NET's, not measured on those systems.

## Verification

`go vet`, `go test -race`, the Windows build, the script tests and the public-safety check pass on macOS; CI runs
the Linux and Windows jobs. Contract comparison against clio 8.1.0.142: 1025 match, 0 unexplained.
