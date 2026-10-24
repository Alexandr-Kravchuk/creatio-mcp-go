# creatio-mcp-go

An MCP server for [Creatio](https://www.creatio.com), written in Go. It answers
[clio](https://github.com/Advance-Technologies-Foundation/clio)'s MCP tools with the same names, arguments,
contracts and response shapes as clio **8.1.0.142**, as one static binary that needs no .NET runtime.

One process serves every environment registered in clio; each call names it with `environment-name`, as
with clio.

## What it serves

Read **and write** tools: applications, entity and other schemas, Freedom UI pages, business rules, data
(OData, ESQ, DataService batches, SQL scripts, data bindings), system settings, users and access rights,
business processes, themes and branding, packages, compilation and restart, Data Forge, and clio's
knowledge, toolkit and telemetry tools. tools/list carries clio's 19 resident tools; the rest are called
through `clio-run` / `clio-run-destructive` or by name, and `get-tool-contract` returns clio's own contract
for each. The 74 MCP prompts and the guidance resources come from the same clio-knowledge bundles clio
installs.

Not in this release:

- the 33 local-machine, infrastructure and workspace tools (`deploy-creatio`, `restore-db-*`,
  `clear-redis-*`, `create-/push-/restore-workspace`, `reg-web-app`, `stop-creatio`, …) — left to clio on
  purpose (decision D3 in [docs/migration-plan.md](docs/migration-plan.md));
- the Git transport of `install-knowledge` / `update-knowledge`: a `git`-type knowledge source is installed
  and updated with clio;
- `page-smoke` (it needs a headless Chromium driven over the DevTools protocol) and
  `get-mobile-page-conversion-guide` (it needs clio's web-to-mobile analysis engine): use clio for them.

Coming from v0.1.x: `odata-read` now takes clio 8.1.0.142's arguments and answers in its shape (the old
`{rows, count}` answer and the `orderBy` key are gone), and `execute-esq` answers as clio 8.1.0.142 does;
see "Breaking changes" in the [release notes](docs/release-notes-v0.2.0.md#breaking-changes).

Exact counts, what was compared with clio and how:
[docs/status/clio-comparison-2026-10-20.md](docs/status/clio-comparison-2026-10-20.md). Known differences
from clio are listed per area in [docs/status](docs/status).

## Install

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/Alexandr-Kravchuk/creatio-mcp-go/main/scripts/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/Alexandr-Kravchuk/creatio-mcp-go/main/scripts/install.ps1 | iex
```

The installer downloads the newest release (pre-releases included), checks it against `SHA256SUMS`,
puts the binary into `~/.local/bin` (Windows: `%LOCALAPPDATA%\creatio-mcp-go\bin`) and registers it as
`creatio-go` in Claude Code and Codex, whichever is installed. Run it again to update. Options
(`--version`, `--dir`, `--name`, `--no-register`) are in [docs/install.md](docs/install.md#1-install).

Homebrew (macOS, Linux): `brew install alexandr-kravchuk/tap/creatio-mcp-go`, then register
`$(brew --prefix)/opt/creatio-mcp-go/bin/creatio-mcp-go` (`brew info creatio-mcp-go` prints the commands).

Manual install: download the archive for your platform from
[Releases](https://github.com/Alexandr-Kravchuk/creatio-mcp-go/releases), unpack it, then:

```bash
claude mcp add creatio-go -- /path/to/creatio-mcp-go
codex mcp add creatio-go -- /path/to/creatio-mcp-go
```

The server reads the environments from clio's `appsettings.json`; a call without `environment-name` is
refused, as in clio. Without clio (no `appsettings.json`), set one environment with
`--env CREATIO_URL=… --env CREATIO_LOGIN=… --env CREATIO_PASSWORD=…`; calls without a name go there.
OAuth, .NET Core sites, Windows, knowledge bundles and write safety: [docs/install.md](docs/install.md).

## Documentation

- [Installation and usage](docs/install.md)
- [Release notes v0.2.1](docs/release-notes-v0.2.1.md), [v0.2.0](docs/release-notes-v0.2.0.md)
- [Comparison with clio 8.1.0.142](docs/status/clio-comparison-2026-10-20.md)
- [creatio-mcp-go or clio MCP](docs/clio-mcp-comparison.md) — pros, cons and measured figures for choosing a server
  ([as slides](docs/clio-mcp-comparison.html))
- [Migration plan](docs/migration-plan.md) — tasks, decisions and status
- [Research record](docs/research.md) — why this exists and how it compares with clio
- [Implementation notes](docs/implementation.md)
- [Parity harnesses](docs/parity.md)
- [Releasing](docs/releasing.md) — CI, release script and workflow

## License

[MIT](LICENSE)
