# T25: network-failure answers after the login and when the login hangs

Date: 2026-10-23. Reference: clio 8.1.0.142 (`clio.dll`, `mcp-server`). This server: before = `df4198a` (v0.2.1),
after = `87bd443`. Measured on macOS and on Windows 11 Pro (en-US, a workstation reached over ssh) against a local
fake stand; each server ran with a temporary `CLIO_HOME` holding one environment that points at the fake stand. Only
the `fresh-dns` scenario uses the system resolver (`nonexistent.invalid`).

## Question

v0.2.1 left three groups of network failures worded differently from clio
([release notes](../release-notes-v0.2.1.md), "Known differences, not changed"):

1. A host that stops resolving or refuses connections while this server holds a session for it: the request
   failed with Go's wording (`get-fsm-mode`: `GetApplicationInfo request failed: Post "...": dial tcp: lookup
   <host>: no such host`). The working assumption was that clio logs in before every call and therefore always
   answers with the login's text. Also: does a connection reset in the middle of a request stay a request failure?
2. A host that accepts TCP and never answers: clio's login waits 100 s, this server's 45 s, with different texts.
3. Leftovers of the v0.2.1 hand-over: the schema transfer tools read a failed cliogate check as a refusal; the
   package hotfix tools print `[Exception] S (host:port)` instead of clio's exception chain; a refused connection
   on Windows (`WSAECONNREFUSED`) was not recognised.

## What clio does (measured)

`scripts/fake-stands/t25/network_faults.py` calls each of 114 tools (the 110 of the v0.2.1 comparison plus
`export-schema`, `import-schema`, `unlock-for-hotfix`, `finish-hotfix`) on one server while a local stand
changes behaviour: refuses (port closed), resets every connection after reading the request, or never answers;
`cached-*` scenarios first let the tool's two calls log in and then take the stand down (or make it reset every
request but the login).

**clio does not log in before every call.** It keeps a container per environment (`SessionContainerCache`, 5 min
idle) and Creatio.Client skips the login while it holds a session. In the `cached-down` run 98 of 113 tools
reused the session for the second call; the 15 tools below logged in again on every call (four of them run in
clio's worker processes):

`compile-status`, `execute-esq`, `get-app-info`, `get-component-info`, `get-fsm-mode`, `get-process-signature`,
`get-request-info`, `get-schema-name-prefix`, `get-sys-setting`, `get-target-package`, `get-user-culture`,
`list-app-sections`, `list-apps`, `list-pages`, `list-sys-settings`.

So for the other tools a host that goes away after the first call fails **the call's own request**, and
Creatio.Client raises a different exception there than at the login:

| Fault | At the login (WebException) | At a call made with a session (AggregateException of `Task.Result`) |
|---|---|---|
| Host does not resolve / refused | `S (host:port)`; chain `[WebException] S (host:port) → [HttpRequestException] S (host:port) → [SocketException] S` | `One or more errors occurred. (S (host:port))`; chain `[AggregateException] One or more errors occurred. (S (host:port)) → [HttpRequestException] … → [SocketException] S` |
| Reset after the request was sent | `An error occurred while sending the request.`; chain `… → [HttpRequestException] An error occurred while sending the request. → [IOException] Unable to read data from the transport connection: Connection reset by peer. → [SocketException] Connection reset by peer` | `One or more errors occurred. (An error occurred while sending the request.)`, same inner chain |
| No answer (login only) | `The operation was canceled.` after 100 s; chain `[WebException] The operation was canceled. → [OperationCanceledException] The operation was canceled.` | not measured |

`S` is the socket sentence (`Connection refused`; for a host that does not resolve on macOS `nodename nor
servname provided, or not known`). Further:

- `ExecuteGetRequest` catches the `HttpRequestException` of its GET and returns an empty body, so
  `list-page-templates`, `create-page`, `last-compilation-log` and `get-email-template` report what an empty
  answer gives (`… unparseable response … <empty body>`, System.Text.Json's "does not contain any JSON tokens",
  `Creatio OData BulkEmail response was empty.`).
- Helpers that catch the login's `WebException` (PageSchemaMetadataHelper: `SelectQuery could not be completed …
  Cannot connect to the application …`) do not catch the AggregateException: `get-page` and the other page tools
  answer its message as is.
- ATF's provider keeps the outer message and its inner one: `One or more errors occurred. (X)X`.
- A reset after the request was sent stays the call's failure, also for the 15 tools above: their login to the
  resetting host succeeded (`cached-reset`: the third login count is 1) and the call was reset.
- A login that gets no answer waits Creatio.Client's own 100 s (`EnsureAuthenticatedAsync(100000)`), whatever
  the request's own timeout is (10 s for `ExecutePostRequest`), and fails with `The operation was canceled.`;
  `get-page` words it `SelectQuery timed out before the environment answered (URL: …). Detail: The operation was
  canceled. (WebException: Timeout). This says nothing …`.
- `export-schema`/`import-schema` (cliogate gate) and `unlock-for-hotfix`/`finish-hotfix` answer exit -1 with the
  exception chain of whichever exception reached them.
- `restart-by-environment-name` (RemoteCommand) logs GetReadableMessageException: a login that could not connect
  is `Cannot connect to the application: <message> (WebException: ConnectFailure). Make sure the site is running
  and accessible.` (found by this run: the v0.2.1 comparison only had its blank-environment case).

The brief's assumption (clio logs in before every call) was wrong for most tools, so making every request fault
read like a login fault would have matched only the 62 tools where both of clio's texts coincide. Decided with the
owner (2026-10-23): follow clio as measured; let the login wait 100 s with clio's text; leave clio's 5-minute idle
expiry of the session for later (see "Known differences").

## Result

Identical answers after `compare-mcp`'s normalization (the stand's port replaced by `PORT`), macOS:

| Scenario | Tools | Identical before | Identical after | Got worse | Still differ |
|---|---:|---:|---:|---|---|
| `fresh-dns` | 114 | 104 | 109 | — | validate-page, get-theme, modify-business-process, create-app, create-entity-schema |
| `fresh-down` | 114 | 104 | 109 | — | validate-page, get-theme, modify-business-process, create-app, create-entity-schema |
| `fresh-reset` | 114 | 48 | 109 | — | validate-page, get-theme, modify-business-process, create-app, create-entity-schema |
| `fresh-hang` | 10 | 2 | 10 | — | — |
| `cached-down` | 113 | 43 | 109 | — | validate-page, modify-business-process, create-app, create-entity-schema |
| `cached-reset` | 113 | 43 | 109 | — | validate-page, modify-business-process, create-app, create-entity-schema |

`cached-*` compare 113 tools: `restart-by-environment-name` runs in the `fresh-*` scenarios only, because the calls
that establish the session start restarts clio keeps tracking, and its recorded answer would only be "an operation
of the 'Restart' family is already running". Its case calls the tool for real (`waitReady: false`), where the v0.2.1
comparison had only the blank-environment validation case. So `fresh-down` before is 104 of 114 where v0.2.1
counted 105 of 110: the four tools added here differed, and the restart's real call differed (the bare message).

Windows 11 Pro (en-US), the same binaries built for windows/amd64 and the same harness:

| Scenario | Tools | Identical before | Identical after | Got worse | Still differ |
|---|---:|---:|---:|---|---|
| `fresh-dns` | 114 | 104 | 109 | — | validate-page, get-theme, modify-business-process, create-app, create-entity-schema |
| `fresh-down` | 114 | 44 | 109 | — | validate-page, get-theme, modify-business-process, create-app, create-entity-schema |
| `fresh-reset` | 114 | 44 | 109 | — | validate-page, get-theme, modify-business-process, create-app, create-entity-schema |
| `fresh-hang` | 5 | 0 | 5 | — | — |
| `cached-down` | 113 | 44 | 109 | — | validate-page, modify-business-process, create-app, create-entity-schema |
| `cached-reset` | 113 | 44 | 109 | — | validate-page, modify-business-process, create-app, create-entity-schema |

On Windows clio words the faults with the system's texts, now measured: `No such host is known.`, `No connection
could be made because the target machine actively refused it.`, `An existing connection was forcibly closed by the
remote host.` (inside `Unable to read data from the transport connection: …`). Before, this server matched only the
first: a refused or reset connection is a Winsock error (`WSAECONNREFUSED`, `WSAECONNRESET`) that it did not
recognise, so it kept Go's text. The same 15 tools log in on every call on Windows. A refused connection takes clio
about 2 s on Windows (median 2.03 s per tool in `fresh-down`); this server answers it at once. The texts are the
same, the time is not, and nothing was changed for it.

Hung login on macOS, per tool (seconds until the answer; on Windows the five tools measured give the same 100 s
and the same texts):

| Tool | clio s | Go before s | Go after s | Before | After |
|---|---:|---:|---:|---|---|
| `describe-environment` | 100.12 | 45.0 | 100.01 | match | match |
| `execute-sql-script` | 100.09 | 45.0 | 100.0 | differ | match |
| `get-fsm-mode` | 100.06 | 45.0 | 100.0 | differ | match |
| `get-page` | 100.09 | 45.0 | 100.0 | differ | match |
| `get-record-rights` | 100.06 | 45.0 | 100.0 | differ | match |
| `get-sys-setting` | 100.13 | 45.01 | 100.01 | differ | match |
| `get-user-culture` | 100.08 | 45.0 | 100.0 | match | match |
| `list-apps` | 100.07 | 45.0 | 100.0 | differ | match |
| `list-packages` | 100.09 | 45.0 | 100.0 | differ | match |
| `list-user-tasks` | 100.1 | 45.0 | 100.0 | differ | match |

The four tools that still differ in every scenario differ in argument validation, before any request
(`validate-page`, `modify-business-process`, `create-app`, `create-entity-schema`), and `get-theme` lacks clio's
platform version gate; both are older differences, unrelated to the network. Per-tool answers:
[evidence/t25/network-faults.json](../../evidence/t25/network-faults.json) (macOS) and
[evidence/t25/network-faults-windows.json](../../evidence/t25/network-faults-windows.json).

## What changed

- `connectfault.go`: a `connectFault` has a kind (no connection, reset after the request was sent, login timeout)
  and knows whether the login or a call made with a session met it; its message, exception chain and ATF provider
  text follow the table above. `requestConnectFailure` classifies a call's fault (`doAuthenticated`,
  `dataWriteSendOnce`, `processWriteSendOnce`); a call marked with `WithLoginBeforeEachCall` keeps the login's
  wording for a connect fault. `getAnswersEmpty` is ExecuteGetRequest's empty body, used by the template catalog,
  the compilation log and the OData read of the email template tools. Windows: `WSAECONNREFUSED` and
  `WSAECONNRESET` are recognised (`connectfault_windows.go`) and get the Windows sentences.
- `cmd/creatio-mcp-go/clio_login.go`: the 15 tools clio logs in for on every call.
- `client.go`: the login before a call runs on its own 100 s (`loginTimeout`), not on the request's deadline, and
  still stops when the call is cancelled; waiting for another call's login is bounded the same way. A route prefix
  (`… transport failure:`) is no longer put in front of a connect fault (`isRequestTransportError`).
- Page metadata reads word only the login's fault as `SelectQuery could not be completed …` and add the timeout
  text; `rightsTransportFault` takes a classified fault first (a login timeout reads `The operation was
  canceled.`, not `A task was canceled.`).
- `SchemaTransferRequirement` returns the read error (exit -1 with the chain), as `DataWriteSQLRequirement` does;
  `PkgWriteExceptionText` prints a connect fault's chain.
- `RestartApplication` (restart-by-environment-name, restart-by-credentials) words its failure with clio's
  GetReadableMessageException (`readableMessage`).
- Empty-body texts as clio words them: the template catalog (`… unparseable response (URL: …). Parser error: …`)
  and the compilation log (System.Text.Json's message for an empty body).

## Tests

Unit tests with injected transports and local listeners only (no DNS):

- `requestfault_test.go`: `TestCallOfAHostThatWentAwayCarriesTheCallsException` (DNS and refused, with a held
  session: the error, the chain, list-apps, get-page, get-record-rights, the three empty-body tools),
  `TestResetAfterTheRequestWasSentIsTheFailedRead` (login and call; a reset while writing keeps Go's text),
  `TestPackageHotfixReportsTheExceptionChain`, `TestRestartReportsClioReadableMessage`. All four fail on `df4198a`
  with the old texts.
- `connectfault_test.go`: `TestLoginRunsOnItsOwnTimeout` (a hanging listener; the request's 20 ms deadline does
  not cut the login; texts of the chain, ATF, get-page, get-record-rights), `TestLoginWaitKeepsItsCategory`
  (rewritten: a waiter waits for the login timeout, a cancelled waiter stops at once),
  `TestLoginPerCallToolKeepsTheLoginWording`, `TestSchemaTransferGateReportsAnUnreadablePackageList`, Windows
  sentences in `TestConnectSentenceIsDotNetSocketTextPerPlatform`. These use new API and do not compile on
  `df4198a`.
- `connectfault_windows_test.go` (Windows only; passed on the workstation, see below): `WSAECONNREFUSED` and `WSAECONNRESET` become .NET faults.
- `cmd/creatio-mcp-go/clio_login_test.go`: a stand that goes away after `find-app` logged in: `get-app-info`
  answers the login's wording, `find-app` the AggregateException's; fails with the tool list switched off.
- Existing tests that compared against `Connection refused (…)` now use the platform's sentence.

On the Windows workstation the windows/amd64 test binaries of `internal/creatio` and `cmd/creatio-mcp-go` pass in
full (no `-race`: it needs cgo there), `TestWindowsSocketErrorsAreDotNetFaults` and the cmd test against a real
closed port included.

Gate on the final tree: `go vet ./...`, `GOOS=windows GOARCH=amd64 go vet` and `go build ./...`,
`go test -race ./...`, `python3 -m pytest -q scripts` (74 passed, 8 skipped), `scripts/check-public-safety.sh`,
`compare-contracts.py` (1025 match, 70 known not-ported, 1 known multi-name, 0 unexplained).

## Reproduce

```sh
go build -o /tmp/go-after ./cmd/creatio-mcp-go
for sc in fresh-dns fresh-down fresh-reset cached-down cached-reset; do
  python3 scripts/fake-stands/t25/network_faults.py probe clio <clio.dll> $sc clio-$sc.json &
  python3 scripts/fake-stands/t25/network_faults.py probe go /tmp/go-after $sc go-$sc.json &
done; wait
python3 scripts/fake-stands/t25/network_faults.py compare clio-cached-down.json go-cached-down.json
python3 scripts/fake-stands/t25/network_faults.py probe clio <clio.dll> fresh-hang hang-clio.json --tools get-page --timeout 400
```

## Known differences, not changed

- **clio forgets an idle session after 5 minutes** (`SessionContainerCache.IdleTtl`, read in the source, not
  measured); this server keeps it until the site rejects it. After a pause longer than that, clio's next call logs
  in again and a host that went away gets the login's wording, where this server words the call's fault. Left for
  later by the owner's decision.
- Theme and admin write tools (`set-logo`, `manage-*` and the like) check the platform version first. With a
  session held, clio's probe lets the call's AggregateException through as the tool's failure; this server reports
  the version check as failed. Not measured: the probed cases fail argument validation first.
- A call that hangs after the login (request timeouts) keeps this server's texts; clio's request timeouts differ
  per call and were not measured. A reset while the request is still being written, or during the connect, keeps
  Go's text.
- The Windows sentences were measured on an English Windows 11. On a Windows in another language clio shows them
  translated; this server always prints the English ones.
- The OAuth token request keeps the client's 45 s timeout; clio's token request was not measured.
- Older, not network: the template catalog's HTML-answer text differs from clio's; `get-theme` has no version gate;
  argument validation of the four tools above.
