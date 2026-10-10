package creatio

// execute-dataservice-batch, ported from clio master 914dab286 (DataServiceBatchCommand.cs,
// DataServiceBatchService): 1–100 explicit insert/update/delete queries submitted ONCE through
// DataService BatchQuery with continueIfError, each correlated back to its input by a fresh queryId.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dataWriteBatchRoute        = "DataService/json/SyncReply/BatchQuery"
	dataWriteBatchMaximumBytes = 200_000
	dataWriteBatchTimeout      = 30 * time.Second
	dataWriteBatchCompleted    = "completed"
	dataWriteBatchFailed       = "failed"
	dataWriteBatchUnknown      = "unknown"
	dataWriteBatchAdvice       = "One batch was submitted without retry. Continuation is enabled; atomicity is not guaranteed. Results are native acknowledgements, not independent readback. Verify affected records before resubmitting unknown items. Use native sequence enrollment for lifecycle changes."
	// dataWriteNoServerTextCause is clio's ServerReportedFailureText.NoServerTextCause.
	dataWriteNoServerTextCause = "the environment reported an unsuccessful response without an error message."
)

// DataWriteBatchValue is clio's DataServiceBatchValue: a typed scalar. Present is false when the input
// object carried no "value" member (a default JsonElement in clio, which no type accepts).
type DataWriteBatchValue struct {
	DataValueType int
	Value         json.RawMessage
	Present       bool
}

// DataWriteBatchOperation is clio's DataServiceBatchOperation. Values keeps the caller's column order;
// a nil entry is a JSON null value object. ValuesNull is a missing or null values member.
type DataWriteBatchOperation struct {
	Operation  *string
	SchemaName *string
	RecordID   string
	ValueNames []string
	Values     map[string]*DataWriteBatchValue
	Null       bool
}

// DataWriteBatchItemResult is clio's DataServiceBatchItemResult.
type DataWriteBatchItemResult struct {
	Index        int                  `json:"index"`
	RecordID     string               `json:"record-id"`
	State        string               `json:"state"`
	RowsAffected *int                 `json:"rows-affected,omitempty"`
	Error        *string              `json:"error,omitempty"`
	Diagnostic   *DataWriteDiagnostic `json:"diagnostic,omitempty"`
}

// DataWriteBatchResult is clio's DataServiceBatchResult.
type DataWriteBatchResult struct {
	Success        bool                       `json:"success"`
	CompletedCount int                        `json:"completed-count"`
	FailedCount    int                        `json:"failed-count"`
	UnknownCount   int                        `json:"unknown-count"`
	Items          []DataWriteBatchItemResult `json:"items"`
	NextStep       string                     `json:"next-step"`
}

// ErrDataWriteBatchBinding is the failure of binding a value inside the operations argument to clio's
// records (a record-id that is not a UUID, a non-integer data-value-type, a member of the wrong JSON type);
// ErrDataWriteBatchNotArray is an operations argument that is not an array at all. clio's binder words
// them differently.
var (
	ErrDataWriteBatchBinding  = errors.New("execute-dataservice-batch operations do not bind")
	ErrDataWriteBatchNotArray = errors.New("execute-dataservice-batch operations is not an array")
)

// DataWriteBatchDecode binds the raw operations argument the way System.Text.Json binds clio's
// DataServiceBatchOperation[]: unknown members are ignored, a missing record-id is the empty UUID.
func DataWriteBatchDecode(raw json.RawMessage) ([]*DataWriteBatchOperation, error) {
	root, err := parseJNode(raw)
	if err != nil {
		return nil, ErrDataWriteBatchNotArray
	}
	if root.kind == jkNull {
		return nil, nil
	}
	if !root.isArray() {
		return nil, ErrDataWriteBatchNotArray
	}
	operations := make([]*DataWriteBatchOperation, 0, len(root.items))
	for _, item := range root.items {
		if item.kind == jkNull {
			operations = append(operations, &DataWriteBatchOperation{Null: true})
			continue
		}
		if !item.isObject() {
			return nil, ErrDataWriteBatchBinding
		}
		operation := &DataWriteBatchOperation{RecordID: emptyGUID}
		var bindErr error
		if operation.Operation, bindErr = dataWriteBatchString(item.get("operation")); bindErr != nil {
			return nil, bindErr
		}
		if operation.SchemaName, bindErr = dataWriteBatchString(item.get("schema-name")); bindErr != nil {
			return nil, bindErr
		}
		if id := item.get("record-id"); id != nil {
			if id.kind != jkString || !dataWriteIsGUID(id.text) {
				return nil, ErrDataWriteBatchBinding
			}
			operation.RecordID = strings.ToLower(id.text)
		}
		if values := item.get("values"); values != nil && values.kind != jkNull {
			if !values.isObject() {
				return nil, ErrDataWriteBatchBinding
			}
			operation.Values = map[string]*DataWriteBatchValue{}
			for _, name := range values.keys {
				value := values.props[name]
				operation.ValueNames = append(operation.ValueNames, name)
				if value.kind == jkNull {
					operation.Values[name] = nil
					continue
				}
				if !value.isObject() {
					return nil, ErrDataWriteBatchBinding
				}
				bound := &DataWriteBatchValue{}
				if dataType := value.get("data-value-type"); dataType != nil {
					number, ok := dataWriteBatchBindInt32(dataType)
					if !ok {
						return nil, ErrDataWriteBatchBinding
					}
					bound.DataValueType = number
				}
				if member := value.get("value"); member != nil {
					bound.Present = true
					bound.Value = json.RawMessage(member.stjJSON())
				}
				operation.Values[name] = bound
			}
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func dataWriteBatchString(node *jnode) (*string, error) {
	if node == nil || node.kind == jkNull {
		return nil, nil
	}
	if node.kind != jkString {
		return nil, ErrDataWriteBatchBinding
	}
	text := node.text
	return &text, nil
}

// dataWriteBatchInt32 is JsonElement.TryGetInt32: an integral JSON number in the Int32 range.
func dataWriteBatchInt32(node *jnode) (int, bool) {
	if node == nil || node.kind != jkInteger {
		return 0, false
	}
	value, err := strconv.ParseInt(node.text, 10, 32)
	return int(value), err == nil
}

// dataWriteBatchBindInt32 binds an int member the way clio's MCP serializer does (web defaults: a number,
// or a string holding one).
func dataWriteBatchBindInt32(node *jnode) (int, bool) {
	if node != nil && node.kind == jkString {
		value, err := strconv.ParseInt(node.text, 10, 32)
		return int(value), err == nil
	}
	return dataWriteBatchInt32(node)
}

// ExecuteDataServiceBatch is DataServiceBatchService.Execute. A validation failure is returned as err, which
// clio raises from the tool.
func (c *Client) ExecuteDataServiceBatch(ctx context.Context, operations []*DataWriteBatchOperation) (DataWriteBatchResult, error) {
	if len(operations) < 1 || len(operations) > 100 {
		return DataWriteBatchResult{}, errors.New("Provide 1–100 explicit operations.")
	}
	for _, operation := range operations {
		if err := dataWriteBatchValidate(operation); err != nil {
			return DataWriteBatchResult{}, err
		}
	}
	queryIDs := make([]string, len(operations))
	queries := newArray()
	for index, operation := range operations {
		queryIDs[index] = newGUID()
		queries.items = append(queries.items, dataWriteBatchQuery(operation, queryIDs[index]))
	}
	envelope := newObject()
	envelope.set("items", queries)
	envelope.set("continueIfError", newBool(true))
	body := envelope.stjJSON()
	if len(body) > dataWriteBatchMaximumBytes {
		return DataWriteBatchResult{}, errors.New("Encoded batch exceeds the 200000-byte limit.")
	}
	var results []DataWriteBatchItemResult
	responseReceived := false
	response, err := c.dataWriteSendOnce(ctx, "POST", dataWriteBatchRoute, body, dataWriteBatchTimeout)
	if err == nil {
		responseReceived = true
		results, err = dataWriteBatchReadResponse(response, operations, queryIDs)
	}
	if err != nil {
		message := DataWriteUntrusted(err.Error())
		if message == nil {
			message = dataWriteText("Batch response unavailable.")
		}
		results = make([]DataWriteBatchItemResult, len(operations))
		for index, operation := range operations {
			results[index] = DataWriteBatchItemResult{Index: index, RecordID: operation.RecordID, State: dataWriteBatchUnknown, Error: message}
		}
	}
	completed, failed := 0, 0
	for index := range results {
		item := &results[index]
		operation := operations[item.Index]
		itemIndex := item.Index
		item.Diagnostic = NewDataWriteDiagnostic(*operation.Operation, *operation.SchemaName, &itemIndex, true,
			responseReceived, item.State == dataWriteBatchCompleted, item.Error)
		switch item.State {
		case dataWriteBatchCompleted:
			completed++
		case dataWriteBatchFailed:
			failed++
		}
	}
	return DataWriteBatchResult{Success: completed == len(operations), CompletedCount: completed, FailedCount: failed,
		UnknownCount: len(results) - completed - failed, Items: results, NextStep: dataWriteBatchAdvice}, nil
}

func dataWriteBatchValidate(operation *DataWriteBatchOperation) error {
	if operation == nil || operation.Null || operation.RecordID == emptyGUID || operation.SchemaName == nil ||
		!dataWriteIdentifierPattern.MatchString(*operation.SchemaName) || operation.Operation == nil ||
		(*operation.Operation != "insert" && *operation.Operation != "update" && *operation.Operation != "delete") {
		return errors.New("Each operation needs insert/update/delete, a schema-name and a non-empty record-id.")
	}
	count := len(operation.ValueNames)
	if (*operation.Operation == "delete" && count != 0) || (*operation.Operation != "delete" && (count < 1 || count > 50)) {
		return errors.New("Insert/update require 1–50 values; delete accepts no values.")
	}
	for _, name := range operation.ValueNames {
		value := operation.Values[name]
		if !dataWriteIdentifierPattern.MatchString(name) || strings.EqualFold(name, "Id") || value == nil || !dataWriteBatchScalar(value) {
			return errors.New("Values require column identifiers other than Id and compatible scalar DataService types (0–12).")
		}
	}
	return nil
}

// dataWriteBatchScalar is DataServiceBatchService.IsScalar.
func dataWriteBatchScalar(value *DataWriteBatchValue) bool {
	if value.DataValueType < 0 || value.DataValueType > 12 {
		return false
	}
	if !value.Present {
		return false
	}
	node, err := parseJNode(value.Value)
	if err != nil {
		return false
	}
	if node.kind == jkNull {
		return true
	}
	switch value.DataValueType {
	case 0, 10:
		return node.kind == jkString && dataWriteIsGUID(node.text)
	case 1, 2, 3, 7, 8, 9:
		return node.kind == jkString
	case 4, 11:
		_, ok := dataWriteBatchInt32(node)
		return ok
	case 5, 6:
		return (node.kind == jkInteger || node.kind == jkFloat) && dataWriteBatchDecimal(string(value.Value))
	case 12:
		return node.kind == jkBool
	}
	return false
}

// dataWriteBatchDecimal is JsonElement.TryGetDecimal: a number within System.Decimal's range.
func dataWriteBatchDecimal(text string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return err == nil && math.Abs(parsed) < 7.9228162514264337593543950335e28
}

func dataWriteBatchQuery(operation *DataWriteBatchOperation, queryID string) *jnode {
	kind, typeName, operationType := *operation.Operation, "DeleteQuery", 3
	switch kind {
	case "insert":
		typeName, operationType = "InsertQuery", 1
	case "update":
		typeName, operationType = "UpdateQuery", 2
	}
	query := newObject()
	query.set("__type", newString("Terrasoft.Nui.ServiceModel.DataContract."+typeName))
	query.set("queryId", newString(queryID))
	query.set("rootSchemaName", newString(*operation.SchemaName))
	query.set("operationType", newInt(operationType))
	if kind != "insert" {
		query.set("filters", dataWriteBatchIDFilter(operation.RecordID))
	}
	if kind != "delete" {
		values := newObject()
		for _, name := range operation.ValueNames {
			value := operation.Values[name]
			parsed, _ := parseJNode(value.Value)
			values.set(name, dataWriteBatchParameter(value.DataValueType, parsed))
		}
		if kind == "insert" {
			values.set("Id", dataWriteBatchParameter(0, newString(operation.RecordID)))
		}
		columnValues := newObject()
		columnValues.set("items", values)
		query.set("columnValues", columnValues)
	}
	return query
}

func dataWriteBatchParameter(dataValueType int, value *jnode) *jnode {
	parameter := newObject()
	parameter.set("dataValueType", newInt(dataValueType))
	parameter.set("value", value)
	expression := newObject()
	expression.set("expressionType", newInt(2))
	expression.set("parameter", parameter)
	return expression
}

// dataWriteBatchIDFilter is the filters member of SelectQueryHelper.BuildSelectQuery with one Id filter.
func dataWriteBatchIDFilter(recordID string) *jnode {
	left := newObject()
	left.set("expressionType", newInt(0))
	left.set("columnPath", newString("Id"))
	parameter := newObject()
	parameter.set("value", newString(recordID))
	parameter.set("dataValueType", newInt(0))
	right := newObject()
	right.set("expressionType", newInt(2))
	right.set("parameter", parameter)
	filter := newObject()
	filter.set("filterType", newInt(1))
	filter.set("comparisonType", newInt(3))
	filter.set("isEnabled", newBool(true))
	filter.set("trimDateTimeParameterToDate", newBool(false))
	filter.set("leftExpression", left)
	filter.set("rightExpression", right)
	items := newObject()
	items.set("filter0", filter)
	group := newObject()
	group.set("filterType", newInt(6))
	group.set("isEnabled", newBool(true))
	group.set("trimDateTimeParameterToDate", newBool(false))
	group.set("logicalOperation", newInt(0))
	group.set("items", items)
	return group
}

// dataWriteBatchReadResponse is the part of Execute after the response arrived: the size cap, parsing and
// ReadResults. An error turns every item into unknown with its text.
func dataWriteBatchReadResponse(response string, operations []*DataWriteBatchOperation, queryIDs []string) ([]DataWriteBatchItemResult, error) {
	if len(response) > dataWriteBatchMaximumBytes {
		return nil, errors.New("Batch response exceeded the 200000-byte limit.")
	}
	root, err := parseJNode([]byte(response))
	if err != nil {
		return nil, errors.New(dataWriteSTJParseError(response))
	}
	if !root.isObject() {
		return nil, fmt.Errorf("The requested operation requires an element of type 'Object', but the target element has type '%s'.", dataWriteSTJKind(root))
	}
	nativeResults := root.get("queryResults")
	if nativeResults == nil || !nativeResults.isArray() {
		if text := dataWriteBatchReadError(root); text != nil {
			return nil, errors.New(*text)
		}
		return nil, errors.New("Batch response omitted per-item outcomes; verify affected records before retrying.")
	}
	known := map[string]bool{}
	for _, id := range queryIDs {
		known[id] = true
	}
	for _, row := range nativeResults.items {
		if !row.isObject() {
			return nil, fmt.Errorf("The requested operation requires an element of type 'Object', but the target element has type '%s'.", dataWriteSTJKind(row))
		}
		id := row.get("queryId")
		if id == nil || id.kind != jkString || !dataWriteIsGUID(id.text) || !known[strings.ToLower(id.text)] {
			return nil, errors.New("Batch response contained an uncorrelated query result.")
		}
	}
	results := make([]DataWriteBatchItemResult, len(operations))
	for index, operation := range operations {
		var matches []*jnode
		for _, row := range nativeResults.items {
			if strings.ToLower(row.get("queryId").text) == queryIDs[index] {
				matches = append(matches, row)
			}
		}
		var success *jnode
		if len(matches) == 1 {
			success = matches[0].get("success")
		}
		if success == nil || success.kind != jkBool {
			results[index] = DataWriteBatchItemResult{Index: index, RecordID: operation.RecordID, State: dataWriteBatchUnknown,
				Error: dataWriteText("Missing, duplicate or malformed native result; verify the record before resubmission.")}
			continue
		}
		match := matches[0]
		var affected *int
		if count := match.get("rowsAffected"); count != nil && count.kind == jkInteger {
			if number, ok := dataWriteBatchInt32(count); ok && number >= 0 {
				affected = &number
			}
		}
		text := dataWriteBatchReadError(match)
		if text == nil && !success.flag {
			text = dataWriteText(dataWriteNoServerTextCause)
		}
		state := dataWriteBatchFailed
		if success.flag {
			state = dataWriteBatchCompleted
		}
		results[index] = DataWriteBatchItemResult{Index: index, RecordID: operation.RecordID, State: state, RowsAffected: affected, Error: text}
	}
	return results, nil
}

// dataWriteBatchReadError is DataServiceBatchService.ReadError: responseStatus.Message (or message), else
// errorInfo.message, as fenced untrusted text.
func dataWriteBatchReadError(node *jnode) *string {
	var text *string
	if status := node.get("responseStatus"); status.isObject() {
		message := status.get("Message")
		if message == nil {
			message = status.get("message")
		}
		if message != nil && message.kind == jkString {
			text = DataWriteUntrusted(message.text)
		}
	}
	if text == nil {
		if info := node.get("errorInfo"); info.isObject() {
			if detail := info.get("message"); detail != nil && detail.kind == jkString {
				text = DataWriteUntrusted(detail.text)
			}
		}
	}
	return text
}

// dataWriteSTJKind is JsonValueKind's name for a node.
func dataWriteSTJKind(node *jnode) string {
	switch node.kind {
	case jkObject:
		return "Object"
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
	}
	return "Null"
}

// dataWriteSTJParseError approximates JsonDocument.Parse's exception text for the usual invalid bodies: an
// empty body and a body whose first character cannot start a JSON value (an HTML page, plain text).
func dataWriteSTJParseError(body string) string {
	trimmed := strings.TrimLeft(body, " \t\r\n")
	if trimmed == "" {
		return "The input does not contain any JSON tokens. Expected the input to start with a valid JSON token, while isFinalBlock is true. LineNumber: 0 | BytePositionInLine: 0."
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	if !strings.ContainsRune("{[\"-0123456789tfn", first) {
		line := strings.Count(body[:len(body)-len(trimmed)], "\n")
		position := len(body) - len(trimmed) - (strings.LastIndex(body[:len(body)-len(trimmed)], "\n") + 1)
		return fmt.Sprintf("'%c' is an invalid start of a value. LineNumber: %d | BytePositionInLine: %d.", first, line, position)
	}
	return "The response is not valid JSON."
}
