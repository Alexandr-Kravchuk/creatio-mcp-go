// Package mobileguide ports clio's web-to-mobile page-conversion engine behind
// get-mobile-page-conversion-guide (clio/Command/McpServer/Tools/MobilePageConverter).
package mobileguide

import _ "embed"

// bundledRulesJSON is clio's bundled WebToMobilePageConversionRules.json (EmbeddedResource
// Clio.Command.McpServer.Data.WebToMobilePageConversionRules.json), copied byte for byte by
// scripts/generate-mobile-conversion-rules.py. The rules catalog falls back to it when the CDN chain
// cannot serve a usable rules document.
//
//go:embed data/WebToMobilePageConversionRules.json
var bundledRulesJSON []byte
