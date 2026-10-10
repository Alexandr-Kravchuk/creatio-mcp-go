package creatio

// create-sys-setting and update-sys-setting: clio master's SysSettingsCommand.TryCreateSysSetting /
// TryUpdateSysSetting over SysSettingsManager (InsertSysSetting, UpdateSysSetting,
// GetAllUsersDefaultWithType, GetFileSecurityPolicy, TryValidateBinaryValue, FindSchemaUIdByName).
// Both answer clio's SysSettingCreateResult / SysSettingUpdateResult with the classified failure fields
// get-sys-setting already uses.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	adminWriteCreateLabel = "creating sys-setting"
	adminWriteUpdateLabel = "updating sys-setting"
	// adminWriteMaxBinaryBytes is SysSettingsManager.MaxBinaryValueBytes.
	adminWriteMaxBinaryBytes = 10 * 1024 * 1024
	adminWriteWriteTimeout   = 100 * time.Second

	adminWriteRefusedUpdateError    = "Failed to update sys-setting. The setting may not exist, or the value did not match the expected type."
	adminWriteRefusedUpdateCause    = "Creatio did not apply the sys-setting write; the setting may not exist, or the value did not match its expected type."
	adminWriteRefusedUpdateRecovery = "Confirm the setting exists (list-sys-settings) and that the value matches its type, then retry."
	adminWritePartialCreateWarning  = "Sys-setting was created, but the initial value could not be applied."
	adminWriteNoServerTextCause     = "the environment reported an unsuccessful response without an error message."
	adminWriteShapeCause            = "Creatio answered with valid JSON that does not match the DataService response the operation expects - the endpoint, the Creatio version, or the service behind the URL may not be the expected one."
	adminWriteShapeRecovery         = "Confirm the environment URL addresses Creatio itself and that its version supports this operation, then retry."

	// The FileSecurityMode lookup ids Terrasoft.Web.FileSecurity.FileSecurityModeProvider fixes.
	adminWriteFileSecurityDisabled  = "9801c625-fafb-4ed3-9383-c3c942a5c1e3"
	adminWriteFileSecurityDenyList  = "60849c6e-24b4-45df-9aad-2f69d419823c"
	adminWriteFileSecurityAllowList = "c6ca9a2f-3a4a-4d51-b67b-de36852cb916"
)

// adminWriteSettingTypes is SysSettingsCommand.SupportedValueTypeNames, in clio's order: the refusal lists them.
var adminWriteSettingTypes = []string{"Text", "ShortText", "MediumText", "LongText", "SecureText", "MaxSizeText",
	"Boolean", "DateTime", "Date", "Time", "Integer", "Money", "Float", "Lookup", "Currency", "Decimal", "Binary"}

// adminWriteSettingCode is SysSettingsManager.SysSettingCodeRegex.
var adminWriteSettingCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// AdminWriteCreateSetting is clio's CreateSysSettingArgs.
type AdminWriteCreateSetting struct {
	Code, Name, ValueTypeName, Description, ReferenceSchemaName *string
	Value                                                       *string
	IsCacheable, IsPersonal                                     *bool
}

// AdminWriteUpdateSetting is clio's UpdateSysSettingArgs.
type AdminWriteUpdateSetting struct {
	Code, Value, ValueTypeName, ValueFilePath *string
}

// AdminWriteCreateSettingResult is clio's SysSettingCreateResult.
type AdminWriteCreateSettingResult struct {
	Success       bool    `json:"success"`
	Code          string  `json:"code"`
	ValueTypeName string  `json:"value-type-name"`
	Value         *string `json:"value"`
	Error         string  `json:"error,omitempty"`
	Warning       string  `json:"warning,omitempty"`
	AdminWriteFailureFields
}

// AdminWriteUpdateSettingResult is clio's SysSettingUpdateResult.
type AdminWriteUpdateSettingResult struct {
	Success bool    `json:"success"`
	Code    string  `json:"code"`
	Value   *string `json:"value"`
	Error   string  `json:"error,omitempty"`
	AdminWriteFailureFields
}

// AdminWriteFailureFields are the classified parts of a SysSettingFailure after error: clio's records put error
// (and warning) between value and error-category.
type AdminWriteFailureFields struct {
	ErrorCategory  string `json:"error-category,omitempty"`
	Cause          string `json:"cause,omitempty"`
	RecoveryAction string `json:"recovery-action,omitempty"`
}

func adminWriteSplit(failure SysSettingFailure) (string, AdminWriteFailureFields) {
	return failure.Error, AdminWriteFailureFields{ErrorCategory: failure.ErrorCategory, Cause: failure.Cause, RecoveryAction: failure.RecoveryAction}
}

// AdminWriteCreateFailure is a create envelope for a failure raised before the command ran.
func AdminWriteCreateFailure(code, valueTypeName string, failure SysSettingFailure) AdminWriteCreateSettingResult {
	message, fields := adminWriteSplit(failure)
	return AdminWriteCreateSettingResult{Code: code, ValueTypeName: valueTypeName, Error: message, AdminWriteFailureFields: fields}
}

// AdminWriteUpdateFailure is an update envelope for a failure raised before the command ran.
func AdminWriteUpdateFailure(code string, failure SysSettingFailure) AdminWriteUpdateSettingResult {
	message, fields := adminWriteSplit(failure)
	return AdminWriteUpdateSettingResult{Code: code, Error: message, AdminWriteFailureFields: fields}
}

// adminWriteValidation is clio's ArgumentException on this path: its message is both error and cause.
type adminWriteValidation string

func (e adminWriteValidation) Error() string { return string(e) }

// adminWriteResponseError is NonJsonWriteResponseException: the write answered with something that is not
// the DataService response it expects (shape: valid JSON of the wrong shape).
type adminWriteResponseError struct{ shape bool }

func (e adminWriteResponseError) Error() string { return "unexpected DataService write response" }

// adminWriteStateError is a local InvalidOperationException whose message clio shows (Unknown category).
type adminWriteStateError string

func (e adminWriteStateError) Error() string { return string(e) }

// adminWriteClassify is SysSettingFailureClassifier.Categorize for the write path.
func adminWriteClassify(err error, label string) SysSettingFailure {
	var validation adminWriteValidation
	var response adminWriteResponseError
	var state adminWriteStateError
	switch {
	case errors.As(err, &validation):
		detail := sysSettingToolClamp(redact.Text(validation.Error()), sysSettingToolMaxCause)
		return SysSettingFailure{detail, "Validation", detail, sysSettingToolValidationRecovery}
	case errors.As(err, &response) && response.shape:
		return SysSettingFailure{"Creatio returned a response of an unexpected shape " + label + ".", "Network", adminWriteShapeCause, adminWriteShapeRecovery}
	case errors.As(err, &response):
		return SysSettingFailure{"Creatio returned a non-JSON response " + label + ".", "Network", sysSettingToolNonJSONCause, sysSettingToolNonJSONRecovery}
	case errors.As(err, &state):
		detail := sysSettingToolClamp(redact.Text(state.Error()), sysSettingToolMaxCause)
		return SysSettingFailure{detail, "Unknown", detail, sysSettingToolUnknownRecovery}
	}
	return sysSettingToolClassify(err, label)
}

func adminWriteBlank(value *string) bool { return value == nil || dataWriteBlank(*value) }

func adminWriteText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// CreateSysSetting is SysSettingsCommand.TryCreateSysSetting.
func (c *Client) CreateSysSetting(ctx context.Context, in AdminWriteCreateSetting) AdminWriteCreateSettingResult {
	code, valueType := adminWriteText(in.Code), adminWriteText(in.ValueTypeName)
	result, err := c.adminWriteCreate(ctx, in)
	if err != nil {
		return AdminWriteCreateFailure(code, valueType, adminWriteClassify(err, adminWriteCreateLabel))
	}
	return result
}

func (c *Client) adminWriteCreate(ctx context.Context, in AdminWriteCreateSetting) (AdminWriteCreateSettingResult, error) {
	code, valueType := adminWriteText(in.Code), adminWriteText(in.ValueTypeName)
	switch {
	case adminWriteBlank(in.Code):
		return AdminWriteCreateSettingResult{}, adminWriteValidation("code is required.")
	case adminWriteBlank(in.Name):
		return AdminWriteCreateSettingResult{}, adminWriteValidation("name is required.")
	case adminWriteBlank(in.ValueTypeName):
		return AdminWriteCreateSettingResult{}, adminWriteValidation("value-type-name is required.")
	case !adminWriteSupportedType(valueType):
		return AdminWriteCreateSettingResult{}, adminWriteValidation(fmt.Sprintf("Unsupported value-type-name '%s'. Allowed values: %s.",
			valueType, strings.Join(adminWriteSettingTypes, ", ")))
	case valueType == "Lookup" && adminWriteBlank(in.ReferenceSchemaName):
		return AdminWriteCreateSettingResult{}, adminWriteValidation("reference-schema-name is required when value-type-name is 'Lookup'.")
	}
	if in.Value != nil && valueType == "Binary" {
		// A Binary initial value is inline Base64, so it meets the same file-security gate as an inline update.
		if err := c.adminWriteRejectInlineBinary(ctx, code); err != nil {
			return AdminWriteCreateSettingResult{}, err
		}
	}
	body := orderedFields{{"valueTypeName", adminWriteInsertTypeName(valueType)}, {"code", code}}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	cacheable, personal := true, false
	if in.IsCacheable != nil {
		cacheable = *in.IsCacheable
	}
	if in.IsPersonal != nil {
		personal = *in.IsPersonal
	}
	body = append(body, field{"description", description}, field{"isCacheable", cacheable}, field{"isPersonal", personal},
		field{"name", adminWriteText(in.Name)})
	if valueType == "Lookup" {
		uid, err := c.adminWriteSchemaUID(ctx, adminWriteText(in.ReferenceSchemaName))
		if err != nil {
			return AdminWriteCreateSettingResult{}, err
		}
		if uid == "" {
			return AdminWriteCreateSettingResult{}, adminWriteValidation(fmt.Sprintf("Entity schema '%s' was not found on the target environment.",
				adminWriteText(in.ReferenceSchemaName)))
		}
		if uid != emptyGUID {
			body = append(body, field{"referenceSchemaUId", uid})
		}
	}
	raw, err := c.adminWriteSettingPost(ctx, "InsertSysSettingRequest", toJNode(body).stjJSON())
	if err != nil {
		return AdminWriteCreateSettingResult{}, err
	}
	inserted, err := adminWriteParseInsert(raw)
	if err != nil {
		return AdminWriteCreateSettingResult{}, err
	}
	if !inserted.success {
		// The platform's own prose is the diagnosis; it is fenced as untrusted data, never shown raw.
		cause := adminWriteNoServerTextCause
		if fenced := DataWriteUntrusted(inserted.message); fenced != nil {
			cause = *fenced
		}
		return AdminWriteCreateSettingResult{Code: code, ValueTypeName: valueType, Error: "Failed " + adminWriteCreateLabel + ": " + cause,
			AdminWriteFailureFields: AdminWriteFailureFields{ErrorCategory: "ProviderFailure", Cause: cause, RecoveryAction: sysSettingToolProviderRecovery}}, nil
	}
	if in.Value == nil {
		return AdminWriteCreateSettingResult{Success: true, Code: code, ValueTypeName: valueType}, nil
	}
	updated, err := c.adminWriteUpdateValue(ctx, code, *in.Value, valueType)
	var response adminWriteResponseError
	if errors.As(err, &response) || (err == nil && !updated) {
		// The insert landed, so a value the environment refused (or answered with a non-DataService page) is
		// a partial success; a rejected session is not, and fails the call.
		return AdminWriteCreateSettingResult{Success: true, Code: code, ValueTypeName: valueType, Warning: adminWritePartialCreateWarning}, nil
	}
	if err != nil {
		return AdminWriteCreateSettingResult{}, err
	}
	assigned, _, err := c.adminWriteAllUsersDefault(ctx, code)
	if err != nil {
		return AdminWriteCreateSettingResult{}, err
	}
	masked := sysSettingToolMask(valueType, assigned)
	return AdminWriteCreateSettingResult{Success: true, Code: code, ValueTypeName: valueType, Value: &masked}, nil
}

func adminWriteSupportedType(name string) bool {
	for _, candidate := range adminWriteSettingTypes {
		if candidate == name {
			return true
		}
	}
	return false
}

// adminWriteInsertTypeName is the CreatioSysSetting subclass InsertSysSetting picks: the Currency and Decimal
// aliases are inserted as Money and Float.
func adminWriteInsertTypeName(name string) string {
	switch name {
	case "Currency":
		return "Money"
	case "Decimal":
		return "Float"
	}
	return name
}

// UpdateSysSetting is SysSettingsCommand.TryUpdateSysSetting.
func (c *Client) UpdateSysSetting(ctx context.Context, in AdminWriteUpdateSetting) AdminWriteUpdateSettingResult {
	code := adminWriteText(in.Code)
	result, err := c.adminWriteUpdate(ctx, in)
	if err != nil {
		return AdminWriteUpdateFailure(code, adminWriteClassify(err, adminWriteUpdateLabel))
	}
	return result
}

func (c *Client) adminWriteUpdate(ctx context.Context, in AdminWriteUpdateSetting) (AdminWriteUpdateSettingResult, error) {
	code := adminWriteText(in.Code)
	if adminWriteBlank(in.Code) {
		return AdminWriteUpdateSettingResult{}, adminWriteValidation("code is required.")
	}
	hasFile := !adminWriteBlank(in.ValueFilePath)
	if in.Value != nil && hasFile {
		return AdminWriteUpdateSettingResult{}, adminWriteValidation("Provide either 'value' or 'value-file-path', not both.")
	}
	if in.Value == nil && !hasFile {
		return AdminWriteUpdateSettingResult{}, adminWriteValidation("value is required (supply 'value' or 'value-file-path').")
	}
	_, existingType, err := c.adminWriteAllUsersDefault(ctx, code)
	if err != nil {
		return AdminWriteUpdateSettingResult{}, err
	}
	targetIsBinary := existingType != nil && *existingType == "Binary"
	var value string
	if hasFile {
		path := *in.ValueFilePath
		if existingType == nil {
			return AdminWriteUpdateSettingResult{}, adminWriteValidation(fmt.Sprintf("Sys-setting '%s' was not found or is not readable by the current user. "+
				"Uploading a file requires an existing, readable Binary setting.", code))
		}
		if !targetIsBinary {
			return AdminWriteUpdateSettingResult{}, adminWriteValidation(fmt.Sprintf("Cannot upload a file to sys-setting '%s': it is type '%s', not Binary. "+
				"A file value can only be written to a Binary setting.", code, *existingType))
		}
		if err := c.adminWriteEnforceFilePolicy(ctx, path); err != nil {
			return AdminWriteUpdateSettingResult{}, err
		}
		if value, err = adminWriteEncodeFile(path); err != nil {
			return AdminWriteUpdateSettingResult{}, err
		}
	} else {
		if targetIsBinary {
			if err := c.adminWriteRejectInlineBinary(ctx, code); err != nil {
				return AdminWriteUpdateSettingResult{}, err
			}
		}
		value = *in.Value
		if targetIsBinary {
			if message := adminWriteValidateBinary(value); message != "" {
				return AdminWriteUpdateSettingResult{}, adminWriteValidation(message)
			}
		}
	}
	fallback := "Text"
	if hasFile {
		fallback = "Binary"
	}
	valueType := fallback
	if !adminWriteBlank(in.ValueTypeName) {
		valueType = *in.ValueTypeName
	}
	updated, err := c.adminWriteUpdateValue(ctx, code, value, valueType)
	if err != nil {
		return AdminWriteUpdateSettingResult{}, err
	}
	if !updated {
		return AdminWriteUpdateSettingResult{Code: code, Error: adminWriteRefusedUpdateError, AdminWriteFailureFields: AdminWriteFailureFields{
			ErrorCategory: "ProviderFailure", Cause: adminWriteRefusedUpdateCause, RecoveryAction: adminWriteRefusedUpdateRecovery}}, nil
	}
	readback, readbackType, err := c.adminWriteAllUsersDefault(ctx, code)
	if err != nil {
		return AdminWriteUpdateSettingResult{}, err
	}
	masked := readback
	if readbackType != nil {
		masked = sysSettingToolMask(*readbackType, readback)
	}
	return AdminWriteUpdateSettingResult{Success: true, Code: code, Value: &masked}, nil
}

// adminWriteAllUsersDefault is SysSettingsManager.GetAllUsersDefaultWithType: the All employees value as
// FormatTypedValue prints it and the setting's type, or ("", nil) for an unknown code.
func (c *Client) adminWriteAllUsersDefault(ctx context.Context, code string) (string, *string, error) {
	settings, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{
		"Id": "Id", "ValueTypeName": "ValueTypeName",
	}, map[string]any{"Code": comparisonFilter("Code", code, 1, 3)}, 1))
	if err != nil {
		return "", nil, err
	}
	if len(settings) == 0 {
		return "", nil, nil
	}
	valueType := rowString(settings[0], "ValueTypeName")
	values, err := c.selectRows(ctx, buildSelectQuery("SysSettingsValue", sysSettingToolValueColumns(), map[string]any{
		"SysSettings":  comparisonFilter("SysSettings", rowString(settings[0], "Id"), 0, 3),
		"SysAdminUnit": comparisonFilter("SysAdminUnit", sysSettingToolAllEmployeesID, 0, 3),
	}, 1))
	if err != nil {
		return "", nil, err
	}
	if len(values) == 0 {
		return "", &valueType, nil
	}
	return sysSettingToolGetValue(valueType, values[0]), &valueType, nil
}

// adminWriteSettingDefinition is GetSysSettingByCode: the type and lookup reference of an existing setting.
func (c *Client) adminWriteSettingDefinition(ctx context.Context, code string) (found bool, valueType, referenceUID string, err error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{
		"ValueTypeName": "ValueTypeName", "ReferenceSchemaUId": "ReferenceSchemaUId",
	}, map[string]any{"Code": comparisonFilter("Code", code, 1, 3)}, 1))
	if err != nil || len(rows) == 0 {
		return false, "", "", err
	}
	return true, rowString(rows[0], "ValueTypeName"), strings.ToLower(rowString(rows[0], "ReferenceSchemaUId")), nil
}

// adminWriteSchemaUID is FindSchemaUIdByName: the UId of the first SysSchema row with the name, "" when none.
func (c *Client) adminWriteSchemaUID(ctx context.Context, name string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"UId": "UId"},
		map[string]any{"Name": comparisonFilter("Name", name, 1, 3)}, 1))
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return strings.ToLower(rowString(rows[0], "UId")), nil
}

// adminWriteUpdateValue is SysSettingsManager.UpdateSysSetting: false for a value clio refuses locally or a
// save the platform reports as not applied; an error for a session, transport or response failure.
func (c *Client) adminWriteUpdateValue(ctx context.Context, code, value, valueTypeName string) (bool, error) {
	if dataWriteBlank(code) || !adminWriteSettingCode.MatchString(code) {
		return false, nil
	}
	found, existingType, referenceUID, err := c.adminWriteSettingDefinition(ctx, code)
	if err != nil {
		return false, err
	}
	optionsType := valueTypeName
	if found {
		optionsType = existingType
	}
	var payload *jnode
	switch {
	case strings.Contains(optionsType, "Text") || strings.Contains(optionsType, "Date") || strings.Contains(optionsType, "Time") ||
		strings.Contains(optionsType, "Lookup"):
		text := value
		if optionsType == "Lookup" {
			if guid, ok := adminWriteParseGUID(text); ok {
				text = guid
			} else {
				if !found {
					return false, nil
				}
				entity, err := c.adminWriteSchemaName(ctx, referenceUID)
				if err != nil {
					return false, err
				}
				if entity == "" {
					return false, nil
				}
				if text, err = c.adminWriteLookupByDisplayValue(ctx, entity, text); err != nil {
					return false, err
				}
			}
		}
		if optionsType == "Date" || optionsType == "DateTime" || optionsType == "Time" {
			parsed, ok := dotnetParseDateTime(text)
			if !ok {
				return false, nil
			}
			text = parsed.Format("2006-01-02T15:04:05.000")
		}
		payload = newString(text)
	case strings.Contains(optionsType, "Boolean"):
		switch strings.ToLower(dataWriteTrim(value)) {
		case "true":
			payload = newBool(true)
		case "false":
			payload = newBool(false)
		default:
			return false, nil
		}
	case strings.Contains(optionsType, "Currency") || strings.Contains(optionsType, "Decimal") ||
		strings.Contains(optionsType, "Money") || strings.Contains(optionsType, "Float"):
		number, ok := dotnetParseDecimal(value)
		if !ok {
			return false, nil
		}
		payload = &jnode{kind: jkFloat, text: number}
	case strings.Contains(optionsType, "Integer"):
		number, ok := dotnetParseInt32(value)
		if !ok {
			return false, nil
		}
		payload = newInt(number)
	case strings.Contains(optionsType, "Binary"):
		if adminWriteValidateBinary(value) != "" {
			return false, nil
		}
		payload = newString(value)
	default:
		return false, nil
	}
	values := newObject()
	values.set(code, payload)
	body := toJNode(orderedFields{{"isPersonal", false}, {"sysSettingsValues", values}})
	raw, err := c.adminWriteSettingPost(ctx, "PostSysSettingsValues", body.stjJSON())
	if err != nil {
		return false, err
	}
	return adminWriteParseSave(raw, code)
}

// adminWriteSchemaName is GetSysSchemaNameByUid.
func (c *Client) adminWriteSchemaName(ctx context.Context, uid string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"Name": "Name"},
		map[string]any{"UId": comparisonFilter("UId", uid, 0, 3)}, 1))
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rowString(rows[0], "Name"), nil
}

// adminWriteLookupByDisplayValue is GetEntityIdByDisplayValue: clio's selectIdByDisplayValue.json template,
// matching the entity's primary display column. No row is the empty GUID; several are refused.
func (c *Client) adminWriteLookupByDisplayValue(ctx context.Context, entity, display string) (string, error) {
	query := toJNode(orderedFields{
		{"rootSchemaName", entity},
		{"filters", orderedFields{{"isEnabled", true}, {"trimDateTimeParameterToDate", false}, {"filterType", 6}, {"logicalOperation", 0},
			{"items", orderedFields{{"8caf69f4-9583-4e77-86c0-716c07ce4ec7", orderedFields{
				{"filterType", 1}, {"comparisonType", 3}, {"isEnabled", true}, {"trimDateTimeParameterToDate", false},
				{"leftExpression", orderedFields{{"expressionType", 1}, {"functionType", 1}, {"macrosType", 35}}},
				{"isAggregative", false}, {"dataValueType", 1},
				{"rightExpression", orderedFields{{"expressionType", 2},
					{"parameter", orderedFields{{"dataValueType", 1}, {"value", display}, {"className", "Terrasoft.Parameter"}}},
					{"className", "Terrasoft.ParameterExpression"}}},
				{"className", "Terrasoft.CompareFilter"},
			}}}},
		}},
		{"useLocalization", true},
		{"columns", orderedFields{{"items", orderedFields{{"Id", orderedFields{{"expression", orderedFields{{"expressionType", 0}, {"columnPath", "Id"}}}}}}}}},
	})
	raw, err := c.adminWriteSettingPost(ctx, "SelectQuery", query.newtonsoftJSON())
	if err != nil {
		return "", err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return "", adminWriteResponseError{}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return "", adminWriteResponseError{shape: true}
	}
	rows, _ := object["rows"].([]any)
	if len(rows) == 0 {
		return emptyGUID, nil
	}
	if len(rows) > 1 {
		return "", adminWriteStateError(fmt.Sprintf("Ambiguous lookup display value '%s' for entity '%s': %d rows match. "+
			"Pass the record GUID instead of the display name to disambiguate.", display, entity, len(rows)))
	}
	row, _ := rows[0].(map[string]any)
	id, _ := row["Id"].(string)
	if guid, ok := adminWriteParseGUID(id); ok {
		return guid, nil
	}
	return emptyGUID, nil
}

// adminWriteSettingPost sends a DataService write and returns its body whatever it is: the caller decides whether
// it is the response the operation expects.
func (c *Client) adminWriteSettingPost(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	response, err := c.serviceRequest(ctx, serviceCall{Route: "DataService/json/SyncReply/" + endpoint, Body: body,
		Timeout: adminWriteWriteTimeout, Limit: maxResponseBytes})
	if err != nil {
		return nil, err
	}
	if looksLikeLoginHTML(response.payload, "/DataService/json/SyncReply/"+endpoint) {
		return nil, authenticationError{err: fmt.Errorf("Creatio rejected the session")}
	}
	return response.payload, nil
}

type adminWriteInsertResponse struct {
	success bool
	message string
}

// adminWriteParseInsert is DeserializeWriteResponse<InsertSysSettingResponse>.
func adminWriteParseInsert(raw []byte) (adminWriteInsertResponse, error) {
	object, err := adminWriteResponseObject(raw)
	if err != nil {
		return adminWriteInsertResponse{}, err
	}
	var decoded struct {
		ResponseStatus *struct {
			Message *string `json:"Message"`
		} `json:"responseStatus"`
		ID           *string `json:"id"`
		RowsAffected *int    `json:"rowsAffected"`
		Success      *bool   `json:"success"`
	}
	if err := json.Unmarshal(object, &decoded); err != nil {
		return adminWriteInsertResponse{}, adminWriteResponseError{shape: true}
	}
	if decoded.ID != nil {
		if _, ok := adminWriteParseGUID(*decoded.ID); !ok {
			return adminWriteInsertResponse{}, adminWriteResponseError{shape: true}
		}
	}
	result := adminWriteInsertResponse{success: decoded.Success != nil && *decoded.Success}
	if decoded.ResponseStatus != nil && decoded.ResponseStatus.Message != nil {
		result.message = *decoded.ResponseStatus.Message
	}
	return result, nil
}

// adminWriteParseSave is DeserializeWriteResponse<UpdateSysSettingResponse> and the per-code saveResult check.
func adminWriteParseSave(raw []byte, code string) (bool, error) {
	object, err := adminWriteResponseObject(raw)
	if err != nil {
		return false, err
	}
	var decoded struct {
		SaveResult   map[string]bool `json:"saveResult"`
		RowsAffected *int            `json:"rowsAffected"`
		Success      *bool           `json:"success"`
	}
	if err := json.Unmarshal(object, &decoded); err != nil {
		return false, adminWriteResponseError{shape: true}
	}
	return decoded.SaveResult[code], nil
}

// adminWriteResponseObject accepts a JSON body other than null; anything else is not a DataService answer.
func adminWriteResponseObject(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) || string(trimmed) == "null" {
		return nil, adminWriteResponseError{}
	}
	if trimmed[0] != '{' {
		return nil, adminWriteResponseError{shape: true}
	}
	return trimmed, nil
}

// adminWriteValidateBinary is TryValidateBinaryValue: "" for a valid payload, else clio's reason.
func adminWriteValidateBinary(value string) string {
	tooLarge := fmt.Sprintf("Binary value exceeds the %s-byte limit.", adminWriteThousands(adminWriteMaxBinaryBytes))
	if dataWriteBlank(value) {
		return "Value is not valid Base64 (Binary settings expect a Base64-encoded payload)."
	}
	if int64(len(value))/4*3 > adminWriteMaxBinaryBytes {
		return tooLarge
	}
	decoded, err := dotnetBase64Decode(value)
	if err != nil {
		return "Value is not valid Base64 (Binary settings expect a Base64-encoded payload)."
	}
	if len(decoded) > adminWriteMaxBinaryBytes {
		return tooLarge
	}
	return ""
}

// dotnetBase64Decode is Convert.TryFromBase64String: white space between characters is ignored.
func dotnetBase64Decode(value string) ([]byte, error) {
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, value)
	return base64.StdEncoding.DecodeString(compact)
}

func adminWriteThousands(value int) string {
	text := strconv.Itoa(value)
	var out []byte
	for index := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, text[index])
	}
	return string(out)
}

// adminWriteRejectInlineBinary is RejectInlineBinaryUnderActivePolicy for a Binary target.
func (c *Client) adminWriteRejectInlineBinary(ctx context.Context, code string) error {
	policy, err := c.adminWriteFilePolicy(ctx)
	if err != nil {
		return err
	}
	if policy.mode == "unknown" {
		return adminWriteValidation("Cannot determine the environment file-security mode; Binary upload was refused.")
	}
	if policy.mode != "disabled" {
		return adminWriteValidation(fmt.Sprintf("Sys-setting '%s' is Binary and this environment has an active file-security policy. "+
			"Provide the value via value-file-path (a file path) so its extension can be validated, rather than an inline Base64 value.", code))
	}
	return nil
}

type adminWriteFileSecurity struct {
	mode         string // disabled, allow, deny, unknown
	extensions   map[string]bool
	allowUnknown bool
}

// adminWriteFilePolicy is GetFileSecurityPolicy: only the explicit Disabled id disables the policy.
func (c *Client) adminWriteFilePolicy(ctx context.Context) (adminWriteFileSecurity, error) {
	raw, _, err := c.adminWriteAllUsersDefault(ctx, "FileSecurityMode")
	if err != nil {
		return adminWriteFileSecurity{}, err
	}
	modeID, ok := adminWriteParseGUID(raw)
	policy := adminWriteFileSecurity{mode: "unknown"}
	switch {
	case !ok:
	case modeID == adminWriteFileSecurityDisabled:
		policy.mode = "disabled"
	case modeID == adminWriteFileSecurityAllowList:
		policy.mode = "allow"
	case modeID == adminWriteFileSecurityDenyList:
		policy.mode = "deny"
	}
	if policy.mode == "disabled" || policy.mode == "unknown" {
		return policy, nil
	}
	listCode := "FileExtensionsDenyList"
	if policy.mode == "allow" {
		listCode = "FileExtensionsAllowList"
	}
	list, _, err := c.adminWriteAllUsersDefault(ctx, listCode)
	if err != nil {
		return adminWriteFileSecurity{}, err
	}
	policy.extensions = map[string]bool{}
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimLeft(dataWriteTrim(entry), ".")
		if dataWriteTrim(entry) != "" {
			policy.extensions[strings.ToLower(entry)] = true
		}
	}
	allowRaw, _, err := c.adminWriteAllUsersDefault(ctx, "AllowFilesWithUnknownType")
	if err != nil {
		return adminWriteFileSecurity{}, err
	}
	policy.allowUnknown = dataWriteBlank(allowRaw) || strings.EqualFold(dataWriteTrim(allowRaw), "true")
	return policy, nil
}

// adminWriteEnforceFilePolicy is EnforceFileSecurityPolicy.
func (c *Client) adminWriteEnforceFilePolicy(ctx context.Context, path string) error {
	policy, err := c.adminWriteFilePolicy(ctx)
	if err != nil {
		return err
	}
	if policy.mode == "disabled" {
		return nil
	}
	if policy.mode == "unknown" {
		return adminWriteValidation("Cannot determine the environment file-security mode; Binary upload was refused.")
	}
	name := filepath.Base(path)
	extension := strings.TrimPrefix(filepath.Ext(path), ".")
	if extension == "" {
		if !policy.allowUnknown {
			return adminWriteValidation(fmt.Sprintf("Cannot upload '%s': files with no extension are not allowed in this environment "+
				"(AllowFilesWithUnknownType is off).", name))
		}
		return nil
	}
	listed := policy.extensions[strings.ToLower(extension)]
	allowed := !listed
	modeName := "DenyList"
	if policy.mode == "allow" {
		allowed, modeName = listed, "AllowList"
	}
	if !allowed {
		return adminWriteValidation(fmt.Sprintf("Cannot upload '%s': files with extension '.%s' are not allowed in this "+
			"environment (%s file-security policy).", name, extension, modeName))
	}
	return nil
}

// adminWriteEncodeFile is EncodeFileToBase64: the file's bytes, read with the 10 MB cap, as Base64.
func adminWriteEncodeFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", adminWriteValidation(fmt.Sprintf("File not found: '%s'.", path))
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, adminWriteMaxBinaryBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > adminWriteMaxBinaryBytes {
		return "", adminWriteValidation(fmt.Sprintf("File '%s' exceeds the %s-byte limit for a Binary sys-setting value.",
			path, adminWriteThousands(adminWriteMaxBinaryBytes)))
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// adminWriteParseGUID is Guid.TryParse for the forms Creatio and agents send ("D", "N", "B", "P"), lower-case.
func adminWriteParseGUID(text string) (string, bool) {
	if !isGUIDText(text) {
		return "", false
	}
	return pkgWriteNormalizeGUID(text), true
}

// dotnetParseInt32 is int.TryParse(NumberStyles.Integer, invariant): surrounding white space and a leading sign.
func dotnetParseInt32(text string) (int, bool) {
	trimmed := strings.TrimFunc(text, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "+") {
		trimmed = trimmed[1:]
		if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "+") {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(trimmed, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(number), true
}

var dotnetDecimalPattern = regexp.MustCompile(`^([+-]?)((?:\d{1,3}(?:,\d{3})*|\d*)(?:,\d+)*)(?:\.(\d*))?([+-]?)$`)

// dotnetParseDecimal is decimal.TryParse(NumberStyles.Number, invariant) printed as System.Text.Json writes the
// decimal: thousands separators dropped, leading zeros dropped, the fraction's digits (scale) kept.
func dotnetParseDecimal(text string) (string, bool) {
	trimmed := strings.TrimFunc(text, unicode.IsSpace)
	match := dotnetDecimalPattern.FindStringSubmatch(trimmed)
	if match == nil || (match[1] != "" && match[4] != "") {
		return "", false
	}
	integer := strings.ReplaceAll(match[2], ",", "")
	fraction := match[3]
	if integer == "" && fraction == "" {
		return "", false
	}
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	if len(strings.TrimLeft(integer, "0")) > 29 {
		return "", false
	}
	result := integer
	if fraction != "" {
		result += "." + fraction
	}
	negative := match[1] == "-" || match[4] == "-"
	if negative && strings.Trim(result, "0.") != "" {
		result = "-" + result
	}
	return result, true
}

// dotnetParseDateTime is DateTime.TryParse(invariant) for the forms an agent sends: ISO 8601 with or without
// time, fraction and zone, and the invariant "MM/dd/yyyy[ HH:mm[:ss]]" form. A zone converts to local time,
// as .NET does.
func dotnetParseDateTime(text string) (time.Time, bool) {
	trimmed := strings.TrimFunc(text, unicode.IsSpace)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999Z07:00"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.In(adminWriteLocalZone()), true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05.9999999",
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "01/02/2006 15:04:05", "01/02/2006 15:04", "01/02/2006", "1/2/2006 15:04:05",
		"1/2/2006 15:04", "1/2/2006", "15:04:05", "15:04"} {
		if parsed, err := time.ParseInLocation(layout, trimmed, adminWriteLocalZone()); err == nil {
			if !strings.Contains(layout, "2006") {
				now := time.Now()
				parsed = time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, adminWriteLocalZone())
			}
			return parsed, true
		}
	}
	return time.Time{}, false
}

// adminWriteLocalZone is the host time zone, which .NET applies to a parsed date with an offset.
func adminWriteLocalZone() *time.Location { return time.Now().Location() }
