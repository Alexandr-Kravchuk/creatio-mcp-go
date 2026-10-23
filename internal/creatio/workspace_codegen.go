package creatio

// The Creatio side of two clio 8.1.0.142 code-generation tools: generate-source-code (GenerateSourceCodeCommand,
// a RemoteCommand posting once to WorkspaceExplorerService) and the schema reads of add-item-model
// (ModelBuilder: EntitySchemaManagerRequest, then one RuntimeEntitySchemaRequest per entity, four at a time).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	gscAllRoute        = "ServiceModel/WorkspaceExplorerService.svc/GenerateAllSchemasSources"
	gscModifiedRoute   = "ServiceModel/WorkspaceExplorerService.svc/GenerateModifiedSchemasSources"
	gscRequiredRoute   = "ServiceModel/WorkspaceExplorerService.svc/GenerateRequiredSchemasSources"
	gscBackgroundRoute = "ServiceModel/WorkspaceExplorerService.svc/GenerateAllSchemasSourcesInBackground"

	// GenerateSourceCodeDefaultTimeout is RemoteCommandOptions' timeout for the generate-source-code verb.
	GenerateSourceCodeDefaultTimeout = 60 * 60 * 1000
	// GenerateSourceCodeMaxTimeout is GenerateSourceCodeTool.MaxTimeoutMilliseconds (3 hours).
	GenerateSourceCodeMaxTimeout = 3 * 60 * 60 * 1000

	// codegenResponseLimit bounds a body read; clio reads whatever arrives, and a Studio schema list runs to
	// megabytes.
	codegenResponseLimit = 64 << 20
)

// GenerateSourceCodeRequest is GenerateSourceCodeOptions as the MCP tool fills it.
type GenerateSourceCodeRequest struct {
	Modified, Required, Background bool
	// TimeoutMilliseconds is the resolved request timeout (the 60-minute default when the caller gave none).
	TimeoutMilliseconds int
}

func (r GenerateSourceCodeRequest) route() string {
	switch {
	case r.Background:
		return gscBackgroundRoute
	case r.Modified:
		return gscModifiedRoute
	case r.Required:
		return gscRequiredRoute
	}
	return gscAllRoute
}

// GenerateSourceCode is GenerateSourceCodeCommand.Execute with MaxAttempts 1: two warnings, one POST with an
// empty body, and the verdict ProceedResponse reads from whatever body came back.
func (c *Client) GenerateSourceCode(ctx context.Context, request GenerateSourceCodeRequest) CommandResult {
	messages := []LogMessage{
		{MessageType: "Warning", Value: "This command may take a while to complete, please wait..."},
		{MessageType: "Warning", Value: "Clio will timeout if the operation takes more than " +
			GenerateSourceCodeMinutes(request.TimeoutMilliseconds) + " minutes. You can adjust the timeout using the " +
			"--timeout option (CLI) or the 'timeout' argument (MCP)."},
	}
	failure := func(lines ...LogMessage) CommandResult {
		return CommandResult{ExitCode: 1, Messages: append(messages, lines...)}
	}
	route := request.route()
	response, err := c.serviceRequest(ctx, serviceCall{Route: route, Body: []byte{},
		Timeout: time.Duration(request.TimeoutMilliseconds) * time.Millisecond, Limit: codegenResponseLimit,
		Label: "WorkspaceExplorerService"})
	if err != nil {
		return failure(LogMessage{MessageType: "Error", Value: remoteCommandFailureText(err)})
	}
	body := string(response.payload)
	endpoint := LogMessage{MessageType: "Error", Value: "Endpoint: " + c.serviceURL(route)}
	if dotnetIsNullOrWhiteSpace(body) {
		return failure(LogMessage{MessageType: "Error", Value: "Empty response received from server."}, endpoint)
	}
	if strings.HasPrefix(strings.TrimLeftFunc(body, dotnetIsWhiteSpace), "<") {
		return failure(LogMessage{MessageType: "Error", Value: "Server returned non-JSON response (looks like HTML)."}, endpoint)
	}
	verdict, parseErr := stjCreatioResponse([]byte(body))
	if parseErr != "" {
		return failure(LogMessage{MessageType: "Error", Value: parseErr}, endpoint,
			LogMessage{MessageType: "Error", Value: "Full response:"}, LogMessage{MessageType: "None", Value: body})
	}
	if !verdict.success {
		return failure(LogMessage{MessageType: "Error", Value: verdict.errorCode + ": " + verdict.message})
	}
	return CommandResult{ExitCode: 0, Messages: append(messages, LogMessage{MessageType: "Info", Value: "Done generate-source-code"})}
}

// GenerateSourceCodeMinutes is TimeSpan.FromMilliseconds(ms).TotalMinutes formatted "0.##": rounded half away
// from zero to two decimals. The separator is the invariant ".", where clio uses the process culture (a recorded
// gap: a region with a decimal comma prints "1,5").
func GenerateSourceCodeMinutes(milliseconds int) string {
	negative := milliseconds < 0
	value := int64(milliseconds)
	if negative {
		value = -value
	}
	hundredths := (value*100 + 30_000) / 60_000
	text := strconv.FormatInt(hundredths/100, 10)
	if fraction := hundredths % 100; fraction != 0 {
		digits := fmt.Sprintf("%02d", fraction)
		text += "." + strings.TrimRight(digits, "0")
	}
	if negative && hundredths != 0 {
		text = "-" + text
	}
	return text
}

// remoteCommandFailureText is RemoteCommand.Execute's catch (GetReadableMessageException) for a request that got
// no answer: a login that could not connect, a request cut by its timeout, or the transport fault itself.
func remoteCommandFailureText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		// The linked CancellationTokenSource fires: TaskCanceledException, unwrapped from the AggregateException.
		return "A task was canceled."
	}
	if fault, ok := asConnectFault(err); ok && isLoginError(err) {
		return "Cannot connect to the application: " + fault.Error() + " (WebException: ConnectFailure). " +
			"Make sure the site is running and accessible."
	}
	if fault := newConnectFault(err); fault != nil {
		// A request on a session already held: HttpRequestException's message, not the WebException wording.
		return fault.Error()
	}
	return err.Error()
}

func dotnetIsNullOrWhiteSpace(text string) bool {
	return strings.TrimFunc(text, dotnetIsWhiteSpace) == ""
}

// ------------------------------------------------------------------------------------------- entity models

const (
	modelSchemaListRoute    = "DataService/json/SyncReply/EntitySchemaManagerRequest"
	modelRuntimeSchemaRoute = "DataService/json/SyncReply/RuntimeEntitySchemaRequest"
	// ModelBuilder's SchemaRequestMaxAttempts and SchemaRequestDelaySeconds; tests shorten the delay.
	modelRequestAttempts = 3
	// clio sends these requests without a timeout (Timeout.Infinite).
	modelRequestTimeout = 24 * time.Hour
	// modelParallelism is PopulateRuntimeSchemas' MaxDegreeOfParallelism.
	modelParallelism = 4
)

var modelRequestDelay = 2 * time.Second

// ModelColumn is ModelBuilder's Column.
type ModelColumn struct {
	Name                string
	DataValueType       int
	ReferenceSchemaName *string
	Description         *string
	IsRequired          bool
}

// ModelSchema is ModelBuilder's Schema: columns in the order the runtime schema lists them.
type ModelSchema struct {
	Name        string
	Description *string
	Columns     []ModelColumn
}

// EntityModelSchemas is ModelBuilder's GetEntitySchemasAsync and PopulateRuntimeSchemas: every entity schema
// the environment lists, first occurrence of a name kept, each with its runtime columns. lines are the log
// lines written meanwhile (a schema without columns); err is the exception the command reports.
func (c *Client) EntityModelSchemas(ctx context.Context, culture string) (schemas []*ModelSchema, lines []LogMessage, err error) {
	payload, err := c.modelPost(ctx, modelSchemaListRoute, []byte{})
	if err != nil {
		return nil, nil, err
	}
	names, err := newtonsoftSchemaNames(payload)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			schemas = append(schemas, &ModelSchema{Name: name})
		}
	}
	return schemas, c.populateRuntimeSchemas(ctx, schemas, culture, &err), err
}

// populateRuntimeSchemas is Parallel.ForEach with four workers: once an iteration fails no new one starts,
// the running ones finish, and their failures make one AggregateException. Lines and failures are reported
// in schema order (clio's own order follows thread timing).
func (c *Client) populateRuntimeSchemas(ctx context.Context, schemas []*ModelSchema, culture string, failure *error) []LogMessage {
	type outcome struct {
		line string
		err  error
	}
	outcomes := make([]outcome, len(schemas))
	started := make([]bool, len(schemas))
	var mu sync.Mutex
	stopped := false
	next := 0
	var group sync.WaitGroup
	for worker := 0; worker < modelParallelism; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				mu.Lock()
				if stopped || next >= len(schemas) {
					mu.Unlock()
					return
				}
				index := next
				next++
				started[index] = true
				mu.Unlock()
				line, err := c.runtimeSchema(ctx, schemas[index], culture)
				mu.Lock()
				outcomes[index] = outcome{line: line, err: err}
				if err != nil {
					stopped = true
				}
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	lines := []LogMessage{}
	var inner []string
	for index, result := range outcomes {
		if !started[index] {
			continue
		}
		if result.line != "" {
			lines = append(lines, LogMessage{MessageType: "None", Value: result.line})
		}
		if result.err != nil {
			inner = append(inner, "("+result.err.Error()+")")
		}
	}
	if len(inner) > 0 {
		*failure = errors.New("One or more errors occurred. " + strings.Join(inner, " "))
	}
	return lines
}

// runtimeSchema is GetRuntimeEntitySchema: the line it logs for a schema without columns, or its exception.
func (c *Client) runtimeSchema(ctx context.Context, schema *ModelSchema, culture string) (string, error) {
	// clio builds the body by interpolation, without escaping the name.
	payload, err := c.modelPost(ctx, modelRuntimeSchemaRoute, []byte(`{"Name" : "`+schema.Name+`"}`))
	if err != nil {
		return "", err
	}
	text := string(payload)
	root, parseErr := parseJNode(payload)
	if parseErr != nil {
		preview := text
		if runes := []rune(text); len(utf16Units(text)) > 200 {
			preview = utf16Prefix(runes, 200)
		}
		return "", fmt.Errorf("Failed to parse runtime schema '%s'. Response starts with: %s", schema.Name, preview)
	}
	items := root.get("schema").get("columns").get("Items")
	if items == nil {
		return "Schema " + schema.Name + " not found", nil
	}
	schema.Description = selectString(root.get("schema").get("description").get(culture))
	if items.kind == jkNull {
		return "", errors.New("Object reference not set to an instance of an object.")
	}
	if !items.isObject() {
		return "", fmt.Errorf("Cannot deserialize the runtime columns of schema '%s'", schema.Name)
	}
	names := map[string]bool{}
	for _, key := range items.keys {
		item := items.props[key]
		if !item.isObject() {
			return "", fmt.Errorf("Cannot deserialize the runtime column '%s' of schema '%s'", key, schema.Name)
		}
		column, err := newtonsoftColumn(item, culture)
		if err != nil {
			return "", err
		}
		if column.Name == nil {
			return "", errors.New("Value cannot be null. (Parameter 'key')")
		}
		if names[*column.Name] {
			return "", errors.New("An item with the same key has already been added. Key: " + *column.Name)
		}
		names[*column.Name] = true
		schema.Columns = append(schema.Columns, ModelColumn{Name: *column.Name, DataValueType: column.DataValueType,
			ReferenceSchemaName: column.ReferenceSchemaName, Description: column.Description, IsRequired: column.IsRequired})
	}
	return "", nil
}

type parsedColumn struct {
	Name                *string
	DataValueType       int
	ReferenceSchemaName *string
	Description         *string
	IsRequired          bool
}

// newtonsoftColumn reads one column with JToken's explicit conversions, in clio's order: name, dataValueType,
// isRequired, referenceSchemaName (only when present), description.<culture>.
func newtonsoftColumn(item *jnode, culture string) (parsedColumn, error) {
	var column parsedColumn
	name, err := jtokenString(item.get("name"))
	if err != nil {
		return column, err
	}
	column.Name = name
	if column.DataValueType, err = jtokenInt(item.get("dataValueType")); err != nil {
		return column, err
	}
	if column.IsRequired, err = jtokenBool(item.get("isRequired")); err != nil {
		return column, err
	}
	if reference, present := item.props["referenceSchemaName"]; present {
		if column.ReferenceSchemaName, err = jtokenString(reference); err != nil {
			return column, err
		}
	}
	column.Description = selectString(item.get("description").get(culture))
	return column, nil
}

// selectString is SelectToken(path)?.ToString(): nil when nothing is there, "" for a JSON null.
func selectString(node *jnode) *string {
	if node == nil {
		return nil
	}
	return node.tokenString()
}

func jtokenTypeName(node *jnode) string {
	switch node.kind {
	case jkNull:
		return "Null"
	case jkBool:
		return "Boolean"
	case jkInteger:
		return "Integer"
	case jkFloat:
		return "Float"
	case jkString:
		return "String"
	case jkArray:
		return "Array"
	}
	return "Object"
}

// jtokenString is (string)JToken: null for an absent or null token, a scalar's text, a container refused.
func jtokenString(node *jnode) (*string, error) {
	if node == nil || node.kind == jkNull {
		return nil, nil
	}
	if node.kind == jkArray || node.kind == jkObject {
		return nil, fmt.Errorf("Can not convert %s to String.", jtokenTypeName(node))
	}
	return node.stringValue(), nil
}

// jtokenInt is (int)JToken: Convert.ToInt32 over the token's value.
func jtokenInt(node *jnode) (int, error) {
	if node == nil {
		return 0, errors.New("Value cannot be null. (Parameter 'value')")
	}
	switch node.kind {
	case jkInteger:
		value, err := strconv.ParseInt(node.text, 10, 32)
		if err != nil {
			return 0, errors.New("Value was either too large or too small for an Int32.")
		}
		return int(value), nil
	case jkFloat:
		value, err := strconv.ParseFloat(node.text, 64)
		if err != nil || value > 2147483647.5 || value < -2147483648.5 {
			return 0, errors.New("Value was either too large or too small for an Int32.")
		}
		return int(roundHalfEven(value)), nil
	case jkBool:
		if node.flag {
			return 1, nil
		}
		return 0, nil
	case jkString:
		value, err := strconv.ParseInt(strings.TrimSpace(node.text), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("The input string '%s' was not in a correct format.", node.text)
		}
		return int(value), nil
	}
	return 0, fmt.Errorf("Can not convert %s to Int32.", jtokenTypeName(node))
}

// jtokenBool is (bool)JToken: Convert.ToBoolean over the token's value.
func jtokenBool(node *jnode) (bool, error) {
	if node == nil {
		return false, errors.New("Value cannot be null. (Parameter 'value')")
	}
	switch node.kind {
	case jkBool:
		return node.flag, nil
	case jkInteger, jkFloat:
		value, _ := strconv.ParseFloat(node.text, 64)
		return value != 0, nil
	case jkString:
		switch strings.ToLower(strings.TrimSpace(node.text)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return false, fmt.Errorf("String '%s' was not recognized as a valid Boolean.", node.text)
	}
	return false, fmt.Errorf("Can not convert %s to Boolean.", jtokenTypeName(node))
}

func roundHalfEven(value float64) float64 {
	floor := float64(int64(value))
	if value < 0 && floor != value {
		floor--
	}
	fraction := value - floor
	switch {
	case fraction > 0.5:
		return floor + 1
	case fraction < 0.5:
		return floor
	}
	if int64(floor)%2 == 0 {
		return floor
	}
	return floor + 1
}

// newtonsoftSchemaNames is JsonConvert.DeserializeObject<EntitySchemaResponse> followed by the name loop:
// the collection's names in order, or the exception clio reports.
func newtonsoftSchemaNames(payload []byte) ([]string, error) {
	text := string(payload)
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" {
		// DeserializeObject returns null; reading Collection throws.
		return nil, errors.New("Object reference not set to an instance of an object.")
	}
	root, err := parseJNode(payload)
	if err != nil {
		if first := []rune(trimmed)[0]; !strings.ContainsRune("{[\"-0123456789tfnNI'/", first) {
			line, position := newtonsoftLinePosition(text, len(text)-len(trimmed))
			return nil, fmt.Errorf("Unexpected character encountered while parsing value: %c. Path '', line %d, position %d.", first, line, position)
		}
		return nil, err
	}
	switch root.kind {
	case jkNull:
		return nil, errors.New("Object reference not set to an instance of an object.")
	case jkArray:
		line, position := newtonsoftLinePosition(text, len(text)-len(trimmed)+1)
		return nil, fmt.Errorf("Cannot deserialize the current JSON array (e.g. [1,2,3]) into type 'Clio.Dto.EntitySchemaResponse' "+
			"because the type requires a JSON object (e.g. {\"name\":\"value\"}) to deserialize correctly.\n"+
			"To fix this error either change the JSON to a JSON object (e.g. {\"name\":\"value\"}) or change the deserialized type "+
			"to an array or a type that implements a collection interface (e.g. ICollection, IList) like List<T> that can be "+
			"deserialized from a JSON array. JsonArrayAttribute can also be added to the type to force it to deserialize from a "+
			"JSON array.\nPath '', line %d, position %d.", line, position)
	case jkObject:
	default:
		return nil, fmt.Errorf("Error converting value to type 'Clio.Dto.EntitySchemaResponse'.")
	}
	collection := root.get("collection")
	if collection == nil || collection.kind == jkNull {
		return nil, errors.New("Value cannot be null. (Parameter 'source')")
	}
	if !collection.isArray() {
		return nil, errors.New("Cannot deserialize the 'collection' member into type 'System.Collections.Generic.List`1[Clio.Dto.EntitySchema]'.")
	}
	names := make([]string, 0, len(collection.items))
	for _, item := range collection.items {
		if item.kind == jkNull {
			// A null item reaches item.Name.
			return nil, errors.New("Object reference not set to an instance of an object.")
		}
		if !item.isObject() {
			return nil, errors.New("Cannot deserialize a 'collection' item into type 'Clio.Dto.EntitySchema'.")
		}
		name, err := jtokenString(item.get("name"))
		if err != nil {
			return nil, err
		}
		if name == nil {
			return nil, errors.New("Value cannot be null. (Parameter 'key')")
		}
		names = append(names, *name)
	}
	return names, nil
}

// newtonsoftLinePosition is JsonTextReader's line (1-based once a character was read, 0 before) and position
// at a byte offset into text.
func newtonsoftLinePosition(text string, offset int) (int, int) {
	line, position := 1, 0
	for _, r := range text[:offset] {
		if r == '\n' {
			line++
			position = 0
			continue
		}
		position++
	}
	if offset == 0 || (line == 1 && position == 0) {
		return 0, 0
	}
	return line, position
}

// modelPost is ExecutePostRequest(url, body, maxAttempts: 3, delaySec: 2): the body whatever the status, a
// request that got no answer tried again after 2 s, up to three times.
func (c *Client) modelPost(ctx context.Context, route string, body []byte) ([]byte, error) {
	var last error
	for attempt := 1; attempt <= modelRequestAttempts; attempt++ {
		response, err := c.serviceRequest(ctx, serviceCall{Method: http.MethodPost, Route: route, Body: body,
			Timeout: modelRequestTimeout, Limit: codegenResponseLimit, Label: "DataService"})
		if err == nil {
			return response.payload, nil
		}
		last = err
		if isLoginError(err) || !isTransportError(err) || attempt == modelRequestAttempts || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(modelRequestDelay):
		}
	}
	if isLoginError(last) {
		// EnsureLegacyAuthentication logs in synchronously before the request: its exception arrives as is.
		return nil, errors.New(modelTransportText(last))
	}
	// .Result wraps whatever the request raised in an AggregateException.
	return nil, errors.New("One or more errors occurred. (" + modelTransportText(last) + ")")
}

// modelTransportText is the message of the exception Creatio.Client raises for a request that got no answer.
func modelTransportText(err error) string {
	if fault, ok := asConnectFault(err); ok {
		return fault.Error()
	}
	if fault := newConnectFault(err); fault != nil {
		return fault.Error()
	}
	return err.Error()
}

// utf16Units counts a string's UTF-16 code units, as .NET's string.Length does.
func utf16Units(text string) []uint16 {
	units := make([]uint16, 0, len(text))
	for _, r := range text {
		if r >= 0x10000 {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			continue
		}
		units = append(units, uint16(r))
	}
	return units
}

// utf16Prefix is text[..count] in UTF-16 units; a surrogate pair cut in half keeps its high half dropped.
func utf16Prefix(runes []rune, count int) string {
	var builder strings.Builder
	units := 0
	for _, r := range runes {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if units+width > count {
			break
		}
		builder.WriteRune(r)
		units += width
	}
	return builder.String()
}
