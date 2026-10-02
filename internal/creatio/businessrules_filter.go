package creatio

import (
	"fmt"
	"strings"
	"time"
)

// decompileStaticFilter turns the persisted ESQ filter envelope of an apply-static-filter action back
// into the friendly filter shape clio's create contract accepts (clio FullToSimpleFilterConverter).
func decompileStaticFilter(envelopeJSON string) (*orderedObject, error) {
	if strings.TrimSpace(envelopeJSON) == "" {
		return nil, fmt.Errorf("apply-static-filter has no persisted ESQ envelope.")
	}
	parsed, err := parseOrderedJSON([]byte(envelopeJSON))
	if err != nil {
		return nil, fmt.Errorf("apply-static-filter ESQ envelope is not valid JSON: %v", err)
	}
	envelope, ok := parsed.(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("apply-static-filter ESQ envelope root must be a JSON object.")
	}
	return decompileFilterGroup(envelope)
}

// ESQ wire enums used by the decompiler (clio EsqFilterEnums).
const (
	esqCompareFilter = 1
	esqIsNullFilter  = 2
	esqInFilter      = 4
	esqExistsFilter  = 5
	esqFilterGroup   = 6

	esqExpressionSchemaColumn = 0
	esqExpressionFunction     = 1
	esqExpressionParameter    = 2
	esqExpressionSubQuery     = 3

	esqComparisonIsNull = 1
	esqComparisonEqual  = 3
	esqComparisonExists = 15
	esqLogicalOr        = 1
)

var esqLeafComparisons = map[int]string{
	3: "EQUAL", 4: "NOT_EQUAL", 5: "LESS", 6: "LESS_OR_EQUAL", 7: "GREATER", 8: "GREATER_OR_EQUAL",
	9: "START_WITH", 10: "NOT_START_WITH", 11: "CONTAIN", 12: "NOT_CONTAIN", 13: "END_WITH", 14: "NOT_END_WITH",
}

var esqAggregationTypes = map[int]string{1: "COUNT", 2: "SUM", 3: "AVG", 4: "MIN", 5: "MAX"}

// esqDateParts maps datePartType to its friendly name; only HourMinute carries a time-of-day value.
var esqDateParts = map[int]string{1: "Day", 2: "Week", 3: "Month", 4: "Year", 5: "Weekday", 6: "Hour", 7: "HourMinute"}

type esqMacros struct {
	name             string
	requiresArgument bool
}

var esqMacrosTypes = map[int]esqMacros{
	1: {"CurrentUser", false}, 2: {"CurrentUserContact", false}, 3: {"Yesterday", false}, 4: {"Today", false},
	5: {"Tomorrow", false}, 6: {"PreviousWeek", false}, 7: {"CurrentWeek", false}, 8: {"NextWeek", false},
	9: {"PreviousMonth", false}, 10: {"CurrentMonth", false}, 11: {"NextMonth", false},
	12: {"PreviousQuarter", false}, 13: {"CurrentQuarter", false}, 14: {"NextQuarter", false},
	15: {"PreviousHalfYear", false}, 16: {"CurrentHalfYear", false}, 17: {"NextHalfYear", false},
	18: {"PreviousYear", false}, 19: {"CurrentYear", false}, 20: {"PreviousHour", false},
	21: {"CurrentHour", false}, 22: {"NextHour", false}, 23: {"NextYear", false},
	24: {"NextNDays", true}, 25: {"PreviousNDays", true}, 26: {"NextNHours", true}, 27: {"PreviousNHours", true},
	34: {"PrimaryColumn", false}, 35: {"PrimaryDisplayColumn", false}, 36: {"PrimaryImageColumn", false},
	37: {"DayOfYearToday", false}, 38: {"DayOfYearTodayPlusDaysOffset", true}, 39: {"NextNDaysOfYear", true},
	40: {"PreviousNDaysOfYear", true}, 41: {"PrimaryColorColumn", false},
}

func newOrdered() *orderedObject { return &orderedObject{values: map[string]any{}} }

func (o *orderedObject) set(key string, value any) {
	if _, seen := o.values[key]; !seen {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func decompileFilterGroup(group *orderedObject) (*orderedObject, error) {
	result := newOrdered()
	logical := "AND"
	if jsonInt(group, "logicalOperation", 0) == esqLogicalOr {
		logical = "OR"
	}
	result.set("logicalOperation", logical)
	filters, groups, backward := []any{}, []any{}, []any{}
	if items := jsonObjectAt(group, "items"); items != nil {
		for _, key := range items.keys {
			item, ok := items.values[key].(*orderedObject)
			if !ok {
				return nil, fmt.Errorf("apply-static-filter item '%s' must be a JSON object.", key)
			}
			if err := routeFilterItem(item, &filters, &groups, &backward); err != nil {
				return nil, err
			}
		}
	}
	if len(filters) > 0 {
		result.set("filters", filters)
	}
	if len(groups) > 0 {
		result.set("groups", groups)
	}
	if len(backward) > 0 {
		result.set("backwardReferenceFilters", backward)
	}
	return result, nil
}

func routeFilterItem(item *orderedObject, filters, groups, backward *[]any) error {
	filterType := jsonInt(item, "filterType", -1)
	var (
		leaf *orderedObject
		err  error
	)
	switch filterType {
	case esqFilterGroup:
		leaf, err = decompileFilterGroup(item)
		if err == nil {
			*groups = append(*groups, leaf)
		}
		return err
	case esqIsNullFilter:
		leaf, err = decompileIsNull(item)
	case esqInFilter:
		leaf, err = decompileLookupIn(item)
	case esqExistsFilter:
		leaf, err = decompileExists(item)
		if err == nil {
			*backward = append(*backward, leaf)
		}
		return err
	case esqCompareFilter:
		return routeCompareFilter(item, filters, backward)
	default:
		return fmt.Errorf("apply-static-filter carries an unsupported filterType '%d'.", filterType)
	}
	if err == nil {
		*filters = append(*filters, leaf)
	}
	return err
}

func routeCompareFilter(item *orderedObject, filters, backward *[]any) error {
	left, err := requireFilterObject(item, "leftExpression", "compare filter")
	if err != nil {
		return err
	}
	leftType := jsonInt(left, "expressionType", esqExpressionSchemaColumn)
	if jsonBool(item, "isAggregative", false) || leftType == esqExpressionSubQuery {
		leaf, err := decompileAggregation(item, left)
		if err == nil {
			*backward = append(*backward, leaf)
		}
		return err
	}
	var leaf *orderedObject
	if leftType == esqExpressionFunction {
		leaf, err = decompileDatePart(item, left)
	} else if right := jsonObjectAt(item, "rightExpression"); right != nil &&
		jsonInt(right, "expressionType", esqExpressionParameter) == esqExpressionFunction {
		leaf, err = decompileMacros(item, left, right)
	} else {
		leaf, err = decompileScalarCompare(item, left)
	}
	if err == nil {
		*filters = append(*filters, leaf)
	}
	return err
}

func decompileIsNull(item *orderedObject) (*orderedObject, error) {
	column, err := requireColumnPath(item, "leftExpression")
	if err != nil {
		return nil, err
	}
	comparison := "IS_NOT_NULL"
	if jsonInt(item, "comparisonType", esqComparisonIsNull) == esqComparisonIsNull {
		comparison = "IS_NULL"
	}
	result := newOrdered()
	result.set("columnPath", column)
	result.set("comparisonType", comparison)
	return result, nil
}

func decompileLookupIn(item *orderedObject) (*orderedObject, error) {
	rights, ok := jsonArrayAt(item, "rightExpressions")
	if !ok || len(rights) == 0 {
		return nil, fmt.Errorf("apply-static-filter lookup filter has no rightExpressions.")
	}
	values := make([]string, 0, len(rights))
	for _, right := range rights {
		expression, ok := right.(*orderedObject)
		if !ok {
			return nil, fmt.Errorf("apply-static-filter lookup rightExpression must be a JSON object.")
		}
		value, err := readLookupValue(expression)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	column, err := requireColumnPath(item, "leftExpression")
	if err != nil {
		return nil, err
	}
	comparison := jsonInt(item, "comparisonType", esqComparisonEqual)
	var comparisonName string
	switch comparison {
	case 3:
		comparisonName = "EQUAL"
	case 4:
		comparisonName = "NOT_EQUAL"
	default:
		return nil, fmt.Errorf("apply-static-filter lookup filter carries an unsupported comparisonType '%d'.", comparison)
	}
	result := newOrdered()
	result.set("columnPath", column)
	result.set("comparisonType", comparisonName)
	if len(values) == 1 {
		result.set("value", values[0])
	} else {
		items := make([]any, 0, len(values))
		for _, value := range values {
			items = append(items, value)
		}
		result.set("value", items)
	}
	return result, nil
}

func decompileScalarCompare(item, left *orderedObject) (*orderedObject, error) {
	right, err := requireFilterObject(item, "rightExpression", "compare filter")
	if err != nil {
		return nil, err
	}
	parameter, err := requireFilterObject(right, "parameter", "compare filter parameter")
	if err != nil {
		return nil, err
	}
	column, err := readColumnPath(left)
	if err != nil {
		return nil, err
	}
	comparison, err := mapLeafComparison(jsonInt(item, "comparisonType", -1))
	if err != nil {
		return nil, err
	}
	value, err := clonePropertyValue(parameter, "value")
	if err != nil {
		return nil, err
	}
	result := newOrdered()
	result.set("columnPath", column)
	result.set("comparisonType", comparison)
	result.set("value", value)
	return result, nil
}

func decompileDatePart(item, left *orderedObject) (*orderedObject, error) {
	datePartType := jsonInt(left, "datePartType", -1)
	datePart, ok := esqDateParts[datePartType]
	if !ok {
		return nil, fmt.Errorf("apply-static-filter carries an unsupported datePartType '%d'.", datePartType)
	}
	argument, err := requireFilterObject(left, "functionArgument", "datePart")
	if err != nil {
		return nil, err
	}
	right, err := requireFilterObject(item, "rightExpression", "datePart")
	if err != nil {
		return nil, err
	}
	parameter, err := requireFilterObject(right, "parameter", "datePart parameter")
	if err != nil {
		return nil, err
	}
	column, err := readColumnPath(argument)
	if err != nil {
		return nil, err
	}
	comparison, err := mapLeafComparison(jsonInt(item, "comparisonType", -1))
	if err != nil {
		return nil, err
	}
	var value any
	if datePart == "HourMinute" {
		value, err = readTimeOfDay(parameter)
	} else {
		value, err = clonePropertyValue(parameter, "value")
	}
	if err != nil {
		return nil, err
	}
	result := newOrdered()
	result.set("columnPath", column)
	result.set("comparisonType", comparison)
	result.set("datePart", datePart)
	result.set("value", value)
	return result, nil
}

func decompileMacros(item, left, right *orderedObject) (*orderedObject, error) {
	macrosType := jsonInt(right, "macrosType", -1)
	macros, ok := esqMacrosTypes[macrosType]
	if !ok {
		return nil, fmt.Errorf("apply-static-filter carries an unsupported macrosType '%d'.", macrosType)
	}
	column, err := readColumnPath(left)
	if err != nil {
		return nil, err
	}
	comparison, err := mapLeafComparison(jsonInt(item, "comparisonType", -1))
	if err != nil {
		return nil, err
	}
	result := newOrdered()
	result.set("columnPath", column)
	result.set("comparisonType", comparison)
	result.set("valueMacros", macros.name)
	if macros.requiresArgument {
		argument, err := requireFilterObject(right, "functionArgument", "macros argument")
		if err != nil {
			return nil, err
		}
		parameter, err := requireFilterObject(argument, "parameter", "macros argument parameter")
		if err != nil {
			return nil, err
		}
		value, err := clonePropertyValue(parameter, "value")
		if err != nil {
			return nil, err
		}
		result.set("valueMacrosArgument", value)
	}
	return result, nil
}

func decompileExists(item *orderedObject) (*orderedObject, error) {
	column, err := requireColumnPath(item, "leftExpression")
	if err != nil {
		return nil, err
	}
	comparison := "NOT_EXISTS"
	if jsonInt(item, "comparisonType", esqComparisonExists) == esqComparisonExists {
		comparison = "EXISTS"
	}
	result := newOrdered()
	result.set("referenceColumnPath", strings.TrimSuffix(column, ".Id"))
	result.set("comparisonType", comparison)
	subFilter, err := readSubFilter(item)
	if err != nil {
		return nil, err
	}
	if subFilter != nil {
		result.set("filter", subFilter)
	}
	return result, nil
}

func decompileAggregation(item, left *orderedObject) (*orderedObject, error) {
	aggregationValue := jsonInt(left, "aggregationType", -1)
	aggregation, ok := esqAggregationTypes[aggregationValue]
	if !ok {
		return nil, fmt.Errorf("apply-static-filter carries an unsupported aggregationType '%d'.", aggregationValue)
	}
	aggregated, err := readColumnPath(left)
	if err != nil {
		return nil, err
	}
	closing := strings.Index(aggregated, "]")
	if !strings.HasPrefix(aggregated, "[") || closing < 0 {
		return nil, fmt.Errorf("apply-static-filter aggregation column '%s' is not a backward reference path.", aggregated)
	}
	referencePath := aggregated[:closing+1]
	remainder := strings.TrimLeft(aggregated[closing+1:], ".")
	right, err := requireFilterObject(item, "rightExpression", "aggregation")
	if err != nil {
		return nil, err
	}
	parameter, err := requireFilterObject(right, "parameter", "aggregation parameter")
	if err != nil {
		return nil, err
	}
	comparison, err := mapLeafComparison(jsonInt(item, "comparisonType", -1))
	if err != nil {
		return nil, err
	}
	value, err := clonePropertyValue(parameter, "value")
	if err != nil {
		return nil, err
	}
	result := newOrdered()
	result.set("referenceColumnPath", referencePath)
	result.set("aggregationType", aggregation)
	result.set("comparisonType", comparison)
	result.set("aggregationValue", value)
	if aggregation != "COUNT" && remainder != "" {
		result.set("aggregationColumnPath", remainder)
	}
	subFilter, err := readSubFilter(left)
	if err != nil {
		return nil, err
	}
	if subFilter == nil {
		if subFilter, err = readSubFilter(item); err != nil {
			return nil, err
		}
	}
	if subFilter != nil {
		result.set("filter", subFilter)
	}
	return result, nil
}

func readSubFilter(owner *orderedObject) (*orderedObject, error) {
	subFilters := jsonObjectAt(owner, "subFilters")
	if items := jsonObjectAt(subFilters, "items"); items == nil || items.len() == 0 {
		return nil, nil
	}
	return decompileFilterGroup(subFilters)
}

func readLookupValue(expression *orderedObject) (string, error) {
	value := jsonObjectAt(expression, "parameter").get("value")
	if lookup, ok := value.(*orderedObject); ok {
		display := jsonStringOrNil(lookup, "Name")
		if display == nil {
			display = jsonStringOrNil(lookup, "displayValue")
		}
		id := jsonStringOrNil(lookup, "Id")
		if id == nil {
			id = jsonStringOrNil(lookup, "value")
		}
		resolved := display
		if resolved == nil {
			resolved = id
		}
		if resolved == nil || *resolved == "" {
			return "", fmt.Errorf("apply-static-filter lookup value carries neither a display name nor an Id.")
		}
		return *resolved, nil
	}
	if scalar, ok := value.(string); ok && scalar != "" {
		return scalar, nil
	}
	return "", fmt.Errorf("apply-static-filter lookup value has an unsupported shape.")
}

// readTimeOfDay renders an HourMinute value as HH:mm:ss, as clio does.
func readTimeOfDay(parameter *orderedObject) (string, error) {
	if raw, ok := jsonString(parameter, "value"); ok {
		raw = strings.Trim(strings.TrimSpace(raw), `"`)
		for _, layout := range []string{"15:04:05", "15:04", time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04"} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.Format("15:04:05"), nil
			}
		}
	}
	if dateValue, ok := jsonString(parameter, "dateValue"); ok && strings.TrimSpace(dateValue) != "" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04:05.999"} {
			if parsed, err := time.Parse(layout, strings.TrimSpace(dateValue)); err == nil {
				return parsed.UTC().Format("15:04:05"), nil
			}
		}
	}
	return "", fmt.Errorf("apply-static-filter datePart HourMinute value could not be parsed back to a time of day.")
}

func requireColumnPath(item *orderedObject, property string) (string, error) {
	expression, err := requireFilterObject(item, property, "filter")
	if err != nil {
		return "", err
	}
	return readColumnPath(expression)
}

func readColumnPath(expression *orderedObject) (string, error) {
	column, _ := jsonString(expression, "columnPath")
	if strings.TrimSpace(column) == "" {
		return "", fmt.Errorf("apply-static-filter expression has no columnPath.")
	}
	return column, nil
}

func clonePropertyValue(owner *orderedObject, property string) (any, error) {
	value := owner.get(property)
	if value == nil {
		return nil, fmt.Errorf("apply-static-filter parameter has no '%s'.", property)
	}
	return value, nil
}

func requireFilterObject(owner *orderedObject, property, context string) (*orderedObject, error) {
	if object := jsonObjectAt(owner, property); object != nil {
		return object, nil
	}
	return nil, fmt.Errorf("apply-static-filter %s has no '%s' object.", context, property)
}

func mapLeafComparison(comparison int) (string, error) {
	if name, ok := esqLeafComparisons[comparison]; ok {
		return name, nil
	}
	return "", fmt.Errorf("apply-static-filter carries an unsupported comparisonType '%d'.", comparison)
}
