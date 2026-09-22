package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SQLSchemaResult struct {
	Success     bool   `json:"success"`
	SchemaName  string `json:"schemaName,omitempty"`
	SchemaUID   string `json:"schemaUId,omitempty"`
	PackageName string `json:"packageName,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Body        string `json:"body,omitempty"`
	BodyLength  int    `json:"bodyLength,omitempty"`
	Error       string `json:"error,omitempty"`
}

// GetSQLSchema resolves one SQL script by name and fetches its designer body.
// SQL script names must be unique across packages and database engines, matching Clio's refusal.
func (c *Client) GetSQLSchema(ctx context.Context, schemaName string) SQLSchemaResult {
	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return SQLSchemaResult{Error: "schema-name is required"}
	}
	query := map[string]any{
		"rootSchemaName": "VwSysSqlScriptInPackage", "operationType": 0, "rowCount": 2,
		"columns": map[string]any{"items": map[string]any{
			"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}},
		}},
		"filters": pageFilters(map[string]any{
			"byName": pageComparisonFilter("Name", schemaName, 1, 3),
		}),
	}
	rows, err := c.selectRows(ctx, query)
	if err != nil {
		return SQLSchemaResult{Error: err.Error()}
	}
	if len(rows) == 0 {
		return SQLSchemaResult{Error: fmt.Sprintf("SQL script schema %q was not found.", schemaName)}
	}
	if len(rows) > 1 {
		return SQLSchemaResult{Error: fmt.Sprintf("SQL script name %q is ambiguous across packages or database engines. Use a unique name.", schemaName)}
	}
	schemaUID := rowString(rows[0], "UId")
	if schemaUID == "" {
		return SQLSchemaResult{Error: fmt.Sprintf("Schema %q metadata is missing UId", schemaName)}
	}
	requestBody, err := json.Marshal(map[string]any{"schemaUId": schemaUID, "useFullHierarchy": false})
	if err != nil {
		return SQLSchemaResult{Error: fmt.Sprintf("encode SQL schema designer request: %v", err)}
	}
	responseBody, err := c.postCreatioServiceJSON(ctx, "ServiceModel/SqlScriptSchemaDesignerService.svc/GetSchema", requestBody, 45*time.Second, maxResponseBytes)
	if err != nil {
		return SQLSchemaResult{Error: err.Error()}
	}
	var response struct {
		Schema    json.RawMessage `json:"schema"`
		ErrorInfo struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return SQLSchemaResult{Error: fmt.Sprintf("SQL schema designer returned invalid JSON: %v", err)}
	}
	if len(response.Schema) == 0 || string(response.Schema) == "null" {
		if response.ErrorInfo.Message != "" {
			return SQLSchemaResult{Error: fmt.Sprintf("Failed to load schema %q via SqlScriptSchemaDesignerService: %s", schemaName, response.ErrorInfo.Message)}
		}
		return SQLSchemaResult{Error: fmt.Sprintf("Failed to load schema %q via SqlScriptSchemaDesignerService", schemaName)}
	}
	var schema struct {
		Name    string          `json:"name"`
		Body    string          `json:"body"`
		Caption json.RawMessage `json:"caption"`
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
	}
	if err := json.Unmarshal(response.Schema, &schema); err != nil {
		return SQLSchemaResult{Error: fmt.Sprintf("SQL schema designer returned an invalid schema: %v", err)}
	}
	return SQLSchemaResult{
		Success: true, SchemaName: firstNonEmpty(schema.Name, schemaName), SchemaUID: schemaUID,
		PackageName: schema.Package.Name, Caption: schemaCaption(schema.Caption),
		Body: schema.Body, BodyLength: utf16Length(schema.Body),
	}
}

func schemaCaption(caption json.RawMessage) string {
	var values []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(caption, &values) == nil && len(values) > 0 {
		return values[0].Value
	}
	var scalar string
	_ = json.Unmarshal(caption, &scalar)
	return scalar
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
