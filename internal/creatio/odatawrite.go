package creatio

// odata-create, odata-update and odata-delete, ported from clio master 914dab286 (ODataCreateTool,
// ODataUpdateTool, ODataDeleteTool, ODataKeyedWrite, ODataFieldValidation, ODataDateTimeGuard, the
// rows-file part of ODataFileContract). The answer types carry clio's JSON names.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// ODataWriteResponse is clio's ODataWriteResponse (odata-update, odata-delete).
type ODataWriteResponse struct {
	Success       bool                 `json:"success"`
	Error         *string              `json:"error,omitempty"`
	ID            *string              `json:"id,omitempty"`
	CorrelationID string               `json:"correlation-id,omitempty"`
	Diagnostic    *DataWriteDiagnostic `json:"diagnostic,omitempty"`
}

// ODataCreateBatchResponse is clio's ODataCreateBatchResponse (odata-create).
type ODataCreateBatchResponse struct {
	Diagnostic    *DataWriteDiagnostic `json:"diagnostic,omitempty"`
	Created       int                  `json:"created"`
	Failed        int                  `json:"failed"`
	Unverified    int                  `json:"unverified"`
	Results       []ODataRowResult     `json:"results"`
	Error         *string              `json:"error,omitempty"`
	CorrelationID string               `json:"correlation-id,omitempty"`
}

// ODataRowResult is clio's per-row outcome. RecordCreated nil is the "unknown" state; clio's MCP serializer
// omits null members, so it is omitted here too.
type ODataRowResult struct {
	Diagnostic       *DataWriteDiagnostic `json:"diagnostic,omitempty"`
	Index            int                  `json:"index"`
	Success          bool                 `json:"success"`
	ID               *string              `json:"id,omitempty"`
	Error            *string              `json:"error,omitempty"`
	RecordCreated    *bool                `json:"record-created,omitempty"`
	RetryGuidance    *string              `json:"retry-guidance,omitempty"`
	responseReceived bool
}

func dataWriteText(text string) *string { return &text }

func dataWriteBool(value bool) *bool { return &value }

// ODataCreateRequest is odata-create's arguments as the tool layer read them. Rows is nil when absent
// or null. ArgumentError is the refusal of unbound keys (clio's BuildLegacyAliasError), "" when none.
type ODataCreateRequest struct {
	Entity        string
	Rows          json.RawMessage
	RowsFile      string
	StopOnError   bool
	ArgumentError string
}

// ODataKeyedRequest is odata-update's and odata-delete's arguments.
type ODataKeyedRequest struct {
	Entity        string
	ID            string
	Data          json.RawMessage
	RowsFile      string
	Confirm       bool
	ArgumentError string
}

const (
	odataCreateMaxRows        = 1000
	odataCreateRowTimeout     = 30 * time.Second
	odataCreateBatchBudget    = 5 * time.Minute
	odataCreateMinRowBudget   = time.Second
	odataWriteTimeout         = 30 * time.Second
	odataUnknownSideEffect    = "Side effect UNKNOWN: Creatio may have written the record before failing (a post-insert entity event handler that throws is reported as a failed request). Do NOT retry blindly - it may duplicate the row. Read the entity back (odata-read, filtering on the values you sent) and re-send only if it is absent."
	odataRowsRequiredMessage  = "rows is required and must be a non-empty array of field/value objects."
	odataDataRequiredMessage  = "data is required and must be a non-empty object of field/value pairs."
	odataInvalidEntityMessage = "entity must be a valid OData entity set name (letters, digits, underscore)."
)

// ODataCreate is clio's ODataCreateTool.Create. client is the resolved environment, or nil with
// resolveFailure set: clio resolves after its own argument checks, so the failure is reported only once
// those pass.
func ODataCreate(ctx context.Context, client *Client, resolveFailure error, request ODataCreateRequest) ODataCreateBatchResponse {
	correlationID := dataWriteCorrelationID()
	result := odataCreateCore(ctx, client, resolveFailure, request)
	result.CorrelationID = correlationID
	if result.Error != nil {
		result.Diagnostic = NewDataWriteDiagnostic("insert", request.Entity, nil, false, false, false, result.Error)
	}
	for index := range result.Results {
		row := &result.Results[index]
		attempted := row.RecordCreated == nil || *row.RecordCreated
		itemIndex := row.Index
		row.Diagnostic = NewDataWriteDiagnostic("insert", request.Entity, &itemIndex, attempted, row.responseReceived, row.Success, row.Error)
	}
	return result
}

func odataCreateRequestError(message string) ODataCreateBatchResponse {
	return ODataCreateBatchResponse{Results: []ODataRowResult{}, Error: &message}
}

func odataCreateCore(ctx context.Context, client *Client, resolveFailure error, request ODataCreateRequest) ODataCreateBatchResponse {
	if request.ArgumentError != "" {
		return odataCreateRequestError(request.ArgumentError)
	}
	if dataWriteBlank(request.Entity) {
		return odataCreateRequestError("entity is required.")
	}
	if !dataWriteValidEntity(request.Entity) {
		return odataCreateRequestError(odataInvalidEntityMessage)
	}
	rows, failure := odataCreateResolveRows(request)
	if failure != "" {
		return odataCreateRequestError(failure)
	}
	if resolveFailure != nil {
		return odataCreateRequestError(redact.Text(resolveFailure.Error()))
	}
	path := "odata/" + dataWriteTrim(request.Entity)
	var propertyTypes map[string]string
	for _, row := range rows {
		if odataHasZoneLessCandidate(row) {
			propertyTypes = client.odataTryGetPropertyTypes(ctx, dataWriteTrim(request.Entity))
			break
		}
	}
	results := client.odataPostRows(ctx, path, rows, request.StopOnError, propertyTypes)
	response := ODataCreateBatchResponse{Results: results}
	for _, row := range results {
		if row.Success {
			response.Created++
			continue
		}
		response.Failed++
		if row.RecordCreated == nil {
			response.Unverified++
		}
	}
	return response
}

func odataCreateResolveRows(request ODataCreateRequest) ([]json.RawMessage, string) {
	hasRowsFile := !dataWriteBlank(request.RowsFile)
	if request.Rows != nil && hasRowsFile {
		return nil, "Provide either rows or rows-file, not both."
	}
	requested := request.Rows
	if requested == nil && hasRowsFile {
		text, failure := DataWriteReadJSONFile(request.RowsFile, "rows-file")
		if failure != "" {
			return nil, failure
		}
		if err := dataWriteValidJSON([]byte(text)); err != nil {
			return nil, "rows-file must contain valid JSON: " + err.Error()
		}
		requested = json.RawMessage(text)
	}
	var rows []json.RawMessage
	if requested == nil || dataWriteJSONKind(requested) != 'a' || json.Unmarshal(requested, &rows) != nil || len(rows) == 0 {
		return nil, odataRowsRequiredMessage
	}
	if len(rows) > odataCreateMaxRows {
		return nil, fmt.Sprintf("rows contains %d entries, which exceeds the %d-row limit for one call. "+
			"Split the input into chunks of at most %d rows and submit them separately.", len(rows), odataCreateMaxRows, odataCreateMaxRows)
	}
	return rows, ""
}

func dataWriteValidJSON(payload []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("'}' or ']' is invalid after a single JSON value. Expected end of data.")
	}
	return nil
}

func (c *Client) odataPostRows(ctx context.Context, path string, rows []json.RawMessage, stopOnError bool,
	propertyTypes map[string]string) []ODataRowResult {
	results := []ODataRowResult{}
	started := time.Now()
	for index, row := range rows {
		remaining := odataCreateBatchBudget - time.Since(started)
		if remaining < 0 {
			remaining = 0
		}
		abort := ""
		if ctx.Err() != nil {
			abort = "row was not attempted: the batch was cancelled."
		} else if remaining < odataCreateMinRowBudget {
			abort = "row was not attempted: the batch exceeded its wall-clock budget. Re-send the remaining rows as a smaller batch."
		}
		if abort != "" {
			results = append(results, ODataRowResult{Index: index, RecordCreated: dataWriteBool(false), Error: &abort})
			break
		}
		timeout := odataCreateRowTimeout
		if remaining < timeout {
			timeout = remaining
		}
		result := c.odataCreateRow(ctx, path, row, index, timeout, propertyTypes)
		results = append(results, result)
		if !result.Success && stopOnError {
			break
		}
	}
	return results
}

func (c *Client) odataCreateRow(ctx context.Context, path string, row json.RawMessage, index int, timeout time.Duration,
	propertyTypes map[string]string) ODataRowResult {
	object, ok := dataWriteParseObject(row)
	if !ok || len(object.keys) == 0 {
		return ODataRowResult{Index: index, RecordCreated: dataWriteBool(false),
			Error: dataWriteText("row must be a non-empty object of field/value pairs.")}
	}
	if zoneLess := odataFindZoneLessDateTime(row, propertyTypes); zoneLess != "" {
		return ODataRowResult{Index: index, RecordCreated: dataWriteBool(false), Error: &zoneLess}
	}
	body, err := c.dataWriteSendOnce(ctx, http.MethodPost, path, row, timeout)
	if err != nil {
		return ODataRowResult{Index: index, RetryGuidance: dataWriteText(odataUnknownSideEffect),
			Error: dataWriteText(redact.Text(err.Error()))}
	}
	result := odataParseCreated(body, index)
	result.responseReceived = true
	return result
}

func odataParseCreated(body string, index int) ODataRowResult {
	unknown := func(message string) ODataRowResult {
		return ODataRowResult{Index: index, RetryGuidance: dataWriteText(odataUnknownSideEffect), Error: &message}
	}
	if dataWriteBlank(body) {
		return ODataRowResult{Index: index, Success: true, RecordCreated: dataWriteBool(true)}
	}
	if dataWriteValidJSON([]byte(body)) != nil {
		return unknown(redact.Text(dataWriteDescribeNonJSON(body)))
	}
	root := json.RawMessage(body)
	if serverError, ok := dataWriteDetectError(root, dataWriteODataContext); ok {
		return unknown(dataWriteAppendForeignKeyHint(redact.Text(serverError), root))
	}
	object, ok := dataWriteParseObject(root)
	if !ok {
		// System.Text.Json's TryGetProperty on a non-object throws; clio reports that exception's text.
		return unknown(fmt.Sprintf("The requested operation requires an element of type 'Object', but the target element has type '%s'.",
			dataWriteKindName(root)))
	}
	id := ""
	if raw, ok := object.raw("Id"); ok {
		switch dataWriteJSONKind(raw) {
		case 's':
			_ = json.Unmarshal(raw, &id)
		case 'n':
			id = string(bytes.TrimSpace(raw))
		}
	}
	if id == "" {
		return unknown("OData create did not return a record Id. The response was not a recognized creation acknowledgement; verify the target before retrying.")
	}
	return ODataRowResult{Index: index, Success: true, RecordCreated: dataWriteBool(true), ID: &id}
}

func dataWriteKindName(raw json.RawMessage) string {
	switch dataWriteJSONKind(raw) {
	case 'a':
		return "Array"
	case 's':
		return "String"
	case 'n':
		return "Number"
	case 't':
		return "True"
	case 'f':
		return "False"
	case 'z':
		return "Null"
	}
	return "Object"
}

// ---- odata-update / odata-delete ----

// ODataUpdate is clio's ODataUpdateTool.Update.
func ODataUpdate(ctx context.Context, client *Client, resolveFailure error, request ODataKeyedRequest) ODataWriteResponse {
	correlationID := dataWriteCorrelationID()
	result, attempted, received := odataUpdateCore(ctx, client, resolveFailure, request)
	result.CorrelationID = correlationID
	result.Diagnostic = NewDataWriteDiagnostic("update", request.Entity, nil, attempted, received, result.Success, result.Error)
	return result
}

// ODataDelete is clio's ODataDeleteTool.Delete.
func ODataDelete(ctx context.Context, client *Client, resolveFailure error, request ODataKeyedRequest) ODataWriteResponse {
	correlationID := dataWriteCorrelationID()
	result, attempted, received := odataDeleteCore(ctx, client, resolveFailure, request)
	result.CorrelationID = correlationID
	result.Diagnostic = NewDataWriteDiagnostic("delete", request.Entity, nil, attempted, received, result.Success, result.Error)
	return result
}

func odataWriteFailure(message string) ODataWriteResponse {
	return ODataWriteResponse{Error: &message}
}

func odataValidateTarget(entity, id, operation string) *ODataWriteResponse {
	var failure ODataWriteResponse
	switch {
	case dataWriteBlank(entity):
		failure = odataWriteFailure("entity is required.")
	case !dataWriteValidEntity(entity):
		failure = odataWriteFailure(odataInvalidEntityMessage)
	case dataWriteBlank(id) || !dataWriteIsGUID(dataWriteTrim(id)):
		failure = odataWriteFailure(fmt.Sprintf("id is required and must be a record GUID; keyless mass %s is not allowed.", operation))
	default:
		return nil
	}
	return &failure
}

func odataRequireConfirmation(confirm bool, entity, id, verb, consequence string) *ODataWriteResponse {
	if confirm {
		return nil
	}
	failure := odataWriteFailure(fmt.Sprintf("Refusing to %s %s(%s) without confirmation. "+
		"This is a destructive operation; re-call odata-%s with \"confirm\": true to authorize this %s.",
		verb, dataWriteTrim(entity), dataWriteTrim(id), verb, consequence))
	return &failure
}

func odataUpdateCore(ctx context.Context, client *Client, resolveFailure error, request ODataKeyedRequest) (ODataWriteResponse, bool, bool) {
	if request.ArgumentError != "" {
		return odataWriteFailure(request.ArgumentError), false, false
	}
	if invalid := odataValidateTarget(request.Entity, request.ID, "update"); invalid != nil {
		return *invalid, false, false
	}
	notConfirmed := odataRequireConfirmation(request.Confirm, request.Entity, request.ID, "update", "change")
	data, failure := odataUpdateResolveData(request, notConfirmed)
	if failure != nil {
		return *failure, false, false
	}
	if notConfirmed != nil {
		return *notConfirmed, false, false
	}
	if resolveFailure != nil {
		return odataWriteFailure(redact.Text(resolveFailure.Error())), false, false
	}
	entity, id := dataWriteTrim(request.Entity), dataWriteTrim(request.ID)
	object, _ := dataWriteParseObject(data)
	validation, propertyTypes := client.odataValidateDataFields(ctx, entity, id, object.keys)
	if validation != "" {
		return odataWriteFailure(validation), false, false
	}
	if zoneLess := odataFindZoneLessDateTime(data, propertyTypes); zoneLess != "" {
		return odataWriteFailure("odata-update rejected: " + zoneLess), false, false
	}
	response, err := client.dataWriteSendOnce(ctx, http.MethodPatch, dataWriteKeyPath(entity, id), data, odataWriteTimeout)
	if err != nil {
		return odataWriteFailure(redact.Text(err.Error())), true, false
	}
	if message := odataValidateWriteResponse(response); message != "" {
		return odataWriteFailure(message), true, true
	}
	return ODataWriteResponse{Success: true, ID: &id}, true, true
}

func odataUpdateResolveData(request ODataKeyedRequest, notConfirmed *ODataWriteResponse) (json.RawMessage, *ODataWriteResponse) {
	fail := func(message string) (json.RawMessage, *ODataWriteResponse) {
		failure := odataWriteFailure(message)
		return nil, &failure
	}
	hasRowsFile := !dataWriteBlank(request.RowsFile)
	if request.Data != nil && hasRowsFile {
		return fail("Provide either data or rows-file, not both.")
	}
	if request.Data == nil && !hasRowsFile {
		return fail(odataDataRequiredMessage)
	}
	data := request.Data
	if data == nil {
		if notConfirmed != nil {
			return nil, notConfirmed
		}
		text, failure := DataWriteReadJSONFile(request.RowsFile, "rows-file")
		if failure != "" {
			return fail(failure)
		}
		if err := dataWriteValidJSON([]byte(text)); err != nil {
			return fail("rows-file must contain valid JSON: " + err.Error())
		}
		data = json.RawMessage(text)
	}
	object, ok := dataWriteParseObject(data)
	if !ok || len(object.keys) == 0 {
		return fail(odataDataRequiredMessage)
	}
	return data, nil
}

func odataDeleteCore(ctx context.Context, client *Client, resolveFailure error, request ODataKeyedRequest) (ODataWriteResponse, bool, bool) {
	if invalid := odataValidateTarget(request.Entity, request.ID, "delete"); invalid != nil {
		return *invalid, false, false
	}
	if notConfirmed := odataRequireConfirmation(request.Confirm, request.Entity, request.ID, "delete", "deletion"); notConfirmed != nil {
		return *notConfirmed, false, false
	}
	if resolveFailure != nil {
		return odataWriteFailure(redact.Text(resolveFailure.Error())), false, false
	}
	id := dataWriteTrim(request.ID)
	response, err := client.dataWriteSendOnce(ctx, http.MethodDelete, dataWriteKeyPath(request.Entity, id), []byte{}, odataWriteTimeout)
	if err != nil {
		return odataWriteFailure(redact.Text(err.Error())), true, false
	}
	if message := odataValidateWriteResponse(response); message != "" {
		return odataWriteFailure(message), true, true
	}
	return ODataWriteResponse{Success: true, ID: &id}, true, true
}

// odataValidateWriteResponse is ODataKeyedWrite.ValidateWriteResponse: an empty body is Creatio's 204,
// a recognized error shape or a body that is not JSON is a failure.
func odataValidateWriteResponse(response string) string {
	if dataWriteBlank(response) {
		return ""
	}
	if dataWriteValidJSON([]byte(response)) != nil {
		return redact.Text(dataWriteDescribeNonJSON(response))
	}
	if serverError, ok := dataWriteDetectError(json.RawMessage(response), dataWriteODataContext); ok {
		return dataWriteAppendForeignKeyHint(redact.Text(serverError), json.RawMessage(response))
	}
	return ""
}

// ---- ODataDateTimeGuard ----

var odataZoneLessPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?$`)

var odataTemporalTypes = map[string]bool{"Edm.DateTimeOffset": true, "Edm.DateTime": true, "Edm.Date": true}

func odataIsZoneLess(value string) bool {
	return odataZoneLessPattern.MatchString(dataWriteTrim(value))
}

func odataHasZoneLessCandidate(payload json.RawMessage) bool {
	object, ok := dataWriteParseObject(payload)
	if !ok {
		return false
	}
	for _, key := range object.keys {
		if text, ok := object.str(key); ok && text != "" && odataIsZoneLess(text) {
			return true
		}
	}
	return false
}

// odataFindZoneLessDateTime is ODataDateTimeGuard.FindZoneLessDateTime: "" when every date-time literal
// carries a zone, otherwise clio's refusal naming the offending fields.
func odataFindZoneLessDateTime(payload json.RawMessage, propertyTypes map[string]string) string {
	object, ok := dataWriteParseObject(payload)
	if !ok {
		return ""
	}
	type offender struct{ name, value string }
	var offenders []offender
	for _, key := range object.keys {
		value, ok := object.str(key)
		if !ok || value == "" || !odataIsZoneLess(value) {
			continue
		}
		if propertyTypes != nil {
			if edmType, known := propertyTypes[key]; known && !odataTemporalTypes[edmType] {
				continue
			}
		}
		offenders = append(offenders, offender{key, value})
	}
	if len(offenders) == 0 {
		return ""
	}
	var subject string
	if len(offenders) == 1 {
		subject = fmt.Sprintf("data field '%s' carries the date-time value '%s', which has", offenders[0].name, offenders[0].value)
	} else {
		parts := make([]string, len(offenders))
		for i, o := range offenders {
			parts[i] = fmt.Sprintf("'%s' ('%s')", o.name, o.value)
		}
		subject = "data fields " + strings.Join(parts, ", ") + " carry date-time values which have"
	}
	return subject + " no UTC designator and no time-zone offset. Creatio publishes date-time columns as " +
		"Edm.DateTimeOffset, whose literal form requires a zone: depending on the platform build such a " +
		"value is either rejected outright or silently stored as 0001-01-01T00:00:00Z while the call " +
		"still reports success (GitHub issue #1369). Send the instant explicitly - " +
		"'2024-01-01T04:00:00Z' for UTC, or '2024-01-01T04:00:00+02:00' for a local offset. clio does " +
		"not append 'Z' for you, because the zone you meant cannot be guessed. Nothing was written."
}

// ---- ODataFieldValidation ----

type odataEntityMetadata struct {
	resolved       bool
	properties     map[string]bool
	propertyTypes  map[string]string
	unverified     string
	depthExceeded  bool
	emptyBody      bool
	serverRejected bool
}

const odataMaxInheritanceDepth = 64

// odataTransientDelay is the pause between attempts after an empty answer; a variable so tests can drop it.
var odataTransientDelay = time.Second

func (c *Client) odataTryGetPropertyTypes(ctx context.Context, entity string) map[string]string {
	metadata := c.odataFetchMetadata(ctx, entity, 10*time.Second, 1)
	if metadata.resolved {
		return metadata.propertyTypes
	}
	return nil
}

func (c *Client) odataFetchMetadata(ctx context.Context, entity string, timeout time.Duration, attempts int) odataEntityMetadata {
	metadata := c.odataFetchMetadataOnce(ctx, entity, timeout)
	for attempt := 1; attempt < attempts && metadata.emptyBody; attempt++ {
		time.Sleep(odataTransientDelay)
		metadata = c.odataFetchMetadataOnce(ctx, entity, timeout)
	}
	return metadata
}

func (c *Client) odataFetchMetadataOnce(ctx context.Context, entity string, timeout time.Duration) odataEntityMetadata {
	body := c.dataWriteGet(ctx, "odata/$metadata", timeout)
	if dataWriteBlank(body) {
		return odataEntityMetadata{unverified: "the OData metadata response was empty.", emptyBody: true}
	}
	if strings.HasPrefix(strings.TrimLeft(body, " \t\r\n\v\f \u0085  "), "<") {
		types, order, err := odataParseCSDL(body)
		if err != nil {
			return odataEntityMetadata{unverified: redact.Text(dataWriteDescribeNonJSON(body))}
		}
		var target *odataCSDLType
		for _, name := range order {
			if strings.EqualFold(name, dataWriteTrim(entity)) {
				target = types[name]
				break
			}
		}
		if target == nil {
			return odataEntityMetadata{unverified: "the OData metadata response did not contain a type definition for the entity."}
		}
		if !odataCollectInherited(target, types) {
			return odataEntityMetadata{depthExceeded: true, unverified: "the OData metadata declares an inheritance chain for the entity deeper than " +
				fmt.Sprintf("%d types, which no Creatio type hierarchy has, so the declared ", odataMaxInheritanceDepth) +
				"properties could not be established."}
		}
		return odataEntityMetadata{resolved: true, properties: target.properties, propertyTypes: target.propertyTypes}
	}
	if dataWriteValidJSON([]byte(body)) == nil {
		if _, ok := dataWriteDetectError(json.RawMessage(body), dataWriteODataContext); ok {
			return odataEntityMetadata{serverRejected: true}
		}
	}
	return odataEntityMetadata{unverified: redact.Text(dataWriteDescribeNonJSON(body))}
}

type odataCSDLType struct {
	name          string
	baseType      string
	properties    map[string]bool
	propertyTypes map[string]string
}

// odataParseCSDL reads EntityType declarations the way clio's XmlReader loop does (DTDs prohibited).
func odataParseCSDL(body string) (map[string]*odataCSDLType, []string, error) {
	types := map[string]*odataCSDLType{}
	var order []string
	decoder := xml.NewDecoder(strings.NewReader(body))
	decoder.Strict = true
	current := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return types, order, nil
		}
		if err != nil {
			return nil, nil, err
		}
		switch element := token.(type) {
		case xml.Directive:
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(string(element))), "DOCTYPE") {
				return nil, nil, errors.New("DTD is prohibited in this XML document")
			}
		case xml.EndElement:
			if element.Name.Local == "EntityType" {
				current = ""
			}
		case xml.StartElement:
			attribute := func(name string) (string, bool) {
				for _, attr := range element.Attr {
					if attr.Name.Space == "" && attr.Name.Local == name {
						return attr.Value, true
					}
				}
				return "", false
			}
			if element.Name.Local == "EntityType" {
				if name, ok := attribute("Name"); ok {
					baseType, _ := attribute("BaseType")
					if _, seen := types[name]; !seen {
						order = append(order, name)
					}
					types[name] = &odataCSDLType{name: name, baseType: baseType, properties: map[string]bool{}, propertyTypes: map[string]string{}}
					current = name
					continue
				}
			}
			typ, ok := types[current]
			if current == "" || !ok {
				continue
			}
			switch element.Name.Local {
			case "Property":
				if name, ok := attribute("Name"); ok {
					typ.properties[name] = true
					if propertyType, ok := attribute("Type"); ok {
						typ.propertyTypes[name] = propertyType
					}
				}
			case "ComplexType", "EntityContainer":
				current = ""
			}
		}
	}
}

func odataCollectInherited(target *odataCSDLType, types map[string]*odataCSDLType) bool {
	chain := []*odataCSDLType{target}
	visited := map[string]bool{target.name: true}
	current := target
	for current.baseType != "" {
		short := current.baseType
		if dot := strings.LastIndex(short, "."); dot >= 0 {
			short = short[dot+1:]
		}
		base, ok := types[short]
		if !ok || visited[base.name] {
			break
		}
		visited[base.name] = true
		if len(chain) >= odataMaxInheritanceDepth {
			return false
		}
		chain = append(chain, base)
		current = base
	}
	for i := len(chain) - 1; i > 0; i-- {
		derived, base := chain[i-1], chain[i]
		for name := range base.properties {
			derived.properties[name] = true
		}
		for name, typ := range base.propertyTypes {
			if _, ok := derived.propertyTypes[name]; !ok {
				derived.propertyTypes[name] = typ
			}
		}
	}
	return true
}

// odataValidateDataFields is ODataFieldValidation.ValidateDataFields: "" when every field exists on the
// entity's OData type, otherwise clio's refusal; it also returns the property types it read.
func (c *Client) odataValidateDataFields(ctx context.Context, entity, id string, fields []string) (string, map[string]string) {
	for _, field := range fields {
		if !dataWriteSimpleIdentifier(field) {
			return fmt.Sprintf("data field '%s' is not a writable OData property name (allowed: letters, digits and underscores; ", field) +
				"a navigation path such as 'Account/Id' is readable but cannot be written - set the foreign-key column, e.g. 'AccountId'). " +
				"No write was performed.", nil
		}
	}
	var keys []string
	seen := map[string]bool{}
	for _, field := range fields {
		if !seen[field] {
			seen[field] = true
			keys = append(keys, field)
		}
	}
	metadata := c.odataFetchMetadata(ctx, entity, 30*time.Second, 3)
	if metadata.resolved {
		var unknown []string
		for _, key := range keys {
			if !metadata.properties[key] {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			return odataUnknownFieldsMessage(entity, id, unknown, false, false), metadata.propertyTypes
		}
		return "", metadata.propertyTypes
	}
	if metadata.depthExceeded {
		return fmt.Sprintf("The pre-write field check for %s(%s) returned a response that could not be verified: %s No write was performed.",
			entity, id, metadata.unverified), nil
	}
	attempts := 3
	if metadata.emptyBody {
		attempts = 1
	}
	return c.odataValidateBySelectProbe(ctx, entity, id, keys, attempts), nil
}

type odataProbeResult struct {
	succeeded   bool
	serverError *string
	unverified  string
	emptyBody   bool
}

func (c *Client) odataProbe(ctx context.Context, entity, id string, keys []string, attempts int, timeout time.Duration) odataProbeResult {
	probe := c.odataProbeOnce(ctx, entity, id, keys, timeout)
	for attempt := 1; attempt < attempts && probe.emptyBody; attempt++ {
		time.Sleep(odataTransientDelay)
		probe = c.odataProbeOnce(ctx, entity, id, keys, timeout)
	}
	return probe
}

func (c *Client) odataProbeOnce(ctx context.Context, entity, id string, keys []string, timeout time.Duration) odataProbeResult {
	path := dataWriteKeyPath(entity, id) + "?$select=Id," + strings.Join(keys, ",")
	body := c.dataWriteGet(ctx, path, timeout)
	if dataWriteBlank(body) {
		return odataProbeResult{unverified: "the probe response was empty.", emptyBody: true}
	}
	if dataWriteValidJSON([]byte(body)) != nil {
		if dataWriteIsMarkup(body) {
			code, known := dataWriteMarkupStatus(body)
			return odataProbeResult{unverified: odataDescribeMarkupProbe(code, known)}
		}
		return odataProbeResult{unverified: "the probe response was not JSON, which Creatio's OData pipeline never returns by itself - " +
			"this points to a proxy, IIS, routing or session problem rather than the request's shape. " +
			"The body is not reproduced here"}
	}
	root := json.RawMessage(body)
	reason, ok := odataIsAddressedRecord(root, id, keys)
	if ok {
		return odataProbeResult{succeeded: true}
	}
	if serverError, detected := dataWriteDetectError(root, dataWriteODataContext); detected {
		redacted := redact.Text(serverError)
		return odataProbeResult{serverError: &redacted}
	}
	return odataProbeResult{unverified: redact.Text(reason)}
}

func odataDescribeMarkupProbe(code int, known bool) string {
	status := "The page states no HTTP status"
	if known {
		status = "The server answered with an " + dataWriteMarkupStatusPhrase(code)
	}
	hint := ""
	if known && code == http.StatusNotFound {
		hint = ". " + dataWriteUnregisteredEntityHint
	}
	return strings.TrimRight("the probe response was not JSON but an HTML error page. "+status+hint, " .")
}

func odataIsAddressedRecord(root json.RawMessage, id string, keys []string) (string, bool) {
	object, ok := dataWriteParseObject(root)
	if !ok {
		return "the probe response was not a JSON object, so it is not the addressed record.", false
	}
	probed, ok := object.raw("Id")
	if !ok || !odataSameKey(probed, id) {
		return "the probe response did not identify itself as the addressed record - it carries no Id equal to the requested key.", false
	}
	var missing []string
	for _, key := range keys {
		if _, ok := object.raw(key); !ok {
			missing = append(missing, "'"+key+"'")
		}
	}
	if len(missing) > 0 {
		return "the probe response is the addressed record but does not carry " + strings.Join(missing, ", ") +
			", so those field(s) are not confirmed to exist.", false
	}
	return "", true
}

func odataSameKey(probed json.RawMessage, id string) bool {
	switch dataWriteJSONKind(probed) {
	case 's':
		var text string
		_ = json.Unmarshal(probed, &text)
		left, leftOK := DataWriteParseGUID(text)
		right, rightOK := DataWriteParseGUID(id)
		if leftOK && rightOK {
			return left == right
		}
		return strings.EqualFold(text, id)
	case 'n':
		return string(bytes.TrimSpace(probed)) == dataWriteTrim(id)
	}
	return false
}

// DataWriteParseGUID parses the GUID spellings .NET Guid.TryParse accepts (D, N, B, P) and returns the
// lower-case D form.
func DataWriteParseGUID(value string) (string, bool) {
	value = dataWriteTrim(value)
	if len(value) >= 2 && ((value[0] == '{' && value[len(value)-1] == '}') || (value[0] == '(' && value[len(value)-1] == ')')) {
		value = value[1 : len(value)-1]
	}
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", false
		}
		value = strings.ReplaceAll(value, "-", "")
	}
	if len(value) != 32 || strings.Trim(strings.ToLower(value), "0123456789abcdef") != "" {
		return "", false
	}
	value = strings.ToLower(value)
	return value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], true
}

var odataUnknownPropertyPattern = regexp.MustCompile(`Could not find a property named '([^']*)'`)

func odataMatchRequestedKey(serverError string, keys []string) string {
	match := odataUnknownPropertyPattern.FindStringSubmatch(serverError)
	if match == nil || dataWriteBlank(match[1]) {
		return ""
	}
	for _, key := range keys {
		if key == match[1] {
			return key
		}
	}
	return ""
}

func (c *Client) odataValidateBySelectProbe(ctx context.Context, entity, id string, keys []string, attempts int) string {
	batch := c.odataProbe(ctx, entity, id, keys, attempts, 30*time.Second)
	if batch.succeeded {
		return ""
	}
	if batch.serverError == nil {
		return fmt.Sprintf("The pre-write field probe for %s(%s) returned a response that could not be verified: %s. ", entity, id, batch.unverified) +
			"No write was performed; check connectivity with odata-read and retry."
	}
	first := odataMatchRequestedKey(*batch.serverError, keys)
	if first == "" {
		return odataProbeRejectedMessage(entity, id, "")
	}
	unknown := []string{first}
	partial := false
	var remaining []string
	for _, key := range keys {
		if key != first {
			remaining = append(remaining, key)
		}
	}
	for i, key := range remaining {
		if i >= 10 {
			partial = true
			break
		}
		single := c.odataProbe(ctx, entity, id, []string{key}, attempts, 10*time.Second)
		if single.succeeded {
			continue
		}
		if single.serverError == nil {
			return fmt.Sprintf("The pre-write field probe for '%s' on %s(%s) returned a response that could not be verified: %s. ", key, entity, id, single.unverified) +
				"No write was performed; retry."
		}
		if odataMatchRequestedKey(*single.serverError, []string{key}) == "" {
			return odataProbeRejectedMessage(entity, id, key)
		}
		unknown = append(unknown, key)
	}
	return odataUnknownFieldsMessage(entity, id, unknown, partial, true)
}

func odataProbeRejectedMessage(entity, id, key string) string {
	subject := fmt.Sprintf("%s(%s)", entity, id)
	if key != "" {
		subject = fmt.Sprintf("'%s' on %s(%s)", key, entity, id)
	}
	return "The pre-write field probe for " + subject + " was rejected by Creatio for a reason that does not " +
		"identify one of the requested fields, so the update was not performed. The server's own wording " +
		"is not reproduced here, because a service or proxy response is not trusted text in an MCP " +
		"transcript; check the environment's own logs, then verify the record Id, the entity name and the " +
		"credentials. No write was performed."
}

func odataUnknownFieldsMessage(entity, id string, unknown []string, partial, viaProbe bool) string {
	quoted := make([]string, len(unknown))
	for i, key := range unknown {
		quoted[i] = "'" + key + "'"
	}
	note := ""
	if partial {
		note = " (the list may be partial: the per-field follow-up probes stop after a limit)"
	}
	verdict := fmt.Sprintf("do not exist on the OData type of %s(%s) (verified against its $metadata)", entity, id)
	if viaProbe {
		verdict = fmt.Sprintf("could not be verified against the service (the $select probe rejected them as unknown properties) on %s(%s)", entity, id)
	}
	return fmt.Sprintf("odata-update rejected: field(s) %s%s %s, so nothing was written. ", strings.Join(quoted, ", "), note, verdict) +
		"Every field in data must exist on the entity's OData type - the same strictness the OData service applies to odata-read $select. " +
		"If a column exists on the entity but is not exposed through OData (for example a Color column), it cannot be written via odata-update: " +
		"verify it with execute-esq and use a supported write path. Fix the field names and retry."
}

// ---- rows-file (ODataFileContract.TryReadJson) ----

const dataWriteMaxPayloadBytes = 10 * 1024 * 1024

// DataWriteReadJSONFile reads a caller-supplied JSON payload file the way clio's ODataFileContract does:
// the path must resolve inside the workspace or the OS temp directory (never clio's configuration
// directory), the file must exist, be at most 10 MB and be valid UTF-8. It returns the text, or clio's
// refusal.
func DataWriteReadJSONFile(path, optionName string) (string, string) {
	if dataWriteBlank(path) {
		return "", optionName + " must not be empty."
	}
	resolved, failure := dataWriteResolveForRead(path, optionName)
	if failure != "" {
		return "", failure
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", redact.Text(fmt.Sprintf("Failed to read %s: %s", optionName, err.Error()))
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, dataWriteMaxPayloadBytes+1))
	if err != nil {
		return "", redact.Text(fmt.Sprintf("Failed to read %s: %s", optionName, err.Error()))
	}
	if len(payload) > dataWriteMaxPayloadBytes {
		return "", fmt.Sprintf("%s is at least %d bytes, which exceeds the %d-byte limit.", optionName, len(payload), dataWriteMaxPayloadBytes)
	}
	payload = bytes.TrimPrefix(payload, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(payload) {
		return "", optionName + " is not valid UTF-8. Re-encode the file as UTF-8 and retry."
	}
	return string(payload), ""
}

// dataWriteResolveForRead is clio's OutputPathConfinement.ResolveForRead.
func dataWriteResolveForRead(path, optionName string) (string, string) {
	full, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Sprintf("%s '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", optionName, path)
	}
	home, _ := os.UserHomeDir()
	current, _ := os.Getwd()
	anchor := schemaGetWorkspaceAnchor(current, home)
	unresolvable := fmt.Sprintf("%s '%s' resolves through an unresolvable symbolic link; refusing to continue.", optionName, path)
	real, ok := schemaGetRealPath(full)
	if !ok {
		return "", unresolvable
	}
	realTemp, ok := schemaGetRealPath(schemaGetAbs(os.TempDir()))
	if !ok {
		return "", unresolvable
	}
	realClioHome, ok := schemaGetRealPath(schemaGetAbs(clioHomeDirectory()))
	if !ok {
		return "", unresolvable
	}
	realHome, realAnchor := home, anchor
	if home != "" {
		if realHome, ok = schemaGetRealPath(home); !ok {
			return "", unresolvable
		}
	}
	if anchor != "" {
		if realAnchor, ok = schemaGetRealPath(anchor); !ok {
			return "", unresolvable
		}
	}
	if schemaGetWithin(realClioHome, real) {
		return "", fmt.Sprintf("%s '%s' resolves inside clio's own configuration directory; refusing to continue.", optionName, path)
	}
	if !schemaGetTrustedAnchor(realAnchor, realHome) {
		realAnchor = ""
	}
	if !schemaGetWithin(realAnchor, real) && !schemaGetWithin(realTemp, real) {
		return "", fmt.Sprintf("%s '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", optionName, path)
	}
	if info, err := os.Stat(real); err != nil || info.IsDir() {
		return "", optionName + " file was not found."
	}
	return real, ""
}

// DataWriteAliasError is clio's McpToolArgumentSupport.BuildLegacyAliasError over the keys a tool's
// argument record does not bind: a key in aliases gets a rename hint, any other is listed as unknown. Keys
// are reported in sorted order (Go maps carry no request order).
func DataWriteAliasError(unbound []string, aliases map[string]string, renameSuffix, unknownHint string) string {
	if len(unbound) == 0 {
		return ""
	}
	sorted := append([]string(nil), unbound...)
	sort.Strings(sorted)
	var mapped, unknown []string
	describe := func(key string) string {
		return strings.NewReplacer("'", "", "\"", "").Replace(dataWriteSanitizeForDisplay(key, 120))
	}
	for _, key := range sorted {
		if canonical, ok := aliases[key]; ok {
			mapped = append(mapped, fmt.Sprintf("'%s' -> '%s'", describe(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", describe(key)))
		}
	}
	join := func(keys []string) string {
		if len(keys) <= 10 {
			return strings.Join(keys, ", ")
		}
		return fmt.Sprintf("%s and %d more", strings.Join(keys[:10], ", "), len(keys)-10)
	}
	var parts []string
	if len(mapped) > 0 {
		parts = append(parts, "Rename: "+join(mapped)+renameSuffix)
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+join(unknown)+". "+unknownHint)
	}
	return strings.Join(parts, " ")
}

// dataWriteSanitizeForDisplay is TextUtilities.SanitizeForDisplay: display-hostile characters become
// spaces and the text is capped (an ellipsis marks the cut).
func dataWriteSanitizeForDisplay(text string, maxLength int) string {
	var builder strings.Builder
	for _, r := range text {
		if dataWriteDisplayHostile(r) {
			r = ' '
		}
		builder.WriteRune(r)
	}
	sanitized := builder.String()
	if runes := []rune(sanitized); len(runes) > maxLength {
		sanitized = string(runes[:maxLength]) + "…"
	}
	return sanitized
}
