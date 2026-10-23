package mobileguide

import (
	"strings"
)

// The pure helpers of MobilePageConversionGuideTool.cs the handler composes around the engine.

// DefaultTemplateRule is MobilePageConversionGuideTool.DefaultTemplateRule: only the rules' default mobile
// template, no web template and no correspondence; nil when the rules declare no default.
func DefaultTemplateRule(rules *Rules) *TemplateMappingRule {
	if rules == nil || rules.DefaultMobileTemplate == nil || isBlank(*rules.DefaultMobileTemplate) {
		return nil
	}
	return &TemplateMappingRule{Mobile: rules.DefaultMobileTemplate}
}

// ResolveTemplateRule is MobilePageConversionGuideTool.ResolveTemplateRule: the first rule for the web template.
func ResolveTemplateRule(rules *Rules, webParentTemplate *string) *TemplateMappingRule {
	if rules == nil || rules.Templates == nil || webParentTemplate == nil || isBlank(*webParentTemplate) {
		return nil
	}
	for _, rule := range rules.Templates {
		if rule != nil && eqip(rule.Web, webParentTemplate) {
			return rule
		}
	}
	return nil
}

// ResolveEffectiveTemplateName is MobilePageConversionGuideTool.ResolveEffectiveTemplateName; chain is the
// bundle's schema chain (PageBundleInfo.Schemas), head to root.
func ResolveEffectiveTemplateName(ownName, parentName *string, chain []*string, rules *Rules) *string {
	if parentName != nil && !isBlank(*parentName) && !eqip(parentName, ownName) && ResolveTemplateRule(rules, parentName) != nil {
		return parentName
	}
	if len(chain) > 0 {
		var firstDistinct *string
		for _, name := range chain {
			if name == nil || isBlank(*name) || eqip(name, ownName) {
				continue
			}
			if firstDistinct == nil {
				firstDistinct = name
			}
			if ResolveTemplateRule(rules, name) != nil {
				return name
			}
		}
		if firstDistinct != nil {
			return firstDistinct
		}
	}
	return parentName
}

// BuildContainerNameMap is MobilePageConversionGuideTool.BuildContainerNameMap (nil when empty).
func BuildContainerNameMap(rule *TemplateMappingRule) *dict[string] {
	if rule == nil || len(rule.Containers) == 0 {
		return nil
	}
	result := newCI[string]()
	for _, container := range rule.Containers {
		if container != nil && container.Web != nil && !isBlank(*container.Web) && container.Mobile != nil &&
			!isBlank(*container.Mobile) && !strings.Contains(*container.Web, ":") && !strings.Contains(*container.Mobile, ":") {
			result.set(*container.Web, *container.Mobile)
		}
	}
	if result.len() == 0 {
		return nil
	}
	return result
}

// BuildPositionalPlacements is MobilePageConversionGuideTool.BuildPositionalPlacements (nil when none).
func BuildPositionalPlacements(rule *TemplateMappingRule) []*PositionalPlacement {
	if rule == nil || len(rule.Containers) == 0 {
		return nil
	}
	byAnchor := newCI[*PositionalPlacement]()
	for _, container := range rule.Containers {
		if container == nil || container.Web == nil || isBlank(*container.Web) || container.Mobile == nil ||
			isBlank(*container.Mobile) || !strings.Contains(*container.Web, ":") || !strings.Contains(*container.Mobile, ":") {
			continue
		}
		webAnchor := dotnetTrim(strings.SplitN(*container.Web, ":", 2)[0])
		mobileAnchor := dotnetTrim(strings.SplitN(*container.Mobile, ":", 2)[0])
		if webAnchor == "" || mobileAnchor == "" || byAnchor.has(webAnchor) {
			continue
		}
		byAnchor.set(webAnchor, &PositionalPlacement{WebAnchor: strp(webAnchor), MobileAnchor: strp(mobileAnchor)})
	}
	if byAnchor.len() == 0 {
		return nil
	}
	var placements []*PositionalPlacement
	for _, pair := range byAnchor.pairs() {
		placements = append(placements, pair.value)
	}
	return placements
}

// BuildComponentNameMap is MobilePageConversionGuideTool.BuildComponentNameMap (nil when empty).
func BuildComponentNameMap(rule *TemplateMappingRule) *dict[*ComponentMappingRule] {
	if rule == nil || len(rule.Components) == 0 {
		return nil
	}
	result := newCI[*ComponentMappingRule]()
	for _, component := range rule.Components {
		if component != nil && component.Web != nil && !isBlank(*component.Web) && component.Mobile != nil && !isBlank(*component.Mobile) {
			result.set(*component.Web, component)
		}
	}
	if result.len() == 0 {
		return nil
	}
	return result
}

// IsFormPage is MobilePageConversionGuideTool.IsFormPage.
func IsFormPage(schemaName, parentTemplate *string, templateRule *TemplateMappingRule, rules *Rules) bool {
	if schemaName != nil && !isBlank(*schemaName) && hasSuffixFold(*schemaName, "FormPage") {
		return true
	}
	if templateRule != nil {
		if templateRule.Mobile == nil || isBlank(*templateRule.Mobile) {
			return false
		}
		templates := []*string{}
		if rules != nil && len(rules.MobileFormPageTemplates) > 0 {
			templates = rules.MobileFormPageTemplates
		} else if bundled := BundledRules(); bundled != nil && bundled.MobileFormPageTemplates != nil {
			templates = bundled.MobileFormPageTemplates
		}
		for _, template := range templates {
			if eqip(template, templateRule.Mobile) {
				return true
			}
		}
		return false
	}
	if parentTemplate == nil {
		return false
	}
	switch *parentTemplate {
	case "PageWithTabsFreedomTemplate", "BasePageFreedomTemplate", "BasePageTemplate":
		return true
	}
	return false
}

// DeriveMobileSchemaName is MobilePageConversionGuideTool.DeriveMobileSchemaName.
func DeriveMobileSchemaName(webSchemaName *string) string {
	if webSchemaName == nil || isBlank(*webSchemaName) {
		return "Mobile_Page"
	}
	name := *webSchemaName
	switch {
	case strings.HasSuffix(name, "_FormPage"):
		return strings.TrimSuffix(name, "_FormPage") + "_MobileFormPage"
	case strings.HasSuffix(name, "_ListPage"):
		return strings.TrimSuffix(name, "_ListPage") + "_MobileListPage"
	}
	return name + "_Mobile"
}

// RulesWarning is MobilePageConversionGuideTool.BuildRulesWarning.
func RulesWarning(rules *Rules, mobileRequestTypes *strSet) *string {
	unknown := UnknownMobileRequestTargets(rules, mobileRequestTypes)
	if len(unknown) == 0 {
		return nil
	}
	return strp("The conversion rules map " + strings.Join(unknown, ", ") + " onto mobile request types the mobile request " +
		"registry does not publish. A binding converted through such an entry becomes a request type the app " +
		"cannot dispatch: the action does nothing and fails silently on the page. Review every affected " +
		"action manually before shipping the converted page.")
}

// IndexByComponentType is MobilePageConversionGuideTool.IndexByComponentType: last wins, blank types skipped.
func IndexByComponentType(entries []*RegistryEntry) *dict[*RegistryEntry] {
	result := newCI[*RegistryEntry]()
	for _, entry := range entries {
		if entry != nil && !isBlank(entry.ComponentType) {
			result.set(entry.ComponentType, entry)
		}
	}
	return result
}
