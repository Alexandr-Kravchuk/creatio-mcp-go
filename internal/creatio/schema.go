package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const mergedSchemaPackageName = "(merged: all packages)"

var friendlyDataValueTypes = map[int]string{
	0: "Guid", 1: "Text", 4: "Integer", 6: "Currency2", 7: "DateTime", 10: "Lookup", 12: "Boolean",
	13: "Binary", 14: "Image", 16: "ImageLookup", 18: "Color", 24: "SecureText", 25: "File",
	27: "ShortText", 28: "MediumText", 29: "MaxSizeText", 30: "LongText", 31: "Decimal1",
	32: "Float", 33: "Decimal3", 34: "Decimal4", 40: "Decimal8", 42: "PhoneNumber",
	43: "RichText", 44: "WebLink", 45: "Email", 47: "Decimal0", 48: "Currency0", 49: "Currency1", 50: "Currency3",
}

type EntitySchemaPropertiesRequest struct {
	SchemaName   string
	RequiredOnly bool
}

type EntitySchemaProperties struct {
	Name                      string                       `json:"name"`
	Title                     *string                      `json:"title"`
	Description               *string                      `json:"description"`
	PackageName               string                       `json:"package-name"`
	ParentSchemaName          *string                      `json:"parent-schema-name"`
	ExtendParent              bool                         `json:"extend-parent"`
	PrimaryColumnName         *string                      `json:"primary-column-name"`
	PrimaryDisplayColumnName  *string                      `json:"primary-display-column-name"`
	OwnColumnCount            int                          `json:"own-column-count"`
	InheritedColumnCount      int                          `json:"inherited-column-count"`
	IndexesCount              *int                         `json:"indexes-count"`
	TrackChangesInDB          bool                         `json:"track-changes-in-db"`
	DBView                    bool                         `json:"db-view"`
	SSPAvailable              *bool                        `json:"ssp-available"`
	Virtual                   bool                         `json:"virtual"`
	UseRecordDeactivation     *bool                        `json:"use-record-deactivation"`
	ShowInAdvancedMode        bool                         `json:"show-in-advanced-mode"`
	AdministratedByOperations bool                         `json:"administrated-by-operations"`
	AdministratedByColumns    bool                         `json:"administrated-by-columns"`
	AdministratedByRecords    bool                         `json:"administrated-by-records"`
	UseDenyRecordRights       *bool                        `json:"use-deny-record-rights"`
	UseLiveEditing            *bool                        `json:"use-live-editing"`
	Columns                   []EntitySchemaPropertyColumn `json:"columns"`
}

type EntitySchemaPropertyColumn struct {
	Name                string  `json:"name"`
	UID                 string  `json:"u-id"`
	Source              string  `json:"source"`
	Title               *string `json:"title"`
	Description         *string `json:"description"`
	Type                string  `json:"type"`
	Required            bool    `json:"required"`
	Indexed             bool    `json:"indexed"`
	ReferenceSchemaName *string `json:"reference-schema-name"`
}

type runtimeSchemaResponse struct {
	Success   bool                    `json:"success"`
	Schema    *runtimeSchemaPayload   `json:"schema"`
	ErrorInfo *runtimeSchemaErrorInfo `json:"errorInfo"`
}

type runtimeSchemaErrorInfo struct {
	Message string `json:"message"`
}

type runtimeSchemaPayload struct {
	Columns                   runtimeSchemaColumns `json:"columns"`
	PrimaryColumnUID          string               `json:"primaryColumnUId"`
	PrimaryDisplayColumnName  string               `json:"primaryDisplayColumnName"`
	PrimaryDisplayColumnUID   string               `json:"primaryDisplayColumnUId"`
	UID                       string               `json:"uId"`
	Name                      string               `json:"name"`
	Caption                   json.RawMessage      `json:"caption"`
	Description               json.RawMessage      `json:"description"`
	ParentUID                 string               `json:"parentUId"`
	ExtendParent              bool                 `json:"extendParent"`
	IsDBView                  bool                 `json:"isDBView"`
	IsTrackChangesInDB        bool                 `json:"isTrackChangesInDB"`
	IsVirtual                 bool                 `json:"isVirtual"`
	ShowInAdvancedMode        bool                 `json:"showInAdvancedMode"`
	AdministratedByOperations bool                 `json:"administratedByOperations"`
	AdministratedByColumns    bool                 `json:"administratedByColumns"`
	AdministratedByRecords    bool                 `json:"administratedByRecords"`
}

type runtimeSchemaColumns struct {
	Items map[string]runtimeSchemaColumn `json:"items"`
}

type runtimeSchemaColumn struct {
	UID                 string          `json:"uId"`
	Name                string          `json:"name"`
	Caption             json.RawMessage `json:"caption"`
	Description         json.RawMessage `json:"description"`
	DataValueType       int             `json:"dataValueType"`
	Required            bool            `json:"isRequired"`
	Inherited           bool            `json:"isInherited"`
	Indexed             bool            `json:"isIndexed"`
	ReferenceSchemaName *string         `json:"referenceSchemaName"`
}

// GetEntitySchemaProperties reads the effective schema snapshot through Creatio's native
// RuntimeEntitySchemaRequest endpoint. It intentionally implements the merged all-packages view;
// package-scoped designer reads are not approximated.
func (c *Client) GetEntitySchemaProperties(ctx context.Context, input EntitySchemaPropertiesRequest) (EntitySchemaProperties, error) {
	if strings.TrimSpace(input.SchemaName) == "" {
		return EntitySchemaProperties{}, fmt.Errorf("schema-name is required")
	}
	requestBody, err := json.Marshal(struct {
		Name string `json:"Name"`
	}{Name: strings.TrimSpace(input.SchemaName)})
	if err != nil {
		return EntitySchemaProperties{}, fmt.Errorf("encode runtime schema request: %w", err)
	}
	responseBody, err := c.postDataServiceJSON(ctx, "RuntimeEntitySchemaRequest", requestBody, 45*time.Second, maxResponseBytes)
	if err != nil {
		return EntitySchemaProperties{}, err
	}
	var response runtimeSchemaResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return EntitySchemaProperties{}, fmt.Errorf("RuntimeEntitySchemaRequest returned invalid JSON: %w", err)
	}
	if !response.Success || response.Schema == nil {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != "" {
			return EntitySchemaProperties{}, fmt.Errorf("runtime schema read failed: %s", response.ErrorInfo.Message)
		}
		return EntitySchemaProperties{}, fmt.Errorf("runtime schema %q was not returned by Creatio", input.SchemaName)
	}
	return mapRuntimeSchema(*response.Schema, input.SchemaName, input.RequiredOnly), nil
}

func mapRuntimeSchema(schema runtimeSchemaPayload, requestedName string, requiredOnly bool) EntitySchemaProperties {
	columns := make([]runtimeSchemaColumn, 0, len(schema.Columns.Items))
	for _, column := range schema.Columns.Items {
		columns = append(columns, column)
	}
	sort.SliceStable(columns, func(i, j int) bool {
		return strings.ToLower(columns[i].Name) < strings.ToLower(columns[j].Name)
	})

	inheritedCount := 0
	resultColumns := make([]EntitySchemaPropertyColumn, 0, len(columns))
	var primaryColumnName *string
	var primaryDisplayColumnName *string
	if value := strings.TrimSpace(schema.PrimaryDisplayColumnName); value != "" {
		primaryDisplayColumnName = stringPointer(value)
	}
	for _, column := range columns {
		if column.Inherited {
			inheritedCount++
		}
		if strings.EqualFold(column.UID, schema.PrimaryColumnUID) {
			primaryColumnName = stringPointer(column.Name)
		}
		if primaryDisplayColumnName == nil && schema.PrimaryDisplayColumnUID != "" && strings.EqualFold(column.UID, schema.PrimaryDisplayColumnUID) {
			primaryDisplayColumnName = stringPointer(column.Name)
		}
		if requiredOnly && !column.Required {
			continue
		}
		source := "own"
		if column.Inherited {
			source = "inherited"
		}
		resultColumns = append(resultColumns, EntitySchemaPropertyColumn{
			Name: column.Name, UID: column.UID, Source: source,
			Title: localizedSchemaText(column.Caption), Description: localizedSchemaText(column.Description),
			Type: friendlyDataValueType(column.DataValueType), Required: column.Required, Indexed: column.Indexed,
			ReferenceSchemaName: column.ReferenceSchemaName,
		})
	}
	name := strings.TrimSpace(schema.Name)
	if name == "" {
		name = strings.TrimSpace(requestedName)
	}
	return EntitySchemaProperties{
		Name: name, Title: localizedSchemaText(schema.Caption), Description: localizedSchemaText(schema.Description),
		PackageName: mergedSchemaPackageName, ExtendParent: schema.ExtendParent,
		PrimaryColumnName: primaryColumnName, PrimaryDisplayColumnName: primaryDisplayColumnName,
		OwnColumnCount: len(columns) - inheritedCount, InheritedColumnCount: inheritedCount,
		TrackChangesInDB: schema.IsTrackChangesInDB, DBView: schema.IsDBView, Virtual: schema.IsVirtual,
		ShowInAdvancedMode:        schema.ShowInAdvancedMode,
		AdministratedByOperations: schema.AdministratedByOperations,
		AdministratedByColumns:    schema.AdministratedByColumns,
		AdministratedByRecords:    schema.AdministratedByRecords,
		Columns:                   resultColumns,
	}
}

func localizedSchemaText(value json.RawMessage) *string {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return stringPointer(text)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(value, &values); err != nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.EqualFold(key, "en-US") {
			if localized := rawJSONString(values[key]); localized != nil {
				return localized
			}
		}
	}
	for _, key := range keys {
		if localized := rawJSONString(values[key]); localized != nil {
			return localized
		}
	}
	return nil
}

func rawJSONString(value json.RawMessage) *string {
	var text string
	if json.Unmarshal(value, &text) != nil {
		return nil
	}
	return stringPointer(text)
}

func stringPointer(value string) *string { return &value }

func friendlyDataValueType(value int) string {
	if name, ok := friendlyDataValueTypes[value]; ok {
		return name
	}
	return fmt.Sprintf("%d", value)
}
