# Business-rule write migration status

All six T10 tools are implemented: `create-entity-business-rules`, `update-entity-business-rules`,
`delete-entity-business-rules`, `create-page-business-rules`, `update-page-business-rules`,
`delete-page-business-rules` (first round: the two delete tools; second round, 2026-10-03: the four
create/update tools).

## What was ported

From clio's `BusinessRuleTool.cs` and the services behind it (`internal/creatio/rulewrite_*.go`,
`cmd/creatio-mcp-go/tool_rule_write.go`):

- Argument binding as clio's System.Text.Json binder does it: property names inside `rules` match
  case-insensitively, unknown keys are ignored (top level too; the delete tools no longer refuse them), a
  value of the wrong JSON type is `invalid-parameter-type` (`'rules' … must be an array`, `… contains a value
  that does not match the documented shape`), and an action without the exact `type` discriminator is clio's
  `'args' … must be an object`.
- Request routing: entity tools check only the collection argument before resolving the environment; page
  tools first name every missing field (environment included) in one message. A failure inside the service
  is one failed item per rule on create and a request-level error on update and delete.
- Attribute providers: entity columns with lazy forward paths through lookups (`Account.Country`); page
  attributes from the merged bundle (view-model attributes bound to data sources, `DataSource.Column` scoped
  attributes, `PageParameters.Name`), and page element names from the merged view config.
- Validators: rule, page (with the candidate-list hint), lookup-constant existence (DataService SelectQuery),
  system-setting operands (type, Lookup reference schema, SecureText/Binary refusals), formula scope and
  ExpressionService.svc/Validate, static filters (structure, schema-aware column/type checks).
- Conversion: the simple-to-metadata converter, including triggers and scoped DataLoaded triggers,
  apply-filter with its ClearValue/PopulateValue child rules, the static ESQ envelope with lookup display-name
  resolution and enrichment, date/time constant normalization, and update-time identity preservation (rule,
  case and group uIds, trigger uIds matched by type/name/scope, caller block uIds).
- Batch semantics: per-rule failures do not stop the batch; one add-on GetSchema/SaveSchema per batch; on create
  the unique-name check and a failed save fail every converted rule; on update a missing/duplicate name fails
  only that rule; post-save ResetScriptCache and BuildConfiguration are best effort.
- The add-on is read and written as an ordered tree and serialized as clio's DTOs serialize with
  System.Text.Json: derived properties before base ones, explicit expression order, two-space indentation and
  the default escaping. The delete tools share this writer (they previously re-serialized through Go maps,
  which sorted keys).

## Verification

- Read/validation parity: `scripts/parity-cases/t10.json`, **67 match, 0 mismatches** against clio 8.1.0.134
  on `s16123120` (binding and request-shape refusals, unknown environment, and validator failures against the
  stock `Contact` entity and `Contacts_FormPage` page that fail before any save (each such case carries a `why` saying it must stay invalid), including the page candidate
  lists, system-setting, lookup-record, formula, apply-filter and static-filter failures).
- Write payload parity without writing: both servers were pointed at a local forwarding proxy that answered
  `SaveSchema`, `ResetScriptCache` and `BuildConfiguration` with HTTP 403 and kept the request body (clio via
  `CLIO_HOME`, this server via the same settings file). Nine batches — four entity creates (field actions,
  OR groups, system variable/setting operands, date/time/number/text/boolean constants, forward-path source,
  formula, apply-filter with both child rules, a static filter with lookup IN, macros, date part, nested group,
  EXISTS and COUNT/SUM backward references), an entity update keeping block uIds, an entity delete, a page
  update, a page create and a page delete — produced **byte-identical SaveSchema bodies** after replacing
  generated GUIDs and rule names by placeholders (preserved identities compared literally). Nothing reached the
  stand's save endpoints.
- Unit tests (`internal/creatio/rulewrite_test.go`, `rulewrite_delete_test.go`): exact add-on request, metadata
  key order/indentation/escaping, triggers, child rules, caption resources, request order, unique-name and
  save failures, update identity preservation, page attributes/elements/hints, the full static-filter envelope
  (golden) and its decompile round trip, filter validator texts, binding refusals, the STJ writer and date
  conversions. MCP tests: all six tools gated by raw name, run through `clio-run` and `clio-run-destructive`,
  binding refusals, annotations.

## Window-only

Every successful create/update/delete ends with `WorkspaceExplorerService.svc/BuildConfiguration`, a
stand-wide static-content rebuild broadcast to all online users, so live success parity is window-only:
`scripts/write-scenarios/window/t10.json` (entity create/read/update/read/delete, apply-filter + static filter,
page create/read/update/read). The scenarios need a package, an entity and a page the run creates; they are
marked `go-tool-missing` until `create-package` (T15; also not in clio 8.1.0.134, only in clio master),
`create-entity-schema` (T8) and `create-page` (T9) are ported. The created packages have no delete tool and
will stay on the stand after that run; the window operator should also check whether `delete-schema` `remote` on the run's entity/page leaves the separate `BusinessRule` add-on schema behind in the run's package. `--plan-only` validates the file.

No object was created on the stand by T10 in this round; the write ledger holds no T10 entries.

## Known differences from clio

- Numeric constants: the MCP layer decodes arguments to float64 before a tool sees them, so a decimal keeps
  no trailing zeros (`12.10` is written `12.1`, clio keeps `12.10`), `12.0` is written `12`, and integers beyond
  2^53 lose precision. Numbers already stored in the add-on keep their text.
- Transport and HTTP failure texts are this server's (`Creatio service … returned HTTP 403`), not clio's .NET
  exception or JSON-parser texts; clio's lenient JSON correction of malformed designer responses is not ported.
  The shared client re-authenticates and retries once on HTTP 401/403, so a rejected SaveSchema can be sent
  twice.
- Entity schemas are fetched once per batch and shared by the attribute map and the static-filter provider;
  clio's providers each keep their own cache, so clio sends more GetSchemaDesignItem requests (same results).
- Date-time constants accept ISO 8601 shapes only (DateTimeOffset.TryParse accepts more spellings), and the
  HourMinute filter time uses the host clock and zone, as in clio (not reproducible between runs).
- Top-level argument names match exactly; names inside `rules` match case-insensitively as in clio (clio's handling of top-level name case was not measured).
