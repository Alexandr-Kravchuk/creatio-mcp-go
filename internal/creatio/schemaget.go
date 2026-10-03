package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// schemaGetKind is clio's SchemaDesignerKind for the two read tools here: the SysSchema manager a name
// resolves under and the designer service that returns the body.
type schemaGetKind struct {
	managerName string
	serviceName string
	getRoute    string
}

var (
	schemaGetSourceCode = schemaGetKind{"SourceCodeSchemaManager", "SourceCodeSchemaDesignerService",
		"ServiceModel/SourceCodeSchemaDesignerService.svc/GetSchema"}
	schemaGetClientUnit = schemaGetKind{"ClientUnitSchemaManager", "ClientUnitSchemaDesignerService",
		"ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema"}
)

// schemaGetResponseBytes bounds a designer GetSchema answer; a full-hierarchy client-unit read carries every
// localizable string of the chain.
const schemaGetResponseBytes = 32 << 20

type SourceCodeSchemaRequest struct {
	SchemaName string
	OutputFile string
}

// SourceCodeSchemaResult is clio's get-schema envelope. Body is left out when output-file received it.
type SourceCodeSchemaResult struct {
	Success     bool   `json:"success"`
	SchemaName  string `json:"schemaName,omitempty"`
	SchemaUID   string `json:"schemaUId,omitempty"`
	PackageName string `json:"packageName,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Body        string `json:"body,omitempty"`
	BodyLength  int    `json:"bodyLength"`
	Error       string `json:"error,omitempty"`
}

type ClientUnitSchemaRequest struct {
	SchemaName    string
	SchemaUID     string
	OutputFile    string
	FullHierarchy bool
}

// ClientUnitSchemaResult is clio's get-client-unit-schema envelope. LocalizableStrings is present only for a
// full-hierarchy read that returned the content inline; it is then an array even when empty.
type ClientUnitSchemaResult struct {
	Success                bool                       `json:"success"`
	SchemaName             string                     `json:"schemaName,omitempty"`
	SchemaUID              string                     `json:"schemaUId,omitempty"`
	PackageName            string                     `json:"packageName,omitempty"`
	Caption                string                     `json:"caption,omitempty"`
	Body                   string                     `json:"body,omitempty"`
	BodyLength             int                        `json:"bodyLength"`
	FullHierarchy          bool                       `json:"fullHierarchy"`
	LocalizableStringCount int                        `json:"localizableStringCount"`
	LocalizableStrings     *[]MergedLocalizableString `json:"localizableStrings,omitempty"`
	Error                  string                     `json:"error,omitempty"`
}

// MergedLocalizableString is one string of a full-hierarchy schema, with the schema that contributed it.
type MergedLocalizableString struct {
	Name            *string                        `json:"name,omitempty"`
	ParentSchemaUID *string                        `json:"parentSchemaUId,omitempty"`
	UID             *string                        `json:"uId,omitempty"`
	Values          []MergedLocalizableStringValue `json:"values"`
}

type MergedLocalizableStringValue struct {
	CultureName *string `json:"cultureName,omitempty"`
	Value       *string `json:"value,omitempty"`
}

// schemaGetDesignerSchema is the part of a designer GetSchema answer the read tools report.
type schemaGetDesignerSchema struct {
	values map[string]json.RawMessage
}

func (s schemaGetDesignerSchema) body() string { return rowText(s.values, "body") }

func (s schemaGetDesignerSchema) name() string {
	if raw, ok := s.values["name"]; ok && string(raw) != "null" {
		return rowText(s.values, "name")
	}
	return ""
}

func (s schemaGetDesignerSchema) packageName() string {
	var pkg map[string]json.RawMessage
	if json.Unmarshal(s.values["package"], &pkg) != nil {
		return ""
	}
	return rowText(pkg, "name")
}

// GetSourceCodeSchema reads a C# source-code schema the way clio's get-schema does: name to UId through
// SysSchema, then SourceCodeSchemaDesignerService.GetSchema. With output-file the body goes to that file.
func (c *Client) GetSourceCodeSchema(ctx context.Context, input SourceCodeSchemaRequest) SourceCodeSchemaResult {
	if strings.TrimSpace(input.SchemaName) == "" {
		return SourceCodeSchemaResult{Error: "schema-name is required"}
	}
	outputPath := ""
	if strings.TrimSpace(input.OutputFile) != "" {
		resolved, refusal := schemaGetResolveOutput(input.OutputFile)
		if refusal != "" {
			return SourceCodeSchemaResult{Error: refusal}
		}
		outputPath = resolved
	}
	schemaUID, err := c.schemaGetResolveSingle(ctx, input.SchemaName, schemaGetSourceCode)
	if err != nil {
		return SourceCodeSchemaResult{Error: err.Error()}
	}
	schema, err := c.schemaGetLoad(ctx, schemaUID, schemaGetSourceCode, input.SchemaName, false)
	if err != nil {
		return SourceCodeSchemaResult{Error: err.Error()}
	}
	body := schema.body()
	result := SourceCodeSchemaResult{
		Success: true, SchemaName: firstNonEmpty(schema.name(), input.SchemaName), SchemaUID: schemaUID,
		PackageName: schema.packageName(), Caption: schemaCaption(schema.values["caption"]), BodyLength: utf16Length(body),
	}
	if outputPath != "" {
		if err := schemaGetWriteAtomic(outputPath, []byte(body)); err != nil {
			return SourceCodeSchemaResult{Error: err.Error()}
		}
		return result
	}
	result.Body = body
	return result
}

// GetClientUnitSchema reads a client-unit (JavaScript) schema the way clio's get-client-unit-schema does. A
// name resolves to its top (most-derived) layer; schema-uid targets one layer as given. full-hierarchy also
// returns the localizable strings merged across the chain; the body stays the layer's own.
func (c *Client) GetClientUnitSchema(ctx context.Context, input ClientUnitSchemaRequest) ClientUnitSchemaResult {
	outputPath := ""
	if strings.TrimSpace(input.OutputFile) != "" {
		resolved, refusal := schemaGetResolveOutput(input.OutputFile)
		if refusal != "" {
			return ClientUnitSchemaResult{Error: refusal}
		}
		outputPath = resolved
	}
	schemaUID := input.SchemaUID
	if strings.TrimSpace(schemaUID) == "" {
		if strings.TrimSpace(input.SchemaName) == "" {
			return ClientUnitSchemaResult{Error: "schema-name or schema-uid is required"}
		}
		resolved, err := c.schemaGetResolveTopLayer(ctx, input.SchemaName)
		if err != nil {
			return ClientUnitSchemaResult{Error: err.Error()}
		}
		schemaUID = resolved
	}
	schema, err := c.schemaGetLoad(ctx, schemaUID, schemaGetClientUnit, input.SchemaName, input.FullHierarchy)
	if err != nil {
		return ClientUnitSchemaResult{Error: err.Error()}
	}
	body := schema.body()
	schemaName := firstNonEmpty(schema.name(), input.SchemaName)
	result := ClientUnitSchemaResult{
		Success: true, SchemaName: schemaName, SchemaUID: schemaUID, PackageName: schema.packageName(),
		Caption: schemaCaption(schema.values["caption"]), BodyLength: utf16Length(body), FullHierarchy: input.FullHierarchy,
	}
	if input.FullHierarchy {
		merged := schemaGetMergedStrings(schema.values["localizableStrings"])
		result.LocalizableStringCount = len(merged)
		if outputPath != "" {
			// clio writes the merged strings with their provenance, not a raw designer dump, as indented JSON.
			content := orderedFields{{"schemaName", schemaName}, {"schemaUId", schemaUID}, {"fullHierarchy", true},
				{"body", body}, {"localizableStrings", schemaGetStringsNode(merged)}}
			if err := schemaGetWriteAtomic(outputPath, toJNode(content).stjIndentedJSON()); err != nil {
				return ClientUnitSchemaResult{Error: err.Error()}
			}
			return result
		}
		result.Body = body
		result.LocalizableStrings = &merged
		return result
	}
	if outputPath != "" {
		if err := schemaGetWriteAtomic(outputPath, []byte(body)); err != nil {
			return ClientUnitSchemaResult{Error: err.Error()}
		}
		return result
	}
	result.Body = body
	return result
}

// schemaGetResolveSingle is clio's single-row UId lookup, used for source-code schemas.
func (c *Client) schemaGetResolveSingle(ctx context.Context, schemaName string, kind schemaGetKind) (string, error) {
	query := map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0, "rowCount": 1,
		"columns": map[string]any{"items": map[string]any{
			"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}},
		}},
		"filters": schemaGetNameAndManagerFilters(schemaName, kind.managerName),
	}
	rows, err := c.schemaGetSelect(ctx, query, schemaName)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("Schema '%s' not found (ManagerName='%s')", schemaName, kind.managerName)
	}
	uid := rowText(rows[0], "UId")
	if strings.TrimSpace(uid) == "" {
		return "", fmt.Errorf("Schema '%s' metadata is missing UId", schemaName)
	}
	return uid, nil
}

// schemaGetLayer is one SysSchema row of a client-unit name: the package that defines or replaces it.
type schemaGetLayer struct {
	uid, name, packageName string
	hierarchyLevel         int
}

// schemaGetResolveTopLayer enumerates every same-named client-unit layer, orders them base to top by package
// hierarchy level with the package name as a case-insensitive tiebreaker, and returns the top layer's UId.
func (c *Client) schemaGetResolveTopLayer(ctx context.Context, schemaName string) (string, error) {
	layers, err := c.schemaGetLayers(ctx, schemaName)
	if err != nil {
		return "", err
	}
	if len(layers) == 0 {
		return "", fmt.Errorf("Schema '%s' not found (ManagerName='%s')", schemaName, schemaGetClientUnit.managerName)
	}
	uid := layers[len(layers)-1].uid
	if strings.TrimSpace(uid) == "" {
		return "", fmt.Errorf("Schema '%s' metadata is missing UId", schemaName)
	}
	return uid, nil
}

func (c *Client) schemaGetLayers(ctx context.Context, schemaName string) ([]schemaGetLayer, error) {
	column := func(path string) map[string]any {
		return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
	}
	packageName, level := column("SysPackage.Name"), column("SysPackage.HierarchyLevel")
	packageName["orderDirection"], packageName["orderPosition"] = 1, 1
	level["orderDirection"], level["orderPosition"] = 1, 0
	query := map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0, "rowCount": -1,
		"columns": map[string]any{"items": map[string]any{
			"UId": column("UId"), "Name": column("Name"), "PackageName": packageName, "HierarchyLevel": level,
		}},
		"filters": schemaGetNameAndManagerFilters(schemaName, schemaGetClientUnit.managerName),
	}
	rows, err := c.schemaGetSelect(ctx, query, schemaName)
	if err != nil {
		return nil, err
	}
	layers := make([]schemaGetLayer, 0, len(rows))
	for _, row := range rows {
		var level int
		_ = json.Unmarshal(row["HierarchyLevel"], &level)
		layers = append(layers, schemaGetLayer{uid: rowText(row, "UId"), name: rowText(row, "Name"),
			packageName: rowText(row, "PackageName"), hierarchyLevel: level})
	}
	// The client-side order is authoritative, so the result does not depend on the row order DataService returns.
	sort.SliceStable(layers, func(i, j int) bool {
		if layers[i].hierarchyLevel != layers[j].hierarchyLevel {
			return layers[i].hierarchyLevel < layers[j].hierarchyLevel
		}
		return compareOrdinalIgnoreCase(layers[i].packageName, layers[j].packageName) < 0
	})
	return layers, nil
}

// schemaGetSelect runs a SysSchema lookup and words a DataService failure envelope as clio does; a transport
// or authentication failure keeps its own text.
func (c *Client) schemaGetSelect(ctx context.Context, query map[string]any, schemaName string) ([]map[string]json.RawMessage, error) {
	rows, err := c.selectRows(ctx, query)
	if err == nil {
		return rows, nil
	}
	if message, ok := strings.CutPrefix(err.Error(), "SelectQuery failed: "); ok && !isTransportError(err) && !isAuthenticationError(err) {
		return nil, fmt.Errorf("SelectQuery for schema '%s' failed: %s", schemaName, message)
	}
	return nil, err
}

func schemaGetNameAndManagerFilters(schemaName, managerName string) map[string]any {
	return map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
		"byName": eqFilter("Name", schemaName, 1), "byManager": eqFilter("ManagerName", managerName, 1),
	}}
}

// schemaGetLoad calls the designer GetSchema. A missing schema object is a failed load that carries the
// service's own reason when it gave one.
func (c *Client) schemaGetLoad(ctx context.Context, schemaUID string, kind schemaGetKind, schemaName string, fullHierarchy bool) (schemaGetDesignerSchema, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "useFullHierarchy": fullHierarchy})
	response, err := c.postCreatioServiceJSON(ctx, kind.getRoute, body, 120*time.Second, schemaGetResponseBytes)
	if err != nil {
		return schemaGetDesignerSchema{}, err
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(response, &decoded); err != nil {
		return schemaGetDesignerSchema{}, fmt.Errorf("%s GetSchema returned invalid JSON: %w", kind.serviceName, err)
	}
	var schema map[string]json.RawMessage
	if raw := decoded["schema"]; len(raw) == 0 || json.Unmarshal(raw, &schema) != nil || schema == nil {
		label := schemaName
		if strings.TrimSpace(label) == "" {
			label = schemaUID
		}
		var errorInfo map[string]json.RawMessage
		failure := ""
		if json.Unmarshal(decoded["errorInfo"], &errorInfo) == nil && errorInfo != nil {
			failure = rowText(errorInfo, "message")
		}
		if strings.TrimSpace(failure) == "" {
			return schemaGetDesignerSchema{}, fmt.Errorf("Failed to load schema '%s' via %s", label, kind.serviceName)
		}
		return schemaGetDesignerSchema{}, fmt.Errorf("Failed to load schema '%s' via %s: %s", label, kind.serviceName, failure)
	}
	return schemaGetDesignerSchema{values: schema}, nil
}

// schemaGetMergedStrings is clio's ExtractMergedLocalizableStrings: each entry keeps its parentSchemaUId and
// per-culture values; absent fields stay absent.
func schemaGetMergedStrings(raw json.RawMessage) []MergedLocalizableString {
	result := []MergedLocalizableString{}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return result
	}
	for _, rawEntry := range entries {
		var entry map[string]json.RawMessage
		_ = json.Unmarshal(rawEntry, &entry)
		values := []MergedLocalizableStringValue{}
		var rawValues []json.RawMessage
		if json.Unmarshal(entry["values"], &rawValues) == nil {
			for _, rawValue := range rawValues {
				var value map[string]json.RawMessage
				_ = json.Unmarshal(rawValue, &value)
				values = append(values, MergedLocalizableStringValue{
					CultureName: schemaGetTokenText(value, "cultureName"), Value: schemaGetTokenText(value, "value")})
			}
		}
		result = append(result, MergedLocalizableString{Name: schemaGetTokenText(entry, "name"),
			ParentSchemaUID: schemaGetTokenText(entry, "parentSchemaUId"), UID: schemaGetTokenText(entry, "uId"), Values: values})
	}
	return result
}

// schemaGetTokenText is Newtonsoft's token?.ToString(): an absent key is null, a JSON null is empty text.
func schemaGetTokenText(values map[string]json.RawMessage, key string) *string {
	if _, ok := values[key]; !ok {
		return nil
	}
	return stringPointer(rowText(values, key))
}

// schemaGetStringsNode renders the merged strings for the output file. System.Text.Json writes null members
// there, unlike the MCP response.
func schemaGetStringsNode(values []MergedLocalizableString) []any {
	nodes := make([]any, 0, len(values))
	for _, value := range values {
		cultures := make([]any, 0, len(value.Values))
		for _, culture := range value.Values {
			cultures = append(cultures, orderedFields{{"cultureName", culture.CultureName}, {"value", culture.Value}})
		}
		nodes = append(nodes, orderedFields{{"name", value.Name}, {"parentSchemaUId", value.ParentSchemaUID},
			{"uId", value.UID}, {"values", cultures}})
	}
	return nodes
}
