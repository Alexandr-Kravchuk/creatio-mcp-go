package creatio

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// apply-static-filter (clio's StaticFilterDeserializer, StaticFilterStructuralValidator,
// SchemaAwareFilterValidator, SimpleToFullFilterConverter, MacrosCatalog, DatePartCatalog and
// LookupValueResolver).

type ruleWriteFilterGroup struct {
	logicalOperation string
	filters          []ruleWriteFilterLeaf
	groups           []*ruleWriteFilterGroup
	backward         []ruleWriteFilterBackward
}

type ruleWriteFilterLeaf struct {
	columnPath     string
	comparisonType string
	value          any
	hasValue       bool
	valueMacros    *string
	macrosArgument *int
	datePart       *string
}

type ruleWriteFilterBackward struct {
	referenceColumnPath string
	comparisonType      string
	aggregationType     *string
	aggregationColumn   *string
	aggregationValue    *float64
	filter              *ruleWriteFilterGroup
}

func ruleWriteDeserializeFilter(value any) *ruleWriteFilterGroup {
	return ruleWriteFilterGroupAt(value, "filter")
}

func ruleWriteFilterGroupAt(value any, path string) *ruleWriteFilterGroup {
	object, ok := value.(map[string]any)
	if !ok {
		ruleWriteFail("%s: must be a JSON object.", path)
	}
	group := &ruleWriteFilterGroup{logicalOperation: ruleWriteFilterRequired(object, "logicalOperation", path)}
	if raw, ok := object["filters"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok {
			ruleWriteFail("%s.filters: must be a JSON array.", path)
		}
		for index, item := range items {
			leafPath := fmt.Sprintf("%s.filters[%d]", path, index)
			leaf, ok := item.(map[string]any)
			if !ok {
				ruleWriteFail("%s: must be a JSON object.", leafPath)
			}
			entry := ruleWriteFilterLeaf{
				columnPath:     ruleWriteFilterRequired(leaf, "columnPath", leafPath),
				comparisonType: ruleWriteFilterRequired(leaf, "comparisonType", leafPath),
			}
			if value, ok := leaf["value"]; ok && value != nil {
				entry.value, entry.hasValue = value, true
			}
			if macros, ok := leaf["valueMacros"].(string); ok {
				entry.valueMacros = &macros
			}
			entry.macrosArgument = ruleWriteFilterOptionalInt(leaf, "valueMacrosArgument", leafPath)
			entry.datePart = ruleWriteFilterOptionalString(leaf, "datePart")
			group.filters = append(group.filters, entry)
		}
	}
	if raw, ok := object["groups"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok {
			ruleWriteFail("%s.groups: must be a JSON array.", path)
		}
		for index, item := range items {
			group.groups = append(group.groups, ruleWriteFilterGroupAt(item, fmt.Sprintf("%s.groups[%d]", path, index)))
		}
	}
	if raw, ok := object["backwardReferenceFilters"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok {
			ruleWriteFail("%s.backwardReferenceFilters: must be a JSON array.", path)
		}
		for index, item := range items {
			itemPath := fmt.Sprintf("%s.backwardReferenceFilters[%d]", path, index)
			reference, ok := item.(map[string]any)
			if !ok {
				ruleWriteFail("%s: must be a JSON object.", itemPath)
			}
			aggregation := ruleWriteFilterOptionalString(reference, "aggregationType")
			var sub *ruleWriteFilterGroup
			if raw, ok := reference["filter"]; ok && raw != nil {
				sub = ruleWriteFilterGroupAt(raw, itemPath+".filter")
			}
			entry := ruleWriteFilterBackward{referenceColumnPath: ruleWriteFilterRequired(reference, "referenceColumnPath", itemPath), aggregationType: aggregation, filter: sub}
			if comparison := ruleWriteFilterOptionalString(reference, "comparisonType"); comparison != nil {
				entry.comparisonType = *comparison
			} else if ruleWriteBlank(aggregation) {
				entry.comparisonType = "EXISTS"
			}
			entry.aggregationColumn = ruleWriteFilterOptionalString(reference, "aggregationColumnPath")
			if raw, ok := reference["aggregationValue"]; ok && raw != nil {
				number, ok := raw.(float64)
				if !ok {
					ruleWriteFail("%s.aggregationValue: must be a number.", itemPath)
				}
				entry.aggregationValue = &number
			}
			group.backward = append(group.backward, entry)
		}
	}
	return group
}

func ruleWriteFilterOptionalString(object map[string]any, name string) *string {
	if text, ok := object[name].(string); ok {
		return &text
	}
	return nil
}

func ruleWriteFilterOptionalInt(object map[string]any, name, path string) *int {
	raw, ok := object[name]
	if !ok || raw == nil {
		return nil
	}
	number, ok := raw.(float64)
	if !ok || number != math.Trunc(number) || number > math.MaxInt32 || number < math.MinInt32 {
		ruleWriteFail("%s.%s: must be an integer.", path, name)
	}
	value := int(number)
	return &value
}

func ruleWriteFilterRequired(object map[string]any, name, path string) string {
	text, ok := object[name].(string)
	if !ok {
		ruleWriteFail("%s.%s: required string property is missing or not a string.", path, name)
	}
	if strings.TrimSpace(text) == "" {
		ruleWriteFail("%s.%s: must not be empty.", path, name)
	}
	return strings.TrimSpace(text)
}

// Catalogs.

type ruleWriteMacros struct {
	name     string
	code     int
	lookup   bool
	argument bool
}

var ruleWriteMacrosCatalog = []ruleWriteMacros{
	{"Yesterday", 3, false, false}, {"Today", 4, false, false}, {"Tomorrow", 5, false, false},
	{"PreviousWeek", 6, false, false}, {"CurrentWeek", 7, false, false}, {"NextWeek", 8, false, false},
	{"PreviousMonth", 9, false, false}, {"CurrentMonth", 10, false, false}, {"NextMonth", 11, false, false},
	{"PreviousQuarter", 12, false, false}, {"CurrentQuarter", 13, false, false}, {"NextQuarter", 14, false, false},
	{"PreviousHalfYear", 15, false, false}, {"CurrentHalfYear", 16, false, false}, {"NextHalfYear", 17, false, false},
	{"PreviousYear", 18, false, false}, {"CurrentYear", 19, false, false}, {"NextYear", 23, false, false},
	{"PreviousHour", 20, false, false}, {"CurrentHour", 21, false, false}, {"NextHour", 22, false, false},
	{"DayOfYearToday", 37, false, false}, {"NextNDays", 24, false, true}, {"PreviousNDays", 25, false, true},
	{"NextNHours", 26, false, true}, {"PreviousNHours", 27, false, true}, {"DayOfYearTodayPlusDaysOffset", 38, false, true},
	{"NextNDaysOfYear", 39, false, true}, {"PreviousNDaysOfYear", 40, false, true},
	{"CurrentUser", 1, true, false}, {"CurrentUserContact", 2, true, false}, {"PrimaryColumn", 34, true, false},
	{"PrimaryDisplayColumn", 35, true, false}, {"PrimaryImageColumn", 36, true, false}, {"PrimaryColorColumn", 41, true, false},
}

func ruleWriteMacrosByName(name string) (ruleWriteMacros, bool) {
	name = strings.TrimSpace(name)
	for _, macros := range ruleWriteMacrosCatalog {
		if strings.EqualFold(macros.name, name) {
			return macros, true
		}
	}
	return ruleWriteMacros{}, false
}

func ruleWriteSortedNames(names []string) string {
	sorted := append([]string{}, names...)
	sort.SliceStable(sorted, func(i, j int) bool { return strings.ToUpper(sorted[i]) < strings.ToUpper(sorted[j]) })
	return strings.Join(sorted, ", ")
}

func ruleWriteMacrosNames() string {
	names := []string{}
	for _, macros := range ruleWriteMacrosCatalog {
		names = append(names, macros.name)
	}
	return ruleWriteSortedNames(names)
}

type ruleWriteDatePart struct {
	name string
	code int
	time bool
}

var ruleWriteDateParts = []ruleWriteDatePart{
	{"Day", 1, false}, {"Week", 2, false}, {"Month", 3, false}, {"Year", 4, false}, {"Weekday", 5, false},
	{"Hour", 6, false}, {"HourMinute", 7, true}, {"Time", 7, true},
}

func ruleWriteDatePartByName(name string) (ruleWriteDatePart, bool) {
	name = strings.TrimSpace(name)
	for _, part := range ruleWriteDateParts {
		if strings.EqualFold(part.name, name) {
			return part, true
		}
	}
	return ruleWriteDatePart{}, false
}

func ruleWriteDatePartNames() string {
	names := []string{}
	for _, part := range ruleWriteDateParts {
		names = append(names, part.name)
	}
	return ruleWriteSortedNames(names)
}

var (
	ruleWriteLeafComparisons     = []string{"EQUAL", "NOT_EQUAL", "IS_NULL", "IS_NOT_NULL", "GREATER", "GREATER_OR_EQUAL", "LESS", "LESS_OR_EQUAL", "CONTAIN", "NOT_CONTAIN", "START_WITH", "NOT_START_WITH", "END_WITH", "NOT_END_WITH"}
	ruleWriteTextComparisons     = []string{"CONTAIN", "NOT_CONTAIN", "START_WITH", "NOT_START_WITH", "END_WITH", "NOT_END_WITH"}
	ruleWriteRelationalFilters   = []string{"GREATER", "GREATER_OR_EQUAL", "LESS", "LESS_OR_EQUAL"}
	ruleWriteAggregationCompares = []string{"EQUAL", "NOT_EQUAL", "GREATER", "GREATER_OR_EQUAL", "LESS", "LESS_OR_EQUAL"}
	ruleWriteBackwardShape       = regexp.MustCompile(`^\[[A-Za-z0-9_]+:[A-Za-z0-9_]+\]$`)
)

func ruleWriteIn(value string, set []string) bool {
	for _, item := range set {
		if item == value {
			return true
		}
	}
	return false
}

func ruleWriteIsUnaryFilter(comparison string) bool {
	return comparison == "IS_NULL" || comparison == "IS_NOT_NULL"
}

// Structural validation.

func ruleWriteValidateFilterStructure(group *ruleWriteFilterGroup, path string) {
	if !strings.EqualFold(group.logicalOperation, "AND") && !strings.EqualFold(group.logicalOperation, "OR") {
		ruleWriteFail("%s.logicalOperation: must be 'AND' or 'OR' (got '%s').", path, group.logicalOperation)
	}
	for index, leaf := range group.filters {
		ruleWriteValidateLeafStructure(leaf, fmt.Sprintf("%s.filters[%d]", path, index))
	}
	for index, nested := range group.groups {
		ruleWriteValidateFilterStructure(nested, fmt.Sprintf("%s.groups[%d]", path, index))
	}
	for index, reference := range group.backward {
		ruleWriteValidateBackwardStructure(reference, fmt.Sprintf("%s.backwardReferenceFilters[%d]", path, index))
	}
}

func ruleWriteValidateLeafStructure(leaf ruleWriteFilterLeaf, path string) {
	if strings.TrimSpace(leaf.columnPath) == "" {
		ruleWriteFail("%s.columnPath: required.", path)
	}
	comparison := strings.ToUpper(leaf.comparisonType)
	if !ruleWriteIn(comparison, ruleWriteLeafComparisons) {
		ruleWriteFail("%s.comparisonType: unsupported value '%s'. Supported: EQUAL, NOT_EQUAL, IS_NULL, IS_NOT_NULL, GREATER, GREATER_OR_EQUAL, LESS, LESS_OR_EQUAL, CONTAIN, NOT_CONTAIN, START_WITH, NOT_START_WITH, END_WITH, NOT_END_WITH.", path, leaf.comparisonType)
	}
	hasMacros := !ruleWriteBlank(leaf.valueMacros)
	hasDatePart := !ruleWriteBlank(leaf.datePart)
	if ruleWriteIsUnaryFilter(comparison) {
		if leaf.hasValue || hasMacros || hasDatePart {
			ruleWriteFail("%s: value, valueMacros and datePart must be omitted when comparisonType is '%s'.", path, comparison)
		}
		return
	}
	if hasDatePart {
		if hasMacros {
			ruleWriteFail("%s: datePart and valueMacros are mutually exclusive.", path)
		}
		if !leaf.hasValue {
			ruleWriteFail("%s.value: required when datePart is set (the part is compared to a constant).", path)
		}
		if ruleWriteIn(comparison, ruleWriteTextComparisons) {
			ruleWriteFail("%s: datePart supports only equality/relational comparisons (EQUAL, NOT_EQUAL, GREATER, GREATER_OR_EQUAL, LESS, LESS_OR_EQUAL); got '%s'.", path, comparison)
		}
		part, ok := ruleWriteDatePartByName(*leaf.datePart)
		if !ok {
			ruleWriteFail("%s.datePart: unknown date part '%s'. Supported: %s.", path, *leaf.datePart, ruleWriteDatePartNames())
		}
		if _, isArray := leaf.value.([]any); isArray {
			ruleWriteFail("%s.value: array values are not supported with datePart.", path)
		}
		if !part.time {
			if _, ok := leaf.value.(float64); !ok {
				ruleWriteFail("%s.value: datePart '%s' expects a JSON integer (e.g. 2021 for Year, 14 for Day, 11 for Hour).", path, *leaf.datePart)
			}
		} else if _, ok := leaf.value.(string); !ok {
			ruleWriteFail("%s.value: datePart '%s' expects a JSON time-of-day string (e.g. \"09:30\" or \"09:30:00\").", path, *leaf.datePart)
		}
		return
	}
	if leaf.hasValue && hasMacros {
		ruleWriteFail("%s: provide either value or valueMacros, not both.", path)
	}
	if !leaf.hasValue && !hasMacros {
		ruleWriteFail("%s: value or valueMacros is required when comparisonType is '%s'.", path, comparison)
	}
	if hasMacros {
		macros, ok := ruleWriteMacrosByName(*leaf.valueMacros)
		if !ok {
			ruleWriteFail("%s.valueMacros: unknown macros '%s'. Supported: %s.", path, *leaf.valueMacros, ruleWriteMacrosNames())
		}
		if ruleWriteIn(comparison, ruleWriteTextComparisons) {
			ruleWriteFail("%s: valueMacros is not supported with text comparison '%s'.", path, comparison)
		}
		if macros.argument && leaf.macrosArgument == nil {
			ruleWriteFail("%s.valueMacrosArgument: required (positive integer) for macros '%s'.", path, *leaf.valueMacros)
		}
		if !macros.argument && leaf.macrosArgument != nil {
			ruleWriteFail("%s.valueMacrosArgument: must be omitted for macros '%s'.", path, *leaf.valueMacros)
		}
		if leaf.macrosArgument != nil && *leaf.macrosArgument <= 0 {
			ruleWriteFail("%s.valueMacrosArgument: must be a positive integer.", path)
		}
		return
	}
	if items, ok := leaf.value.([]any); ok {
		if comparison != "EQUAL" && comparison != "NOT_EQUAL" {
			ruleWriteFail("%s.value: array values are only supported when comparisonType is EQUAL or NOT_EQUAL (multi-value IN on Lookup).", path)
		}
		for index, item := range items {
			if _, ok := item.(string); !ok {
				ruleWriteFail("%s.value[%d]: must be a JSON string (GUID or display name).", path, index)
			}
		}
		if len(items) == 0 {
			ruleWriteFail("%s.value: array must contain at least one element.", path)
		}
	}
}

func ruleWriteValidateBackwardStructure(reference ruleWriteFilterBackward, path string) {
	if strings.TrimSpace(reference.referenceColumnPath) == "" {
		ruleWriteFail("%s.referenceColumnPath: required.", path)
	}
	if !ruleWriteBackwardShape.MatchString(reference.referenceColumnPath) {
		ruleWriteFail("%s.referenceColumnPath: must use shape '[Schema:Column]' (got '%s').", path, reference.referenceColumnPath)
	}
	if ruleWriteBlank(reference.aggregationType) {
		comparison := strings.ToUpper(reference.comparisonType)
		if comparison != "EXISTS" && comparison != "NOT_EXISTS" {
			ruleWriteFail("%s.comparisonType: backward references without aggregationType support only EXISTS or NOT_EXISTS (got '%s').", path, reference.comparisonType)
		}
		if reference.aggregationColumn != nil || reference.aggregationValue != nil {
			ruleWriteFail("%s: aggregationColumnPath and aggregationValue are only allowed when aggregationType is set.", path)
		}
	} else {
		aggregation := strings.ToUpper(*reference.aggregationType)
		if !ruleWriteIn(aggregation, []string{"COUNT", "SUM", "AVG", "MIN", "MAX"}) {
			ruleWriteFail("%s.aggregationType: unsupported value '%s'. Supported: COUNT, SUM, AVG, MIN, MAX.", path, *reference.aggregationType)
		}
		comparison := strings.ToUpper(reference.comparisonType)
		if !ruleWriteIn(comparison, ruleWriteAggregationCompares) {
			ruleWriteFail("%s.comparisonType: aggregation backward references require a relational/equality token (EQUAL, NOT_EQUAL, GREATER, GREATER_OR_EQUAL, LESS, LESS_OR_EQUAL); got '%s'.", path, reference.comparisonType)
		}
		if reference.aggregationValue == nil {
			ruleWriteFail("%s.aggregationValue: required number when aggregationType is set (e.g. COUNT GREATER 10).", path)
		}
		scalar := aggregation != "COUNT"
		if scalar && ruleWriteBlank(reference.aggregationColumn) {
			ruleWriteFail("%s.aggregationColumnPath: required for %s (the numeric child column to aggregate).", path, aggregation)
		}
		if !scalar && !ruleWriteBlank(reference.aggregationColumn) {
			ruleWriteFail("%s.aggregationColumnPath: must be omitted for COUNT.", path)
		}
	}
	if reference.filter != nil {
		ruleWriteValidateFilterStructure(reference.filter, path+".filter")
	}
}

// Schema-aware validation.

type ruleWriteFilterColumn struct {
	name            string
	dataValueType   string
	code            int
	referenceSchema *string
}

// filterColumns is FilterSchemaProvider.GetColumns: every column typed, so an unsupported type fails the
// whole schema as clio's BuildColumns does.
func (s *ruleWriteScope) filterColumns(schemaName string) (map[string]ruleWriteFilterColumn, []string) {
	schema := s.cache.get(schemaName)
	columns := map[string]ruleWriteFilterColumn{}
	for _, name := range schema.columnOrder {
		column := schema.columns[name]
		code := 0
		if column.DataValueType != nil {
			code = *column.DataValueType
		}
		columns[name] = ruleWriteFilterColumn{name: name, dataValueType: ruleWriteColumnType(column), code: code, referenceSchema: column.ReferenceSchema}
	}
	return columns, schema.columnOrder
}

func (s *ruleWriteScope) resolveFilterColumn(columnPath, root, path string) ruleWriteFilterColumn {
	segments := strings.Split(columnPath, ".")
	current := root
	var column ruleWriteFilterColumn
	for index, segment := range segments {
		columns, order := s.filterColumns(current)
		found, ok := columns[segment]
		if !ok {
			ruleWriteFail("filter.path-unknown: Column '%s' not found on schema '%s' (looked up by Name). Available names: %s. (path=%s)", segment, current, ruleWriteSortedNames(order), path)
		}
		column = found
		if index < len(segments)-1 {
			if !strings.EqualFold(column.dataValueType, "Lookup") || column.referenceSchema == nil || *column.referenceSchema == "" {
				ruleWriteFail("%s: segment '%s' on schema '%s' is not a Lookup; forward-path traversal requires Lookup columns.", path, segment, current)
			}
			current = *column.referenceSchema
		}
	}
	return column
}

func ruleWriteIsDateColumn(name string) bool {
	return name == "Date" || name == "DateTime" || name == "Time"
}

func (s *ruleWriteScope) validateFilterSchema(group *ruleWriteFilterGroup, root, path string) {
	for index, leaf := range group.filters {
		s.validateFilterLeafSchema(leaf, root, fmt.Sprintf("%s.filters[%d]", path, index))
	}
	for index, nested := range group.groups {
		s.validateFilterSchema(nested, root, fmt.Sprintf("%s.groups[%d]", path, index))
	}
	for index, reference := range group.backward {
		itemPath := fmt.Sprintf("%s.backwardReferenceFilters[%d]", path, index)
		child, childColumn := ruleWriteParseBackward(reference.referenceColumnPath)
		columns, _ := s.filterColumns(child)
		link, ok := columns[childColumn]
		if !ok {
			ruleWriteFail("%s.referenceColumnPath: column '%s' not found on child schema '%s'.", itemPath, childColumn, child)
		}
		if !strings.EqualFold(link.dataValueType, "Lookup") || !strings.EqualFold(ruleWriteText(link.referenceSchema), root) {
			ruleWriteFail("%s.referenceColumnPath: column '%s' on '%s' must be a Lookup pointing back to root schema '%s'.", itemPath, childColumn, child, root)
		}
		if !ruleWriteBlank(reference.aggregationType) && ruleWriteIn(strings.ToUpper(*reference.aggregationType), []string{"SUM", "AVG", "MIN", "MAX"}) {
			column := s.resolveFilterColumn(ruleWriteText(reference.aggregationColumn), child, itemPath+".aggregationColumnPath")
			if !ruleWriteIsNumeric(column.dataValueType) {
				ruleWriteFail("%s.aggregationColumnPath: column '%s' on '%s' is %s; %s requires a numeric column.", itemPath, ruleWriteText(reference.aggregationColumn), child, column.dataValueType, strings.ToUpper(*reference.aggregationType))
			}
		}
		if reference.filter != nil {
			s.validateFilterSchema(reference.filter, child, itemPath+".filter")
		}
	}
}

func (s *ruleWriteScope) validateFilterLeafSchema(leaf ruleWriteFilterLeaf, root, path string) {
	column := s.resolveFilterColumn(leaf.columnPath, root, path+".columnPath")
	comparison := strings.ToUpper(leaf.comparisonType)
	if !ruleWriteIsFilterable(column.dataValueType) {
		ruleWriteFail("%s.columnPath: column '%s' is %s, which cannot be used as a filter value.", path, leaf.columnPath, column.dataValueType)
	}
	if ruleWriteIsUnaryFilter(comparison) {
		return
	}
	if ruleWriteIn(comparison, ruleWriteRelationalFilters) && !ruleWriteIsRelational(column.dataValueType) {
		ruleWriteFail("%s.comparisonType: '%s' is supported only on numeric and date/time columns. Column '%s' is %s.", path, comparison, leaf.columnPath, column.dataValueType)
	}
	if ruleWriteIn(comparison, ruleWriteTextComparisons) && !ruleWriteIsText(column.dataValueType) {
		ruleWriteFail("%s.comparisonType: '%s' is supported only on text columns. Column '%s' is %s.", path, comparison, leaf.columnPath, column.dataValueType)
	}
	if !ruleWriteBlank(leaf.datePart) {
		if !ruleWriteIsDateColumn(column.dataValueType) {
			ruleWriteFail("%s.datePart: '%s' applies only to Date/DateTime/Time columns. Column '%s' is %s.", path, *leaf.datePart, leaf.columnPath, column.dataValueType)
		}
		return
	}
	if !ruleWriteBlank(leaf.valueMacros) {
		macros, _ := ruleWriteMacrosByName(*leaf.valueMacros)
		isLookup := column.dataValueType == "Lookup" || column.dataValueType == "Guid"
		if !macros.lookup && !ruleWriteIsDateColumn(column.dataValueType) {
			ruleWriteFail("%s.valueMacros: '%s' applies only to Date/DateTime/Time columns. Column '%s' is %s.", path, *leaf.valueMacros, leaf.columnPath, column.dataValueType)
		}
		if macros.lookup && !isLookup {
			ruleWriteFail("%s.valueMacros: '%s' applies only to Lookup columns. Column '%s' is %s.", path, *leaf.valueMacros, leaf.columnPath, column.dataValueType)
		}
		return
	}
	if _, ok := leaf.value.([]any); ok {
		if !strings.EqualFold(column.dataValueType, "Lookup") {
			ruleWriteFail("%s.value: array (multi-value IN) is supported only on Lookup columns. Column '%s' is %s.", path, leaf.columnPath, column.dataValueType)
		}
		return
	}
	switch column.dataValueType {
	case "Boolean":
		if _, ok := leaf.value.(bool); !ok {
			ruleWriteFail("%s.value: must be a JSON boolean when column '%s' is Boolean.", path, leaf.columnPath)
		}
		return
	case "Lookup", "Guid":
		if _, ok := leaf.value.(string); !ok {
			ruleWriteFail("%s.value: must be a JSON string (GUID or display name) when column '%s' is %s.", path, leaf.columnPath, column.dataValueType)
		}
		return
	}
	if ruleWriteIsText(column.dataValueType) {
		if _, ok := leaf.value.(string); !ok {
			ruleWriteFail("%s.value: must be a JSON string when column '%s' is a text type.", path, leaf.columnPath)
		}
	}
	if ruleWriteIsNumeric(column.dataValueType) {
		if _, ok := leaf.value.(float64); !ok {
			ruleWriteFail("%s.value: must be a JSON number when column '%s' is a numeric type.", path, leaf.columnPath)
		}
	}
	if ruleWriteIsDateTime(column.dataValueType) {
		if _, ok := leaf.value.(string); !ok {
			ruleWriteFail("%s.value: must be a JSON string (ISO 8601) when column '%s' is a date/time type.", path, leaf.columnPath)
		}
	}
}

func ruleWriteParseBackward(reference string) (string, string) {
	inner := strings.Trim(reference, "[]")
	parts := strings.Split(inner, ":")
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// ESQ envelope (SimpleToFullFilterConverter.Build), serialized compactly.

var ruleWriteStrictGUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *ruleWriteScope) buildFilterEnvelope(group *ruleWriteFilterGroup, root string) string {
	envelope := ruleWriteObject(
		"rootSchemaName", root, "filterType", 6, "logicalOperation", ruleWriteEsqLogical(group.logicalOperation),
		"isEnabled", true, "items", s.filterItems(group, root, 0), "key", "", "className", "Terrasoft.FilterGroup")
	return ruleWriteSTJ(envelope, false)
}

func ruleWriteEsqLogical(operation string) int {
	if strings.EqualFold(operation, "OR") {
		return 1
	}
	return 0
}

func ruleWriteNestedGroup(root *string, logical int, items *orderedObject) *orderedObject {
	return ruleWriteObject("rootSchemaName", root, "filterType", 6, "logicalOperation", logical, "isEnabled", true,
		"items", items, "key", "", "className", "Terrasoft.FilterGroup")
}

func (s *ruleWriteScope) filterItems(group *ruleWriteFilterGroup, schema string, base int) *orderedObject {
	items := newOrdered()
	for index, leaf := range group.filters {
		items.set(fmt.Sprintf("Filter_%d", base+index), s.filterLeaf(leaf, schema))
	}
	for index, nested := range group.groups {
		items.set(fmt.Sprintf("Group_%d", base+index), ruleWriteNestedGroup(nil, ruleWriteEsqLogical(nested.logicalOperation),
			s.filterItems(nested, schema, (base+index+1)*100)))
	}
	for index, reference := range group.backward {
		items.set(fmt.Sprintf("BackwardReferenceFilter_%d", base+index), s.filterBackward(reference, (base+index+1)*100))
	}
	return items
}

func ruleWriteColumnExpression(path string) *orderedObject {
	return ruleWriteObject("expressionType", 0, "columnPath", path, "className", "Terrasoft.ColumnExpression")
}

func ruleWriteParameterExpression(dataValueType int, dateValue *string, value any) *orderedObject {
	return ruleWriteObject("expressionType", 2,
		"parameter", ruleWriteParameter(dataValueType, dateValue, value), "className", "Terrasoft.ParameterExpression")
}

// ruleWriteParameter keeps "value": null when the value is null (EsqParameterDto.Value has no ignore rule
// of its own, but the serializer's WhenWritingNull drops it, as here).
func ruleWriteParameter(dataValueType int, dateValue *string, value any) *orderedObject {
	return ruleWriteObject("dataValueType", dataValueType, "dateValue", dateValue, "value", value, "className", "Terrasoft.Parameter")
}

var ruleWriteEsqComparisons = map[string]int{
	"EQUAL": 3, "NOT_EQUAL": 4, "LESS": 5, "LESS_OR_EQUAL": 6, "GREATER": 7, "GREATER_OR_EQUAL": 8,
	"START_WITH": 9, "NOT_START_WITH": 10, "CONTAIN": 11, "NOT_CONTAIN": 12, "END_WITH": 13, "NOT_END_WITH": 14,
}

func ruleWriteEsqComparison(comparison string) int {
	value, ok := ruleWriteEsqComparisons[comparison]
	if !ok {
		ruleWriteFail("Unsupported comparison '%s'.", comparison)
	}
	return value
}

func (s *ruleWriteScope) filterLeaf(leaf ruleWriteFilterLeaf, schema string) *orderedObject {
	column := s.resolveFilterColumn(leaf.columnPath, schema, "filter")
	columnPath := leaf.columnPath
	if strings.HasSuffix(columnPath, ".Id") && strings.EqualFold(column.dataValueType, "Guid") {
		columnPath = columnPath[:len(columnPath)-3]
	}
	comparison := strings.ToUpper(leaf.comparisonType)
	if ruleWriteIsUnaryFilter(comparison) {
		isNull := comparison == "IS_NULL"
		code := 2
		if isNull {
			code = 1
		}
		return ruleWriteObject("filterType", 2, "comparisonType", code, "isNull", isNull, "isEnabled", true,
			"leftExpression", ruleWriteColumnExpression(columnPath), "key", "", "className", "Terrasoft.IsNullFilter")
	}
	var dataValueType *int
	if column.code > 0 {
		code := column.code
		dataValueType = &code
	}
	if !ruleWriteBlank(leaf.datePart) {
		part, _ := ruleWriteDatePartByName(*leaf.datePart)
		left := ruleWriteObject("expressionType", 1, "functionType", 3, "datePartType", part.code,
			"functionArgument", ruleWriteColumnExpression(columnPath), "className", "Terrasoft.FunctionExpression")
		if part.time {
			text, _ := leaf.value.(string)
			clock := ruleWriteTimeOfDay(text)
			now := time.Now()
			if s.now != nil {
				now = s.now()
			}
			local := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(clock)
			localISO := local.Format("2006-01-02T15:04:05.000")
			utcISO := local.UTC().Format("2006-01-02T15:04:05.000") + "Z"
			return ruleWriteObject("filterType", 1, "comparisonType", ruleWriteEsqComparison(comparison), "isEnabled", true,
				"trimDateTimeParameterToDate", true, "leftExpression", left, "isAggregative", false, "dataValueType", dataValueType,
				"rightExpression", ruleWriteParameterExpression(9, &utcISO, "\""+localISO+"\""),
				"key", "", "className", "Terrasoft.CompareFilter")
		}
		number, _ := leaf.value.(float64)
		if number != math.Trunc(number) {
			ruleWriteFail("The JSON value is not in a supported Int64 format.")
		}
		return ruleWriteObject("filterType", 1, "comparisonType", ruleWriteEsqComparison(comparison), "isEnabled", true,
			"leftExpression", left, "rightExpression", ruleWriteParameterExpression(4, nil, int64(number)),
			"key", "", "className", "Terrasoft.CompareFilter")
	}
	if !ruleWriteBlank(leaf.valueMacros) {
		macros, _ := ruleWriteMacrosByName(*leaf.valueMacros)
		var argument *orderedObject
		if macros.argument {
			argument = ruleWriteParameterExpression(4, nil, *leaf.macrosArgument)
		}
		var trim *bool
		if !macros.lookup && ruleWriteIsDateColumn(column.dataValueType) {
			value := true
			trim = &value
		}
		right := ruleWriteObject("expressionType", 1, "functionType", 1, "macrosType", macros.code,
			"functionArgument", argument, "className", "Terrasoft.FunctionExpression")
		return ruleWriteObject("filterType", 1, "comparisonType", ruleWriteEsqComparison(comparison), "isEnabled", true,
			"trimDateTimeParameterToDate", trim, "leftExpression", ruleWriteColumnExpression(columnPath), "isAggregative", false,
			"dataValueType", dataValueType, "rightExpression", right, "key", "", "className", "Terrasoft.CompareFilter")
	}
	if strings.EqualFold(column.dataValueType, "Lookup") {
		return s.filterLookupIn(comparison, columnPath, column, leaf.value)
	}
	parameterType := ruleWriteParameterType(column.dataValueType)
	value := ruleWriteScalarValue(leaf.value, parameterType, column.dataValueType)
	return ruleWriteObject("filterType", 1, "comparisonType", ruleWriteEsqComparison(comparison), "isEnabled", true,
		"leftExpression", ruleWriteColumnExpression(columnPath),
		"rightExpression", ruleWriteParameterExpression(parameterType, nil, value), "key", "", "className", "Terrasoft.CompareFilter")
}

func ruleWriteParameterType(name string) int {
	switch name {
	case "Boolean":
		return 12
	case "Integer":
		return 4
	case "Float", "Money", "Money0", "Money1", "Money3", "Float0", "Float1", "Float2", "Float3", "Float4", "Float8":
		return 5
	case "DateTime":
		return 7
	case "Date":
		return 8
	case "Time":
		return 9
	case "Guid":
		return 0
	case "Lookup":
		return 10
	}
	return 28
}

func ruleWriteScalarValue(value any, parameterType int, columnType string) any {
	switch parameterType {
	case 12:
		flag, ok := value.(bool)
		if !ok {
			ruleWriteFail("The requested operation requires an element of type 'True', but the target element has type '%s'.", ruleWriteKindName(value))
		}
		return flag
	case 4:
		number, _ := value.(float64)
		if number != math.Trunc(number) {
			ruleWriteFail("The JSON value is not in a supported Int64 format.")
		}
		return int64(number)
	case 5:
		number, _ := value.(float64)
		return json.Number(ruleWriteNumberText(number))
	case 0, 7, 8, 9:
		text, _ := value.(string)
		return text
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return ruleWriteNumberText(typed)
	case bool:
		return typed
	}
	ruleWriteFail("Unsupported JSON value kind '%s' for column type '%s'.", ruleWriteKindName(value), columnType)
	return nil
}

func ruleWriteKindName(value any) string {
	switch value.(type) {
	case nil:
		return "Null"
	case string:
		return "String"
	case float64:
		return "Number"
	case bool:
		if value.(bool) {
			return "True"
		}
		return "False"
	case []any:
		return "Array"
	}
	return "Object"
}

func (s *ruleWriteScope) filterLookupIn(comparison, columnPath string, column ruleWriteFilterColumn, value any) *orderedObject {
	parameters := []any{}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			text, _ := item.(string)
			parameters = append(parameters, s.lookupParameter(text, column))
		}
	case string:
		parameters = append(parameters, s.lookupParameter(typed, column))
	default:
		ruleWriteFail("filter: Lookup column '%s' expects string or array of strings.", columnPath)
	}
	code := 3
	switch comparison {
	case "EQUAL":
	case "NOT_EQUAL":
		code = 4
	default:
		ruleWriteFail("InFilter on Lookup supports only EQUAL or NOT_EQUAL (got '%s').", comparison)
	}
	return ruleWriteObject("filterType", 4, "comparisonType", code, "isEnabled", true, "trimDateTimeParameterToDate", false,
		"leftExpression", ruleWriteColumnExpression(columnPath), "isAggregative", false, "dataValueType", 10,
		"referenceSchemaName", column.referenceSchema, "rightExpressions", parameters, "key", "", "className", "Terrasoft.InFilter")
}

func (s *ruleWriteScope) lookupParameter(raw string, column ruleWriteFilterColumn) *orderedObject {
	id := s.lookupID(raw, column)
	var display *string
	if !ruleWriteStrictGUID.MatchString(raw) {
		display = &raw
	}
	if display == nil && !ruleWriteBlank(column.referenceSchema) {
		display = s.lookupDisplayName(*column.referenceSchema, id)
	}
	if display == nil || *display == "" {
		ruleWriteFail("filter: could not resolve the display name for Lookup value '%s' on schema '%s'. A Lookup filter value must carry Name/displayValue or the Freedom UI lookup control fails to render it. Pass the lookup's display name instead of a raw GUID (clio resolves the Id and keeps the name), or verify the GUID exists in that schema.", id, ruleWriteText(column.referenceSchema))
	}
	return ruleWriteParameterExpression(10, nil, ruleWriteObject("Name", display, "Id", id, "value", id, "displayValue", display))
}

func (s *ruleWriteScope) lookupID(raw string, column ruleWriteFilterColumn) string {
	if ruleWriteStrictGUID.MatchString(raw) {
		return strings.ToLower(raw)
	}
	if ruleWriteBlank(column.referenceSchema) {
		ruleWriteFail("filter: Lookup column reference schema is missing; cannot resolve display name.")
	}
	schema := *column.referenceSchema
	key := [2]string{schema, raw}
	if id, ok := s.lookupIDs[key]; ok {
		return id
	}
	primary := s.cache.get(schema).PrimaryDisplay
	if ruleWriteBlank(primary) {
		ruleWriteFail("filter: cannot resolve display name '%s' on schema '%s' — primary display column is not defined.", raw, schema)
	}
	rows, err := s.client.ruleWriteSelect(s.ctx, ruleWriteSelectQuery(schema, [][2]string{{"Id", "Id"}}, *primary, raw, 1, 10000), 45*time.Second, true)
	if err != nil {
		ruleWriteFail("%s", err.Error())
	}
	if len(rows) == 0 {
		ruleWriteFail("filter: lookup value '%s' was not found on schema '%s' (column '%s'). The match is exact against the stored display value — a localized or differently-spelled term (e.g. a non-English prompt) will not match. Use odata-read or execute-esq on '%s' to find the actual stored value or its Id, then pass that exact value or the GUID.", raw, schema, *primary, schema)
	}
	if len(rows) > 1 {
		ruleWriteFail("filter: lookup value '%s' is ambiguous on schema '%s' (%d matches found). Use odata-read or execute-esq on '%s' to pick the intended record and pass its GUID.", raw, schema, len(rows), schema)
	}
	id := ruleWriteParseGUID(rowText(rows[0], "Id"))
	if id == "" {
		ruleWriteFail("filter: lookup value '%s' on schema '%s' returned a non-GUID Id.", raw, schema)
	}
	s.lookupIDs[key] = id
	return id
}

// lookupDisplayName is TryResolveDisplayNameById: best effort, nil when the record or its display value
// cannot be read.
func (s *ruleWriteScope) lookupDisplayName(schema, id string) *string {
	key := [2]string{schema, id}
	if name, ok := s.lookupNames[key]; ok {
		return name
	}
	primary := s.cache.get(schema).PrimaryDisplay
	if ruleWriteBlank(primary) {
		s.lookupNames[key] = nil
		return nil
	}
	rows, err := s.client.ruleWriteSelect(s.ctx, ruleWriteSelectQuery(schema, [][2]string{{"Display", *primary}}, "Id", id, 0, 10000), 45*time.Second, true)
	if err != nil || len(rows) == 0 {
		s.lookupNames[key] = nil
		return nil
	}
	raw, present := rows[0]["Display"]
	if !present || string(raw) == "null" {
		s.lookupNames[key] = nil
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || text == "" {
		s.lookupNames[key] = nil
		return nil
	}
	s.lookupNames[key] = &text
	return &text
}

func (s *ruleWriteScope) filterBackward(reference ruleWriteFilterBackward, base int) *orderedObject {
	child, _ := ruleWriteParseBackward(reference.referenceColumnPath)
	sub := reference.filter
	if sub == nil {
		sub = &ruleWriteFilterGroup{logicalOperation: "AND"}
	}
	subFilters := ruleWriteNestedGroup(&child, ruleWriteEsqLogical(sub.logicalOperation), s.filterItems(sub, child, base))
	if !ruleWriteBlank(reference.aggregationType) {
		aggregation := strings.ToUpper(*reference.aggregationType)
		codes := map[string]int{"COUNT": 1, "SUM": 2, "AVG": 3, "MIN": 4, "MAX": 5}
		code, ok := codes[aggregation]
		if !ok {
			ruleWriteFail("Unsupported aggregation '%s'.", *reference.aggregationType)
		}
		column := reference.referenceColumnPath + ".Id"
		valueType := 4
		var value any = int64(*reference.aggregationValue)
		if code != 1 {
			column = reference.referenceColumnPath + "." + ruleWriteText(reference.aggregationColumn)
			valueType = 5
			value = json.Number(strconv.FormatFloat(*reference.aggregationValue, 'f', -1, 64))
		}
		left := ruleWriteObject("expressionType", 3, "functionType", 2, "aggregationType", code, "columnPath", column,
			"subFilters", subFilters, "className", "Terrasoft.AggregationQueryExpression")
		return ruleWriteObject("filterType", 1, "comparisonType", ruleWriteEsqComparison(strings.ToUpper(reference.comparisonType)),
			"isEnabled", true, "isAggregative", true, "leftExpression", left,
			"rightExpression", ruleWriteParameterExpression(valueType, nil, value), "subFilters", subFilters,
			"key", "", "className", "Terrasoft.CompareFilter")
	}
	code := 16
	if strings.EqualFold(reference.comparisonType, "EXISTS") {
		code = 15
	}
	return ruleWriteObject("filterType", 5, "comparisonType", code, "isEnabled", true, "trimDateTimeParameterToDate", false,
		"leftExpression", ruleWriteColumnExpression(reference.referenceColumnPath+".Id"), "isAggregative", true,
		"dataValueType", 4, "subFilters", subFilters, "key", "", "className", "Terrasoft.ExistsFilter")
}

// ruleWriteTimeOfDay is ParseTimeOfDay for the HH:mm[:ss[.fff]] forms the contract documents.
func ruleWriteTimeOfDay(raw string) time.Duration {
	text := strings.TrimSpace(raw)
	for _, layout := range []string{"15:04", "15:04:05", "15:04:05.999999999", "3:04", "3:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute +
				time.Duration(parsed.Second())*time.Second + time.Duration(parsed.Nanosecond())
		}
	}
	ruleWriteFail("filter: datePart HourMinute value '%s' is not a valid time of day (use \"HH:mm\" or \"HH:mm:ss\").", raw)
	return 0
}
