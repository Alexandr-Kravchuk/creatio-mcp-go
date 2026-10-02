package creatio

// Data Forge reads, ported from clio's DataForgeTool and the Common/DataForge clients it resolves. Data Forge
// is never called directly: clio reaches it through Creatio's own DataForgeMaintenanceService and
// DataForgeSchemaReadService REST endpoints with the ordinary Creatio session, so no microservice URL, token
// or system setting is read here. The initialize and update endpoints schedule index writes and are not ported.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dataForgeSource             = "clio+dataforge-service"
	dataForgeStatusRoute        = "rest/DataForgeMaintenanceService/GetServiceStatus"
	dataForgeTablesRoute        = "rest/DataForgeSchemaReadService/GetSimilarTableNames"
	dataForgeLookupsRoute       = "rest/DataForgeSchemaReadService/GetLookupValues"
	dataForgeRelationsRoute     = "rest/DataForgeSchemaReadService/GetTableRelationships"
	dataForgeRequestTimeout     = 60 * time.Second
	dataForgeMinimumVersionText = "10.0.0"
)

// dataForgeDataValueTypeNames is clio's CreatioDataValueType table in its server spelling (GetNameOrOrdinal).
// It is neither friendlyDataValueTypes (the designer vocabulary) nor the get-app-info display names.
var dataForgeDataValueTypeNames = map[int]string{
	0: "Guid", 1: "Text", 4: "Integer", 5: "Float", 6: "Money", 7: "DateTime", 8: "Date", 9: "Time", 10: "Lookup",
	11: "Enum", 12: "Boolean", 13: "Blob", 14: "Image", 15: "CustomObject", 16: "ImageLookup", 17: "Collection",
	18: "Color", 19: "LocalizableString", 20: "Entity", 21: "EntityCollection", 22: "EntityColumnMappingCollection",
	23: "HashText", 24: "SecureText", 25: "File", 26: "Mapping", 27: "ShortText", 28: "MediumText", 29: "MaxSizeText",
	30: "LongText", 31: "Float1", 32: "Float2", 33: "Float3", 34: "Float4", 35: "LocalizableParameterValuesList",
	36: "MetadataText", 37: "StageIndicator", 38: "ObjectList", 39: "CompositeObjectList", 40: "Float8",
	41: "FileLocator", 42: "PhoneText", 43: "RichText", 44: "WebText", 45: "EmailText", 46: "CompositeObject",
	47: "Float0", 48: "Money0", 49: "Money1", 50: "Money3",
}

// DataForgeError is the envelope's error object.
type DataForgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DataForgeHealth is clio's DataForgeHealthResult. correlation-id is always empty: the Creatio proxy returns none.
type DataForgeHealth struct {
	Liveness               bool   `json:"liveness"`
	Readiness              bool   `json:"readiness"`
	DataStructureReadiness bool   `json:"data-structure-readiness"`
	LookupsReadiness       bool   `json:"lookups-readiness"`
	CorrelationID          string `json:"correlation-id"`
}

// DataForgeMaintenanceStatus is clio's DataForgeMaintenanceStatusResult; a null error is left out, as clio does.
type DataForgeMaintenanceStatus struct {
	Success bool    `json:"success"`
	Status  string  `json:"status"`
	Error   *string `json:"error,omitempty"`
}

type DataForgeSimilarTable struct {
	Name        string  `json:"name"`
	Caption     string  `json:"caption"`
	Description *string `json:"description,omitempty"`
}

type DataForgeSimilarLookup struct {
	LookupID   string `json:"lookup-id"`
	SchemaName string `json:"schema-name"`
	Value      string `json:"value"`
	// Score is clio's decimal, converted from the service's double.
	Score *json.Number `json:"score,omitempty"`
}

type DataForgeColumn struct {
	Name                string  `json:"name"`
	Caption             *string `json:"caption,omitempty"`
	Description         *string `json:"description,omitempty"`
	DataType            string  `json:"data-type"`
	Required            bool    `json:"required"`
	ReferenceSchemaName *string `json:"reference-schema-name,omitempty"`
}

type DataForgeCoverage struct {
	Health       bool `json:"health"`
	Tables       bool `json:"tables"`
	Lookups      bool `json:"lookups"`
	Relations    bool `json:"relations"`
	TableColumns bool `json:"table-columns"`
}

// dataForgeEnvelope is the head every Data Forge answer shares.
type dataForgeEnvelope struct {
	Success       bool            `json:"success"`
	Source        string          `json:"source"`
	CorrelationID string          `json:"correlation-id"`
	Warnings      []string        `json:"warnings"`
	Error         *DataForgeError `json:"error,omitempty"`
}

type DataForgeStatusResponse struct {
	dataForgeEnvelope
	Health *DataForgeHealth            `json:"health,omitempty"`
	Status *DataForgeMaintenanceStatus `json:"status,omitempty"`
}

type DataForgeFindTablesResponse struct {
	dataForgeEnvelope
	SimilarTables []DataForgeSimilarTable `json:"similar-tables"`
}

type DataForgeFindLookupsResponse struct {
	dataForgeEnvelope
	SimilarLookups []DataForgeSimilarLookup `json:"similar-lookups"`
}

type DataForgeRelationsResponse struct {
	dataForgeEnvelope
	Relations []string `json:"relations"`
}

type DataForgeColumnsResponse struct {
	dataForgeEnvelope
	Columns []DataForgeColumn `json:"columns"`
}

// DataForgeContextResponse keeps relations and columns in clio's insertion order, which a Go map would lose.
type DataForgeContextResponse struct {
	dataForgeEnvelope
	Health         *DataForgeHealth            `json:"health,omitempty"`
	Status         *DataForgeMaintenanceStatus `json:"status,omitempty"`
	SimilarTables  []DataForgeSimilarTable     `json:"similar-tables"`
	SimilarLookups []DataForgeSimilarLookup    `json:"similar-lookups"`
	Relations      *orderedObject              `json:"relations"`
	Columns        *orderedObject              `json:"columns"`
	Coverage       DataForgeCoverage           `json:"coverage"`
}

// DataForgeRelationPair is one relation-pairs item; a side the caller left out or sent as null is nil.
type DataForgeRelationPair struct {
	SourceTable *string
	TargetTable *string
}

// DataForgeContextRequest is dataforge-context's input. A nil entry in RelationPairs is a JSON null item.
type DataForgeContextRequest struct {
	RequirementSummary string
	CandidateTerms     []*string
	LookupHints        []*string
	RelationPairs      []*DataForgeRelationPair
}

func dataForgeHead(success bool, code string, err error) dataForgeEnvelope {
	head := dataForgeEnvelope{Success: success, Source: dataForgeSource, Warnings: []string{}}
	if err != nil {
		head.Error = &DataForgeError{Code: code, Message: err.Error()}
	}
	return head
}

// DataForgeFailure is the answer a tool gives when it refuses the call before reaching Creatio. Each tool
// keeps its own error code and its own empty collections, as clio's catch blocks do.
func DataForgeFailure(tool, message string) any {
	err := errors.New(message)
	switch tool {
	case "dataforge-status":
		return DataForgeStatusResponse{dataForgeEnvelope: dataForgeHead(false, "status_error", err)}
	case "dataforge-find-tables":
		return dataForgeTablesFailure(err)
	case "dataforge-find-lookups":
		return dataForgeLookupsFailure(err)
	case "dataforge-get-relations":
		return dataForgeRelationsFailure(err)
	case "dataforge-get-table-columns":
		return dataForgeColumnsFailure(err)
	default:
		return dataForgeContextFailure(err)
	}
}

func dataForgeTablesFailure(err error) DataForgeFindTablesResponse {
	return DataForgeFindTablesResponse{dataForgeHead(false, "find_tables_error", err), []DataForgeSimilarTable{}}
}

func dataForgeLookupsFailure(err error) DataForgeFindLookupsResponse {
	return DataForgeFindLookupsResponse{dataForgeHead(false, "find_lookups_error", err), []DataForgeSimilarLookup{}}
}

func dataForgeRelationsFailure(err error) DataForgeRelationsResponse {
	return DataForgeRelationsResponse{dataForgeHead(false, "relations_error", err), []string{}}
}

func dataForgeColumnsFailure(err error) DataForgeColumnsResponse {
	return DataForgeColumnsResponse{dataForgeHead(false, "columns_error", err), []DataForgeColumn{}}
}

func dataForgeContextFailure(err error) DataForgeContextResponse {
	return DataForgeContextResponse{dataForgeEnvelope: dataForgeHead(false, "context_error", err),
		SimilarTables: []DataForgeSimilarTable{}, SimilarLookups: []DataForgeSimilarLookup{},
		Relations: &orderedObject{values: map[string]any{}}, Columns: &orderedObject{values: map[string]any{}}}
}

// dataForgeRequired is clio's EnsureRequired: blank or whitespace-only is missing.
func dataForgeRequired(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required.", name)
	}
	return nil
}

// dataForgeSession carries one tool call. clio checks the platform version once per resolved client; one
// check per call gives the same answers.
type dataForgeSession struct {
	client          *Client
	versionVerified bool
}

// post sends one Data Forge proxy request. Like clio's ExecutePostRequest it returns the body whatever the
// HTTP status: Creatio answers a missing Data Forge configuration with HTTP 400 and an HTML page, and clio
// reports that as an unreadable (empty) answer rather than as a failed call.
func (s *dataForgeSession) post(ctx context.Context, route string, body []byte) ([]byte, error) {
	switch route {
	case dataForgeStatusRoute, dataForgeTablesRoute, dataForgeLookupsRoute, dataForgeRelationsRoute:
	default:
		return nil, fmt.Errorf("unsupported Data Forge route %q", route)
	}
	if err := s.ensureSupportedVersion(ctx); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, dataForgeRequestTimeout)
	defer cancel()
	response, _, err := s.client.doAuthenticated(requestCtx, s.client.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.client.serviceURL(route), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build Data Forge request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		if isTransportError(err) {
			return nil, fmt.Errorf("Creatio service %s transport failure: %w", route, err)
		}
		return nil, err
	}
	payload, err := readResponseLimit(response, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Creatio service %s response: %w", route, err)
	}
	return payload, nil
}

var dataForgeVersionPattern = regexp.MustCompile(`\d+(?:\.\d+){1,3}`)

// dataForgeVersionFields is the order clio's DataForgePlatformVersionGuard searches; names match ignoring case.
var dataForgeVersionFields = []string{"ProductVersion", "productVersion", "CoreVersion", "coreVersion", "Version", "version"}

func dataForgeUnknownVersion() error {
	return fmt.Errorf("Unable to verify Creatio platform version. DataForge MCP tools require Creatio platform version %s or later.", dataForgeMinimumVersionText)
}

// ensureSupportedVersion mirrors DataForgePlatformVersionGuard: Data Forge ships with Creatio 10.0.0, and a
// development build reporting 0.0.0.0 is always allowed.
func (s *dataForgeSession) ensureSupportedVersion(ctx context.Context) error {
	if s.versionVerified {
		return nil
	}
	response, err := s.client.callEnvironmentRoute(ctx, http.MethodPost, applicationInfoRoute, []byte{}, dataForgeRequestTimeout)
	if err != nil {
		return err
	}
	versionText, err := dataForgeExtractVersion(response.payload)
	if err != nil {
		return err
	}
	current, ok := dataForgeParseVersion(dataForgeVersionPattern.FindString(versionText))
	if !ok {
		return dataForgeUnknownVersion()
	}
	if current != [4]int{0, 0, 0, 0} {
		if dataForgeCompareVersions(current, [4]int{10, 0, 0, -1}) < 0 {
			return fmt.Errorf("DataForge MCP tools require Creatio platform version %s or later. Current Creatio platform version: %s. CrtDataForge is included in supported platform versions.",
				dataForgeMinimumVersionText, versionText)
		}
	}
	s.versionVerified = true
	return nil
}

func dataForgeExtractVersion(payload []byte) (string, error) {
	if strings.TrimSpace(string(payload)) == "" {
		return "", dataForgeUnknownVersion()
	}
	document, err := parseOrderedJSON(payload)
	if err != nil {
		// clio lets JsonDocument.Parse throw here, so the call fails with the parser's text.
		return "", fmt.Errorf("GetApplicationInfo returned invalid JSON: %w", err)
	}
	for _, field := range dataForgeVersionFields {
		if value, found := dataForgeFindString(document, field); found && strings.TrimSpace(value) != "" && dataForgeVersionPattern.MatchString(value) {
			return value, nil
		}
	}
	return "", dataForgeUnknownVersion()
}

// dataForgeFindString is clio's depth-first TryFindString: the first property with that name and a string
// value wins, even when that value is not a version; a same-named non-string property is searched into.
func dataForgeFindString(value any, name string) (string, bool) {
	switch typed := value.(type) {
	case *orderedObject:
		for _, key := range typed.keys {
			item := typed.values[key]
			if strings.EqualFold(key, name) {
				if text, ok := item.(string); ok {
					return text, true
				}
			}
			if text, ok := dataForgeFindString(item, name); ok {
				return text, true
			}
		}
	case []any:
		for _, item := range typed {
			if text, ok := dataForgeFindString(item, name); ok {
				return text, true
			}
		}
	}
	return "", false
}

// dataForgeParseVersion is .NET Version.TryParse for 2 to 4 components; a missing component is -1.
func dataForgeParseVersion(text string) ([4]int, bool) {
	version := [4]int{-1, -1, -1, -1}
	parts := strings.Split(text, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return version, false
	}
	for index, part := range parts {
		number, err := strconv.ParseInt(part, 10, 32)
		if err != nil {
			return version, false
		}
		version[index] = int(number)
	}
	return version, true
}

func dataForgeCompareVersions(left, right [4]int) int {
	for index := range left {
		if left[index] != right[index] {
			if left[index] < right[index] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// dataForgeDecode is clio's lenient Deserialize: an empty or unreadable body is "no answer", never an error.
func dataForgeDecode(payload []byte, target any) bool {
	if strings.TrimSpace(string(payload)) == "" {
		return false
	}
	payload = bytes.TrimPrefix(payload, []byte("\xef\xbb\xbf"))
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(target); err != nil {
		return false
	}
	// System.Text.Json refuses trailing content after the document.
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	return true
}

type dataForgeProbe struct {
	HTTPStatusCode int     `json:"HttpStatusCode"`
	Message        *string `json:"Message"`
}

type dataForgeServiceStatus struct {
	IsOnline               bool            `json:"IsOnline"`
	Liveness               *dataForgeProbe `json:"Liveness"`
	Readiness              *dataForgeProbe `json:"Readiness"`
	DataStructureReadiness *string         `json:"DataStructureReadiness"`
	LookupsReadinessInfo   *string         `json:"LookupsReadinessInfo"`
}

// fullStatus is DataForgeMaintenanceClient.GetFullStatus: one GetServiceStatus call read as health and status.
func (s *dataForgeSession) fullStatus(ctx context.Context) (DataForgeHealth, DataForgeMaintenanceStatus, error) {
	payload, err := s.post(ctx, dataForgeStatusRoute, []byte("{}"))
	if err != nil {
		return DataForgeHealth{}, DataForgeMaintenanceStatus{}, err
	}
	var envelope struct {
		Result *dataForgeServiceStatus `json:"GetServiceStatusResult"`
	}
	var status *dataForgeServiceStatus
	if dataForgeDecode(payload, &envelope) && envelope.Result != nil {
		status = envelope.Result
	} else {
		var unwrapped *dataForgeServiceStatus
		if dataForgeDecode(payload, &unwrapped) {
			status = unwrapped
		}
	}
	if status == nil {
		message := "Empty maintenance status response."
		return DataForgeHealth{}, DataForgeMaintenanceStatus{Status: "Unavailable", Error: &message}, nil
	}
	readiness := status.IsOnline && status.Readiness != nil && status.Readiness.HTTPStatusCode == http.StatusOK
	health := DataForgeHealth{
		Liveness: status.IsOnline, Readiness: readiness,
		DataStructureReadiness: readiness && dataForgeReadyText(status.DataStructureReadiness),
		LookupsReadiness:       readiness && dataForgeReadyText(status.LookupsReadinessInfo),
	}
	switch {
	case !status.IsOnline:
		var message *string
		if status.Liveness != nil {
			message = status.Liveness.Message
		}
		return health, DataForgeMaintenanceStatus{Status: "Offline", Error: message}, nil
	case readiness:
		return health, DataForgeMaintenanceStatus{Success: true, Status: "Ready"}, nil
	default:
		var message *string
		if status.Readiness != nil {
			message = status.Readiness.Message
		}
		return health, DataForgeMaintenanceStatus{Status: "NotReady", Error: message}, nil
	}
}

// dataForgeReadyText: a readiness note counts as ready unless it is blank or mentions an error.
func dataForgeReadyText(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != "" && !strings.Contains(strings.ToLower(*value), "error")
}

type dataForgeErrorInfo struct {
	Message *string `json:"Message"`
}

// dataForgeReadPayload is the Success/ErrorInfo head of every DataForgeSchemaReadService result.
type dataForgeReadPayload struct {
	Success   bool                `json:"Success"`
	ErrorInfo *dataForgeErrorInfo `json:"ErrorInfo"`
}

func (p *dataForgeReadPayload) failure(method string) error {
	if p != nil && p.ErrorInfo != nil && p.ErrorInfo.Message != nil {
		return errors.New(*p.ErrorInfo.Message)
	}
	return fmt.Errorf("Empty response from DataForgeSchemaReadService/%s", method)
}

func dataForgeRequestBody(fields map[string]any) []byte {
	encoded, _ := json.Marshal(map[string]any{"request": fields})
	return encoded
}

func (s *dataForgeSession) similarTables(ctx context.Context, query string, limit *int) ([]DataForgeSimilarTable, error) {
	payload, err := s.post(ctx, dataForgeTablesRoute, dataForgeRequestBody(map[string]any{"query": query, "limit": limit}))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result *struct {
			dataForgeReadPayload
			Data []struct {
				Name        *string `json:"Name"`
				Caption     *string `json:"Caption"`
				Description *string `json:"Description"`
			} `json:"Data"`
		} `json:"GetSimilarTableNamesResult"`
	}
	if !dataForgeDecode(payload, &envelope) || envelope.Result == nil {
		return nil, (*dataForgeReadPayload)(nil).failure("GetSimilarTableNames")
	}
	if !envelope.Result.Success {
		return nil, envelope.Result.failure("GetSimilarTableNames")
	}
	tables := []DataForgeSimilarTable{}
	for _, item := range envelope.Result.Data {
		tables = append(tables, DataForgeSimilarTable{Name: dataForgeText(item.Name), Caption: dataForgeText(item.Caption), Description: item.Description})
	}
	return tables, nil
}

func (s *dataForgeSession) similarLookups(ctx context.Context, query string, schemaName *string, limit *int) ([]DataForgeSimilarLookup, error) {
	payload, err := s.post(ctx, dataForgeLookupsRoute, dataForgeRequestBody(map[string]any{"query": query, "schemaName": schemaName, "limit": limit}))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result *struct {
			dataForgeReadPayload
			Data []struct {
				ID                    *string  `json:"id"`
				Name                  *string  `json:"name"`
				ReferenceSchemaName   *string  `json:"referenceSchemaName"`
				ValueID               *string  `json:"valueId"`
				ValueName             *string  `json:"valueName"`
				VectorSimilarityScore *float64 `json:"vectorSimilarityScore"`
			} `json:"Data"`
		} `json:"GetLookupValuesResult"`
	}
	if !dataForgeDecode(payload, &envelope) || envelope.Result == nil {
		return nil, (*dataForgeReadPayload)(nil).failure("GetLookupValues")
	}
	lookups := []DataForgeSimilarLookup{}
	for _, item := range envelope.Result.Data {
		// System.Text.Json reads a Guid only from its 36-character form; anything else voids the whole answer.
		if item.ID != nil && !dataForgeGUIDPattern.MatchString(*item.ID) {
			return nil, (*dataForgeReadPayload)(nil).failure("GetLookupValues")
		}
	}
	if !envelope.Result.Success {
		return nil, envelope.Result.failure("GetLookupValues")
	}
	for _, item := range envelope.Result.Data {
		lookupID := ""
		switch {
		case item.ValueID != nil:
			lookupID = *item.ValueID
		case item.ID != nil:
			lookupID = strings.ToLower(*item.ID)
		}
		schema := item.ReferenceSchemaName
		if schema == nil {
			schema = item.Name
		}
		lookups = append(lookups, DataForgeSimilarLookup{LookupID: lookupID, SchemaName: dataForgeText(schema),
			Value: dataForgeText(item.ValueName), Score: dataForgeDecimal(item.VectorSimilarityScore)})
	}
	return lookups, nil
}

var dataForgeGUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// dataForgeDecimal is clio's (decimal?)double cast, which keeps 15 significant digits.
func dataForgeDecimal(value *float64) *json.Number {
	if value == nil {
		return nil
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(*value, 'g', 15, 64), 64)
	if err != nil {
		return nil
	}
	number := json.Number(strconv.FormatFloat(rounded, 'f', -1, 64))
	return &number
}

func (s *dataForgeSession) relations(ctx context.Context, sourceTable, targetTable string, limit *int) ([]string, error) {
	payload, err := s.post(ctx, dataForgeRelationsRoute, dataForgeRequestBody(map[string]any{"sourceTable": sourceTable, "targetTable": targetTable, "limit": limit}))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result *struct {
			dataForgeReadPayload
			Paths []string `json:"Paths"`
		} `json:"GetTableRelationshipsResult"`
	}
	if !dataForgeDecode(payload, &envelope) || envelope.Result == nil {
		return nil, (*dataForgeReadPayload)(nil).failure("GetTableRelationships")
	}
	if !envelope.Result.Success {
		return nil, envelope.Result.failure("GetTableRelationships")
	}
	if envelope.Result.Paths == nil {
		return []string{}, nil
	}
	return envelope.Result.Paths, nil
}

// columns is DataForgeRuntimeSchemaMapper.MapColumns over the runtime schema read by name. The name is sent
// as given, untrimmed, as clio does. This read does not go through the Data Forge version guard.
func (c *Client) dataForgeColumns(ctx context.Context, tableName string) ([]DataForgeColumn, error) {
	schema, err := c.readRichRuntimeSchema(ctx, map[string]string{"Name": tableName},
		fmt.Sprintf("Runtime schema '%s' was not returned by Creatio.", tableName))
	if err != nil {
		return nil, err
	}
	columns := []DataForgeColumn{}
	for _, column := range schema.Columns.Items {
		if column.IsInherited {
			continue
		}
		dataType, ok := dataForgeDataValueTypeNames[column.DataValueType]
		if !ok {
			dataType = strconv.Itoa(column.DataValueType)
		}
		columns = append(columns, DataForgeColumn{Name: column.Name, Caption: dataForgeLocalized(column.Caption),
			Description: dataForgeLocalized(column.Description), DataType: dataType, Required: column.IsRequired,
			ReferenceSchemaName: column.ReferenceSchemaName})
	}
	sort.SliceStable(columns, func(i, j int) bool { return compareOrdinalIgnoreCase(columns[i].Name, columns[j].Name) < 0 })
	return columns, nil
}

// dataForgeLocalized is clio's GetLocalizedValue: a JSON null or absent caption is null, not an empty string.
func dataForgeLocalized(value json.RawMessage) *string {
	if trimmed := strings.TrimSpace(string(value)); trimmed == "" || trimmed == "null" {
		return nil
	}
	return runtimeLocalizedText(value)
}

func dataForgeText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// DataForgeStatus answers dataforge-status.
func (c *Client) DataForgeStatus(ctx context.Context) DataForgeStatusResponse {
	session := &dataForgeSession{client: c}
	health, status, err := session.fullStatus(ctx)
	if err != nil {
		return DataForgeStatusResponse{dataForgeEnvelope: dataForgeHead(false, "status_error", err)}
	}
	return DataForgeStatusResponse{dataForgeHead(true, "", nil), &health, &status}
}

// DataForgeFindTables answers dataforge-find-tables.
func (c *Client) DataForgeFindTables(ctx context.Context, query string, limit *int) DataForgeFindTablesResponse {
	if err := dataForgeRequired(query, "query"); err != nil {
		return dataForgeTablesFailure(err)
	}
	tables, err := (&dataForgeSession{client: c}).similarTables(ctx, query, limit)
	if err != nil {
		return dataForgeTablesFailure(err)
	}
	return DataForgeFindTablesResponse{dataForgeHead(true, "", nil), tables}
}

// DataForgeFindLookups answers dataforge-find-lookups. A nil schemaName is sent as null, as clio sends it.
func (c *Client) DataForgeFindLookups(ctx context.Context, query string, schemaName *string, limit *int) DataForgeFindLookupsResponse {
	if err := dataForgeRequired(query, "query"); err != nil {
		return dataForgeLookupsFailure(err)
	}
	lookups, err := (&dataForgeSession{client: c}).similarLookups(ctx, query, schemaName, limit)
	if err != nil {
		return dataForgeLookupsFailure(err)
	}
	return DataForgeFindLookupsResponse{dataForgeHead(true, "", nil), lookups}
}

// DataForgeGetRelations answers dataforge-get-relations.
func (c *Client) DataForgeGetRelations(ctx context.Context, sourceTable, targetTable string, limit *int) DataForgeRelationsResponse {
	if err := dataForgeRequired(sourceTable, "source-table"); err != nil {
		return dataForgeRelationsFailure(err)
	}
	if err := dataForgeRequired(targetTable, "target-table"); err != nil {
		return dataForgeRelationsFailure(err)
	}
	paths, err := (&dataForgeSession{client: c}).relations(ctx, sourceTable, targetTable, limit)
	if err != nil {
		return dataForgeRelationsFailure(err)
	}
	return DataForgeRelationsResponse{dataForgeHead(true, "", nil), paths}
}

// DataForgeGetTableColumns answers dataforge-get-table-columns.
func (c *Client) DataForgeGetTableColumns(ctx context.Context, tableName string) DataForgeColumnsResponse {
	if err := dataForgeRequired(tableName, "table-name"); err != nil {
		return dataForgeColumnsFailure(err)
	}
	columns, err := c.dataForgeColumns(ctx, tableName)
	if err != nil {
		return dataForgeColumnsFailure(err)
	}
	return DataForgeColumnsResponse{dataForgeHead(true, "", nil), columns}
}

// dataForgeWarnings is clio's AddDedupedWarning: the first failure of a cause keeps the plain
// "category:item:message" line, and later items with the same category and message are appended to it.
type dataForgeWarnings struct {
	lines   []string
	byCause map[string]*dataForgeCollapsedWarning
}

type dataForgeCollapsedWarning struct {
	index                   int
	category, first, reason string
	also                    []string
}

func (w *dataForgeCollapsedWarning) render() string {
	head := w.category + ":" + w.first + ":" + w.reason
	if len(w.also) == 0 {
		return head
	}
	return head + " (also: " + strings.Join(w.also, ", ") + ")"
}

func (w *dataForgeWarnings) add(category, item string, err error) {
	key := category + "\x00" + err.Error()
	if collapsed, ok := w.byCause[key]; ok {
		collapsed.also = append(collapsed.also, item)
		w.lines[collapsed.index] = collapsed.render()
		return
	}
	collapsed := &dataForgeCollapsedWarning{index: len(w.lines), category: category, first: item, reason: err.Error()}
	w.byCause[key] = collapsed
	w.lines = append(w.lines, collapsed.render())
}

// dataForgeNormalizeTerms is clio's NormalizeTerms: trimmed, blanks dropped, first spelling of each
// case-insensitive duplicate kept, and the fallback used only when nothing is left.
func dataForgeNormalizeTerms(terms []*string, fallback string) []string {
	values := []string{}
	seen := map[string]bool{}
	for _, term := range terms {
		if term == nil || strings.TrimSpace(*term) == "" {
			continue
		}
		value := strings.TrimSpace(*term)
		if key := strings.ToUpper(value); !seen[key] {
			seen[key] = true
			values = append(values, value)
		}
	}
	if len(values) == 0 && strings.TrimSpace(fallback) != "" {
		values = append(values, strings.TrimSpace(fallback))
	}
	return values
}

// dataForgeSetIgnoreCase is a Dictionary with OrdinalIgnoreCase: a repeated key keeps its first spelling and
// position and takes the new value.
func dataForgeSetIgnoreCase(object *orderedObject, key string, value any) {
	for _, existing := range object.keys {
		if strings.EqualFold(existing, key) {
			object.values[existing] = value
			return
		}
	}
	object.keys = append(object.keys, key)
	object.values[key] = value
}

// DataForgeContext answers dataforge-context, mirroring DataForgeContextService.GetContext.
func (c *Client) DataForgeContext(ctx context.Context, request DataForgeContextRequest) DataForgeContextResponse {
	for _, pair := range request.RelationPairs {
		if pair == nil {
			// clio dereferences each pair while building the request.
			return dataForgeContextFailure(errors.New("Object reference not set to an instance of an object."))
		}
	}
	session := &dataForgeSession{client: c}
	health, status, err := session.fullStatus(ctx)
	if err != nil {
		return dataForgeContextFailure(err)
	}
	warnings := &dataForgeWarnings{lines: []string{}, byCause: map[string]*dataForgeCollapsedWarning{}}

	tableTerms := dataForgeNormalizeTerms(request.CandidateTerms, request.RequirementSummary)
	var tables []DataForgeSimilarTable
	for _, term := range tableTerms {
		found, err := session.similarTables(ctx, term, nil)
		if err != nil {
			warnings.add("tables", term, err)
			continue
		}
		tables = append(tables, found...)
	}

	lookupTerms := dataForgeNormalizeTerms(request.LookupHints, "")
	var lookups []DataForgeSimilarLookup
	for _, hint := range lookupTerms {
		found, err := session.similarLookups(ctx, hint, nil, nil)
		if err != nil {
			warnings.add("lookups", hint, err)
			continue
		}
		lookups = append(lookups, found...)
	}

	relations := &orderedObject{values: map[string]any{}}
	for _, pair := range request.RelationPairs {
		if pair.SourceTable == nil || pair.TargetTable == nil || strings.TrimSpace(*pair.SourceTable) == "" || strings.TrimSpace(*pair.TargetTable) == "" {
			continue
		}
		key := *pair.SourceTable + "->" + *pair.TargetTable
		paths, err := session.relations(ctx, *pair.SourceTable, *pair.TargetTable, nil)
		if err != nil {
			warnings.add("relations", key, err)
			continue
		}
		dataForgeSetIgnoreCase(relations, key, paths)
	}

	distinctTables := dataForgeDistinctTables(tables)
	columns := &orderedObject{values: map[string]any{}}
	for _, table := range distinctTables {
		mapped, err := c.dataForgeColumns(ctx, table.Name)
		if err != nil {
			warnings.add("columns", table.Name, err)
			continue
		}
		dataForgeSetIgnoreCase(columns, table.Name, mapped)
	}
	distinctLookups := dataForgeDistinctLookups(lookups)

	return DataForgeContextResponse{
		dataForgeEnvelope: dataForgeEnvelope{Success: true, Source: dataForgeSource, CorrelationID: health.CorrelationID, Warnings: warnings.lines},
		Health:            &health, Status: &status, SimilarTables: distinctTables, SimilarLookups: distinctLookups,
		Relations: relations, Columns: columns,
		Coverage: DataForgeCoverage{
			Health:       true,
			Tables:       len(distinctTables) > 0 || len(tableTerms) == 0,
			Lookups:      len(distinctLookups) > 0 || len(lookupTerms) == 0,
			Relations:    relations.len() > 0 || len(request.RelationPairs) == 0,
			TableColumns: columns.len() == len(distinctTables),
		},
	}
}

func dataForgeDistinctTables(tables []DataForgeSimilarTable) []DataForgeSimilarTable {
	distinct := []DataForgeSimilarTable{}
	seen := map[string]bool{}
	for _, table := range tables {
		if key := strings.ToUpper(table.Name); !seen[key] {
			seen[key] = true
			distinct = append(distinct, table)
		}
	}
	sort.SliceStable(distinct, func(i, j int) bool { return compareOrdinalIgnoreCase(distinct[i].Name, distinct[j].Name) < 0 })
	return distinct
}

func dataForgeDistinctLookups(lookups []DataForgeSimilarLookup) []DataForgeSimilarLookup {
	distinct := []DataForgeSimilarLookup{}
	seen := map[string]bool{}
	for _, lookup := range lookups {
		if key := strings.ToUpper(lookup.SchemaName + ":" + lookup.Value); !seen[key] {
			seen[key] = true
			distinct = append(distinct, lookup)
		}
	}
	sort.SliceStable(distinct, func(i, j int) bool {
		if order := compareOrdinalIgnoreCase(distinct[i].SchemaName, distinct[j].SchemaName); order != 0 {
			return order < 0
		}
		return compareOrdinalIgnoreCase(distinct[i].Value, distinct[j].Value) < 0
	})
	return distinct
}
