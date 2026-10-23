# T27: page-smoke and get-mobile-page-conversion-guide (clio 8.1.0.142)

The two page tools T21a left out ([t21a status](t21a-page-package-status.md#not-ported)).

| Tool | Status | Go |
|---|---|---|
| `page-smoke` | ported, registered | `cmd/creatio-mcp-go/tool_page_smoke.go`, `internal/pagesmoke`, `internal/creatio/pagesmoke_support.go` |
| `get-mobile-page-conversion-guide` | engine ported (530/530 recorded clio `Analyze` answers byte-equal), handler/probes open, not registered | see [t27-mobile-guide-section.md](t27-mobile-guide-section.md) |

## page-smoke

Ported from `PageSmokeTool`, `PageSmokeRunner`, `FreedomUiPageScripts`, `PageEventCollector` and the parts of
`Common/BrowserSession` page-smoke uses. Annotations as clio's attribute: destructive, not read-only, not idempotent;
a direct raw-name call answers confirmation-required, `clio-run` runs it.

- **Arguments**: clio's binder with the MCP serializer's web defaults — member names match ignoring case (`Page`
  binds `page`), a missing `environment-name` or `page` fails the whole record, numbers are read from strings too;
  then `BuildLegacyAliasError` with the tool's aliases, then `Validate` in clio's order.
- **Browser** (`resolver.go`, `locate.go`, `settings.go`): CHROME_PATH, the remembered answer (`page-smoke-browser`
  in clio's appsettings.json, written under clio's settings lock, `decided-at` in Json.NET's spelling), the
  consent question listing Playwright/Puppeteer caches first, then installed browsers, the pinned download and
  "no"; every code (`browser-consent-required`, `-declined`, `-ready`, `-downloading`, `-not-found`,
  `-download-failed`).
- **Download** (`headlessshell.go`, `proxy.go`): chrome-headless-shell 155.0.8059.79 from Chrome for Testing (or
  `CLIO_CHROME_FOR_TESTING_MIRROR`) into `<clio home>/browsers/<version>`, pinned size and SHA-256, resumed with a
  Range request from `.partial-<version>-<platform>.zip`, 90 s per call, zip extracted with the executable bits,
  published by rename.
- **Browser process** (`spawn_unix.go`, `spawn_windows.go`, `launcher.go`): `--remote-debugging-pipe` on fd 3/4
  (Unix, own process group) or `--remote-debugging-io-pipes=<read>,<write>` with an inherited-handle list and no
  window (Windows); no debugging port; a throw-away 0700 profile; tree kill and profile delete on dispose.
- **CDP** (`cdp.go`): NUL-framed messages, a 4 MiB reply bound with clio's too-large sentinel, flat page session,
  detach closes the session.
- **Session** (`session.go`): clio's browser session cache — `<clio home>/sessions/<slug>_<sha256[:16]>.storageState.json`,
  reused while the Shell accepts it (clio 8.1.0.142's `IsSessionExpiredResponse`), else a forms login; the same file
  `get-browser-session` writes.
- **Runner** (`runner.go`, `events.go`, `scripts.go`, `scripts_gen.go`): cookies, `Page.navigate`, the ready wait
  (1.5 s quiet, login/classic seen twice), inventory, the click with the created-record watcher of run-process /
  trigger-smoke, the settle wait, overlays, invalid fields, the 14 KB reply bound (`Fit`, sized as
  System.Text.Json's default output). The page scripts are generated from clio's source:
  `python3 scripts/generate-page-smoke-scripts.py --clio-source <clio> [--check]`.
- **Answer**: written with clio's MCP result encoder (relaxed escaping, nulls left out), so texts are byte-equal.

### Verification

Against `dotnet clio.dll mcp-server` of clio 8.1.0.142, each side with its own temp HOME and CLIO_HOME, compared
as raw text (correlation-id masked; the temp root in the download path replaced):

- No stand (environment on a closed port, a Safe one, an unknown one): 42 calls — every validation branch, wrong
  JSON types, missing required members, case-insensitive names, aliases, the consent question, decline and the
  remembered decline, CHROME_PATH with and without a remembered answer, `browser=existing` (offered and not
  offered), the forms login on a closed port, `browser=download` with a refused mirror and with an invalid mirror
  value. 41 byte-equal; 1 differs only in the order of renamed/unknown argument names (below).
- Real headless Chrome on the fake stand `scripts/fake-stands/t27/fake_shell.py` (forms login + a Freedom UI Shell
  stub): open in add and edit mode, a click that navigates, logs, throws and shows a toast, an ambiguous caption,
  a disabled button, a missing button. 6 of 6 byte-equal with `loadMs` masked.
- Unit tests: `internal/pagesmoke/pagesmoke_test.go`, `cmd/creatio-mcp-go/tool_page_smoke_test.go`.
- `scripts/compare-contracts.py`: 1030 match, 68 known:not-ported, 1 known:fit-multi-name, 0 unexplained
  (before: 1025 / 70). page-smoke moved to match; the inventory's `tasks` map still labels it not ported
  (integration regenerates `docs/clio-inventory.*`).

Not measured: the `browser-downloading` / `browser-ready` paths against a real archive (unit-level only), the
watched entities of a click (shared watcher code, measured for trigger-smoke), Windows and Linux runs.

### Known differences

- Unknown and renamed argument names are listed sorted; clio lists them in request order (Go maps keep none).
  When two keys spell one member in different case, the exact spelling wins here; clio takes the last one sent.
- The headless-shell download uses HTTPS_PROXY / HTTP_PROXY / NO_PROXY only; clio falls back to the operating
  system's proxy settings (WinHTTP/PAC, macOS System Configuration), which Go cannot read.
- A transport failure other than a refused connection or an unknown host keeps Go's error text in the download
  and launch messages.
- The remembered answer is read from appsettings.json on each call; clio reads its copy loaded at start. A new
  `page-smoke-browser` key is appended at the end of the file; clio rewrites the file in its own key order.
- Directory listings of the browser caches are in name order; .NET lists them in file-system order (only matters
  when one cache folder holds two matching executables).
- `record-id` accepts the N, D, B and P GUID formats; clio's Guid.TryParse also accepts the X format.

### Overlap

`internal/pagesmoke/session.go` carries clio's browser session cache, which `get-browser-session` /
`clear-browser-session` (T16) also write; the T16 port should reuse it (note left in the session notes folder).

## get-mobile-page-conversion-guide

Not finished in this task: the rules data, the rules catalog, the answer types and the refusals that need no
analysis are in (`internal/mobileguide`, `scripts/generate-mobile-conversion-rules.py`); the analysis engine
(`WebToMobileAnalysisService`, 8.2K lines), the tool flow, the four probes and the fake stand are not. The tool stays
unregistered, because clio's own comments say a partial guide is worse than none. Details, the exact remaining
steps and the `CLIO_*_LOCAL_FILE` names: [t27-mobile-guide-section.md](t27-mobile-guide-section.md).
