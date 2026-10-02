package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AppInfoRequest identifies one installed application by id or by code; exactly one must be supplied.
type AppInfoRequest struct {
	ID   string
	Code string
}

// AppInfoResponse is clio's get-app-info envelope (ApplicationContextResponse) for a read.
type AppInfoResponse struct {
	Success                 bool            `json:"success"`
	PackageUID              string          `json:"package-u-id,omitempty"`
	PackageName             string          `json:"package-name,omitempty"`
	CanonicalMainEntityName string          `json:"canonical-main-entity-name,omitempty"`
	ApplicationID           string          `json:"application-id,omitempty"`
	ApplicationName         string          `json:"application-name,omitempty"`
	ApplicationCode         string          `json:"application-code,omitempty"`
	ApplicationVersion      *string         `json:"application-version,omitempty"`
	Entities                []AppEntityInfo `json:"entities,omitzero"` // [] on an empty success, absent on failure
	Pages                   []PageListItem  `json:"pages,omitzero"`
	SchemaNamePrefix        *string         `json:"schema-name-prefix,omitempty"`
	Error                   string          `json:"error,omitempty"`
}

// AppEntityInfo is one entity of the application's primary package with its own (not inherited) columns.
type AppEntityInfo struct {
	UID     string          `json:"u-id"`
	Name    string          `json:"name"`
	Caption string          `json:"caption"`
	Columns []AppColumnInfo `json:"columns"`
	Virtual bool            `json:"virtual"`
}

// AppColumnInfo uses the sync-schemas write vocabulary, so a column can be sent back with an action verb.
// data-value-type and reference-schema are clio's legacy aliases of type and reference-schema-name.
type AppColumnInfo struct {
	Name                string              `json:"name"`
	Caption             string              `json:"caption"`
	DataValueType       string              `json:"data-value-type"`
	ReferenceSchema     *string             `json:"reference-schema,omitempty"`
	DefaultValueConfig  *DefaultValueConfig `json:"default-value-config,omitempty"`
	Required            bool                `json:"required"`
	Type                string              `json:"type"`
	ReferenceSchemaName *string             `json:"reference-schema-name,omitempty"`
}

// dataValueTypeDisplayNames is clio's CreatioDataValueType display spelling. get-app-info ships these
// platform client-enum names (SHORT_TEXT, FLOAT1), unlike the friendly names the schema tools use.
var dataValueTypeDisplayNames = map[int]string{
	0: "Guid", 1: "Text", 4: "Integer", 5: "Float", 6: "Money", 7: "DateTime", 8: "Date", 9: "Time",
	10: "Lookup", 11: "Enum", 12: "Boolean", 13: "Blob", 14: "Image", 15: "CUSTOM_OBJECT", 16: "IMAGELOOKUP",
	17: "COLLECTION", 18: "Color", 19: "LOCALIZABLE_STRING", 20: "ENTITY", 21: "ENTITY_COLLECTION",
	22: "ENTITY_COLUMN_MAPPING_COLLECTION", 23: "HASH_TEXT", 24: "SECURE_TEXT", 25: "FILE", 26: "MAPPING",
	27: "SHORT_TEXT", 28: "MEDIUM_TEXT", 29: "MAXSIZE_TEXT", 30: "LONG_TEXT", 31: "FLOAT1", 32: "FLOAT2",
	33: "FLOAT3", 34: "FLOAT4", 35: "LOCALIZABLE_PARAMETER_VALUES_LIST", 36: "METADATA_TEXT",
	37: "STAGE_INDICATOR", 38: "OBJECT_LIST", 39: "COMPOSITE_OBJECT_LIST", 40: "FLOAT8", 41: "FILE_LOCATOR",
	42: "PHONE_TEXT", 43: "RICH_TEXT", 44: "WEB_TEXT", 45: "EMAIL_TEXT", 46: "COMPOSITE_OBJECT",
	47: "FLOAT0", 48: "MONEY0", 49: "MONEY1", 50: "MONEY3",
}

func dataValueTypeDisplayName(value int) string {
	if name, ok := dataValueTypeDisplayNames[value]; ok {
		return name
	}
	return strconv.Itoa(value)
}

const (
	appInfoRowCount   = 10000
	baseObjectCaption = "Base object"
	// allEmployeesAdminUnitID is the "All employees" role: the SysSettingsValue row clio falls back to.
	allEmployeesAdminUnitID = "a29a3ba5-4b0d-de11-9a51-005056c00008"
)

// GetAppInfo mirrors clio's ApplicationInfoService: the installed application, its primary package, the
// package's entities (runtime schema by UId, own columns only), the package's Freedom UI pages and the
// SchemaNamePrefix setting. Every failure is reported inside the envelope, as clio's tool does.
func (c *Client) GetAppInfo(ctx context.Context, input AppInfoRequest) AppInfoResponse {
	id, code := strings.TrimSpace(input.ID), strings.TrimSpace(input.Code)
	if (id == "") == (code == "") {
		return AppInfoResponse{Error: "Provide exactly one identifier: id or code."}
	}
	result, err := c.appInfo(ctx, id, code)
	if err != nil {
		return AppInfoResponse{Error: err.Error()}
	}
	return result
}

func (c *Client) appInfo(ctx context.Context, id, code string) (AppInfoResponse, error) {
	filters := map[string]any{}
	if id != "" {
		filters["filter0"] = comparisonFilter("Id", id, 0, 3)
	} else {
		filters["filter0"] = comparisonFilter("Code", code, 1, 3)
	}
	appRows, err := c.selectRows(ctx, buildSelectQuery("SysInstalledApp", map[string]string{
		"Id": "Id", "Code": "Code", "Name": "Name", "Version": "Version",
	}, filters, appInfoRowCount))
	if err != nil {
		return AppInfoResponse{}, err
	}
	if len(appRows) == 0 {
		return AppInfoResponse{}, fmt.Errorf("Application '%s' not found.", firstNonEmpty(id, code))
	}
	app := appRows[0]
	appID, appName := rowString(app, "Id"), rowString(app, "Name")
	packageUID, packageName, err := c.primaryApplicationPackage(ctx, appID)
	if err != nil {
		return AppInfoResponse{}, err
	}
	entityRows, err := c.selectRows(ctx, buildSelectQuery("ApplicationEntity", map[string]string{
		"UId": "UId", "Name": "Name", "Caption": "Caption",
	}, map[string]any{
		"filter0": comparisonFilter("Application", appID, 0, 3),
		"filter1": comparisonFilter("Package", packageUID, 0, 3),
	}, appInfoRowCount))
	if err != nil {
		return AppInfoResponse{}, err
	}
	seen := map[string]bool{}
	entities := make([]AppEntityInfo, 0, len(entityRows))
	for _, row := range entityRows {
		uid := rowString(row, "UId")
		if seen[strings.ToLower(uid)] {
			continue
		}
		seen[strings.ToLower(uid)] = true
		entity, err := c.appEntityInfo(ctx, packageUID, packageName, appName, uid, rowStringPointer(row, "Name"), rowString(row, "Caption"))
		if err != nil {
			return AppInfoResponse{}, err
		}
		entities = append(entities, entity)
	}
	sort.SliceStable(entities, func(i, j int) bool {
		if order := compareOrdinalIgnoreCase(entities[i].Caption, entities[j].Caption); order != 0 {
			return order < 0
		}
		return compareOrdinalIgnoreCase(entities[i].Name, entities[j].Name) < 0
	})
	pages, err := c.appPrimaryPackagePages(ctx, packageName)
	if err != nil {
		return AppInfoResponse{}, err
	}
	prefix, err := c.schemaNamePrefix(ctx)
	if err != nil {
		return AppInfoResponse{}, err
	}
	canonical := ""
	for _, entity := range entities {
		if strings.EqualFold(entity.Name, packageName) {
			canonical = entity.Name
			break
		}
	}
	return AppInfoResponse{
		Success: true, PackageUID: packageUID, PackageName: packageName, CanonicalMainEntityName: canonical,
		ApplicationID: appID, ApplicationName: appName, ApplicationCode: rowString(app, "Code"),
		ApplicationVersion: rowStringPointer(app, "Version"), Entities: entities, Pages: pages,
		SchemaNamePrefix: &prefix,
	}, nil
}

// primaryApplicationPackage returns the UId and name of the package ApplicationPackagesService marks as the
// application's primary package. catalog.go's resolver returns the name only.
func (c *Client) primaryApplicationPackage(ctx context.Context, appID string) (string, string, error) {
	appIDJSON, _ := json.Marshal(appID)
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ApplicationPackagesService.svc/GetApplicationPackages", appIDJSON, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", "", err
	}
	var response struct {
		Success  bool `json:"success"`
		Packages []struct {
			UID     string `json:"uId"`
			Name    string `json:"name"`
			Primary bool   `json:"isApplicationPrimaryPackage"`
		} `json:"packages"`
		ErrorInfo *runtimeSchemaErrorInfo `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", "", fmt.Errorf("GetApplicationPackages returned invalid JSON: %w", err)
	}
	if !response.Success {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != "" {
			return "", "", errors.New(response.ErrorInfo.Message)
		}
		return "", "", errors.New("Failed to load application packages.")
	}
	for _, pkg := range response.Packages {
		if pkg.Primary {
			return pkg.UID, pkg.Name, nil
		}
	}
	return "", "", errors.New("Primary package not found in response.")
}

// appEntityInfo reads one application entity by UId and keeps its own columns. The design-time schema is
// read only when a runtime caption is missing (or is the "Base object" placeholder of the canonical main
// entity): it is clio's caption fallback and contributes nothing else.
func (c *Client) appEntityInfo(ctx context.Context, packageUID, packageName, appName, uid string, rowName *string, rowCaption string) (AppEntityInfo, error) {
	schema, err := c.readRichRuntimeSchema(ctx, map[string]string{"uId": uid}, fmt.Sprintf("Runtime entity schema '%s' was not returned.", uid))
	if err != nil {
		return AppEntityInfo{}, err
	}
	name := schema.Name
	if strings.TrimSpace(name) == "" {
		if rowName == nil {
			return AppEntityInfo{}, errors.New("Entity name was not returned.")
		}
		name = *rowName
	}
	design := lazyDesignCaptions{load: func() *appDesignSchema { return c.appDesignSchema(ctx, packageUID, name) }}
	columns := make([]AppColumnInfo, 0, len(schema.Columns.Items))
	for _, column := range schema.Columns.Items {
		if column.IsInherited {
			continue
		}
		if strings.TrimSpace(column.Name) == "" {
			return AppEntityInfo{}, errors.New("Runtime schema column name was not returned.")
		}
		caption := preferredRuntimeCaption(column.Caption)
		if caption == "" {
			caption = firstNonBlankTrimmed(design.column(column.Name), column.Name)
		}
		columns = append(columns, AppColumnInfo{
			Name: column.Name, Caption: caption, DataValueType: dataValueTypeDisplayName(column.DataValueType),
			ReferenceSchema: column.ReferenceSchemaName, DefaultValueConfig: appDefaultValueConfig(column.DefValue),
			Required: column.IsRequired, Type: dataValueTypeDisplayName(column.DataValueType),
			ReferenceSchemaName: column.ReferenceSchemaName,
		})
	}
	sort.SliceStable(columns, func(i, j int) bool { return compareOrdinalIgnoreCase(columns[i].Name, columns[j].Name) < 0 })
	return AppEntityInfo{
		UID: uid, Name: name, Columns: columns, Virtual: schema.IsVirtual,
		Caption: appEntityCaption(strings.EqualFold(name, packageName), preferredRuntimeCaption(schema.Caption), &design,
			appName, rowCaption, name),
	}, nil
}

// appEntityCaption is clio's ResolveEntityCaption. The canonical main entity often reads back from runtime
// as "Base object"; then the design caption, or else the application name, is the meaningful one.
func appEntityCaption(canonical bool, runtimeCaption string, design *lazyDesignCaptions, fallbacks ...string) string {
	if canonical && strings.EqualFold(runtimeCaption, baseObjectCaption) {
		designCaption := design.schema()
		if designCaption != "" && !strings.EqualFold(runtimeCaption, designCaption) {
			return designCaption
		}
		if designCaption == "" && len(fallbacks) > 0 && strings.TrimSpace(fallbacks[0]) != "" &&
			!strings.EqualFold(strings.TrimSpace(fallbacks[0]), baseObjectCaption) {
			return strings.TrimSpace(fallbacks[0])
		}
	}
	if runtimeCaption != "" {
		return runtimeCaption
	}
	if designCaption := design.schema(); designCaption != "" {
		return designCaption
	}
	return firstNonBlankTrimmed(fallbacks...)
}

// preferredRuntimeCaption is clio's GetRuntimeLocalizedText for en-US: the trimmed en-US value, else the
// first non-blank value in server order, else "".
func preferredRuntimeCaption(value json.RawMessage) string {
	entries := orderedLocalizedEntries(value)
	var text string
	for _, entry := range entries {
		if entry.culture == "en-US" && json.Unmarshal(entry.value, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	for _, entry := range entries {
		if json.Unmarshal(entry.value, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func firstNonBlankTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// appDefaultValueConfig converts the runtime default the way ApplicationInfoService does before projecting
// it: a non-scalar Const value becomes its JSON text, unlike the column-properties read that rejects it.
func appDefaultValueConfig(defValue *runtimeDefaultValue) *DefaultValueConfig {
	if defValue == nil {
		return nil
	}
	value := defValue.Value
	if !isJSONScalar(value) {
		value, _ = json.Marshal(string(value))
	}
	return newDefaultValueConfig(defValue, value)
}

type appDesignSchema struct {
	Caption []designLocalizableString `json:"caption"`
	Columns []struct {
		Name    string                    `json:"name"`
		Caption []designLocalizableString `json:"caption"`
	} `json:"columns"`
}

// appDesignSchema reads the design-time schema of one entity in the primary package. Any failure yields nil:
// clio treats this read as optional.
func (c *Client) appDesignSchema(ctx context.Context, packageUID, entityName string) *appDesignSchema {
	if !isGUIDText(packageUID) {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"name": entityName, "packageUId": normalizeGUIDText(packageUID), "useFullHierarchy": true, "cultures": []string{"en-US"},
	})
	if err != nil {
		return nil
	}
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil
	}
	var response struct {
		Success bool             `json:"success"`
		Schema  *appDesignSchema `json:"schema"`
	}
	if json.Unmarshal(payload, &response) != nil || !response.Success {
		return nil
	}
	return response.Schema
}

// lazyDesignCaptions loads the design schema at most once, and only if a caption needs it.
type lazyDesignCaptions struct {
	load   func() *appDesignSchema
	loaded bool
	value  *appDesignSchema
}

func (design *lazyDesignCaptions) get() *appDesignSchema {
	if !design.loaded {
		design.value, design.loaded = design.load(), true
	}
	return design.value
}

func (design *lazyDesignCaptions) schema() string {
	if schema := design.get(); schema != nil {
		return designCaptionText(schema.Caption)
	}
	return ""
}

func (design *lazyDesignCaptions) column(name string) string {
	schema := design.get()
	if schema == nil {
		return ""
	}
	for _, column := range schema.Columns {
		if strings.TrimSpace(column.Name) != "" && strings.EqualFold(column.Name, name) {
			return designCaptionText(column.Caption)
		}
	}
	return ""
}

// designCaptionText is clio's GetDesignLocalizedText: blank entries ignored, en-US preferred, else the first.
func designCaptionText(values []designLocalizableString) string {
	var first *designLocalizableString
	for index := range values {
		value := &values[index]
		if strings.TrimSpace(value.CultureName) == "" || strings.TrimSpace(value.Value) == "" {
			continue
		}
		if strings.EqualFold(value.CultureName, "en-US") {
			return strings.TrimSpace(value.Value)
		}
		if first == nil {
			first = value
		}
	}
	if first == nil {
		return ""
	}
	return strings.TrimSpace(first.Value)
}

// appPrimaryPackagePages lists the primary package's Freedom UI (ClientUnit) schemas by schema name.
func (c *Client) appPrimaryPackagePages(ctx context.Context, packageName string) ([]PageListItem, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{
		"Name": "Name", "UId": "UId", "PackageName": "SysPackage.Name", "ParentSchemaName": "[SysSchema:Id:Parent].Name",
	}, map[string]any{
		"filter0": comparisonFilter("ManagerName", "ClientUnitSchemaManager", 1, 3),
		"filter1": comparisonFilter("SysPackage.Name", packageName, 1, 3),
	}, appInfoRowCount))
	if err != nil {
		return nil, err
	}
	pages := make([]PageListItem, 0, len(rows))
	for _, row := range rows {
		name := rowString(row, "Name")
		if strings.TrimSpace(name) == "" {
			continue
		}
		pages = append(pages, PageListItem{SchemaName: name, UID: rowString(row, "UId"),
			PackageName: rowString(row, "PackageName"), ParentSchemaName: rowString(row, "ParentSchemaName")})
	}
	sort.SliceStable(pages, func(i, j int) bool { return compareOrdinalIgnoreCase(pages[i].SchemaName, pages[j].SchemaName) < 0 })
	return pages, nil
}

// schemaNamePrefix reads the SchemaNamePrefix setting for "All employees" over DataService, which is clio's
// own fallback when cliogate cannot answer. Quotes around a legacy value are stripped; unset reads as "".
func (c *Client) schemaNamePrefix(ctx context.Context) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSettingsValue", map[string]string{"TextValue": "TextValue"}, map[string]any{
		"filter0": comparisonFilter("SysSettings.Code", "SchemaNamePrefix", 1, 3),
		"filter1": comparisonFilter("SysAdminUnit", allEmployeesAdminUnitID, 0, 3),
	}, 1))
	if err != nil {
		return "", fmt.Errorf("read SchemaNamePrefix: %w", err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(rowString(rows[0], "TextValue")), `"`)), nil
}
