# creatio-mcp-go v0.2.0 — release notes (draft)

> **Draft.** Not released yet. Numbers are as of `fdc3aa4` (2026-10-20, after the first wave of the clio
> 8.1.0.142 work); the second-wave section below is filled in after streams a and b are merged.

v0.1.0 (2026-09-28) was a read-only preview: 14 tools, one Creatio site per process set by `CREATIO_*`
variables. v0.2.0 is the first release that can stand in for `clio mcp-server` in everyday agent work —
reads **and writes**, every clio environment from one process, and clio 8.1.0.142's own tool contracts.

## Highlights

- **191 of clio 8.1.0.142's 235 tools** (plus `get-tool-contract`), with the same names, arguments,
  contracts and answer shapes. All 19 resident tools are in tools/list, in clio's order.
  Source: [status/clio-comparison-2026-10-20.md](status/clio-comparison-2026-10-20.md).
- **Write tools for every Creatio area** clio covers: applications, entity and other schemas, Freedom UI
  pages, business rules, data, system settings, users and access, business processes, themes and
  branding, packages, compilation and restart, Data Forge (T7–T15).
- **One process, every environment.** Each call names its environment with `environment-name`, resolved
  against clio's `appsettings.json` (forms, OAuth, bearer), re-read when it changes (T1).
- **clio's contracts, served from clio's own answers.** tools/list and `get-tool-contract` come from a
  recorded clio 8.1.0.142 inventory embedded in the binary, in 8.1.0.142's new `get-tool-contract` shape
  (`tool-index` + `legend`, `detail=index-entries|full|short`). Contract comparison against clio 8.1.0.142:
  980 match, 0 unexplained.
- **Guidance, prompts and resources from clio-knowledge** — the same signed bundles clio installs, verified
  the same way; all 74 prompts (T6).
- **Write safety like clio's:** `clio-run` / `clio-run-destructive`, `confirmation-required` for a direct
  call to a hidden write tool, `Safe` environments refused, clio's error-text redaction, progress and
  response deadlines for long operations, `compile-status` / `restart-status` (T4).

## New since v0.1.x

### Knowledge, toolkit and telemetry (T6)

- Read side: `get-guidance`, `list-knowledge-sources`, `info-knowledge`, `list-knowledge-examples`,
  `get-knowledge-feedback-policy`, `get-telemetry-consent`, the `docs://` resources and the 74 prompts.
- Write side (new for this release): `install-knowledge`, `update-knowledge`, `delete-knowledge`,
  `add-` / `remove-` / `enable-` / `disable-knowledge-source`, `configure-knowledge-feedback-policy`,
  `send-telemetry`, `withdraw-telemetry-consent`, `experimental`, `export-component-registry`,
  `install-` / `update-` / `delete-toolkit`.
- They write clio's knowledge store and `appsettings.json` under clio's own OS file locks, so clio and
  this server can share one clio home at the same time. A concurrency test runs Go writers next to real
  clio processes on one home. The GitHub-release and NuGet transports are tested against local fixtures
  only, not yet against the real publishers. Details: [status/t6-write-status.md](status/t6-write-status.md).
- `info-knowledge` with `checkUpdates: true` now asks the publisher.

### Object rights and entity connections (T19)

New in clio 8.1.0.142 and ported from its source: `get-object-rights`, `set-object-rights`,
`set-default-record-rights`, `apply-default-record-rights`, `get-entity-connections`,
`update-entity-connections`. Compared with clio 8.1.0.142 on 151 no-stand calls and 80 fake-stand cases
([status/t19-rights-connections-status.md](status/t19-rights-connections-status.md)).

### New arguments from clio 8.1.0.142 (T20)

- `odata-update`: `rows-file`; `odata-create`: `rows` optional; payload synonyms on both.
- `odata-delete`: `ids` (up to 50), `dry-run`, `cascade`, `cascade-plan-hash`; `id` and `confirm` optional.
- `run-process`: `wait`, `wait-timeout`, `watch`, `cleanup`.
- `describe-business-process`: `mode`, `element`, compact reply, summary and checksum.
- Binding checks for `include-operations` (`get-page`, `update-page`), `known-containers` (`validate-page`),
  `caption-culture` (`update-app-section`), `process-name` (`compile-creatio`).

Details: [status/t20-new-args-status.md](status/t20-new-args-status.md).

### Contracts on clio 8.1.0.142

`docs/clio-inventory.json`, the embedded contracts and the prompt table are regenerated from clio
8.1.0.142; `scripts/clio-inventory.py` and `scripts/compare-contracts.py` read its new
`get-tool-contract` shape.

### Delivery

CI on every push (build, `go vet`, tests, Linux and Windows), a release workflow and a local release
script with macOS signing and notarization ([releasing.md](releasing.md)). `creatio-mcp-go --version`
prints the release tag.

## Second wave (stream a/b)

> **Placeholder — the orchestrator fills this in after streams a and b are merged.**
> Tools being ported in parallel now:
> `localize-page`, `page-smoke`, `trigger-smoke`, `place-section-in-workplace`, `create-package`,
> `get-mobile-page-conversion-guide`, `execute-esq-to-file`, `odata-read-to-file`,
> `get-component-info-to-file`, `get-request-info-to-file`, `list-entity-client-schemas-to-file`.
> To update after the merge: the tool count in Highlights, the contract-comparison numbers, links to the
> status files.

## Known differences from clio

Each one is recorded with its reason in the area reports under [status/](status/). The ones you are most
likely to notice:

- **A call without `environment-name`** goes to this server's default target (`CREATIO_*` variables, then
  clio's active environment). clio 8.1.0.142 refuses it.
- `validate-page` runs a declared subset of clio's rules and says so in every answer; the
  `known-containers` check is not run.
- Several unknown argument keys or payload synonyms in one call are listed in sorted order; clio lists
  them in request order.
- Transport failures (TLS, timeouts) read in Go's words; refused connections, unknown hosts and resets
  use .NET's texts where the area reports say so.
- Floating-point last digits in `advise-theme-palette`, JSON key order of some `descriptor` /
  `operations` objects.
- Telemetry events carry `creatio-mcp-go/<build>` as `clio_version`; served-content attributes are not
  emitted.
- clio 8.1.0.142's Cursor toolkit install copies plugin-runtime directories through an inverted filter;
  this server reproduces it so both leave the same files.

## Not in this release

- **33 local-machine, infrastructure and workspace tools** (T16: `deploy-creatio`, `uninstall-creatio`,
  `restore-db-*`, `clear-redis-*`, `create-/push-/restore-workspace`, `reg-web-app`, `stop-creatio`,
  browser-session tools, …). They stay in clio on purpose (decision D3 in
  [migration-plan.md](migration-plan.md)).
- **The Git transport of `install-knowledge` / `update-knowledge`.** A `git`-type source can be added,
  enabled, disabled, removed and deleted here; installing or updating it points you to clio.
- clio's `mcp-server` startup install of the curated knowledge library and its hourly update. Install it
  once with `clio install-knowledge` (this server's `install-knowledge` works too, but its transports are
  verified only against local fixtures so far).

## Verification status

- Contracts: compared live with clio 8.1.0.142's MCP server, 0 unexplained differences.
- Reads: live parity against clio 8.1.0.134 on two stands, 706 calls, 0 mismatches (2026-10-03).
- The 8.1.0.142 work (T19, T20, T6 writes) was compared with clio 8.1.0.142 without a live Creatio: on
  refusal paths, fake stands and local fixtures.
- Several write success paths (compile, restart, package installs, `install-application`,
  `install-process-builder`, `set-logo` / `set-background-image`, `run-process` launches, Data Forge
  writes) wait for a live "window run" on a disposable stand before the final release (T17).

## Upgrading from v0.1.0

- Registrations with `CREATIO_*` variables keep working; they become the default target.
- With environments registered in clio, no variables are needed: register the server without `--env`
  and pass `environment-name` per call.
- Installation details: [install.md](install.md).
