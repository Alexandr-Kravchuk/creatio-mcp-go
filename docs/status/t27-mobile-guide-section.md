# T27 — get-mobile-page-conversion-guide (section, to merge into t27-page-smoke-mobile.md)

State on 2026-10-23 (second session): **engine ported and byte-equal to clio on 530 recorded calls; tool flow,
probes and registration still open — tool NOT registered.** clio's own design notes say a partial guide is
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

## Not ported yet (lines of clio C#)

| Piece | Lines | Notes |
|---|---|---|
| `MobilePageConversionGuideTool.cs` flow | ~600 of 1,005 | the handler itself: argument binding, environment resolution, page reads, version resolution, catalog loads, template probes, `ResolveEffectiveTemplateName`, `IsFormPage`, `DeriveMobileSchemaName`, container/component name maps, positional placements, `BuildRulesWarning` (needs the engine's `UnknownMobileRequestTargets`) |
| Probes: `MobileActionTargetProbe`, `PageBusinessRuleProbe`, `MobileSectionRegistrationProbe`, `ExistingMobilePageProbe` | 927 + 450 + 194 + 184 | read-only data reads |
| `DefaultPageAddonReader`, `RelatedPageAddonMetadata`, `SchemaNameResolver` (probe helpers) | 120 + 92 + 127 | |
| Live comparison with clio 142 | — | refusal paths + fake stand `scripts/fake-stands/t27/` — not started |

## Next session: facts already established

- The tool takes ONE record argument, so its MCP input is `{"args": {schema-name, target-schema-name, version,
  environment-name, uri, login, password}}`; `main.go` already unwraps `args`. Contract and annotations come from the
  inventory by name (`registerTool(map[string]any{"name": "get-mobile-page-conversion-guide"}, …)`, ReadOnly,
  Idempotent, not Destructive, not OpenWorld).
- `cmd/creatio-mcp-go/tool_t21a_test.go` asserts the tool is NOT registered; flip that assertion in the commit that
  registers it, together with the inventory row.
- clio's order: page read (env resolution happens inside it, so an unknown environment answers
  `Failed to read source page '<name>': <message>` inside the tool's own envelope — to be confirmed live) →
  unsuccessful read `Could not read source page '<name>': <error>. If the page is a Classic UI page, migrate it to a
  Freedom UI web page first.` → source type → `version` check → version resolution → mobile catalog, web catalog,
  mobile request catalog (each failure has its own `Failed to load the … catalog for version '<v>': …` text) → rules →
  mobile template probe (refusal) → web template baseline (refusal) → probes → engine (`Failed to analyze source page …`).
  Whether the Safe-environment refusal applies to this read-only tool is unverified.
- The engine needs data that only `internal/creatio` has unexported: the merged page bundle (`PageGetResult.bundle`,
  a `jnode`), version resolution (`componentInfoResolveVersion`, `componentInfoProbeVersion`,
  `componentInfoThreePartVersion`, `componentInfoMarkersFor`), the parsed web/mobile component catalogs
  (`componentInfoParseCatalog` entries incl. properties and `globalReferences.baseInputs` for
  `MobileRegistryGeneration`) and the mobile request catalog (`dataWriteParseRequestCatalog`). Plan: one new file
  `internal/creatio/mobileguide_bridge.go` exporting exactly these, and keep the engine in `internal/mobileguide`.
- The engine works on Newtonsoft `JObject` and STJ `JsonNode`; it uses JSONPath `SelectToken("columns[0].code")` and
  `SelectTokens("columns[1:]")` for `viewConfigTemplates`. A DOM with both writers already exists unexported as
  `internal/creatio/jsonnode.go`; `mobileguide` needs its own copy or an exported one.
- Registry overrides for the offline comparison: `CLIO_COMPONENT_REGISTRY_LOCAL_FILE`,
  `CLIO_MOBILE_COMPONENT_REGISTRY_LOCAL_FILE`, `CLIO_MOBILE_REQUEST_REGISTRY_LOCAL_FILE`,
  `CLIO_WEB_TO_MOBILE_PAGE_CONVERSION_RULES_LOCAL_FILE` (a missing override file is an IOException, so the rules fall
  back to the bundled copy; the component catalogs fail instead).

## Checks at fcbc7f7

`gofmt -l .` empty, `go vet ./...`, `go test -race ./...` (all packages ok), `GOOS=windows GOARCH=amd64 go build ./...`,
`python3 -m pytest -q scripts` (74 passed, 8 skipped), `scripts/check-public-safety.sh` passed, generator `--check` ok.
No live comparison with clio 142 yet: nothing is registered, so there is nothing to compare.

## How to rerun

    python3 scripts/generate-mobile-conversion-rules.py --clio-source ~/.cache/creatio-mcp-go/clio-8.1.0.142 --check
    go test ./internal/mobileguide/
