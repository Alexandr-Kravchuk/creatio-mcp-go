package creatio

// What set-logo and set-background-image share (clio master 914dab286; the texts match 8.1.0.134 except where
// noted): the slice of clio's SysSettingsCommand / SysSettingsManager that writes a branding setting
// (TryUpdateSysSetting with a file or an inline value, UpdateSysSetting, the quiet read), the package data
// binder (EnvironmentPackageDataBinder over PackageDataBindingWriter and PackageTargetResolver) and
// FeatureStateService. The update-sys-setting tool itself belongs to T12; this is only what branding needs.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	themeWriteBinaryType          = "Binary"
	themeWriteUpdateLabel         = "updating sys-setting"
	themeWriteRefusedUpdate       = "Failed to update sys-setting. The setting may not exist, or the value did not match the expected type."
	themeWriteAllEmployees        = "a29a3ba5-4b0d-de11-9a51-005056c00008"
	themeWriteSecureTextType      = "SecureText"
	themeWriteBindingTimeout      = 100 * time.Second
	themeWriteFileSecurityUnknown = "Cannot determine the environment file-security mode; Binary upload was refused."
	// themeWriteAskForPackage is PackageTargetResolver.AskForPackage (clio master; 8.1.0.134 still advises
	// "Name the package explicitly (see list-packages for the available names).").
	themeWriteAskForPackage = "Ask the user which package should receive the change, then call again with that package named."
)

// themeWriteLocalError is a fault clio raises as ArgumentException: its own prose, reported redacted and cut
// at 300 characters (SysSettingFailureClassifier's Validation arm).
type themeWriteLocalError struct{ message string }

func (e themeWriteLocalError) Error() string { return e.message }

// themeWriteNonJSONError is NonJsonWriteResponseException.
type themeWriteNonJSONError struct{ message string }

func (e themeWriteNonJSONError) Error() string { return e.message }

// themeWriteSettingFailure is the error text of a failed TryUpdateSysSetting: clio's classified failure.Error.
func themeWriteSettingFailure(err error) string {
	var local themeWriteLocalError
	var nonJSON themeWriteNonJSONError
	switch {
	case errors.As(err, &local):
		return sysSettingToolClamp(redact.Text(local.message), sysSettingToolMaxCause)
	case errors.As(err, &nonJSON):
		return "Creatio returned a non-JSON response " + themeWriteUpdateLabel + "."
	}
	return sysSettingToolClassify(err, themeWriteUpdateLabel).Error
}

// themeWriteSetting is one SysSettings definition with its All-Users value as clio formats it.
type themeWriteSetting struct {
	found     bool
	id        string
	valueType string
	value     string
}

// themeWriteReadSetting is SysSettingsManager.GetAllUsersDefaultWithType: the definition by code, then its
// value rows; a missing setting reads ("", no type), a setting without an All-Users row reads "".
func (c *Client) themeWriteReadSetting(ctx context.Context, code string) (themeWriteSetting, error) {
	settings, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{
		"Id": "Id", "ValueTypeName": "ValueTypeName",
	}, map[string]any{"Code": comparisonFilter("Code", code, 1, 3)}, -1))
	if err != nil {
		return themeWriteSetting{}, err
	}
	if len(settings) == 0 {
		return themeWriteSetting{}, nil
	}
	setting := themeWriteSetting{found: true, id: rowString(settings[0], "Id"), valueType: rowString(settings[0], "ValueTypeName")}
	values, err := c.selectRows(ctx, buildSelectQuery("SysSettingsValue", sysSettingToolValueColumns(), map[string]any{
		"SysSettings":  comparisonFilter("SysSettings", setting.id, 0, 3),
		"SysAdminUnit": comparisonFilter("SysAdminUnit", themeWriteAllEmployees, 0, 3),
	}, -1))
	if err != nil {
		return themeWriteSetting{}, err
	}
	if len(values) > 0 {
		setting.value = sysSettingToolGetValue(setting.valueType, values[0])
	}
	return setting, nil
}

// themeWriteSettingIsOn is SetLogoCommand.ReadCompanionIsOn over TryGetSysSettingQuietly: any failure is "off".
func (c *Client) themeWriteSettingIsOn(ctx context.Context, code string) bool {
	setting, err := c.themeWriteReadSetting(ctx, code)
	return err == nil && strings.EqualFold(strings.TrimSpace(setting.value), "true")
}

// themeWriteTryUpdateSetting is SysSettingsCommand.TryUpdateSysSetting(UpdateSysSettingArgs) with either a
// file (value-file-path) or an inline value. It returns "" on success, else clio's failure.Error.
func (c *Client) themeWriteTryUpdateSetting(ctx context.Context, code, value, filePath string) string {
	if err := c.themeWriteUpdateSettingCore(ctx, code, value, filePath); err != nil {
		var refused themeWriteRefusal
		if errors.As(err, &refused) {
			return themeWriteRefusedUpdate
		}
		return themeWriteSettingFailure(err)
	}
	return ""
}

// themeWriteRefusal is UpdateSysSetting returning false.
type themeWriteRefusal struct{}

func (themeWriteRefusal) Error() string { return themeWriteRefusedUpdate }

func (c *Client) themeWriteUpdateSettingCore(ctx context.Context, code, value, filePath string) error {
	hasFile := strings.TrimSpace(filePath) != ""
	existing, err := c.themeWriteReadSetting(ctx, code)
	if err != nil {
		return err
	}
	targetIsBinary := existing.valueType == themeWriteBinaryType
	payload := value
	if hasFile {
		if !existing.found {
			return themeWriteLocalError{fmt.Sprintf("Sys-setting '%s' was not found or is not readable by the current user. Uploading a "+
				"file requires an existing, readable Binary setting.", code)}
		}
		if !targetIsBinary {
			return themeWriteLocalError{fmt.Sprintf("Cannot upload a file to sys-setting '%s': it is type '%s', not Binary. "+
				"A file value can only be written to a Binary setting.", code, existing.valueType)}
		}
		if err := c.themeWriteEnforceFileSecurity(ctx, filePath); err != nil {
			return err
		}
		if payload, err = themeWriteEncodeFile(filePath); err != nil {
			return err
		}
	} else if targetIsBinary {
		policy, err := c.themeWriteFileSecurityPolicy(ctx)
		if err != nil {
			return err
		}
		if policy.mode == "Unknown" {
			return themeWriteLocalError{themeWriteFileSecurityUnknown}
		}
		if policy.active() {
			return themeWriteLocalError{fmt.Sprintf("Sys-setting '%s' is Binary and this environment has an active file-security policy. "+
				"Provide the value via value-file-path (a file path) so its extension can be validated, "+
				"rather than an inline Base64 value.", code)}
		}
		if problem := themeWriteValidateBase64(payload); problem != "" {
			return themeWriteLocalError{problem}
		}
	}
	fallback := "Text"
	if hasFile {
		fallback = themeWriteBinaryType
	}
	updated, err := c.themeWriteUpdateSysSetting(ctx, code, payload, fallback)
	if err != nil {
		return err
	}
	if !updated {
		return themeWriteRefusal{}
	}
	// The read-back clio reports as the updated value; branding only needs it to succeed.
	_, err = c.themeWriteReadSetting(ctx, code)
	return err
}

// themeWriteFileSecurity is SysSettingsManager.GetFileSecurityPolicy.
type themeWriteFileSecurity struct {
	mode         string // Disabled, AllowList, DenyList, Unknown
	extensions   map[string]bool
	allowUnknown bool
}

func (p themeWriteFileSecurity) active() bool { return p.mode == "AllowList" || p.mode == "DenyList" }

func (c *Client) themeWriteFileSecurityPolicy(ctx context.Context) (themeWriteFileSecurity, error) {
	mode, err := c.themeWriteReadSetting(ctx, "FileSecurityMode")
	if err != nil {
		return themeWriteFileSecurity{}, err
	}
	policy := themeWriteFileSecurity{mode: "Unknown"}
	if id, ok := parseGUID(mode.value); ok {
		switch id {
		case "9801c625-fafb-4ed3-9383-c3c942a5c1e3":
			policy.mode = "Disabled"
		case "c6ca9a2f-3a4a-4d51-b67b-de36852cb916":
			policy.mode = "AllowList"
		case "60849c6e-24b4-45df-9aad-2f69d419823c":
			policy.mode = "DenyList"
		}
	}
	if !policy.active() {
		return policy, nil
	}
	listCode := "FileExtensionsDenyList"
	if policy.mode == "AllowList" {
		listCode = "FileExtensionsAllowList"
	}
	list, err := c.themeWriteReadSetting(ctx, listCode)
	if err != nil {
		return themeWriteFileSecurity{}, err
	}
	policy.extensions = map[string]bool{}
	for _, entry := range strings.Split(list.value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			policy.extensions[strings.ToLower(strings.TrimLeft(entry, "."))] = true
		}
	}
	allowUnknown, err := c.themeWriteReadSetting(ctx, "AllowFilesWithUnknownType")
	if err != nil {
		return themeWriteFileSecurity{}, err
	}
	raw := strings.TrimSpace(allowUnknown.value)
	policy.allowUnknown = raw == "" || strings.EqualFold(raw, "true")
	return policy, nil
}

// themeWriteEnforceFileSecurity is SysSettingsCommand.EnforceFileSecurityPolicy (fail closed on Unknown).
func (c *Client) themeWriteEnforceFileSecurity(ctx context.Context, filePath string) error {
	policy, err := c.themeWriteFileSecurityPolicy(ctx)
	if err != nil {
		return err
	}
	if policy.mode == "Disabled" {
		return nil
	}
	if policy.mode == "Unknown" {
		return themeWriteLocalError{themeWriteFileSecurityUnknown}
	}
	fileName := themeWriteFileName(filePath)
	extension := strings.TrimLeft(themeWriteExtension(filePath), ".")
	if extension == "" {
		if !policy.allowUnknown {
			return themeWriteLocalError{fmt.Sprintf("Cannot upload '%s': files with no extension are not allowed in this environment "+
				"(AllowFilesWithUnknownType is off).", fileName)}
		}
		return nil
	}
	listed := policy.extensions[strings.ToLower(extension)]
	if allowed := (policy.mode == "AllowList") == listed; !allowed {
		return themeWriteLocalError{fmt.Sprintf("Cannot upload '%s': files with extension '.%s' are not allowed in this "+
			"environment (%s file-security policy).", fileName, extension, policy.mode)}
	}
	return nil
}

// themeWriteEncodeFile is SysSettingsCommand.EncodeFileToBase64 with its bounded read.
func themeWriteEncodeFile(filePath string) (string, error) {
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return "", themeWriteLocalError{"File not found: '" + filePath + "'."}
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", themeWriteLocalError{"File not found: '" + filePath + "'."}
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, themeWriteImageMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(payload) > themeWriteImageMaxBytes {
		return "", themeWriteLocalError{fmt.Sprintf("File '%s' exceeds the %s-byte limit for a Binary sys-setting value.",
			filePath, themeWriteGroupDigits(themeWriteImageMaxBytes))}
	}
	return base64.StdEncoding.EncodeToString(payload), nil
}

// themeWriteValidateBase64 is SysSettingsManager.TryValidateBinaryValue: "" when valid.
func themeWriteValidateBase64(value string) string {
	tooLarge := fmt.Sprintf("Binary value exceeds the %s-byte limit.", themeWriteGroupDigits(themeWriteImageMaxBytes))
	if strings.TrimSpace(value) == "" {
		return "Value is not valid Base64 (Binary settings expect a Base64-encoded payload)."
	}
	if int64(len(value))/4*3 > themeWriteImageMaxBytes {
		return tooLarge
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
	if err != nil {
		return "Value is not valid Base64 (Binary settings expect a Base64-encoded payload)."
	}
	if len(decoded) > themeWriteImageMaxBytes {
		return tooLarge
	}
	return ""
}

// themeWriteUpdateSysSetting is SysSettingsManager.UpdateSysSetting for the value types branding writes
// (text, Boolean, Binary): the type of the existing setting decides the payload, then PostSysSettingsValues.
// false is a refusal; a body that is not a DataService response is themeWriteNonJSONError.
func (c *Client) themeWriteUpdateSysSetting(ctx context.Context, code, value, fallbackType string) (bool, error) {
	settings, err := c.selectRows(ctx, buildSelectQuery("SysSettings", map[string]string{
		"Id": "Id", "ValueTypeName": "ValueTypeName",
	}, map[string]any{"Code": comparisonFilter("Code", code, 1, 3)}, -1))
	if err != nil {
		return false, err
	}
	valueType := fallbackType
	if len(settings) > 0 {
		valueType = rowString(settings[0], "ValueTypeName")
	}
	var payloadValue any
	switch {
	case strings.Contains(valueType, "Text"):
		payloadValue = value
	case strings.Contains(valueType, "Boolean"):
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			payloadValue = true
		case "false":
			payloadValue = false
		default:
			return false, nil
		}
	case strings.Contains(valueType, themeWriteBinaryType):
		if themeWriteValidateBase64(value) != "" {
			return false, nil
		}
		payloadValue = value
	default:
		// Branding writes only the types above; any other type is clio's "unsupported" refusal here.
		return false, nil
	}
	body := toJNode(orderedFields{{"isPersonal", false}, {"sysSettingsValues", orderedFields{{code, payloadValue}}}}).stjJSON()
	response, err := c.serviceRequest(ctx, serviceCall{Route: "DataService/json/SyncReply/PostSysSettingsValues", Body: body,
		Timeout: themeWriteBindingTimeout, Limit: maxResponseBytes, Label: "PostSysSettingsValues"})
	if err != nil {
		return false, err
	}
	if response.status < http.StatusOK || response.status >= http.StatusMultipleChoices {
		return false, transportError{err: fmt.Errorf("PostSysSettingsValues returned HTTP %d", response.status)}
	}
	return themeWriteParseSaveResult(response.payload, code)
}

// themeWriteParseSaveResult is DeserializeWriteResponse<UpdateSysSettingResponse> plus the saveResult check.
func themeWriteParseSaveResult(payload []byte, code string) (bool, error) {
	notJSON := func(clause string) error {
		return themeWriteNonJSONError{"Failed " + themeWriteUpdateLabel + ": the environment answered with " + clause +
			" where a DataService JSON response was expected - either the session was rejected (expired password " +
			"/ login redirect) or the URL does not reach Creatio (proxy, gateway, wrong path)."}
	}
	text := strings.TrimSpace(string(payload))
	if text == "" {
		return false, notJSON("an empty body")
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		if looksLikeHTML(payload) {
			return false, notJSON("an HTML/XML page")
		}
		return false, notJSON("a body that is not valid JSON")
	}
	if root == nil {
		return false, notJSON("a JSON document carrying no response object")
	}
	var response struct {
		SaveResult map[string]bool `json:"saveResult"`
	}
	if _, ok := root.(map[string]any); !ok || json.Unmarshal(payload, &response) != nil {
		return false, themeWriteNonJSONError{"Failed " + themeWriteUpdateLabel + ": the environment answered with JSON of an unexpected shape - it " +
			"parsed, but it does not match the DataService response this operation expects. The request " +
			"reached something that answers JSON; the endpoint, the Creatio version or the service " +
			"behind the URL may not be the expected one."}
	}
	return response.SaveResult[code], nil
}

// ---------------------------------------------------------------------------------------------- package data

// themeWriteBindingOutcome is PackageDataBindingOutcome.
type themeWriteBindingOutcome struct {
	bound    bool
	warnings []string
}

func themeWriteRefused(warnings []string) themeWriteBindingOutcome {
	return themeWriteBindingOutcome{warnings: warnings}
}

// themeWriteColumnPolicy is DataBindingColumnPolicy: install-time key and force-update columns.
type themeWriteColumnPolicy struct{ keys, forceUpdate []string }

var (
	themeWriteSysSettingsValueColumns = []string{"Id", "SysSettings", "SysAdminUnit", "IsDef", "TextValue", "IntegerValue",
		"FloatValue", "BooleanValue", "DateTimeValue", "GuidValue", "BinaryValue"}
	themeWriteSysSettingsValuePolicy = &themeWriteColumnPolicy{keys: []string{"SysSettings", "SysAdminUnit"},
		forceUpdate: []string{"IsDef", "TextValue", "IntegerValue", "FloatValue", "BooleanValue", "DateTimeValue", "GuidValue", "BinaryValue"}}
	themeWriteFeatureStateColumns = []string{"Id", "Feature", "SysAdminUnit", "FeatureState"}
	themeWriteFeatureStatePolicy  = &themeWriteColumnPolicy{keys: []string{"Feature", "SysAdminUnit"}, forceUpdate: []string{"FeatureState"}}
	themeWriteFeatureColumns      = []string{"Id", "Code", "Name"}
)

// themeWriteRemovalCause is EnvironmentPackageDataBinder.BindingRemovalCause.
type themeWriteRemovalCause int

const (
	themeWriteSourceRowUnavailable themeWriteRemovalCause = iota
	themeWriteSourceRowNotShippable
	themeWriteWithdrawnByCaller
)

func (cause themeWriteRemovalCause) describe() string {
	switch cause {
	case themeWriteWithdrawnByCaller:
		return "this delivery no longer ships it"
	case themeWriteSourceRowNotShippable:
		return "the row it would ship must not travel in a package"
	}
	return "it no longer has a source row"
}

// themeWriteProjection is DataBindingDbSchema: the runtime schema reduced to the delivered columns.
type themeWriteProjection struct {
	uid     string
	columns []schemaWriteEntRuntimeColumn
}

// themeWriteBinder is EnvironmentPackageDataBinder with its PackageDataBindingWriter: one per tool call, so
// projections are cached for one run as clio's transient writer caches them.
type themeWriteBinder struct {
	c           *Client
	ctx         context.Context
	packageUID  string
	packageName string
	selected    bool
	projections map[string]themeWriteProjection
}

func (c *Client) themeWriteNewBinder(ctx context.Context) *themeWriteBinder {
	return &themeWriteBinder{c: c, ctx: ctx, projections: map[string]themeWriteProjection{}}
}

// usePackage is UsePackage over PackageTargetResolver.Resolve(name, requireEditable: true), as clio master
// resolves it: CurrentPackageId holds a package UId, so the current package is matched on UId.
func (b *themeWriteBinder) usePackage(packageName string) (string, error) {
	name, uid, err := b.c.themeWriteResolvePackage(b.ctx, packageName)
	if err != nil {
		return "", err
	}
	b.packageName, b.packageUID, b.selected = name, uid, true
	return name, nil
}

func (c *Client) themeWriteResolvePackage(ctx context.Context, packageName string) (string, string, error) {
	unavailable := func(err error) error {
		return fmt.Errorf("The environment could not be asked which package to deliver the data into: %s", err.Error())
	}
	materialize := func(row targetPackageRow, subject string) (string, string, error) {
		uid, ok := parseGUID(row.UID)
		if !ok || uid == emptyGUID {
			return "", "", errors.New(subject + " has no usable UId in the environment, so package data cannot be addressed to it. " + themeWriteAskForPackage)
		}
		return row.Name, uid, nil
	}
	if name := strings.TrimSpace(packageName); name != "" {
		rows, err := c.selectTargetPackages(ctx, nil)
		if err != nil {
			return "", "", unavailable(err)
		}
		for _, row := range rows {
			if !strings.EqualFold(row.Name, name) {
				continue
			}
			if row.locked() {
				return "", "", fmt.Errorf("Package '%s' is locked, so it cannot receive design-time writes. Unlock it with unlock-package, or name another package.", row.Name)
			}
			return materialize(row, fmt.Sprintf("Package '%s'", row.Name))
		}
		return "", "", fmt.Errorf("Package '%s' was not found in the environment. Check the name against list-packages.", name)
	}
	current, err := c.readSysSettingValue(ctx, currentPackageSettingCode)
	if err != nil {
		return "", "", unavailable(err)
	}
	packageID, ok := parseGUID(current)
	if !ok || packageID == emptyGUID {
		return "", "", fmt.Errorf("No package was named, and the environment's %s system setting does not point at one, so there is nowhere to deliver the package data. %s", currentPackageSettingCode, themeWriteAskForPackage)
	}
	rows, err := c.selectTargetPackages(ctx, map[string]any{"UId": comparisonFilter("UId", packageID, 0, 3)})
	if err != nil {
		return "", "", unavailable(err)
	}
	if len(rows) > 1 {
		return "", "", fmt.Errorf("The environment's %s system setting points at package '%s', which matched %d packages, so the delivery target cannot be told apart. %s", currentPackageSettingCode, current, len(rows), themeWriteAskForPackage)
	}
	if len(rows) == 0 || strings.TrimSpace(rows[0].Name) == "" {
		return "", "", fmt.Errorf("The environment's %s system setting points at package '%s', which could not be resolved to a usable package. %s", currentPackageSettingCode, current, themeWriteAskForPackage)
	}
	if rows[0].locked() {
		return "", "", fmt.Errorf("The environment's %s system setting points at package '%s', which is locked, so it cannot receive design-time writes. Unlock it with unlock-package, or name another package.", currentPackageSettingCode, rows[0].Name)
	}
	return materialize(rows[0], fmt.Sprintf("The %s package '%s'", currentPackageSettingCode, rows[0].Name))
}

func (b *themeWriteBinder) requirePackage() error {
	if !b.selected {
		return errors.New("No package was selected before delivering package data. Call UsePackage first.")
	}
	return nil
}

// bindSysSettingsValue is BindSysSettingsValue: the All-Users value row matched on its natural key.
func (b *themeWriteBinder) bindSysSettingsValue(code string) (themeWriteBindingOutcome, error) {
	folder := "SysSettingsValue_" + code
	var warnings []string
	refuse := func(cause themeWriteRemovalCause) (themeWriteBindingOutcome, error) {
		if err := b.removeIfShipped(folder, "SysSettingsValue", &warnings, cause); err != nil {
			return themeWriteBindingOutcome{}, err
		}
		return themeWriteRefused(warnings), nil
	}
	definitions, err := b.c.selectRows(b.ctx, buildSelectQuery("SysSettings", map[string]string{"Id": "Id", "ValueTypeName": "ValueTypeName"},
		map[string]any{"filter0": comparisonFilter("Code", code, 1, 3)}, -1))
	if err != nil {
		return themeWriteBindingOutcome{}, err
	}
	if len(definitions) > 1 {
		return themeWriteBindingOutcome{}, fmt.Errorf("System setting '%s' has multiple definitions on this environment, so the delivery cannot tell which one to package.", code)
	}
	definitionID := ""
	if len(definitions) == 1 {
		definitionID, _ = parseGUID(rowString(definitions[0], "Id"))
	}
	if definitionID == "" {
		warnings = append(warnings, code+": the setting is not defined on this environment, so there is nothing to bind. A "+
			"value binding references its setting by id, so a target that does not ship this definition could "+
			"not resolve the row either")
		return refuse(themeWriteSourceRowUnavailable)
	}
	if strings.EqualFold(rowString(definitions[0], "ValueTypeName"), themeWriteSecureTextType) {
		warnings = append(warnings, code+": the setting is defined as SecureText on this environment, and a "+
			"secret value is never shipped in a package")
		return refuse(themeWriteSourceRowNotShippable)
	}
	valueRowID, err := b.singleRowID("SysSettingsValue", map[string]any{
		"filter0": comparisonFilter("SysSettings", definitionID, 0, 3),
		"filter1": comparisonFilter("SysAdminUnit", themeWriteAllEmployees, 0, 3),
	}, fmt.Sprintf("All-Users value rows of setting '%s'", definitionID))
	if err != nil {
		return themeWriteBindingOutcome{}, err
	}
	if valueRowID == "" {
		warnings = append(warnings, code+": no All-Users value on this environment")
		return refuse(themeWriteSourceRowUnavailable)
	}
	if err := b.saveBinding(folder, "SysSettingsValue", themeWriteSysSettingsValueColumns, valueRowID, themeWriteSysSettingsValuePolicy); err != nil {
		return themeWriteBindingOutcome{}, err
	}
	return themeWriteBindingOutcome{bound: true}, nil
}

// removeSysSettingsValue is RemoveSysSettingsValue.
func (b *themeWriteBinder) removeSysSettingsValue(code string) ([]string, error) {
	var warnings []string
	err := b.removeIfShipped("SysSettingsValue_"+code, "SysSettingsValue", &warnings, themeWriteWithdrawnByCaller)
	return warnings, err
}

// bindRow is BindRow: one row by its own Id.
func (b *themeWriteBinder) bindRow(schemaName, suffix string, columns []string, rowID string) (themeWriteBindingOutcome, error) {
	folder := schemaName + "_" + suffix
	var warnings []string
	exists := false
	if rowID != "" && rowID != emptyGUID {
		rows, err := b.c.selectRows(b.ctx, buildSelectQuery(schemaName, map[string]string{"Id": "Id"},
			map[string]any{"filter0": comparisonFilter("Id", rowID, 0, 3)}, -1))
		if err != nil {
			return themeWriteBindingOutcome{}, err
		}
		for _, row := range rows {
			exists = exists || strings.TrimSpace(rowString(row, "Id")) != ""
		}
	}
	if !exists {
		shown := rowID
		if shown == "" {
			shown = emptyGUID
		}
		warnings = append(warnings, fmt.Sprintf("%s: row '%s' not found on this environment", folder, shown))
		if err := b.removeIfShipped(folder, schemaName, &warnings, themeWriteSourceRowUnavailable); err != nil {
			return themeWriteBindingOutcome{}, err
		}
		return themeWriteRefused(warnings), nil
	}
	if err := b.saveBinding(folder, schemaName, columns, rowID, nil); err != nil {
		return themeWriteBindingOutcome{}, err
	}
	return themeWriteBindingOutcome{bound: true}, nil
}

// bindFeatureOffState is BindFeatureOffState: the confirmed All-Users off-state plus its Feature row.
func (b *themeWriteBinder) bindFeatureOffState(code string) (themeWriteBindingOutcome, error) {
	stateFolder, definitionFolder := "AdminUnitFeatureState_"+code, "Feature_"+code
	var warnings []string
	refuse := func(cause themeWriteRemovalCause) (themeWriteBindingOutcome, error) {
		if err := b.removeIfShipped(stateFolder, "AdminUnitFeatureState", &warnings, cause); err != nil {
			return themeWriteBindingOutcome{}, err
		}
		if err := b.removeIfShipped(definitionFolder, "Feature", &warnings, cause); err != nil {
			return themeWriteBindingOutcome{}, err
		}
		return themeWriteRefused(warnings), nil
	}
	featureID, err := b.singleRowID("Feature", map[string]any{"filter0": comparisonFilter("Code", code, 1, 3)},
		fmt.Sprintf("the '%s' feature definition", code))
	if err != nil {
		return themeWriteBindingOutcome{}, err
	}
	if featureID, _ = parseGUID(featureID); featureID == "" {
		warnings = append(warnings, code+": the feature is not defined on this environment")
		return refuse(themeWriteSourceRowUnavailable)
	}
	rows, err := b.c.selectRows(b.ctx, buildSelectQuery("AdminUnitFeatureState", map[string]string{"Id": "Id", "FeatureState": "FeatureState"},
		map[string]any{"filter0": comparisonFilter("Feature", featureID, 0, 3), "filter1": comparisonFilter("SysAdminUnit", themeWriteAllEmployees, 0, 3)}, -1))
	if err != nil {
		return themeWriteBindingOutcome{}, err
	}
	if len(rows) > 1 {
		return themeWriteBindingOutcome{}, themeWriteDuplicateRows(len(rows), fmt.Sprintf("All-Users feature state of feature '%s'", featureID))
	}
	if len(rows) == 0 || strings.TrimSpace(rowString(rows[0], "Id")) == "" {
		warnings = append(warnings, code+": no All-Users feature state on this environment (the feature was not turned off here)")
		return refuse(themeWriteSourceRowUnavailable)
	}
	off := themeWriteReadOffState(rows[0]["FeatureState"])
	if off == nil || !*off {
		state := "still on"
		if off == nil {
			state = "not readable as an on/off value"
		}
		warnings = append(warnings, code+": the All-Users feature state on this environment is "+state+", and only a confirmed "+
			"off-state is bound — the binding force-updates FeatureState on install, so delivering this row "+
			"would turn the feature back on for the target")
		return refuse(themeWriteSourceRowNotShippable)
	}
	if err := b.saveBinding(definitionFolder, "Feature", themeWriteFeatureColumns, featureID, nil); err != nil {
		return themeWriteBindingOutcome{}, err
	}
	if err := b.saveBinding(stateFolder, "AdminUnitFeatureState", themeWriteFeatureStateColumns, rowString(rows[0], "Id"), themeWriteFeatureStatePolicy); err != nil {
		return themeWriteBindingOutcome{}, err
	}
	return themeWriteBindingOutcome{bound: true}, nil
}

// themeWriteReadOffState is ReadOffState: a number (0 is off), a boolean (false is off) or their text.
func themeWriteReadOffState(raw json.RawMessage) *bool {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	result := func(off bool) *bool { return &off }
	switch typed := value.(type) {
	case bool:
		return result(!typed)
	case float64:
		if typed != float64(int32(typed)) {
			return nil
		}
		return result(typed == 0)
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			return result(false)
		case "false":
			return result(true)
		}
		if number, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 32); err == nil {
			return result(number == 0)
		}
	}
	return nil
}

// removeBinding is RemoveBinding.
func (b *themeWriteBinder) removeBinding(folder, expectedSchema string) ([]string, error) {
	var warnings []string
	err := b.removeIfShipped(folder, expectedSchema, &warnings, themeWriteWithdrawnByCaller)
	return warnings, err
}

func themeWriteDuplicateRows(count int, subject string) error {
	return fmt.Errorf("Expected at most one match for %s on this environment, but found %d. "+
		"Resolve the duplicates before delivering package data, so the package delivers a row you chose.", subject, count)
}

func (b *themeWriteBinder) singleRowID(schema string, filters map[string]any, subject string) (string, error) {
	rows, err := b.c.selectRows(b.ctx, buildSelectQuery(schema, map[string]string{"Id": "Id"}, filters, -1))
	if err != nil {
		return "", err
	}
	if len(rows) > 1 {
		return "", themeWriteDuplicateRows(len(rows), subject)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.TrimSpace(rowString(rows[0], "Id")), nil
}

// themeWriteExistingBinding is PackageDataBindingRef.
type themeWriteExistingBinding struct{ uid, schema string }

// findBinding is PackageDataBindingWriter.FindBinding.
func (b *themeWriteBinder) findBinding(folder string) (*themeWriteExistingBinding, error) {
	rows, err := b.c.selectRows(b.ctx, buildSelectQuery("SysPackageSchemaData",
		map[string]string{"UId": "UId", "EntitySchemaName": "SysSchema.Name"},
		map[string]any{"filter0": comparisonFilter("Name", folder, 1, 3), "filter1": comparisonFilter("SysPackage.UId", b.packageUID, 0, 3)}, -1))
	if err != nil {
		return nil, err
	}
	if len(rows) > 1 {
		return nil, fmt.Errorf("Package data binding '%s' has multiple registrations in package '%s'.", folder, b.packageUID)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	uid, ok := parseGUID(rowString(rows[0], "UId"))
	if !ok || uid == emptyGUID {
		return nil, fmt.Errorf("Package data binding '%s' in package '%s' carries an unusable UId '%s', so a re-save could not update it in place.", folder, b.packageUID, rowString(rows[0], "UId"))
	}
	return &themeWriteExistingBinding{uid: uid, schema: rowString(rows[0], "EntitySchemaName")}, nil
}

// removeIfShipped is EnvironmentPackageDataBinder.RemoveIfShipped.
func (b *themeWriteBinder) removeIfShipped(folder, expectedSchema string, warnings *[]string, cause themeWriteRemovalCause) error {
	if err := b.requirePackage(); err != nil {
		return err
	}
	existing, err := b.findBinding(folder)
	if err != nil || existing == nil {
		return err
	}
	if strings.TrimSpace(existing.schema) != "" && !strings.EqualFold(existing.schema, expectedSchema) {
		*warnings = append(*warnings, fmt.Sprintf("%s: left untouched — the package already carries a binding of this name for schema "+
			"'%s', which this delivery does not own.", folder, existing.schema))
		return nil
	}
	if strings.TrimSpace(existing.schema) == "" {
		*warnings = append(*warnings, fmt.Sprintf("%s: left untouched — the environment did not report which entity schema the package's "+
			"binding of this name delivers, so this delivery cannot confirm it is the '%s' "+
			"folder it would have shipped.", folder, expectedSchema))
		return nil
	}
	body := toJNode(orderedFields{{"packageUId", b.packageUID}, {"packageSchemaDataName", folder}}).stjJSON()
	payload, err := b.c.callService(b.ctx, serviceCall{Route: "DataService/json/SyncReply/DeletePackageSchemaDataRequest", Body: body,
		Timeout: themeWriteBindingTimeout, Label: "DeletePackageSchemaDataRequest"})
	if err != nil {
		return err
	}
	if err := schemaWriteEntThrowIfUnsuccessful(payload, "DeletePackageSchemaDataRequest"); err != nil {
		return err
	}
	*warnings = append(*warnings, fmt.Sprintf("%s: previously shipped binding removed, %s", folder, cause.describe()))
	return nil
}

// projectSchema is PackageDataBindingWriter.ProjectSchema: every requested column must exist.
func (b *themeWriteBinder) projectSchema(schemaName string, columnNames []string) (themeWriteProjection, error) {
	key := schemaName + "(" + strings.Join(columnNames, ",") + ")"
	if cached, ok := b.projections[key]; ok {
		return cached, nil
	}
	body, _ := json.Marshal(map[string]string{"Name": schemaName})
	payload, err := b.c.postDataServiceJSON(b.ctx, "RuntimeEntitySchemaRequest", body, themeWriteBindingTimeout, maxResponseBytes)
	if err != nil {
		return themeWriteProjection{}, err
	}
	var response struct {
		Success   bool                         `json:"success"`
		Schema    *schemaWriteEntRuntimeSchema `json:"schema"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return themeWriteProjection{}, err
	}
	if !response.Success || response.Schema == nil {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			return themeWriteProjection{}, errors.New(*response.ErrorInfo.Message)
		}
		return themeWriteProjection{}, fmt.Errorf("Runtime schema '%s' was not returned by Creatio.", schemaName)
	}
	if response.Schema.PrimaryColumnUID.isEmpty() {
		return themeWriteProjection{}, fmt.Errorf("Schema '%s' does not expose a primary column.", schemaName)
	}
	all := make([]schemaWriteEntRuntimeColumn, 0, len(response.Schema.Columns.Items))
	for _, column := range response.Schema.Columns.Items {
		all = append(all, column)
	}
	schemaWriteEntSortColumns(all, func(i, j int) bool { return compareOrdinalIgnoreCase(all[i].Name, all[j].Name) < 0 })
	requested := map[string]bool{}
	for _, name := range columnNames {
		requested[strings.ToLower(name)] = true
	}
	projection := themeWriteProjection{uid: response.Schema.UID.String()}
	found := map[string]bool{}
	for _, column := range all {
		if requested[strings.ToLower(column.Name)] {
			projection.columns = append(projection.columns, column)
			found[strings.ToLower(column.Name)] = true
		}
	}
	var missing []string
	for _, name := range columnNames {
		if !found[strings.ToLower(name)] && !themeWriteContainsFold(missing, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return themeWriteProjection{}, fmt.Errorf("Schema '%s' on this environment is missing column(s) required by the data "+
			"binding: %s. Binding a partial projection would ship an incomplete or wildcard-matching binding, so the operation was stopped.",
			schemaName, strings.Join(missing, ", "))
	}
	b.projections[key] = projection
	return projection, nil
}

func themeWriteContainsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

// saveBinding is EnvironmentPackageDataBinder.SaveBinding over PackageDataBindingWriter.SaveBinding.
func (b *themeWriteBinder) saveBinding(folder, schemaName string, columnNames []string, rowID string, policy *themeWriteColumnPolicy) error {
	if err := b.requirePackage(); err != nil {
		return err
	}
	projection, err := b.projectSchema(schemaName, columnNames)
	if err != nil {
		return err
	}
	existing, err := b.findBinding(folder)
	if err != nil {
		return err
	}
	if existing != nil && !strings.EqualFold(existing.schema, schemaName) {
		if strings.TrimSpace(existing.schema) != "" {
			return fmt.Errorf("Package data binding '%s' already exists for schema '%s', but this delivery needs it for '%s'. Rename or "+
				"remove the existing binding first.", folder, existing.schema, schemaName)
		}
		return fmt.Errorf("Package data binding '%s' already exists, but the environment did not report which "+
			"entity schema it delivers, so this delivery cannot confirm it is the '%s' folder "+
			"it would refresh — and refreshing replaces the rows and the schema the registration "+
			"currently carries.", folder, schemaName)
	}
	uid := string(schemaWriteEntNewUID())
	if existing != nil {
		uid = existing.uid
	}
	var keys, forced map[string]bool
	if policy != nil {
		keys, forced = map[string]bool{}, map[string]bool{}
		for _, name := range policy.keys {
			keys[strings.ToLower(name)] = true
		}
		for _, name := range policy.forceUpdate {
			forced[strings.ToLower(name)] = true
		}
	}
	columns := []any{}
	hasKey := false
	for _, column := range projection.columns {
		typeUID, ok := schemaWriteEntBindingTypes[column.DataValueType]
		if !ok {
			return fmt.Errorf("Column '%s' uses unsupported runtime dataValueType '%d' for DB-first binding generation.", column.Name, column.DataValueType)
		}
		name := strings.ToLower(column.Name)
		isKey := name == "id"
		if keys != nil {
			isKey = keys[name]
		}
		hasKey = hasKey || isKey
		columns = append(columns, orderedFields{{"id", string(schemaWriteEntNewUID())}, {"uId", column.UID.String()},
			{"isForceUpdate", forced[name]}, {"isKey", isKey}, {"name", column.Name}, {"caption", column.Name}, {"dataValueTypeUId", typeUID}})
	}
	if !hasKey {
		return fmt.Errorf("Binding '%s' would deliver schema '%s' with no key column, so the install target would match every row of the entity instead of the delivered one. Include the Id column in the delivered set, or supply a column policy naming the natural key.", folder, schemaName)
	}
	body := toJNode(orderedFields{{"uId", uid}, {"name", folder}, {"package", orderedFields{{"uId", b.packageUID}, {"name", b.packageName}}},
		{"entitySchemaUId", projection.uid}, {"entitySchemaName", schemaName}, {"installType", 0},
		{"columns", columns}, {"boundRecordIds", []any{rowID}}}).stjJSON()
	payload, err := b.c.callService(b.ctx, serviceCall{Route: "ServiceModel/SchemaDataDesignerService.svc/SaveSchema", Body: body,
		Timeout: themeWriteBindingTimeout, Label: "SaveSchema"})
	if err != nil {
		return err
	}
	return schemaWriteEntThrowIfUnsuccessful(payload, "SaveSchema")
}

// ---------------------------------------------------------------------------------------------- feature state

// themeWriteSetFeatureOff is FeatureStateService.SetFeatureState(code, AllEmployees, false): an undefined
// feature is left alone; a missing state row is inserted, a row that is on is turned off, then the feature
// cache is cleared. ATF's model saves become one DataService BatchQuery each.
func (c *Client) themeWriteSetFeatureOff(ctx context.Context, code string) error {
	features, err := c.selectRows(ctx, buildSelectQuery("AppFeature", map[string]string{"Id": "Id", "Code": "Code"},
		map[string]any{"filter0": comparisonFilter("Code", code, 1, 3)}, 1))
	if err != nil {
		return err
	}
	featureID := ""
	if len(features) > 0 {
		featureID, _ = parseGUID(rowString(features[0], "Id"))
	}
	if featureID == "" || featureID == emptyGUID {
		return nil
	}
	action := fmt.Sprintf("turning the %s feature off for admin unit '%s'", code, themeWriteAllEmployees)
	states, err := c.selectRows(ctx, buildSelectQuery("AdminUnitFeatureState", map[string]string{"Id": "Id", "FeatureState": "FeatureState"},
		map[string]any{"filter0": comparisonFilter("Feature", featureID, 0, 3), "filter1": comparisonFilter("SysAdminUnit", themeWriteAllEmployees, 0, 3)}, 1))
	if err != nil {
		return err
	}
	parameter := func(dataValueType int, value any) orderedFields {
		return orderedFields{{"expressionType", 2}, {"parameter", orderedFields{{"dataValueType", dataValueType}, {"value", value}}}}
	}
	if len(states) == 0 {
		insert := orderedFields{{"__type", "Terrasoft.Nui.ServiceModel.DataContract.InsertQuery"}, {"rootSchemaName", "AppFeatureState"},
			{"queryKind", 0}, {"columnValues", orderedFields{{"items", orderedFields{
				{"Id", parameter(0, string(schemaWriteEntNewUID()))}, {"Feature", parameter(0, featureID)},
				{"AdminUnit", parameter(0, themeWriteAllEmployees)}, {"FeatureState", parameter(1, 0)}}}}},
			{"includeProcessExecutionData", true}}
		if err := c.themeWriteBatch(ctx, insert, action); err != nil {
			return err
		}
		return c.themeWriteClearFeatureCache(ctx, code)
	}
	if off := themeWriteReadOffState(states[0]["FeatureState"]); off != nil && *off {
		return nil
	}
	stateID := rowString(states[0], "Id")
	writable, err := c.selectRows(ctx, buildSelectQuery("AppFeatureState", map[string]string{"Id": "Id"},
		map[string]any{"filter0": comparisonFilter("Id", stateID, 0, 3)}, 1))
	if err != nil {
		return err
	}
	if len(writable) == 0 {
		return fmt.Errorf("The state row of the %s feature for admin unit '%s' (%s) could not be re-read through AppFeatureState, so the feature state was not changed.", code, themeWriteAllEmployees, stateID)
	}
	update := orderedFields{{"__type", "Terrasoft.Nui.ServiceModel.DataContract.UpdateQuery"}, {"rootSchemaName", "AppFeatureState"},
		{"queryKind", 0}, {"columnValues", orderedFields{{"items", orderedFields{{"FeatureState", parameter(1, 0)}}}}},
		{"includeProcessExecutionData", true},
		{"filters", orderedFields{{"filterType", 6}, {"isEnabled", true}, {"logicalOperation", 0}, {"items", orderedFields{
			{"Id", orderedFields{{"filterType", 1}, {"comparisonType", 3}, {"isEnabled", true},
				{"leftExpression", orderedFields{{"expressionType", 0}, {"columnPath", "Id"}}}, {"rightExpression", parameter(0, stateID)}}}}}}}}
	if err := c.themeWriteBatch(ctx, update, action); err != nil {
		return err
	}
	return c.themeWriteClearFeatureCache(ctx, code)
}

// themeWriteBatch posts one query as ATF's BatchExecute does and raises ThrowIfSaveFailed's text.
func (c *Client) themeWriteBatch(ctx context.Context, query orderedFields, action string) error {
	body := toJNode(orderedFields{{"items", []any{query}}, {"includeProcessExecutionData", true},
		{"instanceId", string(schemaWriteEntNewUID())}}).stjJSON()
	payload, err := c.callService(ctx, serviceCall{Route: "DataService/json/SyncReply/BatchQuery", Body: body,
		Timeout: themeWriteBindingTimeout, Label: "BatchQuery"})
	if err != nil {
		return err
	}
	var response struct {
		Success        *bool `json:"success"`
		ResponseStatus *struct {
			Message string `json:"Message"`
		} `json:"responseStatus"`
		QueryResults []struct {
			Success        *bool `json:"success"`
			ResponseStatus *struct {
				Message string `json:"Message"`
			} `json:"responseStatus"`
		} `json:"queryResults"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("The DataService rejected %s: %s", action, err.Error())
	}
	message := ""
	failed := response.Success != nil && !*response.Success
	if response.ResponseStatus != nil {
		message = response.ResponseStatus.Message
	}
	for _, result := range response.QueryResults {
		if result.Success != nil && !*result.Success {
			failed = true
			if message == "" && result.ResponseStatus != nil {
				message = result.ResponseStatus.Message
			}
		}
	}
	if failed {
		if message == "" {
			message = "unknown error"
		}
		return fmt.Errorf("The DataService rejected %s: %s", action, message)
	}
	return nil
}

// themeWriteClearFeatureCache is FeatureStateService.ClearCache: the code goes Base64-encoded into the path.
func (c *Client) themeWriteClearFeatureCache(ctx context.Context, code string) error {
	route := "rest/FeatureService/ClearFeaturesCacheForAllUsers/" + base64.StdEncoding.EncodeToString([]byte(code))
	_, err := c.callService(ctx, serviceCall{Method: http.MethodGet, Route: route, Label: "FeatureService"})
	return err
}

// ---------------------------------------------------------------------------------------------- OData

// themeWriteODataSingleID is SetBackgroundImageCommand.TryQuerySingleId: the first row's Id, "" when the
// collection is empty, or the reason the answer could not be read. entity is a fixed schema name; the
// filter is built from validated GUIDs and fixed names only.
func (c *Client) themeWriteODataSingleID(ctx context.Context, entity, filter string) (string, string) {
	query := "$filter=" + url.QueryEscape(filter) + "&$select=Id&$top=1"
	status, payload, err := c.themeWriteODataRequest(ctx, http.MethodGet, entity, query, nil)
	if err != nil {
		return "", err.Error()
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if text := themeWriteODataError(payload); text != "" {
			return "", text
		}
		return "", fmt.Sprintf("Response status code does not indicate success: %d.", status)
	}
	if strings.TrimSpace(string(payload)) == "" {
		return "", "the environment returned an empty response."
	}
	var root any
	if json.Unmarshal(payload, &root) != nil {
		return "", "the environment returned a non-JSON response."
	}
	if text := themeWriteODataError(payload); text != "" {
		return "", text
	}
	object, _ := root.(map[string]any)
	rows, ok := object["value"].([]any)
	if !ok {
		return "", "the environment returned an unexpected response."
	}
	if len(rows) > 0 {
		if row, ok := rows[0].(map[string]any); ok {
			if text, ok := row["Id"].(string); ok {
				if id, ok := parseGUID(text); ok && id != emptyGUID {
					return id, ""
				}
			}
		}
	}
	return "", ""
}

// themeWriteODataError recognizes the error envelopes CreatioResponseError.TryDetect reads from an OData
// payload: OData v4 {"error":{"message"}}, the DataService envelope and the bare {"Message"} routing miss.
func themeWriteODataError(payload []byte) string {
	var root map[string]any
	if json.Unmarshal(payload, &root) != nil {
		return ""
	}
	if errorObject, ok := root["error"].(map[string]any); ok {
		if text, ok := errorObject["message"].(string); ok && text != "" {
			return text
		}
		return "Creatio returned an empty error response."
	}
	if _, hasContext := root["@odata.context"]; hasContext {
		return ""
	}
	if status, ok := root["responseStatus"].(map[string]any); ok {
		if text, ok := status["Message"].(string); ok && text != "" {
			return text
		}
	}
	if text, ok := root["Message"].(string); ok {
		if detail, ok := root["MessageDetail"].(string); ok && detail != "" {
			return detail
		}
		if text != "" {
			return text
		}
		return "Creatio returned an empty error response."
	}
	return ""
}

// themeWriteODataPost is ApplicationClient.ExecutePostRequest on an OData collection: a failure is the
// client's exception message.
func (c *Client) themeWriteODataPost(ctx context.Context, entity string, body []byte) error {
	status, payload, err := c.themeWriteODataRequest(ctx, http.MethodPost, entity, "", body)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if text := themeWriteODataError(payload); text != "" {
			return errors.New(text)
		}
		return fmt.Errorf("Response status code does not indicate success: %d.", status)
	}
	return nil
}

func (c *Client) themeWriteODataRequest(ctx context.Context, method, entity, rawQuery string, body []byte) (int, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, themeWriteBindingTimeout)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		target := c.serviceURL("odata/" + entity)
		if rawQuery != "" {
			target += "?" + rawQuery
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(requestCtx, method, target, reader)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		return request, nil
	})
	if err != nil {
		return 0, nil, err
	}
	payload, err := readResponseLimit(response, maxResponseBytes)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, payload, nil
}
