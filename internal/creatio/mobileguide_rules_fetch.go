package creatio

import "context"

// mobileGuideRulesFlavor is clio's RegistryFlavor.WebToMobilePageConversionRules: the web-to-mobile
// page-conversion rules served from the same academy CDN chain as the component registries.
var mobileGuideRulesFlavor = componentInfoFlavor{
	"web-to-mobile-page-conversion-rules",
	"WebToMobilePageConversionRules.json",
	"CLIO_WEB_TO_MOBILE_PAGE_CONVERSION_RULES_LOCAL_FILE",
	"web-to-mobile-page-conversion-rules",
}

// FetchWebToMobileConversionRules is WebToMobilePageConversionRulesRegistryClient.GetAsync: the local
// override, cache, CDN and 'latest' chain clio's component registries use, for the conversion rules file.
// Any error means the catalog falls back to the bundled rules (cancellation aside).
func FetchWebToMobileConversionRules(ctx context.Context, version string) ([]byte, error) {
	result, err := componentInfoFetchRegistry(ctx, mobileGuideRulesFlavor, version)
	if err != nil {
		return nil, err
	}
	return result.content, nil
}
