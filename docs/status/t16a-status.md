# T16a: settings, registration, identity service and browser session (2026-10-23)

Six clio 8.1.0.142 tools that decision D3 had left in clio are now served: `reg-web-app`,
`check-settings-health`, `check-auth-code-flow`, `get-identity-service-config`, `get-browser-session`,
`clear-browser-session`. Branch `t16a-settings-58d76c`, three commits on `cb64392`, not pushed.

## Where the code is

| Tool | Files | clio source |
|---|---|---|
| `reg-web-app` | `cmd/creatio-mcp-go/tool_reg_web_app.go`, `internal/clioconfig/register.go`, `environment.go`, `internal/creatio/regwebapp_runtime.go`, `regwebapp_login.go` | `RegWebAppTool`, `RegAppCommand`, `SettingsRepository.ConfigureEnvironment` / `SetActiveEnvironment`, `EnvironmentSettings.Merge` / `Fill`, `EnvironmentRuntimeDetectionService` |
| `check-settings-health` | `tool_settings_health.go`, `internal/clioconfig/bootstrap.go`, `bind.go` | `SettingsHealthTool`, `SettingsBootstrapService` |
| `get-browser-session`, `clear-browser-session` | `tool_browser_session.go`, `internal/browsersession/session.go` | `GetBrowserSessionTool`, `ClearBrowserSessionTool`, `BrowserSessionService`, `BrowserSessionCache` |
| `check-auth-code-flow`, `get-identity-service-config` | `tool_identity_service.go`, `internal/creatio/identityservice.go` | `CheckAuthCodeFlowTool` / `Command`, `GetIdentityServiceConfigTool` / `Command`, `IdentityServerUrlResolver`, `IdentityServerProbe.IsDiscoveryReachable` |

Annotations are clio's `[McpServerTool]` values: `reg-web-app` and `get-browser-session` write
(confirmation-required by raw name), `clear-browser-session` is destructive, the other three are read-only.

## How the settings file is written

Every write goes through `knowledge.UpdateSettings` (`internal/knowledge/settings_write.go`, not changed): the
`appsettings.json.lock` file lock clio takes, a re-read under the lock, a temporary file with the file's own
permissions and one atomic replace. Before it, `clioconfig.Check` runs the repairing bootstrap, as clio's
`LoadLatestSettings` does (a missing file is created, a version below 3 is migrated), and the write is
refused with clio's texts while the file is broken or a member fails to bind (`settings-shape-mismatch`).
The refusal is checked again on the content read under the lock.

An environment is written as clio's `EnvironmentSettings` model serializes it: members in declaration order,
null members left out, `IdentityService` always written, `AuthFlow` as a number, `AuthAppUri` through its
getter (a `*.creatio.com` site gets its `-is` token endpoint), unknown members kept after the rest. Because
clio rewrites the whole file, every registered environment is re-serialized on each write (an entry without
`EnvironmentPath` gains `""`, as it does in clio). Merge rules are clio's: login/uri/password/maintainer keep
the stored value when blank, `Safe` is reset to `false` when not passed, `ClientSecret`, `AuthAppUri` and
`WorkspacePathes` are dropped when not passed, the client id and redirect settings are inherited, a key is
looked up ignoring case but added with its own case (`DEV` next to `dev`).

The next call sees the new environment without a restart: `environments.go` re-reads the file when its size
or modification time changes, and the atomic replace changes both
(`TestRegWebAppRegistersAnEnvironmentTheNextCallResolves` registers and then resolves in the same process).

`check-settings-health` binds the file member by member with Json.NET's conversion rules for each member
type of clio's `Settings` / `EnvironmentSettings` / `AutoUpdateSettings` model. The rules are checked against
96 answers clio 8.1.0.142 gave for hand-made files (`internal/clioconfig/testdata/health142.json`, replayed
by `TestCheckMatchesClio142Answers`, including the file's version and port range after a migration).

## Browser session

The cache is clio's: `<clio settings folder>/sessions/<slug>_<sha256(login|password|clientId|IsNetCore)[:16]>.storageState.json`,
Playwright storageState JSON as System.Text.Json writes it, an owner-only file in an owner-only directory,
written atomically; a cached session is reused when its `.ASPXAUTH` has not expired and the Shell answers
the cookies without a redirect, 401/403 or the login page. The code is the page-smoke branch's port
(`page-smoke-mobile-354089`, `internal/pagesmoke/session.go`) moved to `internal/browsersession` and
extended with `force-refresh` and `Clear`; a Secure cookie is left out of the export for an `http` address,
as `CookieContainer.GetCookies` leaves it out (measured). At integration page-smoke should call this package.

## Comparison with clio 8.1.0.142

Both servers were run with a temporary `HOME` and `CLIO_HOME` (never this machine's settings) on the same
call sequences, against no stand, a closed port and a local fake Creatio (forms and OAuth logins, .NET
Framework and .NET Core routes, 404 and 500 sites, an identity server with a discovery document, sys
settings). Answers and the resulting `Environments` section were diffed:

- 126 tool calls in 7 sequences, plus the cross-read and the 96 health files; equal except the differences below. Covered: every argument refusal, the active
  environment, Safe environments, merge rules, the bootstrap and shape-mismatch refusals, runtime detection
  (anonymous and authenticated, .NET Framework and .NET Core, unreachable, all-404, all-500, the
  kept-runtime warning), check-login (success, closed port, HTTP 500, OAuth 404), the session cache file
  (name and content byte for byte, reuse, force-refresh, clear), `check-auth-code-flow` (true, false, json,
  identity error, empty answer, closed port, login 500), `get-identity-service-config` (setting with a
  reachable discovery document, derived, unknown, blank, Safe).
- Cross-read: clio's `list-environments` on a file this server wrote and this server's on a file clio wrote
  give the same environments (one pre-existing `list-environments` difference, below).
- `scripts/compare-contracts.py` against 8.1.0.142: 1055 match, 58 known not-ported (29 tools on two
  surfaces), 1 known fit-multi-name, 0 unexplained. Each of the six tools matches on all five rows (index
  line, index entry, default, full and short contract). The inventory still lists them under T16, which is
  stale until it is regenerated.

## Known differences

- clio rewrites the whole `appsettings.json` from its model; this server rewrites the environments, the
  active key and the migrated members and leaves every other section as it found it. A file this server
  creates holds what clio's bootstrap sets (`deploy-creatio-defaults` port range, `SettingsVersion` 3, empty
  `Environments`), not clio's full default template.
- The parse error of an unreadable file is Go's text (`... could not be parsed: <reason>. Line L, position P.`),
  not Json.NET's; the classification and the refusal around it are clio's.
- A file with comments or trailing commas is read by `check-settings-health` as clio reads it, but a write
  to it fails in `knowledge.UpdateSettings`' strict parser, where clio rewrites the file without them.
- `reg-web-app` reads the stored environment from the file at call time. clio's worker can answer from a
  settings snapshot loaded before the call (measured: a file replaced behind clio's back was not seen by
  `FindEnvironment` until clio's next write).
- A rejected login reads `Unauthorized <login> for <uri>`; clio appends its login diagnostics in brackets
  (client ids, timings). Transport failures other than a refused connection, an unknown host and a reset
  (TLS, unusual socket errors) and a non-JSON SelectQuery answer in the detection keep Go's text.
- `add-from-iis`: macOS and Linux answer clio's WSMan text (measured). On Windows clio scans IIS through a
  PowerShell remoting runspace; this server refuses with its own text and points to clio.
- `environment-name: "open"` opens the settings file with `/usr/bin/open` or `cmd /c start`, as clio does;
  not run live (it opens an editor), so the message type of `Open <path>...` (`None`) is not measured.
- `get-identity-service-config` reads the All-Users value of the two settings; clio reads
  `QuerySysSettings` (the current user's value) first. Same as `verify-oauth-app`.
- `check-auth-code-flow` also accepts `environment-name`, as this server's identity assertion tools do; clio
  binds only `environmentName`. A blank or unknown name, a format and every answer are clio's.
- No `correlation-id` in the command envelope, as in every command-style tool here.

## Not verified

- No live stand (none was written to, as the task required); detection and logins only against fakes.
- Windows: cross-built (`GOOS=windows`), unit tests not run on Windows here (the Windows CI job runs them);
  `add-from-iis` on Windows is a refusal.
- OAuth success paths of the detection probe and of check-login (only the OAuth 404 failure was compared).

## Follow-ups for other owners

- `tool_list_environments.go`: clio shows `environmentPath: ""` for an environment whose entry has no
  `EnvironmentPath` (the member defaults to `""`); this server leaves the key out. Seen on the most recently
  registered environment, which is the only one without the member.
- `environments.go`: with an `ActiveEnvironmentKey` that names no environment (for example set with another
  case through `reg-web-app active-environment`), clio refuses every environment tool with "clio settings
  bootstrap is broken. Repair <path> ..."; this server keeps resolving names.
- The identity assertion tools (`get-identity-assertion`, `get-identity-public-jwk`,
  `regenerate-identity-signing-key`) answer a Safe environment with an exit -1 envelope and word login and
  transport failures with this server's texts; `check-auth-code-flow`, which shares clio's RemoteCommand
  path, raises the Safe refusal and uses clio's `GetReadableMessageException` texts
  (`remoteCommandFailureText`). Worth checking those three against clio.
- page-smoke: use `internal/browsersession` instead of its own copy of the cache and login.
