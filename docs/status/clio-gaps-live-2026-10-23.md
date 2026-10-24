# Six behaviour gaps closed and checked live (2026-10-23)

Date: 2026-10-23. Reference: clio 8.1.0.142 (`clio.dll`, `mcp-server`). This server: built from this branch after
the six fixes below. Stand: a disposable on-premises stand deployed for this check (Sales Enterprise + Marketing +
Service Enterprise 10.2.735, MSSQL, .NET Framework on IIS, a datacenter dev server), reached from macOS. Its URL and
environment name are not recorded here.

## What changed

| Tool | clio 8.1.0.142 | This server before | Now |
|---|---|---|---|
| `list-user-tasks`, `describe-business-process` | `[RequiresPackage(CrtProcessBuilder)]` plus the convergence rule: an environment whose CrtProcessBuilder is older than the archive clio carries (1.6.6.104) is refused, `This clio carries CrtProcessBuilder 1.6.6.104, but the target environment has <v>. ...` | presence only | the gate the process write tools use (`processWriteGate`) |
| `get-theme`, `list-themes` | `[RequiresCreatioVersion("10.0.0")]` before the command: `... [version-too-old]`, `[version-check-failed]`, `[version-undeterminable]` | no floor | the gate the theme write tools use (`themeWriteResolve`) |
| `set-fsm-mode` (`GetIsFileDesignMode`, `LoadPackagesToDB/FileSystem`) | 30 attempts 3 s apart for a request that got no answer; login once before the loop; any HTTP answer ends the loop (Creatio.Client 2.1.0) | one attempt | the same loop (`adminWriteFsmPost`) |
| `list-user-tasks` without `environment-name` (CREATIO_* target) | no such mode | `Reading user task catalog from ''...` | quotes the target URL; `compare-mcp.py` accepts that line only in `--go-env-mode=variables` (`known-differences-variables`) |

Every change has a unit test that fails without it.

## Live check

Evidence (verdicts, timings and differing JSON paths only): `evidence/clio-gaps-2026-10-23/`.

### Stand preparation

| Step | Time |
|---|---:|
| Deploy (TeamCity `DeployApplicationCommon`, 30 s in the queue not counted) | 398 s |
| `install-gate` (clio 8.1.0.142, cliogate 2.0.0.53) | 339 s |
| `install-process-builder` with clio 8.1.0.134, which carries CrtProcessBuilder 1.6.5.14 | 165 s |
| `install-process-builder` with clio 8.1.0.142 (1.6.6.104), after the 1.6.5.14 cases | 188 s |

### CrtProcessBuilder 1.6.5.14: the convergence refusal

29 cases: the `list-user-tasks`, `describe-business-process`, `get-theme`, `list-themes` and
`validate-process-graph` cases of the parity files.

| Binary | match | both-failed | mismatch |
|---|---:|---:|---:|
| before (`df4198a`) | 24 | 2 | 3 (`list-user-tasks default`, `describe process by code`, `describe process by caption`: clio refused, this server answered) |
| after | 28 | 1 | 0 |

The remaining both-failed case, `validate-process-graph unknown key`, was both-failed in the 2026-10-22 run as well.
In the before run `describe process no identity` was both-failed too: clio refused with the convergence text, this
server with the identity rule.

### CrtProcessBuilder 1.6.6.104: full read run

`compare-mcp.py --cases scripts/mcp-parity-cases.json scripts/parity-cases/*.json`, name mode, the case set of the
[2026-10-22 run](compare-2026-10-22.md).

| Verdict | 2026-10-22 | 2026-10-23 |
|---|---:|---:|
| match | 823 | 826 |
| known-diff | 41 | 40 |
| both-failed | 17 | 17 |
| mismatch | 2 | 0 |
| **total** | **883** | **883** |

Three cases changed verdict, all to match: `list-user-tasks default` (the catalog read line, v0.2.1), `list-pages
search` (pages with equal sort keys; it matched this time) and `page with editable schema` (clio's regional date
format; equal this time). No case got worse. Timings (match and known-diff, n=866): clio 753.6 s, Go 396.7 s; this
stand is slower than the 2026-10-22 one for both servers. Process start: clio 1.97 s, Go 0.08 s.

### `--go-env-mode=variables`

The 19 cases of `list-user-tasks`, `describe-business-process`, `get-theme` and `list-themes`: 18 match, 1 known-diff
(`list-user-tasks default`: the catalog read line quotes the target URL here, by design).

### `set-fsm-mode off` from on (write)

`compare-mcp-writes.py --scenarios scripts/write-scenarios/timing/fsm-off-from-on.json --order alternate` (7 rounds,
round 0 is the warm-up), this server's requests through a local logging HTTP proxy. All 7 rounds: known-diff only
in the execution log (the first side imports, the second finds file design mode disabled), every mode check matched,
FSM off at the end.

| `off`, seconds | clio | Go |
|---|---|---|
| first side, whole path (import, then cliogate's `SetFileDesignMode`) | 15.11, 14.61, 14.60 (median 14.61) | 14.77, 13.97, 14.49 (median 14.49) |
| second side, the first request after the first side's recycle | 276.90, 80.68, 202.32 (median 202.32) | 137.52, 125.47, 176.58 (median 137.52) |

The new retry loop was not exercised: in all 7 rounds each of this server's `GetIsFileDesignMode` requests was
answered (HTTP 200, the second side's after 93–176 s while the site restarted) and none got no answer, so no request
was sent again. The proxy recorded 35 requests from this server and no failure.

### Fake stands against the real clio

Two canned sites (CrtProcessBuilder 1.6.6.103 with core 8.3.3, CrtProcessBuilder 1.6.6.104 with core 10.2):
`list-user-tasks default`, `get-theme unknown id`, `get-theme non-GUID id` on each. Before: 3 match, 2 both-failed
(the `get-theme` refusals worded differently on the old site), 1 mismatch (`list-user-tasks` on the old site). After:
6 match.

## Not changed

- `pkg-to-db` / `pkg-to-file-system` (`pkgWriteRetryPost`) retry every failure, HTTP 500 and HTML answers and the
  login included; Creatio.Client retries only a request that got no answer.
- `set-fsm-mode` in `--go-env-mode=variables` prints `environment ''` in its own messages.
