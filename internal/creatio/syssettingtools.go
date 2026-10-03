package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// sysSettingToolAllEmployeesID is the SysAdminUnit row whose SysSettingsValue holds a setting's All-Users default.
// clio reads only this row; a personal or role override is never reported as the value.
const sysSettingToolAllEmployeesID = "a29a3ba5-4b0d-de11-9a51-005056c00008"

const (
	sysSettingToolReadLabel = "reading sys-setting"
	sysSettingToolListLabel = "listing sys-settings"
	sysSettingToolMaxCause  = 300
	sysSettingToolEmptyGUID = "00000000-0000-0000-0000-000000000000"
	// sysSettingToolRoundTripLayout is what .NET's "o" format prints for a DateTime of unspecified kind, which is
	// how clio's data layer materializes DataService dates: seven fraction digits and no zone suffix.
	sysSettingToolRoundTripLayout = "2006-01-02T15:04:05.0000000"
)

// The failure texts are clio's SysSettingFailureTexts, so an agent that branches on them sees the same words.
const (
	sysSettingToolAuthenticationCause    = "The environment rejected the credentials of the registered user."
	sysSettingToolAuthenticationRecovery = "Verify the environment credentials (for an expired password, repair the registered profile) and retry."
	sysSettingToolNetworkCause           = "The environment could not be reached."
	sysSettingToolNetworkRecovery        = "Check the environment URL, network connectivity and the VPN, then retry."
	sysSettingToolNonJSONCause           = "Creatio answered with something that is not JSON - a proxy, gateway or WAF page, or a URL that does not reach Creatio."
	sysSettingToolNonJSONRecovery        = "Check that the environment URL points at Creatio itself and that no gateway is intercepting the request, then retry."
	sysSettingToolProviderRecovery       = "Read the cause, correct the reported condition on the environment, and retry."
	sysSettingToolValidationRecovery     = "Correct the argument named in the cause and call the operation again."
	sysSettingToolUnknownCause           = "The operation failed and no cause could be determined from the failure."
	sysSettingToolUnknownRecovery        = "Retry the operation; if it fails again, inspect the Creatio Error.log."
)

// SysSettingFailure is clio's classified failure envelope. clio also returns a correlation-id that names
// its own log line; this server writes no such log, so it returns no ID that nothing could be matched to.
type SysSettingFailure struct {
	Error          string `json:"error,omitempty"`
	ErrorCategory  string `json:"error-category,omitempty"`
	Cause          string `json:"cause,omitempty"`
	RecoveryAction string `json:"recovery-action,omitempty"`
}

// SysSettingGetResult is the get-sys-setting envelope: the requested code and its All-Users default value.
type SysSettingGetResult struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Value   string `json:"value"`
	SysSettingFailure
}

// SysSettingItem is one list-sys-settings row.
type SysSettingItem struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	ValueTypeName string `json:"value-type-name,omitempty"`
	Value         string `json:"value"`
	IsCacheable   bool   `json:"is-cacheable"`
	IsPersonal    bool   `json:"is-personal"`
}

// SysSettingsListResult is the list-sys-settings envelope; settings is [] on failure, as in clio.
type SysSettingsListResult struct {
	Success  bool             `json:"success"`
	Settings []SysSettingItem `json:"settings"`
	SysSettingFailure
}

// GetSysSetting reads the All-Users default of one setting. An unknown code, a setting without values and
// a setting without an All-Users row all answer success with an empty value, as clio does. SecureText
// values are masked so this path does not leak what list-sys-settings hides.
func (c *Client) GetSysSetting(ctx context.Context, code string) SysSettingGetResult {
	if strings.TrimSpace(code) == "" {
		return SysSettingGetResult{Code: code, SysSettingFailure: SysSettingValidationFailure("code is required.")}
	}
	settings, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{
		"Id": "Id", "ValueTypeName": "ValueTypeName",
	}, map[string]any{"Code": comparisonFilter("Code", code, 1, 3)}, 1))
	if err != nil {
		return SysSettingGetResult{Code: code, SysSettingFailure: sysSettingToolClassify(err, sysSettingToolReadLabel)}
	}
	if len(settings) == 0 {
		return SysSettingGetResult{Success: true, Code: code}
	}
	valueType := rowString(settings[0], "ValueTypeName")
	values, err := c.selectRows(ctx, buildSelectQuery("SysSettingsValue", sysSettingToolValueColumns(), map[string]any{
		"SysSettings":  comparisonFilter("SysSettings", rowString(settings[0], "Id"), 0, 3),
		"SysAdminUnit": comparisonFilter("SysAdminUnit", sysSettingToolAllEmployeesID, 0, 3),
	}, 1))
	if err != nil {
		return SysSettingGetResult{Code: code, SysSettingFailure: sysSettingToolClassify(err, sysSettingToolReadLabel)}
	}
	value := ""
	if len(values) > 0 {
		value = sysSettingToolGetValue(valueType, values[0])
	}
	return SysSettingGetResult{Success: true, Code: code, Value: sysSettingToolMask(valueType, value)}
}

// ListSysSettings returns every setting with its All-Users default, in the order Creatio returns the
// SysSettings rows; clio applies no ordering either. Binary values are shown as <binary> and SecureText
// values are masked.
func (c *Client) ListSysSettings(ctx context.Context) SysSettingsListResult {
	settings, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{
		"Id": "Id", "Name": "Name", "Code": "Code", "ValueTypeName": "ValueTypeName",
		"IsCacheable": "IsCacheable", "IsPersonal": "IsPersonal",
	}, nil, -1))
	if err != nil {
		return SysSettingsListResult{Settings: []SysSettingItem{}, SysSettingFailure: sysSettingToolClassify(err, sysSettingToolListLabel)}
	}
	columns := sysSettingToolValueColumns()
	columns["SettingId"] = "SysSettings.Id"
	values, err := c.selectRows(ctx, buildSelectQuery("SysSettingsValue", columns, map[string]any{
		"SysAdminUnit": comparisonFilter("SysAdminUnit", sysSettingToolAllEmployeesID, 0, 3),
	}, -1))
	if err != nil {
		return SysSettingsListResult{Settings: []SysSettingItem{}, SysSettingFailure: sysSettingToolClassify(err, sysSettingToolListLabel)}
	}
	bySetting := make(map[string]map[string]json.RawMessage, len(values))
	for _, row := range values {
		id := strings.ToLower(rowString(row, "SettingId"))
		if _, seen := bySetting[id]; !seen {
			bySetting[id] = row
		}
	}
	items := make([]SysSettingItem, 0, len(settings))
	for _, row := range settings {
		valueType := rowString(row, "ValueTypeName")
		items = append(items, SysSettingItem{
			Code: rowString(row, "Code"), Name: rowString(row, "Name"), ValueTypeName: valueType,
			Value:       sysSettingToolListValue(valueType, bySetting[strings.ToLower(rowString(row, "Id"))]),
			IsCacheable: sysSettingToolBool(row, "IsCacheable"), IsPersonal: sysSettingToolBool(row, "IsPersonal"),
		})
	}
	return SysSettingsListResult{Success: true, Settings: items}
}

func sysSettingToolValueColumns() map[string]string {
	return map[string]string{
		"TextValue": "TextValue", "IntegerValue": "IntegerValue", "FloatValue": "FloatValue",
		"BooleanValue": "BooleanValue", "DateTimeValue": "DateTimeValue", "GuidValue": "GuidValue",
	}
}

// sysSettingToolGetValue mirrors clio's FormatTypedValue (the get path): an unlisted type falls back to the
// text column.
func sysSettingToolGetValue(valueType string, row map[string]json.RawMessage) string {
	if value, ok := sysSettingToolTypedColumn(valueType, row); ok {
		return value
	}
	return rowString(row, "TextValue")
}

// sysSettingToolListValue mirrors clio's SysSettings.DefValue (the list path), which differs from the get path:
// a missing All-Users row and an unlisted type both read "undefined", and Binary is never read back.
func sysSettingToolListValue(valueType string, row map[string]json.RawMessage) string {
	if valueType == "Binary" {
		return "<binary>"
	}
	if row == nil {
		return sysSettingToolMask(valueType, "undefined")
	}
	switch valueType {
	case "MediumText", "ShortText", "LongText", "Text", "MaxSizeText", "SecureText":
		return sysSettingToolMask(valueType, rowString(row, "TextValue"))
	}
	if value, ok := sysSettingToolTypedColumn(valueType, row); ok {
		return value
	}
	return "undefined"
}

func sysSettingToolTypedColumn(valueType string, row map[string]json.RawMessage) (string, bool) {
	switch valueType {
	case "Boolean":
		return strconv.FormatBool(sysSettingToolBool(row, "BooleanValue")), true
	case "Integer":
		return sysSettingToolNumber(row, "IntegerValue", "0"), true
	case "Float", "Money", "Decimal", "Currency":
		return sysSettingToolDecimal(sysSettingToolNumber(row, "FloatValue", "0")), true
	case "Date":
		return sysSettingToolDateTime(row, "DateTimeValue").Format("2006-01-02"), true
	case "Time":
		return sysSettingToolDateTime(row, "DateTimeValue").Format("15:04:05"), true
	case "DateTime":
		return sysSettingToolDateTime(row, "DateTimeValue").Format(sysSettingToolRoundTripLayout), true
	case "Lookup":
		if guid := strings.ToLower(rowString(row, "GuidValue")); guid != "" {
			return guid, true
		}
		return sysSettingToolEmptyGUID, true
	}
	return "", false
}

// sysSettingToolMask replaces a configured SecureText value with "***". An empty value and the "undefined"
// sentinel mean "not configured" and read as empty, as in clio.
func sysSettingToolMask(valueType, value string) string {
	if valueType != "SecureText" {
		return value
	}
	if value == "" || value == "undefined" {
		return ""
	}
	return "***"
}

func sysSettingToolBool(row map[string]json.RawMessage, key string) bool {
	var value bool
	_ = json.Unmarshal(row[key], &value)
	return value
}

// sysSettingToolNumber returns a numeric column's JSON text, or fallback when the column is null or absent; .NET
// reads a null number as its zero value.
func sysSettingToolNumber(row map[string]json.RawMessage, key, fallback string) string {
	var value json.Number
	if err := json.Unmarshal(row[key], &value); err != nil || value == "" {
		return fallback
	}
	return value.String()
}

// sysSettingToolDecimal prints a decimal the way .NET prints a decimal parsed from DataService: "2.00" becomes "2"
// and "0.40" becomes "0.4". Trimming the text keeps every digit Creatio sent, which a float would not.
func sysSettingToolDecimal(text string) string {
	if strings.ContainsAny(text, "eE") {
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			return strconv.FormatFloat(parsed, 'f', -1, 64)
		}
		return text
	}
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "" || text == "-" || text == "-0" {
		return "0"
	}
	return text
}

// sysSettingToolDateTime parses DataService's zone-less "2006-01-02T15:04:05.000" text. A null or unparsable value is
// the zero time, which prints as .NET's DateTime.MinValue.
func sysSettingToolDateTime(row map[string]json.RawMessage, key string) time.Time {
	text := rowString(row, key)
	for _, layout := range []string{"2006-01-02T15:04:05", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// SysSettingValidationFailure is clio's Validation envelope: the rejected argument is both error and cause.
func SysSettingValidationFailure(message string) SysSettingFailure {
	return SysSettingFailure{message, "Validation", message, sysSettingToolValidationRecovery}
}

// sysSettingToolClassify maps a request failure onto clio's categories by the error's type, so the
// category does not depend on how a message happens to be worded.
func sysSettingToolClassify(err error, label string) SysSettingFailure {
	var syntaxErr *json.SyntaxError
	switch {
	case isAuthenticationError(err):
		return SysSettingFailure{"Authentication error " + label + ".", "Authentication", sysSettingToolAuthenticationCause, sysSettingToolAuthenticationRecovery}
	case isTransportError(err), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return SysSettingFailure{"Network error " + label + ".", "Network", sysSettingToolNetworkCause, sysSettingToolNetworkRecovery}
	case errors.As(err, &syntaxErr), strings.Contains(err.Error(), "returned HTML instead of JSON"):
		return SysSettingFailure{"Creatio returned a non-JSON response " + label + ".", "Network", sysSettingToolNonJSONCause, sysSettingToolNonJSONRecovery}
	case strings.HasPrefix(err.Error(), "SelectQuery failed: "):
		detail := sysSettingToolClamp(strings.TrimPrefix(err.Error(), "SelectQuery failed: "), sysSettingToolMaxCause)
		return SysSettingFailure{detail, "ProviderFailure", detail, sysSettingToolProviderRecovery}
	}
	return SysSettingFailure{"Failed " + label + ".", "Unknown", sysSettingToolUnknownCause, sysSettingToolUnknownRecovery}
}

// sysSettingToolConfigurationRecovery is clio's advice for an environment that cannot be resolved.
const sysSettingToolConfigurationRecovery = "Register the environment with reg-web-app, or pick one from list-environments."

// SysSettingConfigurationFailure is clio's classified failure for an environment that cannot be resolved:
// errorText is the tool's own label ("Failed reading sys-setting."), cause the resolution message, cut at
// 300 characters as clio cuts every cause.
func SysSettingConfigurationFailure(errorText, cause string) SysSettingFailure {
	return SysSettingFailure{errorText, "Configuration", sysSettingToolClamp(cause, sysSettingToolMaxCause), sysSettingToolConfigurationRecovery}
}

// SysSettingFailureLabel is the error text clio gives a sys-setting tool failure: "Failed reading sys-setting."
// or "Failed listing sys-settings.".
func SysSettingFailureLabel(list bool) string {
	if list {
		return "Failed " + sysSettingToolListLabel + "."
	}
	return "Failed " + sysSettingToolReadLabel + "."
}

func sysSettingToolClamp(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}
