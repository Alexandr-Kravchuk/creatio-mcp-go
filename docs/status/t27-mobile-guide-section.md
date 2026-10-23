# T27 — get-mobile-page-conversion-guide (section, to merge into t27-page-smoke-mobile.md)

State on 2026-10-23: **partial port, tool NOT registered.** clio's own design notes say a partial guide is
worse than none (it would tell an agent to insert elements the mobile template already has, or drop rules it
cannot see), so the tool stays out of `tools/list` until the engine and its regression tests are complete.
The inventory row stays "not ported".

## Ported

| Piece | clio source (8.1.0.142) | Go | Tests |
|---|---|---|---|
| Bundled rules data | `clio/Command/McpServer/Data/WebToMobilePageConversionRules.json` (EmbeddedResource) | `internal/mobileguide/data/…json`, embedded by `rules_data.go`; copied only by `scripts/generate-mobile-conversion-rules.py`, SHA-256 in generated `rules_data_sum.go` | `rules_data_test.go`: sum matches, bytes equal clio's file when the clio checkout is present |
| Rules models | `WebToMobilePageConversionRulesModels.cs`, `WebToMobileComponentRemovalRules.cs`, `ComponentPropertyFilters.cs` | `internal/mobileguide/rules.go` | `rules_test.go` |
| Rules catalog (CDN chain → bundled fallback, validation, reason-code subset, discriminator anywhere in the object) | `WebToMobilePageConversionRulesCatalog.cs`, flavor `RegistryFlavor.WebToMobilePageConversionRules` (`CLIO_WEB_TO_MOBILE_PAGE_CONVERSION_RULES_LOCAL_FILE`, cache sub-folder `web-to-mobile-page-conversion-rules`) | `rules.go` (`ParseRules`, `GetRules`, `BundledRules`), `internal/creatio/mobileguide_rules_fetch.go` (reuses the component-registry fetch chain) | `rules_test.go`: every refusal shape of `WebToMobilePageConversionRulesCatalogTests.cs`, the four published-but-wrong reason codes, the three unusable discriminators, fallback on unavailable / invalid document, cancellation propagates |

System.Text.Json reading rules reproduced: case-insensitive member names, a missing member keeps its C#
initializer (`[]`, `"items"`, `"caption"`, `"crt.TabContainer"`, `"latest"`), an explicit `null` clears it,
`null` for a non-nullable bool is an error, dictionaries keep first-seen key order with the last value, a
leading UTF-8 BOM is stripped. Any parse or validation error sends the whole document to the bundled rules,
as in clio; only the error texts of refusals that never reach a caller differ (the unknown-discriminator text).

## Not ported yet (lines of clio C#)

| Piece | Lines | Notes |
|---|---|---|
| `WebToMobileAnalysisService.cs` + `.ComponentRemovals.cs` | 7,401 + 816 | the engine; Newtonsoft `JObject` manipulation incl. JSONPath `SelectToken` (index and slice) used by `viewConfigTemplates` |
| `MobilePageConversionGuideModels.cs` | 2,148 | answer types: JSON names, member order, null/default omission — not started |
| `MobilePageConversionGuideTool.cs` | 1,005 | flow read; args, binder refusals, validation order not yet written |
| Probes: `MobileActionTargetProbe`, `PageBusinessRuleProbe`, `MobileSectionRegistrationProbe`, `ExistingMobilePageProbe` | 927 + 450 + 194 + 184 | read-only data reads |
| `ExcludedComponentsPass`, `WebToMobilePropertyPrune`, `DefaultPageAddonReader`, `MissingTargetPageQueueBuilder`, `MobileRegistryGeneration`, `RelatedPageAddonMetadata`, `SchemaNameResolver` | 578 + 199 + 120 + 104 + 81 + 92 + 127 | |
| Engine tests to port | ~22,700 | `WebToMobileConversionServiceTests.cs`, `WebToMobileRealPageRegressionTests.cs`, `WebToMobileGeneralInfoTabRegressionTests.cs`, tool and refusal-wiring tests |
| Live comparison with clio 142 | — | refusal paths + fake stand `scripts/fake-stands/t27/` — not started |

## How to rerun

    python3 scripts/generate-mobile-conversion-rules.py --clio-source ~/.cache/creatio-mcp-go/clio-8.1.0.142 --check
    go test ./internal/mobileguide/
