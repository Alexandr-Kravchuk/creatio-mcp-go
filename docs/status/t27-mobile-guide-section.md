# T27 — get-mobile-page-conversion-guide (section, to merge into t27-page-smoke-mobile.md)

State on 2026-10-23 (third session): **engine byte-equal to clio on 530 recorded calls; handler, probes and
registration done — tool registered; refusals equal clio on 25 of 26 recorded answers; success path not yet compared.** clio's own design notes say a partial guide is
worse than none (it would tell an agent to insert elements the mobile template already has, or drop rules it
cannot see), so the tool stays out of `tools/list` until the engine and its regression tests are complete.
The inventory row stays "not ported".

## Ported

| Piece | clio source (8.1.0.142) | Go | Tests |
|---|---|---|---|
| Bundled rules data | `clio/Command/McpServer/Data/WebToMobilePageConversionRules.json` (EmbeddedResource) | `internal/mobileguide/data/…json`, embedded by `rules_data.go`; copied only by `scripts/generate-mobile-conversion-rules.py`, SHA-256 in generated `rules_data_sum.go` | `rules_data_test.go`: sum matches, bytes equal clio's file when the clio checkout is present |
| Rules models | `WebToMobilePageConversionRulesModels.cs`, `WebToMobileComponentRemovalRules.cs`, `ComponentPropertyFilters.cs` | `internal/mobileguide/rules.go` | `rules_test.go` |
| Rules catalog (CDN chain → bundled fallback, validation, reason-code subset, discriminator anywhere in the object) | `WebToMobilePageConversionRulesCatalog.cs`, flavor `RegistryFlavor.WebToMobilePageConversionRules` (`CLIO_WEB_TO_MOBILE_PAGE_CONVERSION_RULES_LOCAL_FILE`, cache sub-folder `web-to-mobile-page-conversion-rules`) | `rules.go` (`ParseRules`, `GetRules`, `BundledRules`), `internal/creatio/mobileguide_rules_fetch.go` (reuses the component-registry fetch chain) | `rules_test.go`: every refusal shape of `WebToMobilePageConversionRulesCatalogTests.cs`, the four published-but-wrong reason codes, the three unusable discriminators, fallback on unavailable / invalid document, cancellation propagates |

| Answer types | `MobilePageConversionGuideModels.cs` (2,148 lines, 612 without doc comments) — every serialized type, member order, reason-code constants | `internal/mobileguide/models.go`, `response.go` (`Response`) | `response_test.go`: null left out, empty list written as `[]`, dictionary keys in insertion order |
| Tool helpers without the engine | `MobilePageConversionGuideTool.cs`: `Fail` (redacted), `DetectSourceType`, `RejectUnsupportedSourceType`, both template refusal texts, `BuildRequestRegistryWarning`, `WorseResolvedFrom`, the bad-`version` text | `internal/mobileguide/response.go` | `response_test.go` |

Serialization of the answer: clio's MCP options are `McpJsonUtilities.DefaultOptions` — `JsonSerializerDefaults.Web`
plus `DefaultIgnoreCondition = WhenWritingNull` (read from the decompiled `ModelContextProtocol.Core.dll` of the
8.1.0.142 tool package). So every null member is omitted, including members without their own `[JsonIgnore]`, and
`ElementMapEntry` (no `[JsonPropertyName]`) is named by the camelCase policy. Go reproduces this with `omitzero`
(a nil slice/pointer is dropped, an empty non-nil slice is written as `[]`); `requiresVersionConfirmation`
(WhenWritingDefault) is the only bool that is dropped when false. `JsonNode` members are `json.RawMessage` that the
engine must fill; the text goes through `creatio.ClioJSONText` at the tool boundary like the other typed answers.

System.Text.Json reading rules reproduced: case-insensitive member names, a missing member keeps its C#
initializer (`[]`, `"items"`, `"caption"`, `"crt.TabContainer"`, `"latest"`), an explicit `null` clears it,
`null` for a non-nullable bool is an error, dictionaries keep first-seen key order with the last value, a
leading UTF-8 BOM is stripped. Any parse or validation error sends the whole document to the bundled rules,
as in clio; only the error texts of refusals that never reach a caller differ (the unknown-discriminator text).

## Engine (ported 2026-10-23, second session)

`WebToMobileAnalysisService.cs` + `.ComponentRemovals.cs`, `ExcludedComponentsPass`, `WebToMobilePropertyPrune`,
`MissingTargetPageQueueBuilder`, `MobileRegistryGeneration`, `MobileRegistryDeclarations`/`DeclaredPropertyIndex`
are in `internal/mobileguide` (`analyze.go`, `declared.go`, `walk.go`, `walk_branches.go`, `values.go`,
`removals.go`, `passes.go`, `adaptive.go`, `datasections.go`), entry point `Analyze(*AnalyzeInput)`.

- One ordered DOM (`jnode.go`) stands in for both of clio's JSON stacks: a Newtonsoft parse (`toJToken`) turns
  ISO-looking strings into Date tokens (19–40 chars, 'T' at 10; an offset is converted to local time as Json.NET
  does) and re-prints numbers (.NET doubles with ".0", integers via BigInteger); a System.Text.Json parse keeps
  number text verbatim and stores JSON null as a nil child. `JToken.DeepEquals` and `JsonNode.DeepEquals` are
  separate (`jtokenEquals`, `nodeEquals` — decimal value equality), as are the three writers (STJ default
  encoder for `ToJsonString`, Json.NET, clio's relaxed MCP encoder for the answer) and `JToken.ToString()`.
- `dotnet.go`: .NET `Dictionary`/`HashSet` with the free-list slot reuse (enumeration order after a removal is
  .NET's), `OrdinalIgnoreCase` equality/ordering, `SortedSet`, stable `OrderBy`, the JPath subset
  `viewConfigTemplates` use (`name`, `[n]`, `[a:b:c]`, `[*]`, `['x']`; several matches on `SelectToken` is an error).
- **Oracle**: `internal/mobileguide/oracle_test.go` replays `testdata/clio-analyze-records.tar.gz` — every
  `Analyze` call clio 8.1.0.142's own MobilePageConverter suite (800 tests) makes, with its inputs and clio's
  answer serialized by clio's MCP options and encoder. **530 of 530 byte-equal.** Re-record with
  `scripts/mobileguide-oracle/record.sh ~/.cache/creatio-mcp-go/clio-8.1.0.142` (copies the checkout, wraps
  `Analyze`, builds only the MobilePageConverter tests, runs them with `CLIO_ORACLE_DIR`).
- Known, unmeasured: a date-like string read as a Date token and then printed through `JToken.ToString()` (a
  name, a caption) uses en-US culture formatting; an exception message raised inside the engine (malformed
  rules, a non-string `path`) is approximated.

## Also ported in the second session

- Tool helpers (`tool_helpers.go`, cases of `MobilePageConversionGuideToolTests.cs`): `DefaultTemplateRule`,
  `ResolveTemplateRule`, `ResolveEffectiveTemplateName` (chain head to root), `BuildContainerNameMap`,
  `BuildPositionalPlacements`, `BuildComponentNameMap`, `IsFormPage` (bundled `mobileFormPageTemplates` fallback),
  `DeriveMobileSchemaName`, `RulesWarning` (`BuildRulesWarning`), `IndexByComponentType`; the public collectors the tool
  runs over template bundles (`CollectComponentTypesByName`, `CollectLayoutConfigByName`,
  `CollectComponentNodesByName`, `CollectParentByName`), `TargetCarryingRegistryOnlyRequests`,
  `UnknownMobileRequestTargets`, `AnswerText` (the answer writer: clio's relaxed MCP encoder, numbers verbatim).
- Probe pure layers: `PageBusinessRuleProbe.ParseRules` (`probe_rules.go`, cases of `PageBusinessRuleProbeTests.cs`),
  `RelatedPageAddonMetadata.TryFindUntypedDefault`, `ClassifyRelatedPageMetadata`, `ExtractDefaultPageSchemaUId`,
  `CollectSourceEntityNames` (`probe_addon.go`).

## Refusal paths measured on clio 142 (no stand)

`scripts/fake-stands/t27/mobile_refusals.py <out> -- <server command>` runs 13 cases through `clio-run` and by raw
name with a temp HOME/CLIO_HOME (environments `closedport` on 127.0.0.1:9 and `safe` with `Safe: true`); clio's raw
texts are in `internal/mobileguide/testdata/clio-refusals/` (26 files). What they settle:

- No correlation id in this tool's answer. A raw-name call gets clio's "[clio] Executed '…' directly; it is not
  advertised in tools/list…" hint after the JSON (an "Attempted" hint for a binding failure).
- Every environment problem is reported by the source-page read, inside the tool's own envelope
  (`sourceSchemaName` echoed): unknown name → `Failed to read source page 'X': Environment with key 'nope' not
  found. …`; Safe environment → `Failed to read source page 'X': Safe environment confirmation required but it was
  declined or the context is non-interactive. Environment: '[redacted-uri]'.` (so the Safe refusal DOES apply to
  this read-only tool); no environment → `Failed to read source page 'X': Either a configured environment name or an
  explicit URI is required …`; closed port → `Could not read source page 'X': SelectQuery could not be completed …
  If the page is a Classic UI page, migrate it to a Freedom UI web page first.`
- A blank `schema-name` reaches get-page: `Could not read source page '   ': schemaName is required. …`. An empty
  record (`{"args":{}}`) answers `Failed to read source page '': Either a configured environment name …` with no
  `sourceSchemaName`. No `args` at all is the binding failure `Error: tool '…' failed: The arguments dictionary is
  missing a value for the required parameter 'args'. (Parameter 'arguments')`; `schema-name: 5` is
  `invalid-parameter-type: argument 'schema-name' … must be a string. …`; an unknown member is ignored.
- The `version` check runs only after a successful page read, so a bad version on an unreachable stand reports the
  read failure.

## Third session (2026-10-23): handler, probes, registration

Registered and served (read-only, idempotent). Commits `c790cc8` (flow, probes, bridge) and `8e71afc` (tool,
registration, `tool_t21a_test.go` now asserts the tool IS registered with ReadOnly+Idempotent).

- `internal/creatio/mobileguide_bridge.go`: `NewMobileGuideStand(client, failure)` implements `mobileguide.Stand` —
  get-page read (`GetPage`, bundle sections as compact Newtonsoft JSON, schema chain names), version probe
  (`componentInfoProbeVersion`), mobile/web catalogs (clio's validation through `componentInfoParseCatalog`, then the
  raw entries re-read, blank types dropped, stably sorted like the catalog; `references.baseInputs`), the mobile request
  catalog (`dataWriteParseRequestCatalog`), the rules fetch, the version warnings, and the probe client (OData GET with
  30 s like `odataReadGet`, `esqSelect`, AddonSchemaDesignerService.GetSchema, page rule schema context).
- `internal/mobileguide/probes.go`: HTTP halves of the four probes plus SchemaNameResolver / DefaultPageAddonReader,
  behind the `ProbeClient` interface. clio's `Parallel.For` (8) over entity add-on reads runs sequentially (outcomes
  are indexed, same answer).
- `internal/mobileguide/tool.go`: `Run` = the handler in clio's order; `ParseRegistryEntry` (non-test copy of the oracle
  decoder); bundle sections go through `parseNewtonsoft` → `toJsonNode` (Json.NET date reading re-applied).
- `cmd/creatio-mcp-go/tool_get_mobile_page_conversion_guide.go`: one `args` record, members ignoring case, unknown keys
  ignored, `t21aBinder.str` type errors; no environment name and no uri → clio's resolver text (no fallback to the active
  environment, as measured); answer text via `creatio.ClioMcpResultText`.

Tests: `TestMobileGuideRefusalsMatchClio` replays the recorded refusals — **25 of 26 equal**. Not compared: `no-args.raw`
(a direct call `{}` cannot be told from `{"args":{}}` after `unwrapArgs`; through clio-run it matches). Raw-name answers
are compared without clio's trailing `[clio] Executed/Attempted '…' directly; it is not advertised in tools/list…` line:
this server never writes that hint (server-wide, not this tool).

Known gaps, not measured:
- **The success path is NOT compared with clio yet** (no fake stand run; steps 4–5 below). Only the refusals are.
- PageBusinessRuleProbe: clio also merges the page hierarchy's bodies in `PageBusinessRuleSchemaProvider` and reports a
  merge failure in the probe note; the Go context read skips the merge.
- Probe JSON request bodies are Go maps (keys sorted), clio's JObjects keep insertion order; Creatio does not care.
- `rowBool` approximates `JToken.Value<bool?>()` for non-boolean cells.

## Still open

| Step | Notes |
|---|---|
| Probe oracle | wrap the probes' entry points in the clio copy (`scripts/mobileguide-oracle/`) so `MobileActionTargetProbeTests`, `PageBusinessRuleProbeTests`, `MobileSectionRegistrationProbeTests`, `ExistingMobilePageProbeTests` record stubbed service answers and results; replay them against `ProbeClient` fakes |
| Fake stand `scripts/fake-stands/t27/fake_mobile.py` | serve get-page bundles (source page, web template, mobile template) and the probe/version queries from clio fixtures; point `CLIO_*_LOCAL_FILE` at fixtures; compare raw answer text of clio 142 and this server on several pages |
| Docs | `docs/clio-mcp-comparison.md`, `docs/install.md`, `docs/migration-plan.md` still say the tool is not served; the inventory row (`docs/clio-inventory.*`, off-limits here) still says "not ported" |

## Next session: facts already established

- The tool takes ONE record argument, so its MCP input is `{"args": {schema-name, target-schema-name, version,
  environment-name, uri, login, password}}`; through `clio-run` the record's members are the `args` object itself.
  Contract and annotations come from the inventory by name (ReadOnly, Idempotent, not Destructive, not OpenWorld).
- `cmd/creatio-mcp-go/tool_t21a_test.go` asserts the tool is NOT registered; flip that assertion in the commit that
  registers it.
- clio's order: page read → unsuccessful read → source type → `version` check → version resolution → mobile, web,
  mobile request catalogs (each failure `Failed to load the … catalog for version '<v>': …`) → rules → mobile template
  read (refusal) → web template baseline (refusal) → probes → engine (`Failed to analyze source page …`).
- Registry overrides for the offline comparison: `CLIO_COMPONENT_REGISTRY_LOCAL_FILE`,
  `CLIO_MOBILE_COMPONENT_REGISTRY_LOCAL_FILE`, `CLIO_MOBILE_REQUEST_REGISTRY_LOCAL_FILE`,
  `CLIO_WEB_TO_MOBILE_PAGE_CONVERSION_RULES_LOCAL_FILE` (a missing override file is an IOException, so the rules fall
  back to the bundled copy; the component catalogs fail instead).

## Checks at the end of the second session

`gofmt -l .` empty, `go vet ./...`, `go test -race ./...` (all packages ok), `GOOS=windows GOARCH=amd64 go build ./...`,
`python3 -m pytest -q scripts` (74 passed, 8 skipped), `scripts/check-public-safety.sh` passed (one exception added:
the oracle csproj's test-SDK package id), `scripts/compare-contracts.py`: 1030 match, 68 known:not-ported (this tool
among them — it is not registered), 1 known:fit-multi-name, 0 unexplained.

## How to rerun

    python3 scripts/generate-mobile-conversion-rules.py --clio-source ~/.cache/creatio-mcp-go/clio-8.1.0.142 --check
    go test ./internal/mobileguide/                       # includes the 530-call oracle
    scripts/mobileguide-oracle/record.sh ~/.cache/creatio-mcp-go/clio-8.1.0.142   # re-record the oracle
    python3 scripts/fake-stands/t27/mobile_refusals.py <out> -- dotnet <clio.dll> mcp-server
