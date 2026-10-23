package mobileguide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// The rules types are clio's WebToMobilePageConversionRulesModels.cs, WebToMobileComponentRemovalRules.cs and
// ComponentPropertyFilters.cs, read the way System.Text.Json reads them with PropertyNameCaseInsensitive:
// a missing member keeps its initializer ([] / "items" / "latest" ...), an explicit null replaces it with
// null, a null for a non-nullable bool is an error. Go's encoding/json already matches member names
// case-insensitively; the UnmarshalJSON methods below preset the initializers before decoding so an absent
// member keeps them and a JSON null still clears them.

// Rules is WebToMobilePageConversionRules.
type Rules struct {
	Version                      *string                          `json:"version"`
	Templates                    []*TemplateMappingRule           `json:"templates"`
	DefaultMobileTemplate        *string                          `json:"defaultMobileTemplate"`
	MobileFormPageTemplates      []*string                        `json:"mobileFormPageTemplates"`
	Components                   []*ComponentEquivalenceRule      `json:"components"`
	Requests                     []*RequestMappingRule            `json:"requests"`
	TabAreaLayers                *TabAreaLayersRule               `json:"tabAreaLayers"`
	ComponentPropertyOverrides   []*ComponentPropertyOverrideRule `json:"componentPropertyOverrides"`
	ContentContainerTypes        []*string                        `json:"contentContainerTypes"`
	EmptyContainerRemoval        *EmptyContainerRemovalRule       `json:"emptyContainerRemoval"`
	ExcludedComponents           []*ExcludedComponentGroup        `json:"excludedComponents"`
	NonConvertingScopeContainers []*string                        `json:"nonConvertingScopeContainers"`
	ComponentRemovals            []*ComponentRemovalRule          `json:"componentRemovals"`
	ActionComponents             []*ActionComponentRule           `json:"actionComponents"`
}

func (r *Rules) UnmarshalJSON(data []byte) error {
	type plain Rules
	value := plain{
		Version: ptr("latest"), Templates: []*TemplateMappingRule{}, MobileFormPageTemplates: []*string{},
		Components: []*ComponentEquivalenceRule{}, Requests: []*RequestMappingRule{},
		ComponentPropertyOverrides: []*ComponentPropertyOverrideRule{}, ContentContainerTypes: []*string{},
		ExcludedComponents: []*ExcludedComponentGroup{}, NonConvertingScopeContainers: []*string{},
		ComponentRemovals: []*ComponentRemovalRule{}, ActionComponents: []*ActionComponentRule{},
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = Rules(value)
	return nil
}

// TemplateMappingRule is TemplateMappingRule.
type TemplateMappingRule struct {
	Web              *string                 `json:"web"`
	Mobile           *string                 `json:"mobile"`
	Containers       []*ContainerMappingRule `json:"containers"`
	Components       []*ComponentMappingRule `json:"components"`
	DeclaredElements []*DeclaredElementRule  `json:"declaredElements"`
	Note             *string                 `json:"note"`
}

func (r *TemplateMappingRule) UnmarshalJSON(data []byte) error {
	type plain TemplateMappingRule
	value := plain{Containers: []*ContainerMappingRule{}, Components: []*ComponentMappingRule{}, DeclaredElements: []*DeclaredElementRule{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = TemplateMappingRule(value)
	return nil
}

// DeclaredElementRule is DeclaredElementRule.
type DeclaredElementRule struct {
	Name            *string                     `json:"name"`
	Type            *string                     `json:"type"`
	ParentName      *string                     `json:"parentName"`
	PropertyName    *string                     `json:"propertyName"`
	Index           *int                        `json:"index"`
	Values          *JSONElementMap             `json:"values"`
	CaptionResource *DeclaredElementCaptionRule `json:"captionResource"`
	Note            *string                     `json:"note"`
}

func (r *DeclaredElementRule) UnmarshalJSON(data []byte) error {
	type plain DeclaredElementRule
	value := plain{PropertyName: ptr("items"), Values: &JSONElementMap{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = DeclaredElementRule(value)
	return nil
}

// DeclaredElementCaptionRule is DeclaredElementCaptionRule.
type DeclaredElementCaptionRule struct {
	Key      *string `json:"key"`
	Value    *string `json:"value"`
	Property *string `json:"property"`
}

func (r *DeclaredElementCaptionRule) UnmarshalJSON(data []byte) error {
	type plain DeclaredElementCaptionRule
	value := plain{Property: ptr("caption")}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = DeclaredElementCaptionRule(value)
	return nil
}

// ContainerMappingRule is ContainerMappingRule.
type ContainerMappingRule struct {
	Web    *string `json:"web"`
	Mobile *string `json:"mobile"`
	Note   *string `json:"note"`
}

// ComponentMappingRule is ComponentMappingRule.
type ComponentMappingRule struct {
	Web             *string   `json:"web"`
	Mobile          *string   `json:"mobile"`
	MobileType      *string   `json:"mobileType"`
	CarryProperties []*string `json:"carryProperties"`
	Note            *string   `json:"note"`
}

func (r *ComponentMappingRule) UnmarshalJSON(data []byte) error {
	type plain ComponentMappingRule
	value := plain{CarryProperties: []*string{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ComponentMappingRule(value)
	return nil
}

// TabAreaLayersRule is TabAreaLayersRule.
type TabAreaLayersRule struct {
	Note             *string                   `json:"note"`
	TabComponentType *string                   `json:"tabComponentType"`
	MainTabContainer *SynthesizedContainerRule `json:"mainTabContainer"`
}

func (r *TabAreaLayersRule) UnmarshalJSON(data []byte) error {
	type plain TabAreaLayersRule
	value := plain{TabComponentType: ptr("crt.TabContainer")}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = TabAreaLayersRule(value)
	return nil
}

// SynthesizedContainerRule is SynthesizedContainerRule.
type SynthesizedContainerRule struct {
	NamePrefix    *string                   `json:"namePrefix"`
	Values        *JSONElementMap           `json:"values"`
	AreaContainer *SynthesizedContainerRule `json:"areaContainer"`
}

func (r *SynthesizedContainerRule) UnmarshalJSON(data []byte) error {
	type plain SynthesizedContainerRule
	value := plain{Values: &JSONElementMap{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = SynthesizedContainerRule(value)
	return nil
}

// ComponentPropertyOverrideRule is ComponentPropertyOverrideRule.
type ComponentPropertyOverrideRule struct {
	Filters            []*ElementFilterRule `json:"filters"`
	Values             *JSONElementMap      `json:"values"`
	MergeNestedObjects strictBool           `json:"mergeNestedObjects"`
	Note               *string              `json:"note"`
}

func (r *ComponentPropertyOverrideRule) UnmarshalJSON(data []byte) error {
	type plain ComponentPropertyOverrideRule
	value := plain{Values: &JSONElementMap{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ComponentPropertyOverrideRule(value)
	return nil
}

// EmptyContainerRemovalRule is EmptyContainerRemovalRule.
type EmptyContainerRemovalRule struct {
	Note           *string   `json:"note"`
	RemovableTypes []*string `json:"removableTypes"`
}

func (r *EmptyContainerRemovalRule) UnmarshalJSON(data []byte) error {
	type plain EmptyContainerRemovalRule
	value := plain{RemovableTypes: []*string{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = EmptyContainerRemovalRule(value)
	return nil
}

// ExcludedComponentGroup is ExcludedComponentGroup.
type ExcludedComponentGroup struct {
	Filters []*ExcludedComponentFilterRule `json:"filters"`
}

func (r *ExcludedComponentGroup) UnmarshalJSON(data []byte) error {
	type plain ExcludedComponentGroup
	value := plain{Filters: []*ExcludedComponentFilterRule{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ExcludedComponentGroup(value)
	return nil
}

// ExcludedComponentFilterRule is ExcludedComponentFilterRule.
type ExcludedComponentFilterRule struct {
	Type                    *string   `json:"type"`
	ExceptTypes             []*string `json:"exceptTypes"`
	ChildSlots              []*string `json:"childSlots"`
	ParentType              *string   `json:"parentType"`
	PropertiesContainerName *string   `json:"propertiesContainerName"`
	Note                    *string   `json:"note"`
}

func (r *ExcludedComponentFilterRule) UnmarshalJSON(data []byte) error {
	type plain ExcludedComponentFilterRule
	value := plain{ExceptTypes: []*string{}, ChildSlots: []*string{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ExcludedComponentFilterRule(value)
	return nil
}

// IsAllowList is ExcludedComponentFilterRule.IsAllowList.
func (r *ExcludedComponentFilterRule) IsAllowList() bool {
	return isNullOrWhiteSpace(r.Type) && len(r.ExceptTypes) > 0
}

// MatchesType is ExcludedComponentFilterRule.MatchesType.
func (r *ExcludedComponentFilterRule) MatchesType(componentType string) bool {
	if componentType == "" {
		return false
	}
	if r.IsAllowList() {
		for _, except := range r.ExceptTypes {
			if except != nil && strings.EqualFold(*except, componentType) {
				return false
			}
		}
		return true
	}
	return r.Type != nil && strings.EqualFold(componentType, *r.Type)
}

// RequestMappingRule is RequestMappingRule.
type RequestMappingRule struct {
	Web         *string        `json:"web"`
	Mobile      *string        `json:"mobile"`
	Category    *string        `json:"category"`
	ParamMap    *StringMapRule `json:"paramMap"`
	TargetParam *string        `json:"targetParam"`
	TargetKind  *string        `json:"targetKind"`
	Note        *string        `json:"note"`
}

func (r *RequestMappingRule) UnmarshalJSON(data []byte) error {
	type plain RequestMappingRule
	value := plain{ParamMap: &StringMapRule{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = RequestMappingRule(value)
	return nil
}

// ComponentEquivalenceRule is ComponentEquivalenceRule.
type ComponentEquivalenceRule struct {
	Web                 []*string                 `json:"web"`
	Mobile              []*string                 `json:"mobile"`
	Category            *string                   `json:"category"`
	PrimaryWeb          *string                   `json:"primaryWeb"`
	Note                *string                   `json:"note"`
	Filters             []*ElementFilterRule      `json:"filters"`
	Path                []*string                 `json:"path"`
	ViewConfigTemplates []*ViewConfigTemplateRule `json:"viewConfigTemplates"`
}

func (r *ComponentEquivalenceRule) UnmarshalJSON(data []byte) error {
	type plain ComponentEquivalenceRule
	value := plain{Web: []*string{}, Mobile: []*string{}, Filters: []*ElementFilterRule{}, Path: []*string{}, ViewConfigTemplates: []*ViewConfigTemplateRule{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ComponentEquivalenceRule(value)
	return nil
}

// ElementFilterRule is ElementFilterRule (its Values has no initializer: absent is null).
type ElementFilterRule struct {
	Type   *string         `json:"type"`
	Values *JSONElementMap `json:"values"`
}

// ViewConfigTemplateRule is ViewConfigTemplateRule.
type ViewConfigTemplateRule struct {
	ParentName               *string          `json:"parentName"`
	PropertyName             *string          `json:"propertyName"`
	Value                    *json.RawMessage `json:"value"`
	PreserveSourceProperties strictBool       `json:"preserveSourceProperties"`
}

// ComponentRemovalRule is ComponentRemovalRule.
type ComponentRemovalRule struct {
	Type    *string                  `json:"type"`
	Filters *ComponentPropertyFilter `json:"filters"`
	Reason  *string                  `json:"reason"`
}

// ActionComponentRule is ActionComponentRule.
type ActionComponentRule struct {
	Type                *string   `json:"type"`
	ActionPropertyNames []*string `json:"actionPropertyNames"`
}

func (r *ActionComponentRule) UnmarshalJSON(data []byte) error {
	type plain ActionComponentRule
	value := plain{ActionPropertyNames: []*string{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ActionComponentRule(value)
	return nil
}

// Filter discriminators and logical operations (ComponentPropertyFilterTypes, ComponentPropertyLogicalOperations).
const (
	FilterTypeGroup   = "Group"
	FilterTypeIsEmpty = "IsEmpty"
	LogicalAnd        = "and"
	LogicalOr         = "or"
)

// ComponentPropertyFilter is the polymorphic ComponentPropertyFilter: a Group (LogicalOperation, Items) or an
// IsEmpty (LeftExpression), discriminated by a "filterType" member that may appear anywhere in the object
// (AllowOutOfOrderMetadataProperties). The discriminator name and value are matched exactly.
type ComponentPropertyFilter struct {
	FilterType       string
	LogicalOperation *string
	Items            []*ComponentPropertyFilter
	LeftExpression   *string
}

func (f *ComponentPropertyFilter) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	raw, ok := members["filterType"]
	if !ok {
		return errMissingDiscriminator
	}
	var discriminator string
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return err
	}
	switch discriminator {
	case FilterTypeGroup:
		var group struct {
			LogicalOperation *string                    `json:"logicalOperation"`
			Items            []*ComponentPropertyFilter `json:"items"`
		}
		group.Items = []*ComponentPropertyFilter{}
		if err := json.Unmarshal(data, &group); err != nil {
			return err
		}
		*f = ComponentPropertyFilter{FilterType: FilterTypeGroup, LogicalOperation: group.LogicalOperation, Items: group.Items}
	case FilterTypeIsEmpty:
		var isEmpty struct {
			LeftExpression *string `json:"leftExpression"`
		}
		if err := json.Unmarshal(data, &isEmpty); err != nil {
			return err
		}
		*f = ComponentPropertyFilter{FilterType: FilterTypeIsEmpty, LeftExpression: isEmpty.LeftExpression}
	default:
		return fmt.Errorf("Read unrecognized type discriminator id '%s'.", discriminator)
	}
	return nil
}

var errMissingDiscriminator = errors.New("A componentRemovals filter does not declare a recognizable 'filterType' discriminator.")

// strictBool is a non-nullable C# bool: a JSON null is an error, as System.Text.Json reports it.
type strictBool bool

func (b *strictBool) UnmarshalJSON(data []byte) error {
	var value *bool
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("The JSON value could not be converted to System.Boolean.")
	}
	*b = strictBool(*value)
	return nil
}

// JSONElementMap is IReadOnlyDictionary<string, JsonElement>: keys in first-seen order, a repeated key keeps
// its first position and takes the last value (Dictionary indexer semantics).
type JSONElementMap struct {
	Keys   []string
	Values map[string]json.RawMessage
}

func (m *JSONElementMap) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("The JSON value could not be converted to a dictionary.")
	}
	m.Keys, m.Values = nil, map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key := keyToken.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if _, seen := m.Values[key]; !seen {
			m.Keys = append(m.Keys, key)
		}
		m.Values[key] = value
	}
	_, err = decoder.Token()
	return err
}

// StringMapRule is IReadOnlyDictionary<string, string> with the same ordering rules as JSONElementMap.
type StringMapRule struct {
	Keys   []string
	Values map[string]*string
}

func (m *StringMapRule) UnmarshalJSON(data []byte) error {
	var raw JSONElementMap
	if err := raw.UnmarshalJSON(data); err != nil {
		return err
	}
	m.Keys, m.Values = raw.Keys, map[string]*string{}
	for _, key := range raw.Keys {
		var value *string
		if err := json.Unmarshal(raw.Values[key], &value); err != nil {
			return err
		}
		m.Values[key] = value
	}
	return nil
}

// --- catalog ---------------------------------------------------------------------------------------------

// removalReasonCodes is the one reason a componentRemovals rule may report (ReasonCodes.DropUnsupportedRequest).
var removalReasonCodes = map[string]bool{"drop-unsupported-request": true}

// ParseRules is WebToMobilePageConversionRulesCatalog.ParseStream: nil for a blank document, an error for
// one that does not parse or fails validation.
func ParseRules(data []byte) (*Rules, error) {
	text := strings.TrimPrefix(string(data), byteOrderMark)
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var rules *Rules
	if err := json.Unmarshal([]byte(text), &rules); err != nil {
		return nil, err
	}
	if err := validateRules(rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func validateRules(rules *Rules) error {
	if rules == nil {
		return nil
	}
	for _, rule := range rules.ActionComponents {
		if rule == nil {
			return errors.New("actionComponents carries a null rule.")
		}
		if isNullOrWhiteSpace(rule.Type) {
			return errors.New("An actionComponents rule declares no 'type'.")
		}
		for _, name := range rule.ActionPropertyNames {
			if isNullOrWhiteSpace(name) {
				return fmt.Errorf("The actionComponents rule for '%s' declares a blank action property name.", *rule.Type)
			}
		}
	}
	for _, rule := range rules.ComponentRemovals {
		if rule == nil {
			return errors.New("componentRemovals carries a null rule.")
		}
		if rule.Type == nil || *rule.Type == "" {
			return errors.New("A componentRemovals rule declares no 'type'.")
		}
		if rule.Filters == nil {
			return fmt.Errorf("The componentRemovals rule for '%s' declares no 'filters'.", *rule.Type)
		}
		if rule.Reason != nil && *rule.Reason != "" && !removalReasonCodes[*rule.Reason] {
			return fmt.Errorf("The componentRemovals rule for '%s' names the reason code '%s', which is not valid on a droppedElements record.", *rule.Type, *rule.Reason)
		}
		if err := validateRemovalFilter(rule.Filters, *rule.Type); err != nil {
			return err
		}
	}
	return nil
}

func validateRemovalFilter(filter *ComponentPropertyFilter, componentType string) error {
	switch filter.FilterType {
	case FilterTypeGroup:
		if len(filter.Items) == 0 {
			return fmt.Errorf("A componentRemovals Group filter for '%s' declares no 'items'.", componentType)
		}
		if operation := filter.LogicalOperation; operation != nil && *operation != "" &&
			!strings.EqualFold(*operation, LogicalAnd) && !strings.EqualFold(*operation, LogicalOr) {
			return fmt.Errorf("A componentRemovals Group filter for '%s' names the logical operation '%s', which is neither 'and' nor 'or'.", componentType, *operation)
		}
		for _, item := range filter.Items {
			if item == nil {
				return fmt.Errorf("A componentRemovals Group filter for '%s' carries a null item.", componentType)
			}
			if err := validateRemovalFilter(item, componentType); err != nil {
				return err
			}
		}
	case FilterTypeIsEmpty:
		if filter.LeftExpression == nil || *filter.LeftExpression == "" {
			return fmt.Errorf("A componentRemovals IsEmpty filter for '%s' declares no 'leftExpression'.", componentType)
		}
	default:
		return fmt.Errorf("Unsupported componentRemovals filter on '%s'.", componentType)
	}
	return nil
}

var (
	bundledRulesOnce   sync.Once
	bundledRulesParsed *Rules
)

// BundledRules is WebToMobilePageConversionRulesCatalog.LoadBundled: the embedded rules, parsed once.
func BundledRules() *Rules {
	bundledRulesOnce.Do(func() {
		rules, err := ParseRules(bundledRulesJSON)
		if err != nil {
			panic("bundled conversion rules do not parse: " + err.Error())
		}
		if rules == nil {
			var empty Rules
			_ = empty.UnmarshalJSON([]byte("{}"))
			rules = &empty
		}
		bundledRulesParsed = rules
	})
	return bundledRulesParsed
}

// RulesFetcher reads the rules document for a version through the CDN chain
// (creatio.FetchWebToMobileConversionRules in production).
type RulesFetcher func(ctx context.Context, version string) ([]byte, error)

// GetRules is WebToMobilePageConversionRulesCatalog.GetRulesAsync: the fetched document when it parses and
// validates, the bundled rules otherwise. Only cancellation is returned as an error.
func GetRules(ctx context.Context, requestedVersion string, fetch RulesFetcher) (*Rules, error) {
	version := strings.TrimSpace(requestedVersion)
	if version == "" {
		version = "latest"
	}
	content, err := fetch(ctx, version)
	if err == nil {
		if rules, parseErr := ParseRules(content); parseErr == nil && rules != nil {
			return rules, nil
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return BundledRules(), nil
}

// byteOrderMark is what StreamReader strips from the start of a UTF-8 document.
const byteOrderMark = "\xef\xbb\xbf"

func ptr[T any](value T) *T { return &value }

// isNullOrWhiteSpace is string.IsNullOrWhiteSpace.
func isNullOrWhiteSpace(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}
