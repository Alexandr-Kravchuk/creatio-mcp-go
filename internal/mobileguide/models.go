package mobileguide

import (
	"bytes"
	"encoding/json"
)

// The answer types of MobilePageConversionGuideModels.cs (clio 8.1.0.142), serialized by clio's MCP options:
// McpJsonUtilities.DefaultOptions = JsonSerializerDefaults.Web (camelCase) with DefaultIgnoreCondition
// WhenWritingNull (checked in the decompiled ModelContextProtocol.Core.dll), so EVERY null member is left out (Go: omitzero, which drops nil and keeps an empty non-nil slice),
// not only those with an explicit [JsonIgnore]. Lists initialised to [] in C# are written as [] unless the engine
// sets them to null; the Go side keeps that distinction with nil (omitted) versus an empty non-nil slice.
// JsonNode members stay raw JSON (json.RawMessage) written by the engine's own writer; nil is omitted.

// OrderedMap is IReadOnlyDictionary<string, V> as a Dictionary serializes it: insertion order.
type OrderedMap[V any] struct {
	Keys   []string
	Values map[string]V
}

// Set adds or replaces a key; a new key goes last, a replaced one keeps its place.
func (m *OrderedMap[V]) Set(key string, value V) {
	if m.Values == nil {
		m.Values = map[string]V{}
	}
	if _, ok := m.Values[key]; !ok {
		m.Keys = append(m.Keys, key)
	}
	m.Values[key] = value
}

func (m *OrderedMap[V]) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range m.Keys {
		if index > 0 {
			buffer.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(m.Values[key])
		if err != nil {
			return nil, err
		}
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// SourceComponentInfo is SourceComponentInfo.
type SourceComponentInfo struct {
	Name        *string `json:"name,omitzero"`
	Type        *string `json:"type,omitzero"`
	ParentName  *string `json:"parentName,omitzero"`
	IsContainer bool    `json:"isContainer"`
}

// DataSectionConflict is DataSectionConflict.
type DataSectionConflict struct {
	Section *string  `json:"section,omitzero"`
	Path    []string `json:"path,omitzero"`
	Entry   *string  `json:"entry,omitzero"`
	Kind    *string  `json:"kind,omitzero"`
}

// ContainerMapEntry is ContainerMapEntry.
type ContainerMapEntry struct {
	Web    *string `json:"web,omitzero"`
	Mobile *string `json:"mobile,omitzero"`
}

// ComponentSuggestion is ComponentSuggestion.
type ComponentSuggestion struct {
	SourceType           *string  `json:"sourceType,omitzero"`
	SourceNames          []string `json:"sourceNames,omitzero"`
	Category             *string  `json:"category,omitzero"`
	SuggestedMobileTypes []string `json:"suggestedMobileTypes,omitzero"`
	PrimaryWebMerge      *string  `json:"primaryWebMerge,omitzero"`
	Note                 *string  `json:"note,omitzero"`
}

// DroppedElement is DroppedElement.
type DroppedElement struct {
	WebName *string       `json:"webName,omitzero"`
	WebType *string       `json:"webType,omitzero"`
	Reason  []*ReasonCode `json:"reason,omitzero"`
}

// ReasonCode is ReasonCode; Params values are JsonNode.
type ReasonCode struct {
	Code   *string                      `json:"code,omitzero"`
	Params *OrderedMap[json.RawMessage] `json:"params,omitzero"`
}

// Reason codes (ReasonCodes).
const (
	DropEmptyContainer                     = "drop-empty-container"
	DropContainerNoMobileEquivalent        = "drop-container-no-mobile-equivalent"
	DropExcludedByRule                     = "drop-excluded-by-rule"
	DropParentExcluded                     = "drop-parent-excluded"
	DropInheritedChrome                    = "drop-inherited-chrome"
	DropTargetMissing                      = "drop-target-missing"
	DropUnsupportedRequest                 = "drop-unsupported-request"
	DropNonConvertingScope                 = "drop-non-converting-scope"
	DropTypeNotInMobileRegistry            = "drop-type-not-in-mobile-registry"
	DropUnknownRequest                     = "drop-unknown-request"
	DropNoRuleInScope                      = "drop-no-rule-in-scope"
	DropNotAnActionInScope                 = "drop-not-an-action-in-scope"
	DropRequestChromeNative                = "drop-request-chrome-native"
	DropRequestUnsupported                 = "drop-request-unsupported"
	DropRequestPropertyNotDeclared         = "drop-request-property-not-declared"
	DropRequestTargetMissing               = "drop-request-target-missing"
	DropRequestElementEmptyContainer       = "drop-request-element-empty-container"
	DropRequestElementExcluded             = "drop-request-element-excluded"
	FlagRequestUnmapped                    = "flag-request-unmapped"
	DropRuleConditionMixedAndOr            = "drop-rule-condition-mixed-and-or"
	DropRuleConditionUnsupportedComparison = "drop-rule-condition-unsupported-comparison"
	DropRuleConditionUnconvertible         = "drop-rule-condition-unconvertible"
	DropRuleNoActionConverts               = "drop-rule-no-action-converts"
	SkipNormalizationPathBlocked           = "skip-normalization-path-blocked"
)

// CaptionResource is CaptionResource.
type CaptionResource struct {
	Key         *string `json:"key,omitzero"`
	SourceValue *string `json:"sourceValue,omitzero"`
}

// ViewConfigDiffOperation is ViewConfigDiffOperation.
type ViewConfigDiffOperation struct {
	Operation    *string         `json:"operation,omitzero"`
	Name         *string         `json:"name,omitzero"`
	ParentName   *string         `json:"parentName,omitzero"`
	PropertyName *string         `json:"propertyName,omitzero"`
	Index        *int            `json:"index,omitzero"`
	Values       json.RawMessage `json:"values,omitzero"`
}

// UnresolvedParent is UnresolvedParent.
type UnresolvedParent struct {
	Name       *string `json:"name,omitzero"`
	ParentName *string `json:"parentName,omitzero"`
}

// Element map operations (ElementMapOperations).
const (
	OperationInsert           = "insert"
	OperationMerge            = "merge"
	OperationDrop             = "drop"
	OperationRelocateChildren = "relocate-children"
)

// ElementMapEntry is ElementMapEntry: no [JsonPropertyName], so the Web camelCase policy names its members;
// its internal members (DeclaredByRule, PositionalAnchor, MergeParentName) are never written.
type ElementMapEntry struct {
	WebName         *string          `json:"webName,omitzero"`
	WebType         *string          `json:"webType,omitzero"`
	Operation       *string          `json:"operation,omitzero"`
	Name            *string          `json:"name,omitzero"`
	MobileType      *string          `json:"mobileType,omitzero"`
	ParentName      *string          `json:"parentName,omitzero"`
	PropertyName    *string          `json:"propertyName,omitzero"`
	ParentSource    *string          `json:"parentSource,omitzero"`
	Index           *int             `json:"index,omitzero"`
	CaptionResource *CaptionResource `json:"captionResource,omitzero"`
	Values          json.RawMessage  `json:"values,omitzero"`
	Reason          []*ReasonCode    `json:"reason,omitzero"`

	DeclaredByRule   bool    `json:"-"`
	PositionalAnchor *string `json:"-"`
	MergeParentName  *string `json:"-"`
}

// MobileComponentContract is MobileComponentContract (Example and DesignerDefaults are JsonElement?).
type MobileComponentContract struct {
	ComponentType     *string         `json:"componentType,omitzero"`
	Container         *bool           `json:"container,omitzero"`
	Description       *string         `json:"description,omitzero"`
	AllowedProperties []string        `json:"allowedProperties,omitzero"`
	Example           json.RawMessage `json:"example,omitzero"`
	DesignerDefaults  json.RawMessage `json:"designerDefaults,omitzero"`
}

// WorkplaceInfo is WorkplaceInfo.
type WorkplaceInfo struct {
	ID              *string `json:"id,omitzero"`
	Name            *string `json:"name,omitzero"`
	IsMobile        bool    `json:"isMobile"`
	ContainsSection bool    `json:"containsSection"`
}

// SectionRegistrationInfo is SectionRegistrationInfo.
type SectionRegistrationInfo struct {
	SourcePageIsSection       *bool            `json:"sourcePageIsSection,omitzero"`
	SysModuleID               *string          `json:"sysModuleId,omitzero"`
	SectionCode               *string          `json:"sectionCode,omitzero"`
	SectionCaption            *string          `json:"sectionCaption,omitzero"`
	EntitySchemaName          *string          `json:"entitySchemaName,omitzero"`
	MobileSectionSchemaUID    *string          `json:"mobileSectionSchemaUId,omitzero"`
	MobileSectionRegistered   *bool            `json:"mobileSectionRegistered,omitzero"`
	IsFormPage                bool             `json:"isFormPage"`
	CurrentWorkplaces         []*WorkplaceInfo `json:"currentWorkplaces,omitzero"`
	AvailableMobileWorkplaces []*WorkplaceInfo `json:"availableMobileWorkplaces,omitzero"`
	RegistrationActions       []string         `json:"registrationActions,omitzero"`
	Note                      *string          `json:"note,omitzero"`
	ProbeOk                   bool             `json:"probeOk"`
}

// ExistingMobilePageInfo is ExistingMobilePageInfo.
type ExistingMobilePageInfo struct {
	SchemaName *string `json:"schemaName,omitzero"`
	SchemaUID  *string `json:"schemaUId,omitzero"`
	Source     *string `json:"source,omitzero"`
}

// Guide is MobilePageConversionGuide.
type Guide struct {
	SourcePage                *string                         `json:"sourcePage,omitzero"`
	SourceType                *string                         `json:"sourceType,omitzero"`
	SourceTemplate            *string                         `json:"sourceTemplate,omitzero"`
	SourceStructure           []*SourceComponentInfo          `json:"sourceStructure,omitzero"`
	LayoutResolution          *string                         `json:"layoutResolution,omitzero"`
	WebOnlySections           []string                        `json:"webOnlySections,omitzero"`
	DataSources               []string                        `json:"dataSources,omitzero"`
	ModelConfig               json.RawMessage                 `json:"modelConfig,omitzero"`
	ViewModelConfig           json.RawMessage                 `json:"viewModelConfig,omitzero"`
	ModelConfigDiff           json.RawMessage                 `json:"modelConfigDiff,omitzero"`
	ViewModelConfigDiff       json.RawMessage                 `json:"viewModelConfigDiff,omitzero"`
	DataSectionConflicts      []*DataSectionConflict          `json:"dataSectionConflicts,omitzero"`
	RecommendedMobileTemplate *string                         `json:"recommendedMobileTemplate,omitzero"`
	TemplateMatch             *string                         `json:"templateMatch,omitzero"`
	ContainerMap              []*ContainerMapEntry            `json:"containerMap,omitzero"`
	ComponentSuggestions      []*ComponentSuggestion          `json:"componentSuggestions,omitzero"`
	ViewConfigDiff            []*ViewConfigDiffOperation      `json:"viewConfigDiff,omitzero"`
	NameMap                   *OrderedMap[*string]            `json:"nameMap,omitzero"`
	UnresolvedParents         []*UnresolvedParent             `json:"unresolvedParents,omitzero"`
	DroppedElements           []*DroppedElement               `json:"droppedElements,omitzero"`
	MobileContracts           []*MobileComponentContract      `json:"mobileContracts,omitzero"`
	SectionRegistration       *SectionRegistrationInfo        `json:"sectionRegistration,omitzero"`
	ExistingMobilePages       []*ExistingMobilePageInfo       `json:"existingMobilePages,omitzero"`
	PageBusinessRules         *PageBusinessRuleConversionInfo `json:"pageBusinessRules,omitzero"`
	RequestConversions        *RequestConversionInfo          `json:"requestConversions,omitzero"`
	AdaptiveLayout            []*AdaptiveLayoutGroup          `json:"adaptiveLayout,omitzero"`
	TabAreaLayers             []*TabAreaLayerGroup            `json:"tabAreaLayers,omitzero"`
	Normalizations            *OrderedMap[*NormalizationInfo] `json:"normalizations,omitzero"`
	PrunedProperties          []*PrunedPropertyEntry          `json:"prunedProperties,omitzero"`
	PropertyPruneApplied      bool                            `json:"propertyPruneApplied"`
	ResourceStrings           *OrderedMap[*string]            `json:"resourceStrings,omitzero"`
	GuidanceArticle           *string                         `json:"guidanceArticle,omitzero"`
	SuggestedTargetSchemaName *string                         `json:"suggestedTargetSchemaName,omitzero"`
}

// PageBusinessRuleConversionInfo is PageBusinessRuleConversionInfo.
type PageBusinessRuleConversionInfo struct {
	ProbeOk        bool                         `json:"probeOk"`
	Note           *string                      `json:"note,omitzero"`
	ConvertedRules []*ConvertedPageBusinessRule `json:"convertedRules,omitzero"`
	DroppedRules   []*DroppedPageBusinessRule   `json:"droppedRules,omitzero"`
}

// ConvertedPageBusinessRule is ConvertedPageBusinessRule.
type ConvertedPageBusinessRule struct {
	Caption *string         `json:"caption,omitzero"`
	Rule    json.RawMessage `json:"rule,omitzero"`
}

// DroppedPageBusinessRule is DroppedPageBusinessRule.
type DroppedPageBusinessRule struct {
	Caption *string       `json:"caption,omitzero"`
	Reason  []*ReasonCode `json:"reason,omitzero"`
}

// RequestConversionInfo is RequestConversionInfo.
type RequestConversionInfo struct {
	ConvertedRequests        []*ConvertedRequest        `json:"convertedRequests,omitzero"`
	DroppedRequests          []*DroppedRequest          `json:"droppedRequests,omitzero"`
	FlaggedRequests          []*FlaggedRequest          `json:"flaggedRequests,omitzero"`
	UnresolvedTargetRequests []*UnresolvedTargetRequest `json:"unresolvedTargetRequests,omitzero"`
	TargetsProbed            bool                       `json:"targetsProbed"`
	TargetsNote              *string                    `json:"targetsNote,omitzero"`
	MissingTargetPages       []*MissingTargetPage       `json:"missingTargetPages,omitzero"`
}

// MissingTargetPageReference is MissingTargetPageReference.
type MissingTargetPageReference struct {
	ElementName *string `json:"elementName,omitzero"`
	Binding     *string `json:"binding,omitzero"`
}

// MissingTargetPage is MissingTargetPage.
type MissingTargetPage struct {
	Target                      *string                       `json:"target,omitzero"`
	TargetKind                  *string                       `json:"targetKind,omitzero"`
	ResolvedCandidateSchemaName *string                       `json:"resolvedCandidateSchemaName,omitzero"`
	References                  []*MissingTargetPageReference `json:"references,omitzero"`
}

// PrunedPropertyEntry is PrunedPropertyEntry.
type PrunedPropertyEntry struct {
	Name       *string  `json:"name,omitzero"`
	Type       *string  `json:"type,omitzero"`
	WebName    *string  `json:"webName,omitzero"`
	Properties []string `json:"properties,omitzero"`
	Bindings   []string `json:"bindings,omitzero"`
}

// ConvertedRequest is ConvertedRequest.
type ConvertedRequest struct {
	ElementName   *string `json:"elementName,omitzero"`
	Binding       *string `json:"binding,omitzero"`
	WebRequest    *string `json:"webRequest,omitzero"`
	MobileRequest *string `json:"mobileRequest,omitzero"`
}

// DroppedRequest is DroppedRequest.
type DroppedRequest struct {
	ElementName *string       `json:"elementName,omitzero"`
	Binding     *string       `json:"binding,omitzero"`
	WebRequest  *string       `json:"webRequest,omitzero"`
	Reason      []*ReasonCode `json:"reason,omitzero"`
}

// FlaggedRequest is FlaggedRequest.
type FlaggedRequest struct {
	ElementName *string       `json:"elementName,omitzero"`
	Binding     *string       `json:"binding,omitzero"`
	Request     *string       `json:"request,omitzero"`
	Reason      []*ReasonCode `json:"reason,omitzero"`
}

// Unresolved target states (UnresolvedTargetRequest.StateMissing / StateUnknown).
const (
	StateMissing = "missing"
	StateUnknown = "unknown"
)

// UnresolvedTargetRequest is UnresolvedTargetRequest.
type UnresolvedTargetRequest struct {
	ElementName                 *string `json:"elementName,omitzero"`
	Binding                     *string `json:"binding,omitzero"`
	WebRequest                  *string `json:"webRequest,omitzero"`
	TargetKind                  *string `json:"targetKind,omitzero"`
	Target                      *string `json:"target,omitzero"`
	State                       *string `json:"state,omitzero"`
	BindingRemoved              bool    `json:"bindingRemoved"`
	ResolvedCandidateSchemaName *string `json:"resolvedCandidateSchemaName,omitzero"`
}

// AdaptiveLayoutGroup is AdaptiveLayoutGroup.
type AdaptiveLayoutGroup struct {
	ContainerName       *string               `json:"containerName,omitzero"`
	ColumnsByBreakpoint *OrderedMap[[]string] `json:"columnsByBreakpoint,omitzero"`
	Items               []*AdaptiveLayoutItem `json:"items,omitzero"`
}

// TabAreaLayerGroup is TabAreaLayerGroup.
type TabAreaLayerGroup struct {
	TabName              *string  `json:"tabName,omitzero"`
	MainTabContainerName *string  `json:"mainTabContainerName,omitzero"`
	AreaName             *string  `json:"areaName,omitzero"`
	MovedChildren        []string `json:"movedChildren,omitzero"`
}

// NormalizationInfo is NormalizationInfo.
type NormalizationInfo struct {
	Normalized []*NormalizationEntry `json:"normalized,omitzero"`
	Skipped    []*NormalizationSkip  `json:"skipped,omitzero"`
}

// NormalizationEntry is NormalizationEntry.
type NormalizationEntry struct {
	Name       *string  `json:"name,omitzero"`
	Type       *string  `json:"type,omitzero"`
	Properties []string `json:"properties,omitzero"`
}

// NormalizationSkip is NormalizationSkip.
type NormalizationSkip struct {
	Name       *string       `json:"name,omitzero"`
	Type       *string       `json:"type,omitzero"`
	Properties []string      `json:"properties,omitzero"`
	Reason     []*ReasonCode `json:"reason,omitzero"`
}

// AdaptiveLayoutItem is AdaptiveLayoutItem.
type AdaptiveLayoutItem struct {
	Name                 *string         `json:"name,omitzero"`
	LayoutConfigAdaptive json.RawMessage `json:"layoutConfigAdaptive,omitzero"`
}
