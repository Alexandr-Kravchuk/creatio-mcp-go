package creatio

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// clio's CreatioDataValueType table (code, server name, kind) as the business-rule code uses it.
type ruleWriteKind int

const (
	ruleWriteKindGuid ruleWriteKind = iota
	ruleWriteKindText
	ruleWriteKindNumeric
	ruleWriteKindDateTime
	ruleWriteKindLookup
	ruleWriteKindBoolean
	ruleWriteKindEnum
	ruleWriteKindNonFilterable
)

type ruleWriteTypeInfo struct {
	code int
	name string
	kind ruleWriteKind
}

var ruleWriteTypeTable = []ruleWriteTypeInfo{
	{0, "Guid", ruleWriteKindGuid}, {1, "Text", ruleWriteKindText}, {4, "Integer", ruleWriteKindNumeric},
	{5, "Float", ruleWriteKindNumeric}, {6, "Money", ruleWriteKindNumeric}, {7, "DateTime", ruleWriteKindDateTime},
	{8, "Date", ruleWriteKindDateTime}, {9, "Time", ruleWriteKindDateTime}, {10, "Lookup", ruleWriteKindLookup},
	{11, "Enum", ruleWriteKindEnum}, {12, "Boolean", ruleWriteKindBoolean}, {13, "Blob", ruleWriteKindNonFilterable},
	{14, "Image", ruleWriteKindNonFilterable}, {15, "CustomObject", ruleWriteKindNonFilterable},
	{16, "ImageLookup", ruleWriteKindNonFilterable}, {17, "Collection", ruleWriteKindNonFilterable},
	{18, "Color", ruleWriteKindText}, {19, "LocalizableString", ruleWriteKindText}, {20, "Entity", ruleWriteKindNonFilterable},
	{21, "EntityCollection", ruleWriteKindNonFilterable}, {22, "EntityColumnMappingCollection", ruleWriteKindNonFilterable},
	{23, "HashText", ruleWriteKindText}, {24, "SecureText", ruleWriteKindText}, {25, "File", ruleWriteKindNonFilterable},
	{26, "Mapping", ruleWriteKindNonFilterable}, {27, "ShortText", ruleWriteKindText}, {28, "MediumText", ruleWriteKindText},
	{29, "MaxSizeText", ruleWriteKindText}, {30, "LongText", ruleWriteKindText}, {31, "Float1", ruleWriteKindNumeric},
	{32, "Float2", ruleWriteKindNumeric}, {33, "Float3", ruleWriteKindNumeric}, {34, "Float4", ruleWriteKindNumeric},
	{35, "LocalizableParameterValuesList", ruleWriteKindNonFilterable}, {36, "MetadataText", ruleWriteKindText},
	{37, "StageIndicator", ruleWriteKindNonFilterable}, {38, "ObjectList", ruleWriteKindNonFilterable},
	{39, "CompositeObjectList", ruleWriteKindNonFilterable}, {40, "Float8", ruleWriteKindNumeric},
	{41, "FileLocator", ruleWriteKindNonFilterable}, {42, "PhoneText", ruleWriteKindText}, {43, "RichText", ruleWriteKindText},
	{44, "WebText", ruleWriteKindText}, {45, "EmailText", ruleWriteKindText}, {46, "CompositeObject", ruleWriteKindNonFilterable},
	{47, "Float0", ruleWriteKindNumeric}, {48, "Money0", ruleWriteKindNumeric}, {49, "Money1", ruleWriteKindNumeric},
	{50, "Money3", ruleWriteKindNumeric},
}

func ruleWriteTypeName(code int) (string, bool) {
	for _, info := range ruleWriteTypeTable {
		if info.code == code {
			return info.name, true
		}
	}
	return "", false
}

func ruleWriteTypeKind(name string) (ruleWriteKind, bool) {
	for _, info := range ruleWriteTypeTable {
		if strings.EqualFold(info.name, name) {
			return info.kind, true
		}
	}
	return 0, false
}

func ruleWriteIsKind(name string, kind ruleWriteKind) bool {
	found, ok := ruleWriteTypeKind(name)
	return ok && found == kind
}

func ruleWriteIsText(name string) bool     { return ruleWriteIsKind(name, ruleWriteKindText) }
func ruleWriteIsNumeric(name string) bool  { return ruleWriteIsKind(name, ruleWriteKindNumeric) }
func ruleWriteIsDateTime(name string) bool { return ruleWriteIsKind(name, ruleWriteKindDateTime) }
func ruleWriteIsFilterable(name string) bool {
	kind, ok := ruleWriteTypeKind(name)
	return ok && kind != ruleWriteKindNonFilterable
}

func ruleWriteIsRelational(name string) bool {
	return ruleWriteIsNumeric(name) || ruleWriteIsDateTime(name)
}

func ruleWriteUnsupportedForEquality(name string) bool {
	return strings.EqualFold(name, "RichText") || strings.EqualFold(name, "Image")
}

// Business-rule constants (clio's BusinessRuleConstants).
const (
	ruleWriteRuleTypeName           = "Terrasoft.Core.BusinessRules.BusinessRule"
	ruleWriteMetadataTypeName       = "Terrasoft.Core.BusinessRules.BusinessRules"
	ruleWriteCaseTypeName           = "Terrasoft.Core.BusinessRules.Models.BusinessRuleCase"
	ruleWriteTriggerTypeName        = "Terrasoft.Core.BusinessRules.Models.Trigger"
	ruleWriteContextExpression      = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleContextExpression"
	ruleWriteParameterMapping       = "Terrasoft.Core.BusinessRules.Models.Expressions.ParameterMapping"
	ruleWriteSchemaVariableTypeName = "Terrasoft.Core.ExpressionEngine.Schema.Variables.ExpressionSchemaVariable"
	ruleWriteRecordVariableConfig   = "Terrasoft.Core.ExpressionEngine.Schema.Variables.ExpressionSchemaRecordVariableConfig"
	ruleWriteSchemaParameter        = "Terrasoft.Core.ExpressionEngine.Schema.Parameters.ExpressionSchemaParameter"
	ruleWriteSetValuesAction        = "Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleActionSetValues"
	ruleWriteFilterLookupAction     = "Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleActionFilterLookup"
	ruleWriteSetFilterAction        = "Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleActionSetFilter"
	ruleWriteSetValueItem           = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleSetValueItem"
	ruleWriteFilterLookupExpression = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleFilterLookupExpression"

	ruleWriteChangeTrigger     = 0
	ruleWriteDataLoadedTrigger = 2

	ruleWriteSupportedComparisons  = "equal, not-equal, is-filled-in, is-not-filled-in, greater-than, greater-than-or-equal, less-than, less-than-or-equal, contain, not-contain"
	ruleWriteSupportedActions      = "make-editable, make-read-only, make-required, make-optional, set-values, apply-filter, apply-static-filter"
	ruleWriteSupportedPageActions  = "hide-element, show-element, make-editable, make-read-only, make-required, make-optional"
	ruleWriteSupportedSystemValues = "CurrentDate, CurrentTime, CurrentDateTime, CurrentUser, CurrentUserContact, CurrentUserAccount, CurrentUserRoles"
)

var ruleWriteActionTypeNames = map[string]string{
	"make-editable":       brActionPrefix + "BusinessRuleActionEditableElement",
	"make-read-only":      brActionPrefix + "BusinessRuleActionReadonlyElement",
	"make-required":       brActionPrefix + "BusinessRuleActionRequiredElement",
	"make-optional":       brActionPrefix + "BusinessRuleActionOptionalElement",
	"apply-filter":        ruleWriteFilterLookupAction,
	"apply-static-filter": ruleWriteSetFilterAction,
}

var ruleWritePageActionTypeNames = map[string]string{
	"hide-element":   brActionPrefix + "BusinessRuleActionHideElement",
	"show-element":   brActionPrefix + "BusinessRuleActionShowElement",
	"make-editable":  brActionPrefix + "BusinessRuleActionEditableElement",
	"make-read-only": brActionPrefix + "BusinessRuleActionReadonlyElement",
	"make-required":  brActionPrefix + "BusinessRuleActionRequiredElement",
	"make-optional":  brActionPrefix + "BusinessRuleActionOptionalElement",
}

var ruleWriteComparisonValues = map[string]int{
	"is-not-filled-in": 0, "is-filled-in": 1, "equal": 2, "not-equal": 3, "less-than": 5, "less-than-or-equal": 6,
	"greater-than": 7, "greater-than-or-equal": 8, "contain": 11, "not-contain": 12,
}

// ruleWriteComparison looks a comparison up case-insensitively, as clio's OrdinalIgnoreCase dictionary does.
func ruleWriteComparison(name string) (int, bool) {
	for key, value := range ruleWriteComparisonValues {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return 0, false
}

func ruleWriteIsComparisonIn(name string, names ...string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	for _, candidate := range names {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func ruleWriteIsUnary(name string) bool {
	return ruleWriteIsComparisonIn(name, "is-filled-in", "is-not-filled-in")
}

type ruleWriteSystemVariable struct {
	name, dataValueType, referenceSchema string
}

var ruleWriteSystemVariables = []ruleWriteSystemVariable{
	{"CurrentDate", "Date", ""}, {"CurrentTime", "Time", ""}, {"CurrentDateTime", "DateTime", ""},
	{"CurrentUser", "Lookup", "SysAdminUnit"}, {"CurrentUserContact", "Lookup", "Contact"},
	{"CurrentUserAccount", "Lookup", "Account"}, {"CurrentUserRoles", "ObjectList", "SysAdminUnit"},
}

func ruleWriteSystemVariableByName(name string) (ruleWriteSystemVariable, bool) {
	for _, variable := range ruleWriteSystemVariables {
		if strings.EqualFold(variable.name, name) {
			return variable, true
		}
	}
	return ruleWriteSystemVariable{}, false
}

// ruleWriteOperandType is clio's OperandTypeContext / OperandType: a data value type and, for lookups,
// the referenced schema ("" when none).
type ruleWriteOperandType struct {
	dataValueType   string
	referenceSchema *string
}

func (t ruleWriteOperandType) asValueType() ruleWriteOperandType {
	if strings.EqualFold(t.dataValueType, "ObjectList") {
		return ruleWriteOperandType{dataValueType: "Lookup", referenceSchema: t.referenceSchema}
	}
	return t
}

var ruleWriteTextOperand = ruleWriteOperandType{dataValueType: "Text"}

// Value conversion (clio's BusinessRuleHelpers).

var ruleWriteTimeZoneSuffix = regexp.MustCompile(`(?i)(?:Z|[+-]\d{2}:\d{2})$`)

// ruleWriteDateTimeConstant parses a Date/DateTime/Time constant and returns the UTC instant clio
// normalizes it to, written the way System.Text.Json writes a UTC DateTime.
func ruleWriteDateTimeConstant(value any, dataValueType string) (string, bool) {
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	switch dataValueType {
	case "Date":
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil || len(raw) != 10 {
			return "", false
		}
		return ruleWriteSTJDateTime(parsed.UTC()), true
	case "DateTime":
		if !ruleWriteTimeZoneSuffix.MatchString(raw) {
			return "", false
		}
		parsed, ok := ruleWriteParseOffsetDateTime(raw)
		if !ok {
			return "", false
		}
		return ruleWriteSTJDateTime(parsed.UTC()), true
	case "Time":
		if !ruleWriteTimeZoneSuffix.MatchString(raw) {
			return "", false
		}
		parsed, ok := ruleWriteParseOffsetDateTime("2000-01-01T" + raw)
		if !ok {
			return "", false
		}
		utc := parsed.UTC()
		clock := time.Date(1, 1, 1, utc.Hour(), utc.Minute(), utc.Second(), utc.Nanosecond(), time.UTC)
		return ruleWriteSTJDateTime(clock), true
	}
	return "", false
}

// ruleWriteParseOffsetDateTime accepts the ISO 8601 shapes DateTimeOffset.TryParse accepts in practice
// for these constants: a date, an optional time with optional fraction, and a Z or ±HH:mm offset.
func ruleWriteParseOffsetDateTime(raw string) (time.Time, bool) {
	normalized := raw
	if strings.HasSuffix(strings.ToLower(normalized), "z") {
		normalized = normalized[:len(normalized)-1] + "Z"
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04Z07:00", "2006-01-02Z07:00",
	} {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// ruleWriteSTJDateTime writes a UTC DateTime as System.Text.Json does: seconds always, the fraction only
// when non-zero (100 ns ticks) and without trailing zeros, then Z.
func ruleWriteSTJDateTime(value time.Time) string {
	text := value.Format("2006-01-02T15:04:05")
	if ticks := value.Nanosecond() / 100; ticks != 0 {
		fraction := strconv.Itoa(ticks)
		for len(fraction) < 7 {
			fraction = "0" + fraction
		}
		text += "." + strings.TrimRight(fraction, "0")
	}
	return text + "Z"
}
