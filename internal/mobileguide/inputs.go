package mobileguide

import (
	"encoding/json"
)

// RegistryEntry is the part of clio's ComponentRegistryEntry the analysis reads. The dictionaries are the
// ordinal ones System.Text.Json builds while reading the registry, in document order.
type RegistryEntry struct {
	ComponentType    string
	Container        *bool
	Description      *string
	Example          json.RawMessage // JsonElement?; nil when absent or null
	DesignerDefaults json.RawMessage
	Properties       *dict[*PropertyDefinition]
	Inputs           *dict[*jn]
	Outputs          *dict[*jn]
}

// PropertyDefinition is ComponentPropertyDefinition's type and default.
type PropertyDefinition struct {
	Type    *string
	Default *jn // JsonElement?; nil when absent
}

// Bundle is the part of PageBundleInfo the analysis reads; the configs are System.Text.Json nodes.
type Bundle struct {
	ViewConfig       *jn
	ViewModelConfig  *jn
	ModelConfig      *jn
	ResourcesStrings *jn
	Handlers         *string
	Converters       *string
	Validators       *string
}

// PositionalPlacement is WebToMobileAnalysisService.PositionalPlacement.
type PositionalPlacement struct {
	WebAnchor    *string
	MobileAnchor *string
}

// PageBusinessRuleProbeResult is PageBusinessRuleProbeResult (its Rules are the internal read-model).
type PageBusinessRuleProbeResult struct {
	ProbeOk bool
	Note    *string
	Rules   []*SourcePageBusinessRule
}

// Page rule condition issues (PageRuleConditionIssue).
const (
	ConditionIssueNone = iota
	ConditionIssueMixedAndOr
	ConditionIssueUnrecognizedComparison
)

// SourcePageBusinessRule is SourcePageBusinessRule.
type SourcePageBusinessRule struct {
	Caption        *string
	Condition      *jn
	ConditionIssue int
	Actions        []*SourcePageRuleAction
}

// SourcePageRuleAction is SourcePageRuleAction.
type SourcePageRuleAction struct {
	ActionType   *string
	ElementItems []*string
}

// Action target states (ActionTargetState).
const (
	TargetUnknown = iota
	TargetResolved
	TargetMissing
)

// ActionTargetResolution is ActionTargetResolution.
type ActionTargetResolution struct {
	Kind                        *string
	Target                      *string
	State                       int
	ResolvedCandidateSchemaName *string
}

// ActionTargetsProbeResult is MobileActionTargetProbeResult (the part the analysis reads).
type ActionTargetsProbeResult struct {
	ProbeOk      bool
	Note         *string
	TargetsByKey *dict[*ActionTargetResolution]
}

// RegistryGeneration is MobileRegistryGeneration.
type RegistryGeneration struct {
	BaseInputs *dict[*jn]
}

// CatalogIsRuntimeDerived is MobileRegistryGeneration.CatalogIsRuntimeDerived.
func (g *RegistryGeneration) CatalogIsRuntimeDerived() bool {
	return g != nil && g.BaseInputs.len() > 0 && g.declaresInherited("layoutConfig") && g.declaresInherited("visible")
}

func (g *RegistryGeneration) declaresInherited(key string) bool {
	for _, declared := range g.BaseInputs.keys() {
		if eqi(declared, key) {
			return true
		}
	}
	return false
}

// AnalyzeInput carries WebToMobileAnalysisService.Analyze's arguments; nil is C#'s null.
type AnalyzeInput struct {
	Bundle                        *Bundle
	MobileTypes                   *strSet
	WebTypes                      *strSet
	WebByType                     *dict[*RegistryEntry]
	MobileByType                  *dict[*RegistryEntry]
	Rules                         *Rules
	TemplateRule                  *TemplateMappingRule
	MobileRequestTypes            *strSet
	SourcePage                    *string
	SourceTemplate                *string
	SuggestedTarget               *string
	ContainerNameMap              *dict[string]
	SectionRegistration           *SectionRegistrationInfo
	PageBusinessRulesProbe        *PageBusinessRuleProbeResult
	TemplateComponentNames        *strSet
	ComponentNameMap              *dict[*ComponentMappingRule]
	PositionalPlacements          []*PositionalPlacement
	MobileContainerParents        *dict[string]
	MobileTemplateViewModelConfig *jn
	MobileTemplateModelConfig     *jn
	MobileTemplateTypesByName     *dict[*string]
	MobileTemplateLayoutConfigs   *dict[*jn]
	MobileTemplateNodesByName     *dict[*jn]
	WebTemplateBaselineNodes      *dict[*jn]
	WebTemplateResources          *jn
	ActionTargetsProbe            *ActionTargetsProbeResult
	ExistingMobilePages           []*ExistingMobilePageInfo
	MobileRegistryGeneration      *RegistryGeneration
}

// rawNode parses a JsonElement's raw text (JsonNode.Parse(element.GetRawText())).
func rawNode(raw json.RawMessage) *jn {
	if len(raw) == 0 {
		return nil
	}
	node, err := parseSTJ(raw)
	if err != nil {
		return nil
	}
	return node
}
