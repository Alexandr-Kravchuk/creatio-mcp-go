# T20: arguments clio 8.1.0.142 added to tools already served

Ported from the 8.1.0.142 tag and compared with that version's own MCP server (`dotnet clio.dll mcp-server`),
never against a live stand: validation paths on an unknown or unreachable environment, everything else
against the canned Creatio in `scripts/fake-stands/t20` (rules, cases and usage in `fake_creatio.py`).

| Tool | Arguments | State | Compared |
|---|---|---|---|
| odata-update | `rows-file` without `data`; synonyms `values`/`payload`/`body`/`fields`; `rowsFile`/`rows_file` renames, unknown keys | done | 82 validation calls with odata-create/delete, all equal but two noted below |
| odata-create | `rows` optional; synonyms `payload`/`values`/`body`/`data`; renames, unknown keys | done | as above |
| odata-delete | `ids` (50), `dry-run`, `cascade`, `cascade-plan-hash`; `id` and `confirm` optional | done | validation calls above; 15 fake-stand calls (plans, hash, cascade, batch, refusals): equal answers, same requests |
| run-process | `wait`, `wait-timeout`, `watch`, `cleanup`, plus 142's RunSpecifiedProcessVersion launch and version facts | done | 21 validation calls (one differs only in transport-error text); 11 fake-stand calls: equal, same requests |
| describe-business-process | `mode`, `element`, identity synonyms; compact reply, summary, checksum, valueOmitted, modifiedOn, model order | done | 22 fake-stand calls, checksums included: equal |
| compile-creatio | `process-name` | already matched | 4 validation calls equal |
| update-app-section | `caption-culture` | already matched on the refusals | 2 validation calls equal; the write itself needs a stand |
| get-page / update-page | `include-operations` | binding refusal fixed on update-page | 2 calls equal; the reply shape needs a stand |
| validate-page | `known-containers` | binding refusals fixed; the check is not run | the parent-container check stays outside this server's validate-page subset, which says so in every answer |

Last fake-stand run: `compare-mcp.py` 48 match, 0 mismatch; requests per route and rule identical on both sides.

## Known differences

- A call without `environment-name`: clio 142 refuses the binding; this server keeps resolving it to its
  default target, as for every other tool.
- Several payload or identity synonyms in one call are named in sorted order (Go maps keep no request order).
- describe-business-process: members of the graph's typed blocks follow clio's model order and declarations
  (`processdescribe_model.go`); a server member clio's model does not know in a block without an overflow
  bag is dropped as clio drops it. The 18,000-byte summary edge and the Unicode tables of the MCP result
  encoder (unassigned code points) are not measured on a stand.
- Transport failures read in Go's words, not .NET's.

## Left

- validate-page `known-containers` check (clio's PageParentNameValidation) - part of the validate-page subset.
- get-page / update-page `include-operations` and update-app-section `caption-culture` replies: compare on a
  stand.
- Write scenario `scripts/write-scenarios/t11.json` "clio 142 OData arguments" added, not run (disposable stand
  only). run-process wait/watch/cleanup has no scenario: it needs a process on the stand that creates records.
