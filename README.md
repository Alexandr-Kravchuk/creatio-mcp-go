# creatio-mcp-go

An MCP server for [Creatio](https://www.creatio.com), written in Go. It serves part of
[clio](https://github.com/Advance-Technologies-Foundation/clio)'s MCP tools with the same names and
response shapes, as one static binary that needs no .NET runtime.

**Preview:** read-only tools. One process serves every environment registered in clio; each call
names it with `environment-name`, as with clio.

## Install

Download the archive for your platform from
[Releases](https://github.com/Alexandr-Kravchuk/creatio-mcp-go/releases) and unpack it.

Claude Code:

```bash
claude mcp add creatio-go -- /path/to/creatio-mcp-go
```

Codex:

```bash
codex mcp add creatio-go -- /path/to/creatio-mcp-go
```

The server reads the environments from clio's `appsettings.json`. Without clio, set one default
environment with `--env CREATIO_URL=… --env CREATIO_LOGIN=… --env CREATIO_PASSWORD=…`.
OAuth, .NET Core sites, Windows and the tool list: [docs/install.md](docs/install.md).

## Documentation

- [Installation and usage](docs/install.md)
- [Research record](docs/research.md) — why this exists and how it compares with clio
- [Implementation notes](docs/implementation.md)
- [Releasing](docs/releasing.md) — CI, release script and workflow

## License

[MIT](LICENSE)
