# T6 write side (2026-10-20)

The 15 T6 tools that were still clio-only are ported from clio 8.1.0.142 (the owner put all of them into the
release). Contracts were already served from `internal/cliocontract`; `scripts/compare-contracts.py` now
reports `match` for all 15 on every surface (tools/list absence, index, tool-index, contract, short and
full detail): 950 match, 100 known:not-ported (other tasks), 1 known:fit-multi-name, 0 unexplained.

## Tools

| Tool | Code | Tests | Compared with clio 142 |
|---|---|---|---|
| `install-knowledge`, `update-knowledge` | `internal/knowledge/manage.go`, `transport.go` (GitHub release + NuGet v3), `store_write.go` | lifecycle states, unverified-candidate refusal, transports against `httptest`, concurrency proof | harness: 30/30 calls match (install, already-installed, up-to-date, updated, replayed sequence rejected, mutable-release advisory, NuGet, `info-knowledge checkUpdates`, `get-guidance` after an update, disable, delete, reinstall, remove), store layout and `current.json`/history equal |
| `delete-knowledge` | store `Delete` (quarantine rename, sweep) | quarantine and sweep | harness |
| `add-`, `remove-`, `enable-`, `disable-knowledge-source` | `settings_knowledge.go` over `settings_write.go` | messages, curated source refusal, settings written as clio writes them | harness: 27/27 match plus 1 known (installing a git-type source) |
| `configure-knowledge-feedback-policy` | `feedback_write.go`, `tool_knowledge_lifecycle.go` | auto refusal, off | harness (7 calls) |
| `send-telemetry`, `withdraw-telemetry-consent` | `internal/telemetry` (outbox, run state, consent, background OTLP upload) | validation order and codes, files, upload to a local collector (delivered, transient kept, permanent dropped), endpoint resolution | harness: 29/29 match, including the event and run-state files byte-escaping and the OTLP payload the local collector received |
| `experimental` | `tool_experimental.go` | table, settings | harness: 15/15 match, ConsoleTables wrapping included |
| `export-component-registry` | `internal/creatio/componentregistry_export.go`, `tool_export_component_registry.go` | byte-faithful file, refusal of an existing output-file, aliases | harness: 16/16 match, written files included |
| `install-`, `update-`, `delete-toolkit` | `internal/toolkit` | stub CLIs on a temporary PATH, temporary HOME, local git checkout for Cursor | harness: 28/28 match: CLI invocations, `~/.claude/settings.json`, `~/.codex/config.toml`, `~/.agents/plugins/marketplace.json`, Cursor files, `~/.clio/managed-skills.json` |

The harness is `scripts/compare-t6-writes.py --clio-dll <clio.dll> --go-bin <binary> --work <scratch>`
(summary in `evidence/t6-writes.json`). Each side gets its own temporary `CLIO_HOME`, `HOME`, `TMPDIR` and
telemetry root seeded with the settings clio wrote at its first start; knowledge comes from a local signed
GitHub-release/NuGet fixture, telemetry goes to a local collector, the registry is a local file, coding
agents are stub CLIs. Nothing touches the network or this machine's clio settings, cache or agents.

## The concurrency proof

clio and this server serialize on the same OS locks: `flock(LOCK_EX)` on Unix and a no-sharing
`CreateFile` on Windows, which is exactly what .NET takes for `FileShare.None`, on clio's files
(`sources/.locks/<key>.lock`, `sources/.locks/library-<key>.lock`, `appsettings.json.lock`), in clio's order
(source, then library), with clio's timeouts (30 s store, 60 s settings). Publication keeps clio's crash-safe
order: staging, high-water mark, one directory rename, atomic `current.json`, prune. Settings updates re-read
the file under the lock, compare it again before publishing (an editor outside the lock causes a re-apply,
three attempts) and publish atomically (`ReplaceFileW` with clio's retry window on Windows).

- `TestConcurrentWritersKeepStoreAndSettingsConsistent`: four goroutine writers mix install, update, delete,
  source add/remove and feature writes on one home while a publisher releases a new version every 70 ms and
  four readers watch. Fails if a reader ever finds `appsettings.json` torn or missing a section nobody wrote,
  if an active generation is unreadable while the marker still names it, if any write is lost, or if the
  quiescent store breaks an invariant (strict `current.json`, digests, high-water mark ≥ active, only active
  and previous generations, no staging/quarantine/temporary leftovers).
- `TestConcurrentWithClioProcesses` (runs when `CLIO_DLL` names a clio 8.1.0.142 `clio.dll`): the same Go
  writers next to three loops of real clio processes (`install-`, `update-`, `delete-knowledge`,
  `add-`/`disable-knowledge-source`, `experimental`) on the same home. Last run: 33 clio processes, 6 clio
  publications and 178 Go publications interleaved on the same sources, ~31,000 store reads, no violation;
  afterwards clio's `info-knowledge` accepts the generations Go wrote and Go reads the one clio wrote.
- `TestLockExcludesAnotherProcess`, `TestSettingsWriteWaitsForAnotherProcess`: a second OS process holds the
  lock; this process is refused (try-lock) or waits (lock) until it lets go.
- Negative control: with the OS lock disabled the same tests fail with lost settings updates, torn reads and
  "not owned by Clio" store errors.

## Known differences, each with its reason

- A `git`-type knowledge source can be added, enabled, disabled, removed and deleted, but `install-` and
  `update-knowledge` do not run Git: they answer with a pointer to `clio install-/update-knowledge`. The
  runtime does not read Git checkouts (T6 read side) and the Git CLI transport is not ported. clio's
  "not installed; use install-knowledge" refusal for an update is reproduced.
- Telemetry's `clio_version` attribute carries `creatio-mcp-go/<build>`; the served-content attributes clio
  adds after guidance reads (`guidance_reads`, ...) are not emitted, this server does not meter them.
- clio 8.1.0.142 copies a Cursor plugin-runtime directory through an inverted filter
  (`CopyDirectoryWithFilter` copies entries the filter rejects), so only `__pycache__`/`.pyc` content of a
  runtime directory reaches `~/.cursor`. This is reproduced for parity and is a clio defect to report.
- Deliberately safer than clio, invisible in answers: a source directory is created with its ownership marker
  in place (temporary directory + rename), readers wait up to 200 ms for a missing marker and treat a
  directory deleted under them as "not installed", a publication re-creates a source directory a concurrent
  delete removed while it waited for the lock, store JSON files retry a replace refused by an open handle on
  Windows, and telemetry temporary files have unique names.
- Network failures use .NET's texts for refused connections, unknown hosts and resets; other transport
  errors (TLS, timeouts) keep Go's wording.
- clio's `mcp-server` startup bootstrap of the curated library and its hourly knowledge autoupdate are not
  tools and are not ported.

Removed from the T6 read-side list: `info-knowledge checkUpdates` now contacts the publisher, and diagnostics
from repositories are redacted with clio's rules before they are fenced.

## Shared files touched

- `internal/creatio/componentregistry_export.go` (new file; uses the package's component-registry transport and
  output-path confinement).
- `scripts/compare-t6-writes.py`, `evidence/t6-writes.json` (new).
- No change to `registry.go`, `write_safety.go`, `tool_args.go` or `main.go`.

## Not verified

- No live Windows run: the Windows lock, `ReplaceFileW` publish and contention retry are cross-compiled and
  unit-tested only through the Windows CI job.
- The real network transports (api.github.com, nuget.org, the production telemetry collector) were never
  contacted; all transport tests use loopback fixtures.
