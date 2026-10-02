package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EntitySchemaColumnPropertiesRequest names one column. An empty PackageName reads the merged runtime
// schema across all packages; a package name reads that package layer through the schema designer.
type EntitySchemaColumnPropertiesRequest struct {
	PackageName string
	SchemaName  string
	ColumnName  string
}

// EntitySchemaColumnProperties is clio's get-entity-schema-column-properties shape. In merged mode the
// runtime endpoint does not expose track-changes, do-not-control-integrity or localizable-text, so those
// are null rather than a false that would be a guess.
type EntitySchemaColumnProperties struct {
	SchemaName            string              `json:"schema-name"`
	PackageName           string              `json:"package-name"`
	ColumnName            string              `json:"column-name"`
	Source                string              `json:"source"`
	Title                 *string             `json:"title"`
	Description           *string             `json:"description"`
	Type                  string              `json:"type"`
	Required              bool                `json:"required"`
	Indexed               bool                `json:"indexed"`
	Cloneable             bool                `json:"cloneable"`
	TrackChanges          *bool               `json:"track-changes"`
	DefaultValueSource    *string             `json:"default-value-source"`
	DefaultValue          *string             `json:"default-value"`
	ReferenceSchemaName   *string             `json:"reference-schema-name"`
	SimpleLookup          bool                `json:"simple-lookup"`
	Cascade               bool                `json:"cascade"`
	DoNotControlIntegrity *bool               `json:"do-not-control-integrity"`
	MultilineText         bool                `json:"multiline-text"`
	LocalizableText       *bool               `json:"localizable-text"`
	AccentInsensitive     bool                `json:"accent-insensitive"`
	Masked                bool                `json:"masked"`
	FormatValidated       bool                `json:"format-validated"`
	UseSeconds            bool                `json:"use-seconds"`
	DefaultValueConfig    *DefaultValueConfig `json:"default-value-config"`
	UsageType             *string             `json:"usage-type"`
}

// richRuntimeSchema is the part of a RuntimeEntitySchemaRequest answer that column-level reads need. It is
// kept apart from schema.go's payload, which carries only what get-entity-schema-properties maps.
type richRuntimeSchema struct {
	UID                      string          `json:"uId"`
	Name                     string          `json:"name"`
	Caption                  json.RawMessage `json:"caption"`
	IsVirtual                bool            `json:"isVirtual"`
	PrimaryDisplayColumnName string          `json:"primaryDisplayColumnName"`
	PrimaryDisplayColumnUID  string          `json:"primaryDisplayColumnUId"`
	Columns                  struct {
		Items map[string]richRuntimeColumn `json:"items"`
	} `json:"columns"`
}

type richRuntimeColumn struct {
	UID                 string               `json:"uId"`
	Name                string               `json:"name"`
	Caption             json.RawMessage      `json:"caption"`
	Description         json.RawMessage      `json:"description"`
	DataValueType       int                  `json:"dataValueType"`
	IsRequired          bool                 `json:"isRequired"`
	IsInherited         bool                 `json:"isInherited"`
	IsIndexed           bool                 `json:"isIndexed"`
	ReferenceSchemaName *string              `json:"referenceSchemaName"`
	IsValueCloneable    bool                 `json:"isValueCloneable"`
	DefValue            *runtimeDefaultValue `json:"defValue"`
	IsSimpleLookup      bool                 `json:"isSimpleLookup"`
	IsCascade           bool                 `json:"isCascade"`
	IsMultilineText     bool                 `json:"isMultilineText"`
	IsAccentInsensitive bool                 `json:"isAccentInsensitive"`
	IsValueMasked       bool                 `json:"isValueMasked"`
	IsMasked            bool                 `json:"isMasked"`
	IsFormatValidated   bool                 `json:"isFormatValidated"`
	UseSeconds          bool                 `json:"useSeconds"`
	UsageType           int                  `json:"usageType"`
}

// primaryDisplayColumn resolves the display column the way clio's runtime reader does: the explicit name,
// else the column whose UId matches primaryDisplayColumnUId.
func (schema *richRuntimeSchema) primaryDisplayColumn() string {
	if strings.TrimSpace(schema.PrimaryDisplayColumnName) != "" {
		return schema.PrimaryDisplayColumnName
	}
	if schema.PrimaryDisplayColumnUID == "" {
		return ""
	}
	for _, column := range schema.Columns.Items {
		if strings.EqualFold(column.UID, schema.PrimaryDisplayColumnUID) {
			return column.Name
		}
	}
	return ""
}

// readRichRuntimeSchema posts one RuntimeEntitySchemaRequest (by Name or by uId) and returns the schema, or
// Creatio's own error text, or notReturned when Creatio answered without a schema and without a reason.
func (c *Client) readRichRuntimeSchema(ctx context.Context, request any, notReturned string) (*richRuntimeSchema, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode runtime schema request: %w", err)
	}
	payload, err := c.postDataServiceJSON(ctx, "RuntimeEntitySchemaRequest", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var response struct {
		Success   bool                    `json:"success"`
		Schema    *richRuntimeSchema      `json:"schema"`
		ErrorInfo *runtimeSchemaErrorInfo `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("RuntimeEntitySchemaRequest returned invalid JSON: %w", err)
	}
	if !response.Success || response.Schema == nil {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != "" {
			return nil, errors.New(response.ErrorInfo.Message)
		}
		return nil, errors.New(notReturned)
	}
	return response.Schema, nil
}

var usageTypeNames = map[int]string{0: "General", 1: "Advanced", 2: "None"}

// GetEntitySchemaColumnProperties mirrors clio's RemoteEntitySchemaColumnManager.GetColumnProperties.
func (c *Client) GetEntitySchemaColumnProperties(ctx context.Context, input EntitySchemaColumnPropertiesRequest) (EntitySchemaColumnProperties, error) {
	if strings.TrimSpace(input.SchemaName) == "" {
		return EntitySchemaColumnProperties{}, errors.New("schema-name is required.")
	}
	if strings.TrimSpace(input.ColumnName) == "" {
		return EntitySchemaColumnProperties{}, errors.New("column-name is required.")
	}
	if strings.TrimSpace(input.PackageName) != "" {
		return c.packageColumnProperties(ctx, input)
	}
	return c.mergedColumnProperties(ctx, input)
}

func (c *Client) mergedColumnProperties(ctx context.Context, input EntitySchemaColumnPropertiesRequest) (EntitySchemaColumnProperties, error) {
	schemaName := strings.TrimSpace(input.SchemaName)
	schema, err := c.readRichRuntimeSchema(ctx, map[string]string{"Name": schemaName},
		fmt.Sprintf("Runtime schema '%s' was not returned by Creatio.", schemaName))
	if err != nil {
		return EntitySchemaColumnProperties{}, err
	}
	name := schema.Name
	if strings.TrimSpace(name) == "" {
		name = schemaName
	}
	var column *richRuntimeColumn
	for key := range schema.Columns.Items {
		candidate := schema.Columns.Items[key]
		if strings.EqualFold(candidate.Name, strings.TrimSpace(input.ColumnName)) {
			column = &candidate
			break
		}
	}
	if column == nil {
		return EntitySchemaColumnProperties{}, fmt.Errorf("Column '%s' was not found in merged schema '%s'.", input.ColumnName, name)
	}
	if column.DefValue != nil && column.DefValue.ValueSourceType == defaultSourceConst && !isJSONScalar(column.DefValue.Value) {
		return EntitySchemaColumnProperties{}, errors.New("designer default value must be a scalar JSON value.")
	}
	var value json.RawMessage
	if column.DefValue != nil {
		value = column.DefValue.Value
	}
	config := newDefaultValueConfig(column.DefValue, value)
	flatValue := friendlyDefaultValue(config)
	config = c.enrichLookupConstDefault(ctx, config, column.ReferenceSchemaName)
	config = c.enrichSystemValueDefault(ctx, config, column.DataValueType)
	var source *string
	if config != nil {
		source = stringPointer(config.Source)
	}
	sourceName := "own"
	if column.IsInherited {
		sourceName = "inherited"
	}
	return EntitySchemaColumnProperties{
		SchemaName: name, PackageName: mergedSchemaPackageName, ColumnName: column.Name, Source: sourceName,
		Title: runtimeLocalizedText(column.Caption), Description: runtimeLocalizedText(column.Description),
		Type: friendlyDataValueType(column.DataValueType), Required: column.IsRequired, Indexed: column.IsIndexed,
		Cloneable: column.IsValueCloneable, DefaultValueSource: source, DefaultValue: flatValue,
		ReferenceSchemaName: column.ReferenceSchemaName, SimpleLookup: column.IsSimpleLookup, Cascade: column.IsCascade,
		MultilineText: column.IsMultilineText, AccentInsensitive: column.IsAccentInsensitive,
		Masked: column.IsValueMasked || column.IsMasked, FormatValidated: column.IsFormatValidated,
		UseSeconds: column.UseSeconds, DefaultValueConfig: config, UsageType: friendlyUsageType(column.UsageType),
	}, nil
}

func friendlyUsageType(ordinal int) *string {
	if name, ok := usageTypeNames[ordinal]; ok {
		return &name
	}
	return stringPointer(fmt.Sprintf("%d", ordinal))
}

// Markers clio puts in record-resolution when a lookup Const default's display value cannot be read.
const (
	recordResolutionNoAccess            = "no-access"
	recordResolutionNotFound            = "not-found-or-no-access"
	recordResolutionNoDisplayColumn     = "display-column-unavailable"
	selectQueryServerFailurePrefix      = "SelectQuery failed: " // selectRows' wording for success=false
	systemValuesRoute                   = "ServiceModel/EntitySchemaDesignerService.svc/GetSystemValues"
	systemValueSourceInvalid            = "invalid-source"
	systemValueSourceUnsupportedType    = "unsupported-type"
	systemValueSourceNotFoundForType    = "not-found-for-type"
	systemValueSourceCaptionMissing     = "caption-unavailable"
	systemValueSourceCatalogUnavailable = "catalog-unavailable"
)

// enrichLookupConstDefault adds the referenced record's display value to a lookup Const default. It is
// fail-soft like clio: a resolution that yields nothing returns the config unchanged, and no failure here
// can fail the column read.
func (c *Client) enrichLookupConstDefault(ctx context.Context, config *DefaultValueConfig, referenceSchema *string) *DefaultValueConfig {
	if config == nil || config.Source != "Const" || referenceSchema == nil || strings.TrimSpace(*referenceSchema) == "" {
		return config
	}
	recordID := rawJSONText(config.Value)
	if !isGUIDText(recordID) || isEmptyGUID(recordID) {
		return config
	}
	display, marker := c.resolveLookupDisplayValue(ctx, strings.TrimSpace(*referenceSchema), normalizeGUIDText(recordID))
	if display == nil && marker == nil {
		return config
	}
	enriched := *config
	enriched.DisplayValue, enriched.RecordResolution = display, marker
	return &enriched
}

func (c *Client) resolveLookupDisplayValue(ctx context.Context, schemaName, recordID string) (*string, *string) {
	reference, err := c.readRichRuntimeSchema(ctx, map[string]string{"Name": schemaName},
		fmt.Sprintf("Runtime schema '%s' was not returned by Creatio.", schemaName))
	displayColumn := ""
	if err == nil {
		displayColumn = reference.primaryDisplayColumn()
	}
	if strings.TrimSpace(displayColumn) == "" {
		return nil, stringPointer(recordResolutionNoDisplayColumn)
	}
	rows, err := c.selectRows(ctx, buildSelectQuery(schemaName, map[string]string{"DisplayValue": displayColumn},
		map[string]any{"filter0": comparisonFilter("Id", recordID, 0, 3)}, 1))
	if err != nil {
		message := err.Error()
		if !strings.HasPrefix(message, selectQueryServerFailurePrefix) {
			// Transport, timeout or a non-JSON body says nothing about the record: GUID only, no marker.
			return nil, nil
		}
		if isAccessDeniedText(message) {
			return nil, stringPointer(recordResolutionNoAccess)
		}
		return nil, stringPointer(recordResolutionNotFound)
	}
	if len(rows) == 0 {
		return nil, stringPointer(recordResolutionNotFound)
	}
	return nonBlankOrNil(rowString(rows[0], "DisplayValue")), nil
}

func isAccessDeniedText(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{"does not have permission", "securityexception", "not have rights", "access denied"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// systemValueTypeUIDs maps runtime data value types to the DataValueType UIds GetSystemValues expects:
// clio's RuntimeDataValueTypeUIdMap plus the Date, Time, ImageLookup and Color types it adds for reads.
var systemValueTypeUIDs = map[int]string{
	0: "23018567-a13c-4320-8687-fd6f9e3699bd", 1: "8b3f29bb-ea14-4ce5-a5c5-293a929b6ba2",
	4: "6b6b74e2-820d-490e-a017-2b73d4ccf2b0", 6: "969093e2-2b4e-463b-883a-3d3b8c61f0cd",
	7: "d21e9ef4-c064-4012-b286-fa1a8171da44", 8: "603d4960-a1a2-45e9-b232-206a54421b01",
	9: "04cc757b-8f06-482c-8a1a-0c0e171d2410", 10: "b295071f-7ea9-4e62-8d1a-919bf3732ff2",
	12: "90b65bf8-0ffc-4141-8779-2420877af907", 16: "b039feb0-ee7c-4884-8aa6-d6d45d84316f",
	18: "dafb71f9-ee9f-4e0b-a4d7-37aa15987155", 24: "3509b9dd-2c90-4540-b82e-8f6ae85d8248",
	27: "325a73b8-0f47-44a0-8412-7606f78003ac", 28: "ddb3a1ee-07e8-4d62-b7a9-d0e618b00fbd",
	29: "c0f04627-4620-4bc0-84e5-9419dc8516b1", 30: "5ca35f10-a101-4c67-a96a-383da6afacfc",
	31: "07ba84ce-0bf7-44b4-9f2c-7b15032eb98c", 32: "5cc8060d-6d10-4773-89fc-8c12d6f659a6",
	33: "3f62414e-6c25-4182-bcef-a73c9e396f31", 34: "ff22e049-4d16-46ee-a529-92d8808932dc",
	40: "a4aaf398-3531-4a0d-9d75-a587f5b5b59e", 42: "26cba63c-daf1-4f36-b2ea-73c0d675d90c",
	43: "79bccffa-8c8b-4863-b376-a69d2244182b", 44: "26cba64c-daf1-4f36-b2ea-73c0d695d90c",
	45: "66cba64c-daf1-4f36-b8ea-73c0d695d90c", 47: "57ee4c31-5ec4-45fa-b95d-3a2868aa89a8",
	48: "969093e2-2b4e-463b-883a-3d3b8c61f0cd", 49: "969093e2-2b4e-463b-883a-3d3b8c61f0cd",
	50: "969093e2-2b4e-463b-883a-3d3b8c61f0cd",
}

// enrichSystemValueDefault adds the native caption of a SystemValue default ("Current user contact") from
// the designer's system-value catalog, or the reason it is unavailable. The stored GUID is never changed.
func (c *Client) enrichSystemValueDefault(ctx context.Context, config *DefaultValueConfig, dataValueType int) *DefaultValueConfig {
	if config == nil || config.Source != "SystemValue" {
		return config
	}
	withSource := func(display *string, resolution string) *DefaultValueConfig {
		enriched := *config
		enriched.DisplayValue, enriched.SourceResolution = display, nil
		if resolution != "" {
			enriched.SourceResolution = stringPointer(resolution)
		}
		return &enriched
	}
	selector := ""
	if config.ValueSource != nil {
		selector = *config.ValueSource
	}
	if !isGUIDText(selector) || isEmptyGUID(selector) {
		return withSource(nil, systemValueSourceInvalid)
	}
	typeUID, ok := systemValueTypeUIDs[dataValueType]
	if !ok {
		return withSource(nil, systemValueSourceUnsupportedType)
	}
	body, _ := json.Marshal(map[string]string{"dataValueTypeUId": typeUID})
	payload, err := c.postCreatioServiceJSON(ctx, systemValuesRoute, body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return withSource(nil, systemValueSourceCatalogUnavailable)
	}
	var response struct {
		Success bool `json:"success"`
		Items   []struct {
			Value        string `json:"value"`
			DisplayValue string `json:"displayValue"`
		} `json:"items"`
	}
	if json.Unmarshal(payload, &response) != nil || !response.Success {
		return withSource(nil, systemValueSourceCatalogUnavailable)
	}
	wanted := normalizeGUIDText(selector)
	matches := 0
	display := ""
	for _, item := range response.Items {
		if isGUIDText(item.Value) && normalizeGUIDText(item.Value) == wanted {
			matches++
			display = item.DisplayValue
		}
	}
	switch {
	case matches > 1:
		// clio's SingleOrDefault throws on a duplicate, which it reports as an unavailable catalog.
		return withSource(nil, systemValueSourceCatalogUnavailable)
	case matches == 0:
		return withSource(nil, systemValueSourceNotFoundForType)
	case strings.TrimSpace(display) == "":
		return withSource(nil, systemValueSourceCaptionMissing)
	default:
		return withSource(&display, "")
	}
}

// normalizeGUIDText reduces any accepted GUID spelling to lower-case 8-4-4-4-12 form.
func normalizeGUIDText(value string) string {
	hex := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			return r
		case r >= 'A' && r <= 'F':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, value)
	if len(hex) != 32 {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

func isEmptyGUID(value string) bool {
	return normalizeGUIDText(value) == "00000000-0000-0000-0000-000000000000"
}
