package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// designSchema is the part of an EntitySchemaDesignerService GetSchemaDesignItem answer that a package-layer
// column read needs. Localizable strings arrive as culture/value lists, not as objects.
type designSchema struct {
	Name    string `json:"name"`
	Package *struct {
		Name string `json:"name"`
	} `json:"package"`
	Columns          []designColumn `json:"columns"`
	InheritedColumns []designColumn `json:"inheritedColumns"`
}

type designLocalizableString struct {
	CultureName string `json:"cultureName"`
	Value       string `json:"value"`
}

type designColumn struct {
	Name                  string                    `json:"name"`
	Caption               []designLocalizableString `json:"caption"`
	Description           []designLocalizableString `json:"description"`
	DataValueType         *int                      `json:"type"`
	RequirementType       int                       `json:"requirementType"`
	UsageType             int                       `json:"usageType"`
	DefValue              *runtimeDefaultValue      `json:"defValue"`
	IsValueCloneable      bool                      `json:"isValueCloneable"`
	IsTrackChangesInDB    bool                      `json:"isTrackChangesInDB"`
	Indexed               bool                      `json:"indexed"`
	MultiLineText         bool                      `json:"isMultiLineText"`
	AccentInsensitive     bool                      `json:"isAccentInsensitive"`
	LocalizableText       bool                      `json:"isLocalizableText"`
	UseSeconds            bool                      `json:"useSeconds"`
	Masked                bool                      `json:"isMasked"`
	ValueMasked           bool                      `json:"isValueMasked"`
	FormatValidated       bool                      `json:"isFormatValidated"`
	SimpleLookup          bool                      `json:"isSimpleLookup"`
	Cascade               bool                      `json:"isCascade"`
	DoNotControlIntegrity bool                      `json:"doNotControlIntegrity"`
	ReferenceSchema       *struct {
		Name string `json:"name"`
	} `json:"referenceSchema"`
}

// designLocalizedText is clio's GetLocalizableValue for the en-US culture clio runs under: the exact
// culture, else the first value.
func designLocalizedText(values []designLocalizableString) *string {
	if len(values) == 0 {
		return nil
	}
	for _, value := range values {
		if strings.EqualFold(value.CultureName, "en-US") {
			return stringPointer(value.Value)
		}
	}
	return stringPointer(values[0].Value)
}

// packageColumnProperties reads one column of one package layer: the package by name from SysPackage, then
// the schema as that package sees it through GetSchemaDesignItem (useFullHierarchy=false, as clio sends).
// Own columns win over inherited ones, so source tells whether THIS package layer defines the column.
func (c *Client) packageColumnProperties(ctx context.Context, input EntitySchemaColumnPropertiesRequest) (EntitySchemaColumnProperties, error) {
	packageName := strings.TrimSpace(input.PackageName)
	packageRows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"UId": "UId", "Name": "Name"},
		map[string]any{"filter0": comparisonFilter("Name", packageName, 1, 3)}, 100))
	if err != nil {
		return EntitySchemaColumnProperties{}, err
	}
	packageUID := ""
	for _, row := range packageRows {
		if strings.EqualFold(rowString(row, "Name"), packageName) {
			packageUID = rowString(row, "UId")
			break
		}
	}
	if packageUID == "" {
		return EntitySchemaColumnProperties{}, fmt.Errorf("Package '%s' was not found.", input.PackageName)
	}
	schemaName := input.SchemaName
	body, err := json.Marshal(map[string]any{"name": schemaName, "packageUId": packageUID, "useFullHierarchy": false, "cultures": []string{}})
	if err != nil {
		return EntitySchemaColumnProperties{}, fmt.Errorf("encode GetSchemaDesignItem request: %w", err)
	}
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return EntitySchemaColumnProperties{}, fmt.Errorf("Entity schema '%s' is not available in package '%s': %w", schemaName, input.PackageName, err)
	}
	var response struct {
		Success   bool                    `json:"success"`
		Schema    *designSchema           `json:"schema"`
		ErrorInfo *runtimeSchemaErrorInfo `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return EntitySchemaColumnProperties{}, fmt.Errorf("GetSchemaDesignItem returned invalid JSON: %w", err)
	}
	if !response.Success || response.Schema == nil {
		reason := fmt.Sprintf("GetSchemaDesignItem returned no schema for '%s'.", schemaName)
		if response.ErrorInfo != nil && response.ErrorInfo.Message != "" {
			reason = response.ErrorInfo.Message
		}
		return EntitySchemaColumnProperties{}, fmt.Errorf("Entity schema '%s' is not available in package '%s': %s", schemaName, input.PackageName, reason)
	}
	schema := response.Schema
	column, source := findDesignColumn(schema, input.ColumnName)
	if column == nil {
		return EntitySchemaColumnProperties{}, fmt.Errorf("Column '%s' was not found in schema '%s'.", input.ColumnName, schema.Name)
	}
	var value json.RawMessage
	if column.DefValue != nil {
		value = column.DefValue.Value
	}
	config := newDefaultValueConfig(column.DefValue, value)
	flatValue := friendlyDefaultValue(config)
	var referenceSchema *string
	if column.ReferenceSchema != nil {
		referenceSchema = nonBlankOrNil(column.ReferenceSchema.Name)
	}
	config = c.enrichLookupConstDefault(ctx, config, referenceSchema)
	dataValueType := -1 // no type: clio reports a SystemValue caption as unsupported-type
	typeName := "<none>"
	if column.DataValueType != nil {
		dataValueType = *column.DataValueType
		typeName = friendlyDataValueType(dataValueType)
	}
	config = c.enrichSystemValueDefault(ctx, config, dataValueType)
	var defaultSource *string
	if config != nil {
		defaultSource = stringPointer(config.Source)
	}
	resultPackage := input.PackageName
	if schema.Package != nil && schema.Package.Name != "" {
		resultPackage = schema.Package.Name
	}
	trackChanges, doNotControlIntegrity, localizableText := column.IsTrackChangesInDB, column.DoNotControlIntegrity, column.LocalizableText
	return EntitySchemaColumnProperties{
		SchemaName: schema.Name, PackageName: resultPackage, ColumnName: column.Name, Source: source,
		Title: designLocalizedText(column.Caption), Description: designLocalizedText(column.Description),
		Type: typeName, Required: column.RequirementType != 0, Indexed: column.Indexed,
		Cloneable: column.IsValueCloneable, TrackChanges: &trackChanges, DefaultValueSource: defaultSource,
		DefaultValue: flatValue, ReferenceSchemaName: referenceSchema, SimpleLookup: column.SimpleLookup,
		Cascade: column.Cascade, DoNotControlIntegrity: &doNotControlIntegrity, MultilineText: column.MultiLineText,
		LocalizableText: &localizableText, AccentInsensitive: column.AccentInsensitive,
		Masked: column.ValueMasked || column.Masked, FormatValidated: column.FormatValidated,
		UseSeconds: column.UseSeconds, DefaultValueConfig: config, UsageType: friendlyUsageType(column.UsageType),
	}, nil
}

func findDesignColumn(schema *designSchema, name string) (*designColumn, string) {
	for index := range schema.Columns {
		if strings.EqualFold(schema.Columns[index].Name, name) {
			return &schema.Columns[index], "own"
		}
	}
	for index := range schema.InheritedColumns {
		if strings.EqualFold(schema.InheritedColumns[index].Name, name) {
			return &schema.InheritedColumns[index], "inherited"
		}
	}
	return nil, ""
}
