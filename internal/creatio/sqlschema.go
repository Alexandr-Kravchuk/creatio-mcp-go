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
	BodyLength  int    `json:"bodyLength"` // always present, 0 on failure, as in clio
	Error       string `json:"error,omitempty"`
}

// GetSQLSchema resolves one SQL script by name and fetches its designer body.
// SQL script names must be unique across packages and database engines, matching Clio's refusal.
func (c *Client) GetSQLSchema(ctx context.Context, schemaName string) SQLSchemaResult {
	return c.GetSQLSchemaTo(ctx, schemaName, "")
}

// GetSQLSchemaTo is GetSQLSchema with clio 8.1.0.142's output-file: the path is confined before any request
// (the workspace or the OS temp directory, no existing target), the body is written there atomically and
// left out of the response.
func (c *Client) GetSQLSchemaTo(ctx context.Context, schemaName, outputFile string) SQLSchemaResult {
	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return SQLSchemaResult{Error: "schema-name is required"}
	}
	outputPath := ""
	if strings.TrimSpace(outputFile) != "" {
		resolved, refusal := schemaGetResolveOutput(outputFile)
		if refusal != "" {
			return SQLSchemaResult{Error: refusal}
		}
		outputPath = resolved
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
	result := SQLSchemaResult{
		Success: true, SchemaName: firstNonEmpty(schema.Name, schemaName), SchemaUID: schemaUID,
		PackageName: schema.Package.Name, Caption: schemaCaption(schema.Caption, c.profileCultureFunc(ctx)),
		BodyLength: utf16Length(schema.Body),
	}
	if outputPath != "" {
		if err := schemaGetWriteAtomic(outputPath, []byte(schema.Body)); err != nil {
			return SQLSchemaResult{Error: err.Error()}
		}
		return result
	}
	result.Body = schema.Body
	return result
}

// schemaCaption is clio 8.1.0.142's SchemaDesignerHelper.ExtractCaption: of several non-blank localized
// values the one in the preferred culture (the user's profile culture, asked only when needed), then en-US,
// then the first.
func schemaCaption(caption json.RawMessage, preferredCulture func() string) string {
	var entries []json.RawMessage
	if json.Unmarshal(caption, &entries) != nil {
		var scalar string
		_ = json.Unmarshal(caption, &scalar)
		return scalar
	}
	type localized struct{ culture, value string }
	var values []localized
	for _, raw := range entries {
		var entry map[string]json.RawMessage
		if json.Unmarshal(raw, &entry) != nil || entry == nil {
			continue
		}
		value := rowText(entry, "value")
		if strings.TrimSpace(value) == "" {
			continue
		}
		values = append(values, localized{rowText(entry, "cultureName"), value})
	}
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0].value
	}
	find := func(culture string) (string, bool) {
		if strings.TrimSpace(culture) == "" {
			return "", false
		}
		for _, entry := range values {
			if strings.EqualFold(entry.culture, strings.TrimSpace(culture)) {
				return entry.value, true
			}
		}
		return "", false
	}
	preferred := ""
	if preferredCulture != nil {
		preferred = preferredCulture()
	}
	if value, ok := find(preferred); ok {
		return value
	}
	if value, ok := find(schemaWriteDefaultCulture); ok {
		return value
	}
	return values[0].value
}

// profileCultureFunc defers the profile-culture request until a caption really has several cultures.
func (c *Client) profileCultureFunc(ctx context.Context) func() string {
	return func() string { return c.schemaWriteProfileCulture(ctx) }
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
