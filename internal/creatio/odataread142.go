package creatio

// odata-read as clio 8.1.0.142 runs it (ODataReadTool), shared by odata-read and odata-read-to-file
// (ODataReadToFileTool / ODataFileContract): argument validation and normalization, the request path with the
// structured filter, the response classification and identity checks, and the file summary.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	odataReadMinTop     = 1
	odataReadMaxTop     = 100
	odataReadDefaultTop = 25
	odataReadTimeout    = 30 * time.Second

	// ODataReadMaxFileBytes is ODataFileContract.MaxResponseBytes, the file mode's response ceiling.
	ODataReadMaxFileBytes = 64 << 20

	odataReadValidArguments = "Valid: entity, environment-name, filters, select, expand, order-by, top, skip, count. " +
		"Raw filter strings are not supported; use the structured filters object. " +
		"To keep a large response on disk, call odata-read-to-file instead."
	odataReadToFileValidArguments = "Valid: entity, environment-name, output-file, filters, select, expand, order-by, top, skip, count. " +
		"Raw filter strings are not supported; use the structured filters object."

	// ODataRead error codes (ODataReadErrorCodes).
	odataCodeArgument          = "argument"
	odataCodeEntityNotFound    = "entity-not-found"
	odataCodeInvalidQuery      = "invalid-query"
	odataCodeServerReported    = "server-reported-error"
	odataCodeNonJSON           = "non-json-response"
	odataCodeIncompleteResonse = "incomplete-response"
	odataCodeTransport         = "transport"
)

var (
	odataReadAliases = map[string]string{
		"environmentName": "environment-name", "environment_name": "environment-name",
		"orderBy": "order-by", "order_by": "order-by", "limit": "top",
	}
	odataReadToFileAliases = func() map[string]string {
		aliases := map[string]string{"outputFile": "output-file", "output_file": "output-file"}
		for key, value := range odataReadAliases {
			aliases[key] = value
		}
		return aliases
	}()
	odataFilterGroupAliases     = map[string]string{"and": "all", "or": "any"}
	odataFilterConditionAliases = map[string]string{"column": "field", "operator": "op", "values": "in"}
	odataSupportedOperators     = []string{"eq", "ne", "gt", "ge", "lt", "le", "contains", "startswith", "endswith"}
	odataEntityNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	odataGUIDPattern            = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ODataReadArgs is clio's ODataReadArgs (and ODataReadToFileArgs) after binding.
type ODataReadArgs struct {
	Entity          string
	Select          json.RawMessage // nil when absent
	Expand          json.RawMessage
	OrderBy         string
	Top             *int
	Skip            *int
	Count           bool
	Filters         *ODataFilters
	FiltersProvided bool
	Extension       map[string]json.RawMessage
	OutputFile      string
}

// ODataFilters is clio's ODataFilters.
type ODataFilters struct {
	All       []*ODataFilterCondition
	Any       []*ODataFilterCondition
	Extension map[string]json.RawMessage
}

// ODataFilterCondition is clio's ODataFilterCondition. Value and In are nil when absent (a JSON null is
// "null", which is present).
type ODataFilterCondition struct {
	Field     string
	Op        string
	Value     json.RawMessage
	In        json.RawMessage
	Extension map[string]json.RawMessage
}

// ODataUnknownMember is clio's ODataUnknownMember.
type ODataUnknownMember struct {
	Argument    string   `json:"argument"`
	Name        string   `json:"name"`
	Suggestions []string `json:"suggestions"`
}

// ODataReadResponse is clio 8.1.0.142's ODataReadResponse.
type ODataReadResponse struct {
	Success          bool                 `json:"success"`
	Error            *string              `json:"error,omitempty"`
	Count            *int                 `json:"count,omitempty"`
	Value            json.RawMessage      `json:"value,omitempty"`
	NextLink         *string              `json:"next-link,omitempty"`
	TotalCount       *int64               `json:"total-count,omitempty"`
	StatusCode       *int                 `json:"status-code,omitempty"`
	Entity           *string              `json:"entity,omitempty"`
	ErrorCode        *string              `json:"error-code,omitempty"`
	CorrelationID    *string              `json:"correlation-id,omitempty"`
	OutputFile       *string              `json:"output-file,omitempty"`
	RowCount         *int                 `json:"row-count,omitempty"`
	ColumnSizes      *odataColumnSizes    `json:"column-sizes,omitempty"`
	UnknownMembers   []ODataUnknownMember `json:"unknown-members,omitempty"`
	ValidColumnCount *int                 `json:"valid-column-count,omitempty"`
}

// odataColumnSizes keeps the columns in the order the rows first name them, as clio's Dictionary does.
type odataColumnSizes struct {
	keys  []string
	sizes map[string]int64
}

func (s *odataColumnSizes) MarshalJSON() ([]byte, error) {
	node := newObject()
	for _, key := range s.keys {
		node.set(key, &jnode{kind: jkInteger, text: strconv.FormatInt(s.sizes[key], 10)})
	}
	return node.newtonsoftJSON(), nil
}

// ODataReadFailure is ODataReadResponse.Failure.
func ODataReadFailure(message, code string, statusCode *int, entity string) ODataReadResponse {
	response := ODataReadResponse{Error: &message, ErrorCode: &code, StatusCode: statusCode}
	if entity != "" {
		response.Entity = &entity
	}
	return response
}

// ODataReadStamp sets the correlation-id the one exit of both read tools stamps on every answer.
func ODataReadStamp(response ODataReadResponse) ODataReadResponse {
	id := dataWriteCorrelationID()
	response.CorrelationID = &id
	return response
}

// ---- validation (ODataReadTool.ValidateAndNormalizeArguments / ValidateTarget) ----

// odataReadValidate returns the argument refusal, or the normalized select and expand lists.
func odataReadValidate(args ODataReadArgs, toFile bool) (string, []string, []string) {
	aliases, hint := odataReadAliases, odataReadValidArguments
	if toFile {
		aliases, hint = odataReadToFileAliases, odataReadToFileValidArguments
	}
	if _, ok := args.Extension["filter"]; ok {
		return "Argument 'filter' is unsupported because raw filter strings are not accepted. " +
			"Use a structured filter, for example: " +
			"filters: {\"all\":[{\"field\":\"Name\",\"op\":\"eq\",\"value\":\"Acme\"}]}.", nil, nil
	}
	if message := DataWriteAliasError(odataKeys(args.Extension), aliases, ".", hint); message != "" {
		return message, nil, nil
	}
	if args.Skip != nil && *args.Skip < 0 {
		return fmt.Sprintf("skip must be zero or greater (got %d).", *args.Skip), nil, nil
	}
	selectColumns, problem := odataNormalizeColumnList(args.Select, "select")
	if problem != "" {
		return problem, nil, nil
	}
	expandColumns, problem := odataNormalizeColumnList(args.Expand, "expand")
	if problem != "" {
		return problem, nil, nil
	}
	if problem := odataValidateMemberList("select", selectColumns); problem != "" {
		return problem, nil, nil
	}
	if problem := odataValidateMemberList("expand", expandColumns); problem != "" {
		return problem, nil, nil
	}
	if problem := odataValidateOrderBy(args.OrderBy); problem != "" {
		return problem, nil, nil
	}
	if args.FiltersProvided && args.Filters == nil {
		return "filters must be a structured object containing at least one condition in all or any; null is not supported.", nil, nil
	}
	if args.Filters == nil {
		return "", selectColumns, expandColumns
	}
	if message := DataWriteAliasError(odataKeys(args.Filters.Extension), odataFilterGroupAliases, ".", "Valid filter groups: all, any."); message != "" {
		return "filters: " + message, nil, nil
	}
	type pathCondition struct {
		path      string
		condition *ODataFilterCondition
	}
	var conditions []pathCondition
	for index, condition := range args.Filters.All {
		conditions = append(conditions, pathCondition{fmt.Sprintf("filters.all[%d]", index), condition})
	}
	for index, condition := range args.Filters.Any {
		conditions = append(conditions, pathCondition{fmt.Sprintf("filters.any[%d]", index), condition})
	}
	if len(conditions) == 0 {
		return "filters must contain at least one condition in all or any.", nil, nil
	}
	for _, entry := range conditions {
		if problem := odataValidateCondition(entry.path, entry.condition); problem != "" {
			return problem, nil, nil
		}
	}
	return "", selectColumns, expandColumns
}

func odataKeys(extension map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(extension))
	for key := range extension {
		keys = append(keys, key)
	}
	return keys
}

// odataReadValidateTarget is ODataReadTool.ValidateTarget.
func odataReadValidateTarget(args ODataReadArgs) string {
	if !odataEntityAccepted(args.Entity) {
		if dataWriteBlank(args.Entity) {
			return "entity is required."
		}
		return "entity must be a valid OData entity set name (letters, digits, underscore)."
	}
	if args.Top != nil && (*args.Top < odataReadMinTop || *args.Top > odataReadMaxTop) {
		return fmt.Sprintf("top must be between %d and %d (got %d). Omit top to use the default of %d.",
			odataReadMinTop, odataReadMaxTop, *args.Top, odataReadDefaultTop)
	}
	return ""
}

func odataEntityAccepted(entity string) bool {
	return !dataWriteBlank(entity) && odataEntityNamePattern.MatchString(dataWriteTrim(entity))
}

// odataIsValidMemberPath is ODataKeyFormatter.IsValidMemberPath.
func odataIsValidMemberPath(path string) bool {
	if dataWriteBlank(path) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment != dataWriteTrim(segment) || !odataEntityNamePattern.MatchString(segment) {
			return false
		}
	}
	return true
}

// odataIsIdish is ODataKeyFormatter.IsIdish.
func odataIsIdish(field string) bool {
	last := field
	if slash := strings.LastIndex(field, "/"); slash >= 0 {
		last = field[slash+1:]
	}
	if !strings.HasSuffix(last, "Id") {
		return false
	}
	if len(last) == 2 {
		return true
	}
	preceding := rune(last[len(last)-3])
	return (preceding >= 'a' && preceding <= 'z') || (preceding >= '0' && preceding <= '9')
}

// odataLiteralFor is ODataKeyFormatter.LiteralFor.
func odataLiteralFor(field string, value *jnode) string {
	switch value.kind {
	case jkNull:
		return "null"
	case jkInteger, jkFloat:
		return value.text
	case jkBool:
		return strconv.FormatBool(value.flag)
	case jkString:
		if odataGUIDPattern.MatchString(value.text) && odataIsIdish(field) {
			return value.text
		}
		return "'" + strings.ReplaceAll(value.text, "'", "''") + "'"
	default:
		return "'" + strings.ReplaceAll(string(value.stjJSON()), "'", "''") + "'"
	}
}

func odataColumnListError(argument string) string {
	return argument + " must be an array of column names or a comma-separated string, " +
		`for example ["Id","Name"] or "Id,Name".`
}

// odataNormalizeColumnList is ODataReadTool.TryNormalizeColumnList.
func odataNormalizeColumnList(raw json.RawMessage, argument string) ([]string, string) {
	if raw == nil {
		return nil, ""
	}
	node, err := parseJNode(raw)
	if err != nil || node.kind == jkNull {
		return nil, ""
	}
	var parsed []string
	switch node.kind {
	case jkString:
		for _, part := range strings.Split(node.text, ",") {
			if trimmed := strings.TrimFunc(part, dotnetIsWhiteSpace); trimmed != "" {
				parsed = append(parsed, trimmed)
			}
		}
	case jkArray:
		for _, item := range node.items {
			if item.kind != jkString {
				return nil, odataColumnListError(argument)
			}
			if !dataWriteBlank(item.text) {
				parsed = append(parsed, strings.TrimFunc(item.text, dotnetIsWhiteSpace))
			}
		}
	default:
		return nil, odataColumnListError(argument)
	}
	return parsed, ""
}

func odataValidateMemberList(argument string, members []string) string {
	for index, member := range members {
		if !odataIsValidMemberPath(member) {
			return fmt.Sprintf("%s[%d] must be an OData member path containing only letters, digits, ", argument, index) +
				"underscores, and '/' separators; nested query options are not supported."
		}
	}
	return ""
}

func odataValidateOrderBy(orderBy string) string {
	if dataWriteBlank(orderBy) {
		return ""
	}
	for _, clause := range strings.Split(orderBy, ",") {
		parts := odataSplitTrimmed(clause, " ")
		ok := (len(parts) == 1 || len(parts) == 2) && odataIsValidMemberPath(parts[0]) &&
			(len(parts) == 1 || strings.EqualFold(parts[1], "asc") || strings.EqualFold(parts[1], "desc"))
		if !ok {
			return "order-by must be a comma-separated list of '<field> [asc|desc]' clauses, where each " +
				"field is an OData member path containing only letters, digits, underscores, and '/' separators."
		}
	}
	return ""
}

// odataSplitTrimmed is string.Split(separator, RemoveEmptyEntries | TrimEntries).
func odataSplitTrimmed(text, separator string) []string {
	var parts []string
	for _, part := range strings.Split(text, separator) {
		if trimmed := strings.TrimFunc(part, dotnetIsWhiteSpace); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

func odataValidateCondition(path string, condition *ODataFilterCondition) string {
	if condition == nil {
		return path + " must be a filter condition object; null is not supported."
	}
	if message := DataWriteAliasError(odataKeys(condition.Extension), odataFilterConditionAliases, ".",
		"Valid filter condition members: field, op, value, in."); message != "" {
		return path + ": " + message
	}
	if dataWriteBlank(condition.Field) {
		return path + ".field is required."
	}
	if !odataIsValidMemberPath(condition.Field) {
		return path + ".field must be an OData member path containing only letters, digits, underscores, and '/' separators."
	}
	hasValue, hasIn := condition.Value != nil, condition.In != nil
	if hasValue == hasIn {
		return path + " must provide exactly one of value or in."
	}
	if hasIn {
		node, err := parseJNode(condition.In)
		if err != nil || node.kind != jkArray || len(node.items) == 0 {
			return path + ".in must be a non-empty array."
		}
		if !dataWriteBlank(condition.Op) {
			return path + ".op must be omitted when in is provided; in expands to equality conditions."
		}
		return ""
	}
	operation := condition.Op
	if dataWriteBlank(operation) {
		operation = "eq"
	}
	for _, supported := range odataSupportedOperators {
		if supported == operation {
			return ""
		}
	}
	return fmt.Sprintf("%s.op must be one of: %s (got %s).", path, strings.Join(odataSupportedOperators, ", "), operation)
}

// ---- request path ----

func odataJoinConditions(conditions []string, separator string) string {
	switch len(conditions) {
	case 0:
		return ""
	case 1:
		return conditions[0]
	default:
		return "(" + strings.Join(conditions, separator) + ")"
	}
}

func odataBuildCondition(condition *ODataFilterCondition) string {
	if condition == nil || dataWriteBlank(condition.Field) {
		return ""
	}
	field := condition.Field
	if condition.In != nil {
		if node, err := parseJNode(condition.In); err == nil && node.kind == jkArray {
			parts := make([]string, 0, len(node.items))
			for _, item := range node.items {
				parts = append(parts, field+" eq "+odataLiteralFor(field, item))
			}
			return odataJoinConditions(parts, " or ")
		}
	}
	if condition.Value == nil {
		return ""
	}
	value, err := parseJNode(condition.Value)
	if err != nil {
		return ""
	}
	operation := condition.Op
	if dataWriteBlank(operation) {
		operation = "eq"
	}
	switch operation {
	case "contains", "startswith", "endswith":
		return operation + "(" + field + "," + odataLiteralFor(field, value) + ")"
	}
	if value.kind == jkNull && (operation == "eq" || operation == "ne") {
		return field + " " + operation + " null"
	}
	return field + " " + operation + " " + odataLiteralFor(field, value)
}

func odataBuildConditions(conditions []*ODataFilterCondition) []string {
	var built []string
	for _, condition := range conditions {
		if text := odataBuildCondition(condition); text != "" {
			built = append(built, text)
		}
	}
	return built
}

func odataBuildFilter(filters *ODataFilters) string {
	var parts []string
	if all := odataJoinConditions(odataBuildConditions(filters.All), " and "); all != "" {
		parts = append(parts, all)
	}
	if anyOf := odataJoinConditions(odataBuildConditions(filters.Any), " or "); anyOf != "" {
		parts = append(parts, anyOf)
	}
	return strings.Join(parts, " and ")
}

// odataEscapeDataString is Uri.EscapeDataString: everything but the RFC 3986 unreserved characters is
// percent-encoded from its UTF-8 bytes, upper-case hex.
func odataEscapeDataString(text string) string {
	var builder strings.Builder
	for _, b := range []byte(text) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '.' || b == '_' || b == '~' {
			builder.WriteByte(b)
		} else {
			fmt.Fprintf(&builder, "%%%02X", b)
		}
	}
	return builder.String()
}

// odataReadRequestPath is ODataReadTool.BuildRequestPath.
func odataReadRequestPath(args ODataReadArgs, selectColumns, expandColumns []string) string {
	var parts []string
	if args.Filters != nil {
		if filter := odataBuildFilter(args.Filters); filter != "" {
			parts = append(parts, "$filter="+odataEscapeDataString(filter))
		}
	}
	if len(selectColumns) > 0 {
		parts = append(parts, "$select="+odataEscapeDataString(strings.Join(selectColumns, ",")))
	}
	if len(expandColumns) > 0 {
		parts = append(parts, "$expand="+odataEscapeDataString(strings.Join(expandColumns, ",")))
	}
	if !dataWriteBlank(args.OrderBy) {
		parts = append(parts, "$orderby="+odataEscapeDataString(dataWriteTrim(args.OrderBy)))
	}
	if args.Skip != nil {
		parts = append(parts, "$skip="+strconv.Itoa(*args.Skip))
	}
	if args.Count {
		parts = append(parts, "$count=true")
	}
	top := odataReadDefaultTop
	if args.Top != nil {
		top = *args.Top
	}
	parts = append(parts, "$top="+strconv.Itoa(top))
	return "odata/" + dataWriteTrim(args.Entity) + "?" + strings.Join(parts, "&")
}

// ---- the request ----

// odataReadGet is the GET both read paths send: the body whatever the status (clio's IApplicationClient
// reads it that way), err on a transport or session failure, errResponseTooLarge past the ceiling.
func (c *Client) odataReadGet(ctx context.Context, path string, limit int) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, odataReadTimeout)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.serviceURL(path), nil)
		if err == nil {
			request.Header.Set("Accept", "application/json")
		}
		return request, err
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > limit {
		return payload, errResponseTooLarge
	}
	return payload, nil
}

// ODataReadPrepare is the part of both read tools before the environment: argument validation, then the
// target. It returns the refusal (already a full answer) or the normalized lists.
func ODataReadPrepare(args ODataReadArgs, toFile bool) (*ODataReadResponse, []string, []string) {
	problem, selectColumns, expandColumns := odataReadValidate(args, toFile)
	if problem != "" {
		failure := ODataReadFailure(problem, odataCodeArgument, nil, "")
		return &failure, nil, nil
	}
	if problem := odataReadValidateTarget(args); problem != "" {
		entity := ""
		if odataEntityAccepted(args.Entity) {
			entity = dataWriteTrim(args.Entity)
		}
		failure := ODataReadFailure(problem, odataCodeArgument, nil, entity)
		return &failure, nil, nil
	}
	return nil, selectColumns, expandColumns
}

// ODataReadTransportFailure is the answer for a failure before or around the request (environment
// resolution included): the redacted message, error-code transport.
func ODataReadTransportFailure(message string, args ODataReadArgs) ODataReadResponse {
	entity := ""
	if !dataWriteBlank(args.Entity) {
		entity = dataWriteTrim(args.Entity)
	}
	return ODataReadFailure(redact.Text(message), odataCodeTransport, nil, entity)
}

// ODataRead142 is ODataReadTool.ReadCore after the environment was resolved.
func (c *Client) ODataRead142(ctx context.Context, args ODataReadArgs, selectColumns, expandColumns []string) ODataReadResponse {
	body, err := c.odataReadGet(ctx, odataReadRequestPath(args, selectColumns, expandColumns), maxResponseBytes)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return ODataReadTransportFailure(fmt.Sprintf("response exceeds %d bytes", maxResponseBytes), args)
		}
		return ODataReadTransportFailure(err.Error(), args)
	}
	response := odataParseReadResponse(strings.TrimPrefix(string(body), "\uFEFF"), args)
	return c.odataExplainColumns(ctx, response, args, selectColumns, expandColumns)
}

// odataParseReadResponse is ODataReadTool.ParseODataResponse.
func odataParseReadResponse(body string, args ODataReadArgs) ODataReadResponse {
	entity := dataWriteTrim(args.Entity)
	fail := func(message, code string) ODataReadResponse { return ODataReadFailure(message, code, nil, entity) }
	if dataWriteBlank(body) {
		return fail(odataDescribeEmptyRead, odataCodeTransport)
	}
	if dataWriteIsMarkup(body) {
		return odataMarkupFailure(body, entity)
	}
	root, err := parseJNode([]byte(body))
	if err != nil {
		return fail(odataDescribeNonJSONRead, odataCodeNonJSON)
	}
	if !odataHasMatchingIdentity(root, entity) {
		if kind, ok := odataClassify(json.RawMessage(body)); ok {
			return fail(odataDescribeServerReportedReadError(kind, odataDescribeCallerQuery(args, kind), odataDescribeStructuredError(root)),
				odataErrorCodeFor(kind))
		}
	}
	if value := root.get("value"); root.isObject() && value != nil {
		if value.kind == jkArray && odataMatchesContext(root, entity, false) {
			return odataParseCollection(root, value, body, entity, args.Count)
		}
		return fail(odataDescribeNonJSONRead, odataCodeNonJSON)
	}
	if root.isObject() && odataMatchesContext(root, entity, true) {
		count := 1
		raw := json.RawMessage(strings.TrimFunc(body, esqJSONWhiteSpace))
		return ODataReadResponse{Success: true, Count: &count, Value: raw}
	}
	return fail(odataDescribeNonJSONRead, odataCodeNonJSON)
}

func odataMarkupFailure(body, entity string) ODataReadResponse {
	status, known := dataWriteMarkupStatus(body)
	var statusCode *int
	if known {
		statusCode = &status
	}
	code := odataCodeTransport
	if known && status == http.StatusNotFound {
		code = odataCodeEntityNotFound
	}
	return ODataReadFailure(odataDescribeMarkupError(entity, statusCode), code, statusCode, entity)
}

// odataDescribeMarkupError is ODataReadTool.DescribeMarkupError.
func odataDescribeMarkupError(entity string, status *int) string {
	if dataWriteBlank(entity) {
		entity = "<unnamed>"
	}
	switch {
	case status != nil && *status == http.StatusNotFound:
		return fmt.Sprintf("Entity '%s' is not exposed over OData on this environment (HTTP 404). %s Use "+
			"execute-esq to read schemas that never get an OData entity set.", entity, dataWriteUnregisteredEntityHint)
	case status != nil:
		return fmt.Sprintf("The OData request for entity '%s' was answered with an %s instead of an OData response. Verify the "+
			"environment URL, the authentication and any proxy.", entity, dataWriteMarkupStatusPhrase(*status))
	default:
		return odataDescribeNonJSONRead
	}
}

func odataParseCollection(root, value *jnode, body, entity string, countRequested bool) ODataReadResponse {
	count := len(value.items)
	total := odataTotalCount(root)
	if countRequested && total == nil {
		return ODataReadFailure("Creatio did not return @odata.count for count=true; total count cannot be verified.",
			odataCodeIncompleteResonse, nil, entity)
	}
	var raw struct {
		Value json.RawMessage `json:"value"`
	}
	_ = esqRawMember([]byte(body), "value", &raw.Value)
	return ODataReadResponse{Success: true, Count: &count, Value: raw.Value, NextLink: odataNextLink(root), TotalCount: total}
}

func odataTotalCount(root *jnode) *int64 {
	node := root.get("@odata.count")
	if node == nil || node.kind != jkInteger {
		return nil
	}
	value, err := strconv.ParseInt(node.text, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func odataNextLink(root *jnode) *string {
	node := root.get("@odata.nextLink")
	if node == nil || node.kind != jkString {
		return nil
	}
	text := node.text
	return &text
}

// ---- identity (ODataReadTool.MatchesTopLevelContext and friends) ----

func odataContextFragment(root *jnode) (string, bool) {
	context := root.get("@odata.context")
	if context == nil || context.kind != jkString {
		return "", false
	}
	start := strings.LastIndex(context.text, "#")
	if start < 0 {
		return "", false
	}
	fragment := context.text[start+1:]
	return fragment, fragment != ""
}

func odataMatchesContext(root *jnode, entity string, single bool) bool {
	fragment, ok := odataContextFragment(root)
	if !ok {
		return false
	}
	const suffix = "/$entity"
	if single {
		if !strings.HasSuffix(fragment, suffix) {
			return false
		}
		fragment = strings.TrimSuffix(fragment, suffix)
	} else if strings.HasSuffix(fragment, suffix) {
		return false
	}
	set := fragment
	if start := strings.Index(fragment, "("); start >= 0 {
		if !odataBalancedTrailingProjection(fragment, start) {
			return false
		}
		set = fragment[:start]
	}
	return set != "" && !strings.Contains(set, "/") && strings.EqualFold(set, entity)
}

func odataBalancedTrailingProjection(fragment string, start int) bool {
	depth := 0
	for index := start; index < len(fragment); index++ {
		switch fragment[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
			if depth == 0 {
				return index > start+1 && index == len(fragment)-1
			}
		}
	}
	return false
}

func odataHasMatchingIdentity(root *jnode, entity string) bool {
	return root.isObject() && (odataMatchesContext(root, entity, true) || odataMatchesContext(root, entity, false))
}

// ---- caller query restatement (ODataReadTool.DescribeCallerQuery) ----

func odataFilterFields(args ODataReadArgs) []string {
	if args.Filters == nil {
		return nil
	}
	seen := map[string]bool{}
	var fields []string
	for _, group := range [][]*ODataFilterCondition{args.Filters.All, args.Filters.Any} {
		for _, condition := range group {
			if condition == nil || dataWriteBlank(condition.Field) {
				continue
			}
			field := dataWriteTrim(condition.Field)
			if !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
	}
	return fields
}

func odataDescribeCallerQuery(args ODataReadArgs, kind odataErrorKind) string {
	if kind != odataKindInvalidQuery {
		return ""
	}
	fields := odataFilterFields(args)
	var sent []string
	if len(fields) > 0 {
		sent = append(sent, "filter fields: "+strings.Join(fields, ", "))
	}
	if columns, problem := odataNormalizeColumnList(args.Select, "select"); problem == "" && len(columns) > 0 {
		sent = append(sent, "select: "+strings.Join(columns, ", "))
	}
	if columns, problem := odataNormalizeColumnList(args.Expand, "expand"); problem == "" && len(columns) > 0 {
		sent = append(sent, "expand: "+strings.Join(columns, ", "))
	}
	if !dataWriteBlank(args.OrderBy) {
		sent = append(sent, "order-by: "+dataWriteTrim(args.OrderBy))
	}
	var described []string
	if len(sent) > 0 {
		described = append(described, "Names this request sent - "+strings.Join(sent, "; ")+".")
	}
	if hint := odataLookupColumnHint(fields); hint != "" {
		described = append(described, hint)
	}
	return strings.Join(described, " ")
}

func odataLookupColumnHint(fields []string) string {
	var suspects []string
	seen := map[string]bool{}
	for _, field := range fields {
		if len(field) > 2 && odataIsIdish(field) && !strings.Contains(field, "/") && !seen[field] {
			seen[field] = true
			suspects = append(suspects, field)
		}
	}
	if len(suspects) == 0 {
		return ""
	}
	quoted := make([]string, len(suspects))
	rewrites := make([]string, len(suspects))
	for index, field := range suspects {
		quoted[index] = "'" + field + "'"
		rewrites[index] = "'" + field + "' -> '" + field[:len(field)-2] + "/Id'"
	}
	verb := "look"
	if len(suspects) == 1 {
		verb = "looks"
	}
	return strings.Join(quoted, ", ") + " " + verb + " like a lookup column; OData exposes lookups as navigation paths, so filter on the " +
		"navigation path instead: " + strings.Join(rewrites, ", ") + "."
}

// ---- the file mode (ODataFileContract) ----

// ODataReadToFile is ODataReadToFileTool.ReadToFileCore after the environment and the output path were
// resolved: the bounded fetch, one classification and summary pass, then the byte-exact file.
func (c *Client) ODataReadToFile(ctx context.Context, args ODataReadArgs, selectColumns, expandColumns []string, path string) ODataReadResponse {
	entity := dataWriteTrim(args.Entity)
	body, err := c.odataReadGet(ctx, odataReadRequestPath(args, selectColumns, expandColumns), ODataReadMaxFileBytes)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return ODataReadFailure(fmt.Sprintf("OData response is at least %d bytes, which exceeds the "+
				"%d-byte limit for one call. Narrow the query with select, or page it with top and skip.", len(body), ODataReadMaxFileBytes),
				odataCodeTransport, nil, entity)
		}
		return ODataReadFailure(redact.Text(err.Error()), odataCodeTransport, nil, entity)
	}
	summary, failure := odataFileSummary(body, entity, args.Count)
	if failure != nil {
		return c.odataExplainColumns(ctx, *failure, args, selectColumns, expandColumns)
	}
	if refusal := toFileWriteNew(path, body); refusal != nil {
		var exists toFileExistsError
		if errors.As(refusal, &exists) {
			return ODataReadFailure(redact.Text(schemaGetExistsMessage(path)), odataCodeArgument, nil, entity)
		}
		return ODataReadFailure(redact.Text("Failed to write output-file: "+refusal.Error()), odataCodeTransport, nil, entity)
	}
	summary.OutputFile = &path
	return summary
}

// odataFileSummary is ODataFileContract.BuildSummary: the inline read's classification, then the row and
// column-size summary.
func odataFileSummary(body []byte, entity string, countRequested bool) (ODataReadResponse, *ODataReadResponse) {
	fail := func(message, code string, status *int) (ODataReadResponse, *ODataReadResponse) {
		failure := ODataReadFailure(message, code, status, entity)
		return ODataReadResponse{}, &failure
	}
	if len(body) == 0 {
		return fail(odataDescribeEmptyRead, odataCodeTransport, nil)
	}
	root, err := parseJNode(body)
	if err != nil {
		text := string(body)
		if dataWriteIsMarkup(text) {
			failure := odataMarkupFailure(text, entity)
			return ODataReadResponse{}, &failure
		}
		return fail(odataDescribeNonJSONRead, odataCodeNonJSON, nil)
	}
	if !odataHasMatchingIdentity(root, entity) {
		if kind, ok := odataClassify(json.RawMessage(body)); ok {
			return fail(odataDescribeServerReportedReadError(kind, "", odataDescribeStructuredError(root)), odataErrorCodeFor(kind), nil)
		}
	}
	if !root.isObject() {
		return fail(odataDescribeNonJSONRead, odataCodeNonJSON, nil)
	}
	var isRead bool
	if value := root.get("value"); value != nil {
		isRead = value.kind == jkArray && odataMatchesContext(root, entity, false)
	} else {
		isRead = odataMatchesContext(root, entity, true)
	}
	if !isRead {
		return fail(odataDescribeNonJSONRead, odataCodeNonJSON, nil)
	}
	rows := root
	if value := root.get("value"); value != nil && value.kind == jkArray {
		rows = value
	}
	total := odataTotalCount(root)
	if countRequested && total == nil {
		return fail("Creatio did not return @odata.count for count=true; total count cannot be verified.", odataCodeIncompleteResonse, nil)
	}
	recordCount := 1
	if rows.kind == jkArray {
		recordCount = len(rows.items)
	}
	rowCount, sizes := odataSummarizeRows(rows, body)
	return ODataReadResponse{Success: true, Count: &recordCount, NextLink: odataNextLink(root), TotalCount: total,
		RowCount: &rowCount, ColumnSizes: sizes}, nil
}

// odataSummarizeRows counts object rows and totals each column's UTF-8 byte size (the raw JSON of the value
// as it arrived), skipping @odata.* annotations.
func odataSummarizeRows(rows *jnode, body []byte) (int, *odataColumnSizes) {
	sizes := &odataColumnSizes{sizes: map[string]int64{}}
	rawRows := odataRawRows(body)
	elements := []*jnode{rows}
	if rows.kind == jkArray {
		elements = rows.items
	}
	count := 0
	for index, row := range elements {
		if !row.isObject() {
			continue
		}
		count++
		var raw map[string]json.RawMessage
		if index < len(rawRows) {
			raw = rawRows[index]
		}
		for _, key := range row.keys {
			if strings.HasPrefix(key, "@odata.") {
				continue
			}
			size := int64(len(row.props[key].stjJSON()))
			if value, ok := raw[key]; ok {
				size = int64(len(strings.TrimFunc(string(value), esqJSONWhiteSpace)))
			}
			if _, seen := sizes.sizes[key]; !seen {
				sizes.keys = append(sizes.keys, key)
			}
			sizes.sizes[key] += size
		}
	}
	return count, sizes
}

// odataRawRows reads each row's members as raw text, so a column's size is the length of the bytes that
// arrived (JsonElement.GetRawText).
func odataRawRows(body []byte) []map[string]json.RawMessage {
	var envelope struct {
		Value []json.RawMessage `json:"value"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Value != nil {
		rows := make([]map[string]json.RawMessage, len(envelope.Value))
		for index, raw := range envelope.Value {
			_ = json.Unmarshal(raw, &rows[index]) // a row that is not an object stays nil and is skipped
		}
		return rows
	}
	var single map[string]json.RawMessage
	if json.Unmarshal(body, &single) == nil {
		return []map[string]json.RawMessage{single}
	}
	return nil
}
