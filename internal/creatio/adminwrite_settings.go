package creatio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type AdminWriteCreateSetting struct {
	Code                string
	Name                string
	ValueTypeName       string
	Value               *string
	Description         string
	IsCacheable         *bool
	IsPersonal          *bool
	ReferenceSchemaName string
}

type AdminWriteUpdateSetting struct {
	Code          string
	Value         *string
	ValueTypeName string
	ValueFilePath string
}

type AdminWriteCreateSettingResult struct {
	Success       bool    `json:"success"`
	Code          string  `json:"code"`
	ValueTypeName string  `json:"value-type-name"`
	Value         *string `json:"value"`
	SysSettingFailure
	Warning string `json:"warning,omitempty"`
}

type AdminWriteUpdateSettingResult struct {
	Success bool    `json:"success"`
	Code    string  `json:"code"`
	Value   *string `json:"value"`
	SysSettingFailure
}

var adminWriteSettingTypes = []string{"Text", "ShortText", "MediumText", "LongText", "SecureText", "MaxSizeText", "Boolean", "DateTime", "Date", "Time", "Integer", "Money", "Float", "Lookup", "Currency", "Decimal", "Binary"}
var adminWriteSettingCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func (c *Client) AdminWriteCreateSysSetting(ctx context.Context, in AdminWriteCreateSetting) AdminWriteCreateSettingResult {
	result := AdminWriteCreateSettingResult{Code: in.Code, ValueTypeName: in.ValueTypeName}
	fail := func(err error) AdminWriteCreateSettingResult {
		result.SysSettingFailure = adminWriteSettingFailure(err, "creating sys-setting")
		return result
	}
	if strings.TrimSpace(in.Code) == "" {
		return fail(fmt.Errorf("code is required."))
	}
	if strings.TrimSpace(in.Name) == "" {
		return fail(fmt.Errorf("name is required."))
	}
	if strings.TrimSpace(in.ValueTypeName) == "" {
		return fail(fmt.Errorf("value-type-name is required."))
	}
	valid := false
	for _, name := range adminWriteSettingTypes {
		if name == in.ValueTypeName {
			valid = true
			break
		}
	}
	if !valid {
		return fail(fmt.Errorf("Unsupported value-type-name '%s'. Allowed values: %s.", in.ValueTypeName, strings.Join(adminWriteSettingTypes, ", ")))
	}
	if in.ValueTypeName == "Lookup" && strings.TrimSpace(in.ReferenceSchemaName) == "" {
		return fail(fmt.Errorf("reference-schema-name is required when value-type-name is 'Lookup'."))
	}
	cacheable := true
	if in.IsCacheable != nil {
		cacheable = *in.IsCacheable
	}
	personal := false
	if in.IsPersonal != nil {
		personal = *in.IsPersonal
	}
	body := map[string]any{"valueTypeName": in.ValueTypeName, "code": in.Code, "description": in.Description,
		"isCacheable": cacheable, "isPersonal": personal, "name": in.Name}
	if in.ReferenceSchemaName != "" {
		rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"UId": "UId"}, map[string]any{"Name": comparisonFilter("Name", in.ReferenceSchemaName, 1, 3)}, 1))
		if err != nil {
			return fail(err)
		}
		if len(rows) == 0 {
			return fail(fmt.Errorf("Entity schema '%s' was not found on the target environment.", in.ReferenceSchemaName))
		}
		body["referenceSchemaUId"] = rowString(rows[0], "UId")
	}
	data, _ := json.Marshal(body)
	response, err := c.postDataServiceJSON(ctx, "InsertSysSettingRequest", data, 60*time.Second, maxResponseBytes)
	if err != nil {
		return fail(err)
	}
	var inserted struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(response, &inserted); err != nil {
		return fail(err)
	}
	if !inserted.Success {
		return fail(fmt.Errorf("provider refused creating sys-setting"))
	}
	result.Success = true
	if in.Value == nil {
		return result
	}
	updated := c.AdminWriteUpdateSysSetting(ctx, AdminWriteUpdateSetting{Code: in.Code, Value: in.Value, ValueTypeName: in.ValueTypeName})
	if !updated.Success {
		result.Warning = "Sys-setting was created, but the initial value could not be applied."
		return result
	}
	result.Value = updated.Value
	return result
}

func (c *Client) AdminWriteUpdateSysSetting(ctx context.Context, in AdminWriteUpdateSetting) AdminWriteUpdateSettingResult {
	result := AdminWriteUpdateSettingResult{Code: in.Code}
	fail := func(err error) AdminWriteUpdateSettingResult {
		result.SysSettingFailure = adminWriteSettingFailure(err, "updating sys-setting")
		return result
	}
	if strings.TrimSpace(in.Code) == "" {
		return fail(fmt.Errorf("code is required."))
	}
	if in.Value != nil && in.ValueFilePath != "" {
		return fail(fmt.Errorf("Provide either 'value' or 'value-file-path', not both."))
	}
	if in.Value == nil && in.ValueFilePath == "" {
		return fail(fmt.Errorf("value is required (supply 'value' or 'value-file-path')."))
	}
	settings, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{"ValueTypeName": "ValueTypeName"}, map[string]any{"Code": comparisonFilter("Code", in.Code, 1, 3)}, 1))
	if err != nil {
		return fail(err)
	}
	valueType := in.ValueTypeName
	if len(settings) > 0 {
		valueType = rowString(settings[0], "ValueTypeName")
	}
	if valueType == "" {
		valueType = "Text"
	}
	value := ""
	if in.ValueFilePath != "" {
		if valueType != "Binary" {
			return fail(fmt.Errorf("Cannot upload a file to sys-setting '%s': it is type '%s', not Binary. A file value can only be written to a Binary setting.", in.Code, valueType))
		}
		bytes, err := os.ReadFile(in.ValueFilePath)
		if err != nil {
			return fail(err)
		}
		if len(bytes) > 10<<20 {
			return fail(fmt.Errorf("Binary value exceeds the 10,485,760-byte limit."))
		}
		value = base64.StdEncoding.EncodeToString(bytes)
	} else {
		value = *in.Value
	}
	if !adminWriteSettingCode.MatchString(in.Code) {
		return adminWriteUpdateRefusal(in.Code)
	}
	var payload any = value
	switch valueType {
	case "Boolean":
		parsed, err := strconv.ParseBool(strings.ToLower(value))
		if err != nil {
			return adminWriteUpdateRefusal(in.Code)
		}
		payload = parsed
	case "Integer":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return adminWriteUpdateRefusal(in.Code)
		}
		payload = parsed
	case "Money", "Currency", "Float", "Decimal":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return adminWriteUpdateRefusal(in.Code)
		}
		payload = parsed
	case "Binary":
		if len(value) > (10<<20)*4/3+4 {
			return adminWriteUpdateRefusal(in.Code)
		}
		if _, err := base64.StdEncoding.DecodeString(value); err != nil {
			return adminWriteUpdateRefusal(in.Code)
		}
	}
	body, _ := json.Marshal(map[string]any{"isPersonal": false, "sysSettingsValues": map[string]any{in.Code: payload}})
	response, err := c.postDataServiceJSON(ctx, "PostSysSettingsValues", body, 60*time.Second, maxResponseBytes)
	if err != nil {
		return fail(err)
	}
	var saved struct {
		SaveResult map[string]bool `json:"saveResult"`
	}
	if err := json.Unmarshal(response, &saved); err != nil {
		return fail(err)
	}
	if !saved.SaveResult[in.Code] {
		return adminWriteUpdateRefusal(in.Code)
	}
	read := c.GetSysSetting(ctx, in.Code)
	if !read.Success {
		return fail(fmt.Errorf("readback failed"))
	}
	result.Success = true
	result.Value = &read.Value
	return result
}

func adminWriteUpdateRefusal(code string) AdminWriteUpdateSettingResult {
	return AdminWriteUpdateSettingResult{Code: code, SysSettingFailure: SysSettingFailure{
		Error:         "Failed to update sys-setting. The setting may not exist, or the value did not match the expected type.",
		ErrorCategory: "ProviderFailure", Cause: "The setting may not exist, or the value did not match the expected type.",
		RecoveryAction: sysSettingToolProviderRecovery}}
}

func adminWriteSettingFailure(err error, label string) SysSettingFailure {
	if strings.HasSuffix(err.Error(), "required.") || strings.HasPrefix(err.Error(), "value is required") || strings.HasPrefix(err.Error(), "Unsupported value-type-name") || strings.HasPrefix(err.Error(), "Provide either") || strings.HasPrefix(err.Error(), "Entity schema") || strings.HasPrefix(err.Error(), "Cannot upload") {
		return SysSettingValidationFailure(err.Error())
	}
	return sysSettingToolClassify(err, label)
}
