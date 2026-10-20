package creatio

// clio 8.1.0.142's ExecuteEsqQueryHints: answers an execute-esq call that carries SQL text, or the query
// parts at the top level instead of in the query SelectQuery object, with the shape and - where the mapping
// is trivially safe - the equivalent query ready to resend; and refuses a SelectQuery whose parts Creatio
// would silently drop. Nothing here executes anything.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	esqHintDefaultRowCount = 100
	esqHintMaxRowCount     = 10_000

	esqSQLPrefix = "query holds SQL text, but execute-esq runs a SelectQuery JSON object, never SQL; nothing was executed."

	esqFlatPrefix = "query is missing: the schema, columns and filters belong inside the 'query' SelectQuery object, not at the top " +
		"level; nothing was executed."

	esqReadOnlyMessage = "execute-esq is read-only: it runs a SELECT as a SelectQuery JSON object, and never UPDATE, DELETE, INSERT or any " +
		"other write or DDL statement; nothing was executed. To change data use odata-update, odata-delete or " +
		"odata-create (or the matching data tool); to read, send a SelectQuery in 'query'."
)

var (
	esqSchemaKeys = []string{"schemaname", "rootschemaname", "entity", "entityname", "table", "tablename", "from", "object"}
	esqColumnKeys = []string{"columns", "select", "fields"}
	esqFilterKeys = []string{"filters", "filter", "where"}
	esqLimitKeys  = []string{"rowcount", "top", "limit", "take", "maxrows"}
	esqSQLKeys    = []string{"sql", "sqlquery", "sqltext", "statement"}

	esqQueryPartKeys = esqKeySet(esqSchemaKeys, esqColumnKeys, esqFilterKeys, esqLimitKeys, esqSQLKeys)

	esqSelectQueryMembers = esqKeySet([]string{
		"queryid", "querykind", "columnvalues", "rootschemaname", "operationtype", "includeprocessexecutiondata",
		"forcebackgroundmode", "processexecutionoptions",
		"querytype", "filters", "columns", "allcolumns", "serveresqcacheparameters", "ispageable", "isdistinct",
		"uselocalization", "userecorddeactivation", "rowcount", "conditionalvalues", "ishierarchical",
		"hierarchicalmaxdepth", "hierarchicalcolumnname", "hierarchicalcolumnvalue", "hierarchicalfulldataload",
		"hierarchicalcolumnfiltersvalue", "rowsoffset", "chunksize",
		"queryoptimize", "usemetrics", "adminunitrolesources", "querysource", "ignoredisplayvalues", "type", "$type",
	})

	esqMisplacedPartKeys = esqKeySet(esqSchemaKeys, esqColumnKeys, esqFilterKeys, esqLimitKeys, esqSQLKeys,
		[]string{"orderby", "order", "sort", "sortby", "sorting", "groupby", "having", "join", "joins",
			"offset", "skip", "page", "pagesize", "distinct"})

	esqTranslatableKeys = esqKeySet([]string{"rootschemaname", "operationtype", "allcolumns", "columns", "filters", "rowcount"})

	esqReadShapingStems = []string{"column", "filter", "row", "where", "order", "sort", "limit", "select", "criteri", "condition"}

	esqIdentifier   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	esqGUIDLiteral  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	esqSQLStart     = regexp.MustCompile(`(?i)^\s*(select|with)\b`)
	esqWriteSQL     = regexp.MustCompile(`(?i)^\s*(update|delete|insert|merge|upsert|replace|drop|truncate|alter|create|exec|execute|grant|revoke)\b`)
	esqSelectShape  = regexp.MustCompile(`(?is)^\s*select\s+(?:top\s+(?P<top>\d+)\s+)?(?P<cols>.+?)\s+from\s+(?P<table>\S+?)(?:\s+where\s+(?P<where>.+?))?(?:\s+limit\s+(?P<limit>\d+))?\s*;?\s*$`)
	esqUnsupported  = regexp.MustCompile(`(?i)\b(join|group\s+by|order\s+by|having|union|distinct|offset|fetch|into)\b|[()]|--|/\*|;\s*\S`)
	esqEqualityTerm = regexp.MustCompile(`^\s*\[?(?P<col>[A-Za-z_][A-Za-z0-9_]*)\]?\s*=\s*(?P<val>'(?:[^']|'')*'|-?\d+)\s*$`)
	esqAndSeparator = regexp.MustCompile(`(?i)\s+and\s+`)
	esqDateLiteral  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?$`)
)

func esqKeySet(lists ...[]string) map[string]bool {
	set := map[string]bool{}
	for _, list := range lists {
		for _, key := range list {
			set[key] = true
		}
	}
	return set
}

// esqHint is ExecuteEsqQueryHints.Hint: what was wrong, and the equivalent query when it is safe.
type esqHint struct {
	message   string
	suggested json.RawMessage
}

// esqKey is ExecuteEsqQueryHints.Key: '-' and '_' removed, lower-cased.
func esqKey(name string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(name))
}

// esqFlatEntry is one key of the flattened call, in the order clio meets it.
type esqFlat struct {
	keys   []string
	values map[string]*jnode
}

func (f *esqFlat) set(key string, value *jnode) {
	if _, ok := f.values[key]; !ok {
		f.keys = append(f.keys, key)
	}
	f.values[key] = value
}

func (f *esqFlat) has(key string) bool { _, ok := f.values[key]; return ok }

func (f *esqFlat) first(keys []string) (string, bool) {
	for _, key := range keys {
		if f.has(key) {
			return key, true
		}
	}
	return "", false
}

// esqExtensionFlat normalizes the top-level keys the argument record did not bind. Go maps keep no request
// order, so they are taken in sorted order.
func esqExtensionFlat(extension map[string]json.RawMessage) *esqFlat {
	flat := &esqFlat{values: map[string]*jnode{}}
	keys := make([]string, 0, len(extension))
	for key := range extension {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		node, err := parseJNode(extension[key])
		if err != nil {
			node = jsonNullNode()
		}
		flat.set(esqKey(key), node)
	}
	return flat
}

// esqHintTryBuild is ExecuteEsqQueryHints.TryBuild. query is nil when the argument was absent.
func esqHintTryBuild(query *jnode, extension map[string]json.RawMessage) *esqHint {
	flat := esqExtensionFlat(extension)
	sql, haveSQL := "", false
	switch {
	case query == nil || query.kind == jkNull:
	case query.kind == jkString:
		if esqWriteSQL.MatchString(query.text) {
			return &esqHint{message: esqReadOnlyMessage}
		}
		if !esqSQLStart.MatchString(query.text) {
			return nil
		}
		sql, haveSQL = query.text, true
	case query.kind == jkObject:
		if esqHasRootSchemaName(query) || esqHasSelectQueryColumns(query) {
			return nil
		}
		for _, key := range query.keys {
			flat.set(esqKey(key), query.props[key])
		}
	default:
		return nil
	}
	if !haveSQL {
		if key, ok := flat.first(esqSQLKeys); ok && flat.values[key].kind == jkString {
			sql, haveSQL = flat.values[key].text, true
		}
	}
	if haveSQL {
		hint := esqFromSQL(sql)
		return &hint
	}
	for _, group := range [][]string{esqSchemaKeys, esqColumnKeys, esqFilterKeys} {
		if _, ok := flat.first(group); ok {
			hint := esqFromFlat(flat, esqFlatPrefix)
			return &hint
		}
	}
	return nil
}

func esqHasSelectQueryColumns(query *jnode) bool {
	columns := query.get("columns")
	return columns.isObject()
}

func esqHasRootSchemaName(query *jnode) bool {
	root := query.get("rootSchemaName")
	return root != nil && root.kind == jkString && strings.TrimFunc(root.text, dotnetIsWhiteSpace) != ""
}

// esqCheckSelectQueryShape is ExecuteEsqQueryHints.CheckSelectQueryShape: warnings for members clio does not
// know, a refusal for query parts sent at the top level or a part Creatio would silently drop.
func esqCheckSelectQueryShape(query *jnode, extension map[string]json.RawMessage) ([]string, *esqHint) {
	var unknown []string
	for _, name := range query.keys {
		if esqIsUnknownMember(esqKey(name)) {
			unknown = append(unknown, "'"+odataDescribeCallerKey(name)+"'")
		}
	}
	var warnings []string
	if len(unknown) > 0 {
		warnings = []string{"query carries " + strings.Join(unknown, ", ") + ", which clio does not know as a SelectQuery member. It was " +
			"sent as is: if this Creatio version does not know it either, the DataService ignored it and the rows " +
			"were read without it."}
	}
	extensionKeys := make([]string, 0, len(extension))
	for key := range extension {
		extensionKeys = append(extensionKeys, key)
	}
	sort.Strings(extensionKeys)
	var stray []string
	for _, key := range extensionKeys {
		if esqQueryPartKeys[esqKey(key)] {
			stray = append(stray, "'"+odataDescribeCallerKey(key)+"'")
		}
	}
	if len(stray) > 0 {
		return warnings, &esqHint{message: "query parts were also sent at the top level (" + strings.Join(stray, ", ") +
			"), where execute-esq ignores them, so running 'query' would drop them; nothing was executed. Put every " +
			"part inside the 'query' SelectQuery object and send the call once."}
	}
	var problems []string
	for _, name := range query.keys {
		if problem := esqShapeProblem(name, query.props[name]); problem != "" {
			problems = append(problems, problem)
		}
	}
	if len(problems) == 0 {
		return warnings, nil
	}
	prefix := "query is not a valid SelectQuery: " + strings.Join(problems, "; ") +
		". Creatio would ignore those parts and run the rest, so nothing was executed."
	flat := &esqFlat{values: map[string]*jnode{}}
	for _, name := range query.keys {
		flat.set(esqKey(name), query.props[name])
	}
	if !esqIsTranslatable(flat) {
		return warnings, &esqHint{message: prefix + " It also carries parts a translation would drop or misread, so no query is suggested."}
	}
	flat.remove("operationtype")
	flat.remove("allcolumns")
	hint := esqFromFlat(flat, prefix)
	return warnings, &hint
}

func (f *esqFlat) remove(key string) {
	if !f.has(key) {
		return
	}
	delete(f.values, key)
	for index, existing := range f.keys {
		if existing == key {
			f.keys = append(f.keys[:index], f.keys[index+1:]...)
			break
		}
	}
}

func esqIsTranslatable(flat *esqFlat) bool {
	for _, key := range flat.keys {
		if !esqTranslatableKeys[key] && !esqQueryPartKeys[key] {
			return false
		}
	}
	if all, ok := flat.values["allcolumns"]; ok && all.kind == jkBool && all.flag {
		return false
	}
	if operation, ok := flat.values["operationtype"]; ok {
		if value, isInt := esqInt32(operation); !isInt || value != 0 {
			return false
		}
	}
	for _, key := range esqFilterKeys {
		if filters, ok := flat.values[key]; ok && esqIsFilterGroup(filters) {
			return false
		}
	}
	return true
}

func esqIsFilterGroup(filters *jnode) bool {
	return filters.isObject() && (filters.get("items") != nil || filters.get("filterType") != nil)
}

func esqIsMisplacedPart(key string) bool {
	if esqSelectQueryMembers[key] {
		return false
	}
	if esqMisplacedPartKeys[key] {
		return true
	}
	for _, stem := range esqReadShapingStems {
		if strings.Contains(key, stem) {
			return true
		}
	}
	return false
}

func esqIsUnknownMember(key string) bool {
	return !esqSelectQueryMembers[key] && !esqIsMisplacedPart(key)
}

func esqShapeProblem(name string, value *jnode) string {
	key := esqKey(name)
	described := odataDescribeCallerKey(name)
	if value.kind == jkNull && esqSelectQueryMembers[key] {
		return ""
	}
	switch key {
	case "columns":
		if value.isObject() && value.get("items").isObject() {
			return ""
		}
		return "'" + described + `' must be an object {"items":{"<alias>":{"expression":{"expressionType":0,"columnPath":"<column>"}}}}, not ` + esqDescribe(value)
	case "filters":
		if esqIsFilterGroup(value) {
			if items := value.get("items"); items == nil || items.isObject() {
				return ""
			}
		}
		return "'" + described + `' must be a filter object {"filterType":6,"items":{"<key>":{...}}}, not ` + esqDescribe(value)
	case "rowcount":
		if _, ok := esqInt32(value); ok {
			return ""
		}
		return "'" + described + "' must be a whole number, not " + esqDescribe(value)
	default:
		if esqIsMisplacedPart(key) {
			return "'" + described + "' is not a SelectQuery member"
		}
		return ""
	}
}

func esqDescribe(value *jnode) string {
	switch value.kind {
	case jkArray:
		return "an array"
	case jkString:
		return "a string"
	case jkInteger, jkFloat:
		return "a number"
	case jkObject:
		return "an object without an 'items' map"
	case jkBool:
		return "a boolean"
	default:
		return "null"
	}
}

// esqInt32 is JsonElement.TryGetInt32 on a number.
func esqInt32(value *jnode) (int, bool) {
	if value == nil || value.kind != jkInteger {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value.text, 10, 32)
	return int(parsed), err == nil
}

func esqInt64(value *jnode) (int64, bool) {
	if value == nil || value.kind != jkInteger {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value.text, 10, 64)
	return parsed, err == nil
}

type esqFilterTerm struct {
	column string
	value  *jnode
}

func esqFromSQL(sql string) esqHint {
	if esqWriteSQL.MatchString(sql) {
		return esqHint{message: esqReadOnlyMessage}
	}
	match := esqSelectShape.FindStringSubmatchIndex(sql)
	if esqUnsupported.MatchString(sql) || match == nil {
		return esqHint{message: esqSQLPrefix + " This statement is not a single-table SELECT of plain columns with simple " +
			"equality filters, so no query is suggested: build the SelectQuery from the shape below."}
	}
	group := func(name string) (string, bool) {
		index := esqSelectShape.SubexpIndex(name)
		if match[2*index] < 0 {
			return "", false
		}
		return sql[match[2*index]:match[2*index+1]], true
	}
	columnsText := strings.TrimFunc(func() string { text, _ := group("cols"); return text }(), dotnetIsWhiteSpace)
	if columnsText == "*" {
		return esqHint{message: esqSQLPrefix + " 'SELECT *' has no safe SelectQuery equivalent: list the columns you need " +
			"(allColumns pulls binary and unrelated fields)."}
	}
	top, hasTop := group("top")
	limit, hasLimit := group("limit")
	if hasTop && hasLimit {
		return esqHint{message: esqSQLPrefix + " TOP and LIMIT were both given, so the row cap is ambiguous and no query is suggested."}
	}
	tableText, _ := group("table")
	table, tableOK := esqReadTable(tableText)
	var columns []string
	for _, part := range strings.Split(columnsText, ",") {
		columns = append(columns, strings.Trim(strings.TrimFunc(part, dotnetIsWhiteSpace), "[]\"`"))
	}
	valid := tableOK
	for _, column := range columns {
		if !esqIdentifier.MatchString(column) {
			valid = false
		}
	}
	if !valid {
		return esqHint{message: esqSQLPrefix + " Aliases, expressions and dotted column paths are not translated."}
	}
	var filters []esqFilterTerm
	if where, ok := group("where"); ok && where != "" {
		for _, term := range esqAndSeparator.Split(where, -1) {
			filter, ok := esqReadEqualityTerm(term)
			if !ok {
				return esqHint{message: esqSQLPrefix + " Only 'column = value' terms joined by AND are translated."}
			}
			filters = append(filters, filter)
		}
	}
	var rows *int
	if hasTop || hasLimit {
		text := top
		if !hasTop {
			text = limit
		}
		value, err := strconv.ParseInt(text, 10, 32)
		cap := int(value)
		if err != nil {
			cap = -1
		}
		rows = &cap
	}
	return esqSuggest(esqSQLPrefix, table, columns, filters, rows)
}

func esqReadEqualityTerm(term string) (esqFilterTerm, bool) {
	match := esqEqualityTerm.FindStringSubmatch(term)
	if match == nil {
		return esqFilterTerm{}, false
	}
	column := match[esqEqualityTerm.SubexpIndex("col")]
	literal := match[esqEqualityTerm.SubexpIndex("val")]
	if strings.HasPrefix(literal, "'") {
		return esqFilterTerm{column, newString(strings.ReplaceAll(literal[1:len(literal)-1], "''", "'"))}, true
	}
	number, err := strconv.ParseInt(literal, 10, 64)
	if err != nil {
		return esqFilterTerm{}, false
	}
	return esqFilterTerm{column, &jnode{kind: jkInteger, text: strconv.FormatInt(number, 10)}}, true
}

func esqReadTable(raw string) (string, bool) {
	name := strings.TrimFunc(raw, dotnetIsWhiteSpace)
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "dbo.") || strings.HasPrefix(lower, "[dbo].") {
		name = name[strings.Index(name, ".")+1:]
	}
	name = strings.Trim(name, "[]\"`")
	return name, esqIdentifier.MatchString(name)
}

func esqFromFlat(flat *esqFlat, prefix string) esqHint {
	for _, group := range [][]string{esqSchemaKeys, esqColumnKeys, esqFilterKeys, esqLimitKeys} {
		count := 0
		for _, key := range group {
			if flat.has(key) {
				count++
			}
		}
		if count > 1 {
			return esqHint{message: prefix + " Two keys were given for the same part of the query (for example " +
				"'filters' and 'where'), so no query is suggested."}
		}
	}
	schema, haveSchema := "", false
	for _, key := range esqSchemaKeys {
		if value, ok := flat.values[key]; ok && value.kind == jkString && strings.TrimFunc(value.text, dotnetIsWhiteSpace) != "" {
			schema, haveSchema = value.text, true
			break
		}
	}
	columns, haveColumns := esqReadColumnNames(flat)
	filters, filtersOK := []esqFilterTerm{}, true
	if key, ok := flat.first(esqFilterKeys); ok {
		if flat.values[key].isObject() {
			filters, filtersOK = esqReadEqualityObject(flat.values[key])
		} else {
			filtersOK = false
		}
	}
	var rows *int
	if key, ok := flat.first(esqLimitKeys); ok {
		value, isInt := esqInt32(flat.values[key])
		if !isInt {
			value = -1
		}
		rows = &value
	}
	safe := haveSchema && esqIdentifier.MatchString(schema) && haveColumns && len(columns) > 0 && filtersOK
	if safe {
		for _, column := range columns {
			if !esqIdentifier.MatchString(column) {
				safe = false
			}
		}
	}
	if !safe {
		return esqHint{message: prefix + " The values are not a single schema name with a plain list of column " +
			"names and simple equality filters, so no query is suggested."}
	}
	return esqSuggest(prefix, schema, columns, filters, rows)
}

func esqReadColumnNames(flat *esqFlat) ([]string, bool) {
	key, ok := flat.first(esqColumnKeys)
	if !ok || flat.values[key].kind != jkArray {
		return nil, false
	}
	columns := []string{}
	for _, item := range flat.values[key].items {
		if item.kind == jkString {
			columns = append(columns, strings.TrimFunc(item.text, dotnetIsWhiteSpace))
		} else {
			columns = append(columns, "")
		}
	}
	return columns, true
}

func esqReadEqualityObject(filters *jnode) ([]esqFilterTerm, bool) {
	result := []esqFilterTerm{}
	for _, name := range filters.keys {
		value := filters.props[name]
		_, isInt := esqInt64(value)
		if !(value.kind == jkString || isInt) || !esqIdentifier.MatchString(name) {
			return nil, false
		}
		result = append(result, esqFilterTerm{name, value})
	}
	return result, true
}

func esqIsTypeAmbiguous(value *jnode) bool {
	return value.kind == jkInteger || value.kind == jkFloat || (value.kind == jkString && esqDateLiteral.MatchString(value.text))
}

func esqSuggest(prefix, schema string, columns []string, filters []esqFilterTerm, requestedRows *int) esqHint {
	if requestedRows != nil && (*requestedRows <= 0 || *requestedRows > esqHintMaxRowCount) {
		return esqHint{message: prefix + " The row cap must be 1-" + strconv.Itoa(esqHintMaxRowCount) + ", so no query is suggested."}
	}
	for _, filter := range filters {
		if esqIsTypeAmbiguous(filter.value) {
			return esqHint{message: prefix + " The filter on '" + filter.column + "' compares with a number or a date, and the " +
				"literal cannot tell its column type (Integer, Decimal, Boolean or DateTime), so no query is " +
				"suggested: build the filter with the column's dataValueType (get-guidance name=esq-filters)."}
		}
	}
	rowCount := esqHintDefaultRowCount
	if requestedRows != nil {
		rowCount = *requestedRows
	}
	query := esqBuildSelectQuery(schema, columns, filters, rowCount)
	message := prefix + " The equivalent query is returned in 'suggested-query' - send it as 'query' to run " +
		"it. Check that '" + schema + "' is the Creatio schema name, that each column is an ESQ column name (a lookup is " +
		"'Account' or 'Account.Id', not the physical 'AccountId'), and that each filter fits its column (a quoted " +
		"value is sent as Text, a GUID as Guid; a date-shaped one gets no suggestion). The row cap is " + strconv.Itoa(rowCount)
	if requestedRows == nil {
		message += " (default; none was given)."
	} else {
		message += "."
	}
	return esqHint{message: message, suggested: query}
}

// esqBuildSelectQuery is SelectQueryHelper.BuildSelectQuery as System.Text.Json writes it.
func esqBuildSelectQuery(schema string, columns []string, filters []esqFilterTerm, rowCount int) json.RawMessage {
	integer := func(value int) *jnode { return &jnode{kind: jkInteger, text: strconv.Itoa(value)} }
	object := func(pairs ...any) *jnode {
		node := newObject()
		for index := 0; index < len(pairs); index += 2 {
			node.set(pairs[index].(string), pairs[index+1].(*jnode))
		}
		return node
	}
	items := newObject()
	seen := map[string]bool{}
	for _, column := range columns {
		if seen[column] {
			continue
		}
		seen[column] = true
		items.set(column, object(
			"expression", object("expressionType", integer(0), "columnPath", newString(column)),
			"orderDirection", integer(0), "orderPosition", integer(-1), "isVisible", newBool(true)))
	}
	filterItems := newObject()
	for index, filter := range filters {
		dataType := 1
		if esqGUIDLiteral.MatchString(filter.value.text) {
			dataType = 0
		}
		filterItems.set("filter"+strconv.Itoa(index), object(
			"filterType", integer(1), "comparisonType", integer(3), "isEnabled", newBool(true),
			"trimDateTimeParameterToDate", newBool(false),
			"leftExpression", object("expressionType", integer(0), "columnPath", newString(filter.column)),
			"rightExpression", object("expressionType", integer(2),
				"parameter", object("value", newString(filter.value.text), "dataValueType", integer(dataType)))))
	}
	query := object(
		"rootSchemaName", newString(schema), "operationType", integer(0), "allColumns", newBool(false),
		"isDistinct", newBool(false), "ignoreDisplayValues", newBool(false), "rowCount", integer(rowCount),
		"rowsOffset", integer(-1), "isPageable", newBool(false),
		"columns", object("items", items),
		"filters", object("filterType", integer(6), "isEnabled", newBool(true), "trimDateTimeParameterToDate", newBool(false),
			"logicalOperation", integer(0), "items", filterItems))
	return json.RawMessage(query.stjJSON())
}
