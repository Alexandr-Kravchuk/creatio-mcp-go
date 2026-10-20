package creatio

// execute-esq as clio 8.1.0.142 runs it (ExecuteEsqTool.Run), shared by execute-esq and execute-esq-to-file:
// the SQL / flat-argument hints and the SelectQuery shape check of ExecuteEsqQueryHints, the timeout clamp,
// the response ceiling, the DataService failure shapes and the unknown-column check.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	esqDefaultTimeoutMS = 30_000
	esqMinTimeoutMS     = 1_000
	esqMaxTimeoutMS     = 120_000

	// ESQMaxResponseBytes is execute-esq's MCP-safe response budget (ExecuteEsqTool.MaxResponseSizeBytes).
	ESQMaxResponseBytes = 200_000
	// ESQToFileMaxResponseBytes is execute-esq-to-file's ceiling, ODataFileContract.MaxResponseBytes (64 MiB).
	ESQToFileMaxResponseBytes = 64 << 20

	esqResultTooLargeClass = "result-too-large"

	esqQueryShapeExample = " Supply the SelectQuery in the 'query' argument; schema-name, columns and row-count are not top-level arguments. " +
		"Minimal clio-run call (replace <environment-name>):\n" +
		"{\"command\":\"execute-esq\",\"args\":{\"environment-name\":\"<environment-name>\",\n" +
		"\"query\":{\"rootSchemaName\":\"Contact\",\"operationType\":0,\"allColumns\":false,\"rowCount\":1," +
		"\"columns\":{\"items\":{\"Id\":{\"expression\":{\"expressionType\":0,\"columnPath\":\"Id\"}}}}}}}"

	esqGuidanceHint142 = "ESQ is a Creatio-specific JSON format that is easy to get wrong from general API knowledge. " +
		"If you have not already, call get-guidance for 'esq' and 'esq-filters' and follow their shapes before " +
		"composing or retrying this query (common mistakes: wrong expressionType/comparisonType enum values, " +
		"invented filter properties, and plain ISO date strings instead of JSON-encoded date values)."
)

// ExecuteEsqResponse is clio 8.1.0.142's ExecuteEsqResponse.
type ExecuteEsqResponse struct {
	Success        bool            `json:"success"`
	Error          *string         `json:"error,omitempty"`
	Count          *int            `json:"count,omitempty"`
	Rows           json.RawMessage `json:"rows,omitempty"`
	Hint           *string         `json:"hint,omitempty"`
	ErrorClass     *string         `json:"error-class,omitempty"`
	OutputFile     *string         `json:"output-file,omitempty"`
	SuggestedQuery json.RawMessage `json:"suggested-query,omitempty"`
	Warnings       []string        `json:"warnings,omitempty"`
}

// ESQFailure142 is ExecuteEsqResponse.Failure: the message plus the ESQ guidance hint.
func ESQFailure142(message string) ExecuteEsqResponse {
	hint := esqGuidanceHint142
	return ExecuteEsqResponse{Error: &message, Hint: &hint}
}

// ESQFailureWithoutGuidance is ExecuteEsqResponse.FailureWithoutGuidance.
func ESQFailureWithoutGuidance(message string) ExecuteEsqResponse {
	return ExecuteEsqResponse{Error: &message}
}

func esqResultTooLarge(maximum int, suggestFileTwin bool) ExecuteEsqResponse {
	message := fmt.Sprintf("SelectQuery result exceeds the %d UTF-8 byte limit. ", maximum) +
		"Select explicit columns instead of allColumns, lower rowCount, or page with rowsOffset, then retry."
	if suggestFileTwin {
		message += " When every row is needed, run the same query with execute-esq-to-file, which writes the rows to a local file."
	}
	class := esqResultTooLargeClass
	return ExecuteEsqResponse{Error: &message, ErrorClass: &class}
}

// ESQPrepared is a query that passed every local check of ExecuteEsqTool.Run, ready to send.
type ESQPrepared struct {
	query      []byte
	rootSchema string
	columns    []string
	warnings   []string
}

// ESQPrepare runs ExecuteEsqTool.Run up to the remote call: the hints for SQL text or a flat call, the query
// normalization, the rootSchemaName check, the SelectQuery shape check and the environment-name check.
// query is the bound argument (nil when absent), extension the top-level keys the argument record does not
// bind, environmentName the bound environment-name (nil when absent). failure is the answer when the call
// must not run.
func ESQPrepare(query json.RawMessage, extension map[string]json.RawMessage, environmentName *string) (ESQPrepared, *ExecuteEsqResponse) {
	var node *jnode
	if len(bytes.TrimSpace(query)) > 0 {
		parsed, err := parseJNode(query)
		if err != nil {
			failure := ESQFailure142("query must be a JSON SelectQuery object." + esqQueryShapeExample)
			return ESQPrepared{}, &failure
		}
		node = parsed
	}
	if hint := esqHintTryBuild(node, extension); hint != nil {
		failure := esqFromHint(*hint)
		return ESQPrepared{}, &failure
	}
	normalized, problem := esqNormalizeQuery(node)
	if problem != "" {
		failure := ESQFailure142(problem + esqQueryShapeExample)
		return ESQPrepared{}, &failure
	}
	root := normalized.get("rootSchemaName")
	if root == nil || root.kind != jkString || strings.TrimFunc(root.text, dotnetIsWhiteSpace) == "" {
		failure := ESQFailure142("query must be a SelectQuery object with a non-empty 'rootSchemaName'." + esqQueryShapeExample)
		return ESQPrepared{}, &failure
	}
	warnings, hint := esqCheckSelectQueryShape(normalized, extension)
	if hint != nil {
		failure := esqFromHint(*hint)
		return ESQPrepared{}, &failure
	}
	if environmentName != nil && strings.TrimFunc(*environmentName, dotnetIsWhiteSpace) == "" {
		failure := ESQFailure142("environment-name is required.")
		return ESQPrepared{}, &failure
	}
	var body []byte
	if node.kind == jkString {
		body = []byte(strings.TrimFunc(node.text, esqJSONWhiteSpace))
	} else {
		body = bytes.TrimSpace(query)
	}
	return ESQPrepared{query: body, rootSchema: root.text, columns: esqRequestedColumns(normalized), warnings: warnings}, nil
}

func esqFromHint(hint esqHint) ExecuteEsqResponse {
	response := ESQFailure142(hint.message + esqQueryShapeExample)
	response.SuggestedQuery = hint.suggested
	return response
}

// esqNormalizeQuery is ExecuteEsqTool.TryNormalizeQuery: an object, or a string holding one.
func esqNormalizeQuery(node *jnode) (*jnode, string) {
	switch {
	case node != nil && node.kind == jkObject:
		return node, ""
	case node != nil && node.kind == jkString:
		parsed, err := parseJNode([]byte(node.text))
		if err != nil {
			return nil, redact.Text("query is not valid JSON: " + err.Error())
		}
		if parsed.kind != jkObject {
			return nil, "query string must contain a JSON SelectQuery object."
		}
		return parsed, ""
	default:
		return nil, "query must be a JSON SelectQuery object."
	}
}

func esqRequestedColumns(query *jnode) []string {
	columns := query.get("columns")
	if !columns.isObject() {
		return nil
	}
	items := columns.get("items")
	if !items.isObject() {
		return nil
	}
	return append([]string(nil), items.keys...)
}

// ESQExecute sends a prepared query and reads the answer as ExecuteEsqTool.Run does. maxResponseBytes is the
// ceiling (200 000 inline, 64 MiB for the file twin); suggestFileTwin adds the pointer to
// execute-esq-to-file to the too-large refusal.
func (c *Client) ESQExecute(ctx context.Context, prepared ESQPrepared, timeoutMS *int, maxResponseBytes int, suggestFileTwin bool) ExecuteEsqResponse {
	timeout := esqDefaultTimeoutMS
	if timeoutMS != nil {
		timeout = min(max(*timeoutMS, esqMinTimeoutMS), esqMaxTimeoutMS)
	}
	response, err := c.serviceRequest(ctx, serviceCall{Route: "DataService/json/SyncReply/SelectQuery", Body: prepared.query,
		Timeout: time.Duration(timeout) * time.Millisecond, Limit: maxResponseBytes})
	if err != nil {
		switch {
		case errors.Is(err, errResponseTooLarge):
			return esqWithWarnings(esqResultTooLarge(maxResponseBytes, suggestFileTwin), prepared.warnings)
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
			return ESQFailureWithoutGuidance(fmt.Sprintf("SelectQuery request timed out or was canceled (timeout window %d-%d ms). "+
				"Increase 'timeout' or narrow the query, then retry.", esqMinTimeoutMS, esqMaxTimeoutMS))
		default:
			return ESQFailure142(redact.Text(err.Error()))
		}
	}
	body := strings.TrimPrefix(string(response.payload), "\uFEFF")
	return esqWithWarnings(esqParseResponse(body, prepared.columns, prepared.rootSchema), prepared.warnings)
}

func esqWithWarnings(response ExecuteEsqResponse, warnings []string) ExecuteEsqResponse {
	if len(warnings) > 0 {
		response.Warnings = warnings
	}
	return response
}

// esqParseResponse is ExecuteEsqTool.ParseSelectQueryResponse.
func esqParseResponse(body string, requestedColumns []string, rootSchema string) ExecuteEsqResponse {
	if strings.TrimFunc(body, dotnetIsWhiteSpace) == "" {
		return ESQFailure142("SelectQuery returned an empty response.")
	}
	root, err := parseJNode([]byte(body))
	if err != nil {
		return ESQFailure142("Failed to parse SelectQuery response: " + redact.Text(err.Error()) + " | Response: " + esqTruncateRedacted(body))
	}
	if root.kind != jkObject {
		return ESQFailure142("Failed to parse SelectQuery response: " + redact.Text(fmt.Sprintf(
			"The requested operation requires an element of type 'Object', but the target element has type '%s'.", esqKindName(root))) +
			" | Response: " + esqTruncateRedacted(body))
	}
	success := root.get("success")
	hasSuccess := success != nil
	successIsTrue := hasSuccess && success.kind == jkBool && success.flag
	rows := root.get("rows")
	hasRowsArray := rows != nil && rows.kind == jkArray
	explicitFailure := hasSuccess && !successIsTrue
	unmarked := !successIsTrue && !hasRowsArray &&
		(root.get("responseStatus") != nil || root.get("errorInfo") != nil || root.get("ExceptionMessage") != nil || root.get("Message") != nil)
	if explicitFailure || unmarked {
		if message, ok := esqExtractErrorMessage(root); ok {
			return ESQFailure142(redact.Text(message))
		}
		return ESQFailure142(esqTruncateRedacted(body))
	}
	var raw struct {
		Rows json.RawMessage `json:"rows"`
	}
	if hasRowsArray {
		if message := esqDetectUnknownColumns(rows, requestedColumns, rootSchema); message != "" {
			return ESQFailure142(message)
		}
		_ = esqRawMember([]byte(body), "rows", &raw.Rows)
		count := len(rows.items)
		return ExecuteEsqResponse{Success: true, Count: &count, Rows: raw.Rows}
	}
	return ExecuteEsqResponse{Success: true, Rows: json.RawMessage(strings.TrimFunc(body, esqJSONWhiteSpace))}
}

func esqJSONWhiteSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' }

// esqRawMember copies the raw text of one top-level member, exactly as it arrived (the last one of that name,
// as JsonElement.TryGetProperty reads it).
func esqRawMember(body []byte, name string, target *json.RawMessage) error {
	object, ok := dataWriteParseObjectLast(body)
	if !ok {
		return errors.New("not an object")
	}
	if value, ok := object[name]; ok {
		*target = append(json.RawMessage(nil), bytes.TrimFunc(value, esqJSONWhiteSpace)...)
	}
	return nil
}

func dataWriteParseObjectLast(raw []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func esqKindName(node *jnode) string {
	switch node.kind {
	case jkArray:
		return "Array"
	case jkString:
		return "String"
	case jkInteger, jkFloat:
		return "Number"
	case jkBool:
		if node.flag {
			return "True"
		}
		return "False"
	default:
		return "Null"
	}
}

func esqDetectUnknownColumns(rows *jnode, requested []string, rootSchema string) string {
	if len(requested) == 0 || len(rows.items) == 0 {
		return ""
	}
	present := map[string]bool{}
	for _, row := range rows.items {
		if !row.isObject() {
			return ""
		}
		for _, key := range row.keys {
			present[key] = true
		}
	}
	var missing []string
	for _, alias := range requested {
		if !present[alias] {
			missing = append(missing, "'"+alias+"'")
		}
	}
	if len(missing) == 0 {
		return ""
	}
	suffix := ""
	if strings.TrimFunc(rootSchema, dotnetIsWhiteSpace) != "" {
		suffix = " on schema '" + rootSchema + "'"
	}
	word := "columns"
	if len(missing) == 1 {
		word = "column"
	}
	return "unknown " + word + " " + strings.Join(missing, ", ") + suffix + ": the requested column was not returned by the " +
		"SelectQuery, which means its columnPath did not resolve. Verify the column path against the schema " +
		"(get-entity-schema-properties / find-entity-schema) and read the 'esq' guidance for the columns shape."
}

func esqExtractErrorMessage(root *jnode) (string, bool) {
	if status := root.get("responseStatus"); status.isObject() {
		if message := status.get("Message"); message != nil && message.kind == jkString {
			if code := status.get("ErrorCode"); code != nil && code.kind == jkString && strings.TrimFunc(code.text, dotnetIsWhiteSpace) != "" {
				return code.text + ": " + message.text, true
			}
			return message.text, true
		}
	}
	if info := root.get("errorInfo"); info.isObject() {
		if message := info.get("message"); message != nil && message.kind == jkString {
			return message.text, true
		}
	}
	if message := root.get("ExceptionMessage"); message != nil && message.kind == jkString {
		return message.text, true
	}
	if message := root.get("Message"); message != nil && message.kind == jkString {
		return message.text, true
	}
	return "", false
}

// esqTruncateRedacted is ExecuteEsqTool.TruncateRedacted: redacted, then cut at 500 UTF-16 units.
func esqTruncateRedacted(value string) string {
	value = redact.Text(value)
	units := utf16.Encode([]rune(value))
	if len(units) > 500 {
		return string(utf16.Decode(units[:500])) + "..."
	}
	return value
}
