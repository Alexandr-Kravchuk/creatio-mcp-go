# creatio-mcp-go

## 1. Question

Can clio's `list-apps` behaviour be reproduced from Go, without `ATF.Repository` or `creatio.client`, with output matching clio?

## 2. Answer and evidence

**Not established in this checkout yet.** The implementation is a deliberately small, wire-level pilot, but its required live comparison could not run in the build environment: no Go toolchain is installed and outbound DNS is unavailable, so the official MCP Go SDK cannot be downloaded. The harness records the required evidence once those prerequisites are available: it runs clio `list-apps --json` and the Go client against the same forms-authenticated environment, normalizes the two result sets, and writes only counts, SHA-256 fingerprints, and mismatch counts to `evidence/latest.json`. Raw application data is never persisted.

The C# reference shows that this command uses **Creatio DataService SelectQuery**, not OData v4: `POST /[0/]DataService/json/SyncReply/SelectQuery` against `SysInstalledApp`. This pilot sends that documented JSON endpoint directly and makes every transport, HTTP, HTML-login-page, malformed-JSON, and `success:false` failure loud. It intentionally does not inherit the vendor provider behaviour that can turn a failed read into an empty successful list.

To produce the decision evidence on a machine with Go and network access:

```bash
export CLIO_ROOT=/path/to/clio
export CREATIO_URL=https://example.invalid
export CREATIO_LOGIN=example-user
export CREATIO_PASSWORD=replace-me
./scripts/compare-with-clio.sh
```

For OAuth, set `CREATIO_CLIENT_ID`, `CREATIO_CLIENT_SECRET`, and `CREATIO_AUTH_APP_URI` (the IdentityService `/connect/token` endpoint) instead of forms credentials, then run the same harness. It selects clio's matching OAuth client-credentials mode and compares the two results. Run and record both modes before treating the question as answered.

## 3. What this does not prove

This is one read-only command. It says nothing about the write path, package installation, IIS/DISM/PowerShell operations, or clio's other 249 CLI verbs.

## 4. Kill criterion

If `list-apps` cannot be reproduced without a vendor assembly for both authentication modes clio supports (forms authentication and OAuth client credentials), a full rewrite should stop here. **The kill criterion has not been evaluated**, because neither a Go build nor a live Go-versus-clio diff could be produced in this environment.

## Run as an MCP server

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) over stdio and exposes one tool, `list-apps`. Connection details are read only from environment variables; see `.env.example`. The `--list-apps-json` mode exists solely for the comparison harness and returns clio's JSON field names.

## Related work

[`CRACKISH/mcp-creatio`](https://github.com/CRACKISH/mcp-creatio) and its `mcp-creatio` npm package are related OData v4 work. They are useful evidence that a non-.NET read path exists, but they do not answer this pilot's `ATF.Repository` model-mapping and SelectQuery-translation question.

## Reference behaviour

The pilot references, but does not copy, these paths in the clio checkout:

- `clio/Command/ListInstalledApplications.cs`
- `clio/Command/InstalledApplicationQueryService.cs`
- `clio/CreatioModel/SysInstalledApp.cs`
- `clio/Common/ICreatioClientTransport.cs`
- `clio/Common/ServiceUrlBuilder.cs`
- `clio/Common/ClassifyingDataProvider.cs`
- `docs/knowledge/Common/remotedataprovider-swallows-every-failure-into-success-false.md`
