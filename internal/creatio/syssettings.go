package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	schemaNamePrefixSettingCode = "SchemaNamePrefix"
	currentPackageSettingCode   = "CurrentPackageId"
	genericSchemaNamePrefixRead = "Failed to read SchemaNamePrefix."
)

type SchemaNamePrefixResult struct {
	Success          bool   `json:"success"`
	SchemaNamePrefix string `json:"schema-name-prefix"`
	Error            string `json:"error,omitempty"`
	// The classification clio adds when the environment itself cannot be resolved.
	ErrorCategory  string `json:"error-category,omitempty"`
	Cause          string `json:"cause,omitempty"`
	RecoveryAction string `json:"recovery-action,omitempty"`
}

// SchemaNamePrefixConfigurationFailure is clio's answer when the environment cannot be resolved.
func SchemaNamePrefixConfigurationFailure(cause string) SchemaNamePrefixResult {
	failure := SysSettingConfigurationFailure(genericSchemaNamePrefixRead, cause)
	return SchemaNamePrefixResult{Error: failure.Error, ErrorCategory: failure.ErrorCategory, Cause: failure.Cause, RecoveryAction: failure.RecoveryAction}
}

// GetSchemaNamePrefix reads the SchemaNamePrefix system setting the way clio does: the value is trimmed,
// stripped of surrounding quotes and trimmed again. An empty prefix is a successful answer.
func (c *Client) GetSchemaNamePrefix(ctx context.Context) SchemaNamePrefixResult {
	value, err := c.readSysSettingValue(ctx, schemaNamePrefixSettingCode)
	if err != nil {
		// clio promotes only authentication and network messages into error; anything else gets its generic label.
		message := genericSchemaNamePrefixRead
		if isAuthenticationError(err) || isTransportError(err) {
			message = err.Error()
		}
		return SchemaNamePrefixResult{Error: message}
	}
	return SchemaNamePrefixResult{Success: true, SchemaNamePrefix: strings.TrimSpace(strings.Trim(strings.TrimSpace(value), `"`))}
}

// readSysSettingValue mirrors clio's SysSettingsManager.GetSysSettingValueByCode: QuerySysSettings first,
// then the SysSettingsValue row of the All employees unit when that answer is empty or unusable. A failure
// that never reached the server, or a rejected session, is returned instead of falling back.
func (c *Client) readSysSettingValue(ctx context.Context, code string) (string, error) {
	value, err := c.querySysSetting(ctx, code)
	if err != nil && (isAuthenticationError(err) || isTransportError(err) || ctx.Err() != nil) {
		return "", err
	}
	if err == nil && value != "" {
		return value, nil
	}
	return c.selectSysSettingValue(ctx, code)
}

func (c *Client) querySysSetting(ctx context.Context, code string) (string, error) {
	body, err := json.Marshal(map[string]any{"sysSettingsNameCollection": []string{code}})
	if err != nil {
		return "", err
	}
	response, err := c.postDataServiceJSON(ctx, "QuerySysSettings", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", err
	}
	var decoded struct {
		Success *bool                      `json:"success"`
		Values  map[string]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return "", fmt.Errorf("QuerySysSettings returned invalid JSON: %w", err)
	}
	if decoded.Success != nil && !*decoded.Success {
		return "", fmt.Errorf("QuerySysSettings rejected the request")
	}
	return sysSettingText(decoded.Values[code]), nil
}

// sysSettingText renders a QuerySysSettings value: a string as is, a lookup as its value id, and any other
// scalar as its JSON text.
func sysSettingText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var lookup struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &lookup) == nil && len(lookup.Value) > 0 {
		return sysSettingText(lookup.Value)
	}
	return string(raw)
}

func (c *Client) selectSysSettingValue(ctx context.Context, code string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSettingsValue", map[string]string{
		"TextValue": "TextValue", "GuidValue": "GuidValue", "ValueTypeName": "SysSettings.ValueTypeName",
	}, map[string]any{
		"Code":      comparisonFilter("SysSettings.Code", code, 1, 3),
		"AdminUnit": comparisonFilter("SysAdminUnit", allEmployeesAdminUnitID, 0, 3),
	}, 1))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	if rowString(rows[0], "ValueTypeName") == "Lookup" {
		return rowString(rows[0], "GuidValue"), nil
	}
	return rowString(rows[0], "TextValue"), nil
}
