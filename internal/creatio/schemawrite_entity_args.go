package creatio

// Argument binding for the entity-schema write tools. clio binds the MCP arguments into System.Text.Json
// records: unknown properties are ignored, a value of the wrong JSON type fails the whole binding, and a
// JSON object of localizations becomes a dictionary. Go receives the arguments already decoded into a map,
// which has lost the order of an object's keys; localization maps are therefore read with en-US first and
// the other cultures in ordinal order.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SchemaWriteEntBindingError is clio's invalid-parameter-type failure for an argument of the wrong shape.
type SchemaWriteEntBindingError struct{ Tool string }

func (e SchemaWriteEntBindingError) Error() string {
	return fmt.Sprintf("invalid-parameter-type: argument 'args' for MCP tool '%s' must be an object. Received an incompatible JSON value.", e.Tool)
}

type schemaWriteEntBinder struct{ tool string }

func (b schemaWriteEntBinder) fail() error { return SchemaWriteEntBindingError{Tool: b.tool} }

func (b schemaWriteEntBinder) str(object map[string]any, key string) (*string, error) {
	value, ok := object[key]
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, b.fail()
	}
	return &text, nil
}

func (b schemaWriteEntBinder) text(object map[string]any, key string) (string, error) {
	value, err := b.str(object, key)
	if err != nil || value == nil {
		return "", err
	}
	return *value, nil
}

func (b schemaWriteEntBinder) boolean(object map[string]any, key string) (*bool, error) {
	value, ok := object[key]
	if !ok || value == nil {
		return nil, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return nil, b.fail()
	}
	return &flag, nil
}

func (b schemaWriteEntBinder) object(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, b.fail()
	}
	return object, nil
}

// locMap reads a culture -> text object; nil when absent or null.
func (b schemaWriteEntBinder) locMap(object map[string]any, key string) (schemaWriteEntLocMap, error) {
	value, ok := object[key]
	if !ok || value == nil {
		return nil, nil
	}
	entries, ok := value.(map[string]any)
	if !ok {
		return nil, b.fail()
	}
	keys := make([]string, 0, len(entries))
	for culture := range entries {
		keys = append(keys, culture)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		leftDefault, rightDefault := strings.EqualFold(keys[i], schemaWriteDefaultCulture), strings.EqualFold(keys[j], schemaWriteDefaultCulture)
		if leftDefault != rightDefault {
			return leftDefault
		}
		return keys[i] < keys[j]
	})
	result := schemaWriteEntLocMap{}
	for _, culture := range keys {
		text, ok := entries[culture].(string)
		if entries[culture] == nil {
			text, ok = "", true
		}
		if !ok {
			return nil, b.fail()
		}
		result = append(result, schemaWriteEntLocPair{Key: culture, Value: text})
	}
	return result, nil
}

func (b schemaWriteEntBinder) defaultConfig(object map[string]any, key string) (*schemaWriteEntDefaultConfig, error) {
	value, ok := object[key]
	if !ok || value == nil {
		return nil, nil
	}
	if _, isObject := value.(map[string]any); !isObject {
		return nil, b.fail()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, b.fail()
	}
	config := &schemaWriteEntDefaultConfig{}
	if json.Unmarshal(encoded, config) != nil {
		return nil, b.fail()
	}
	return config, nil
}

func (b schemaWriteEntBinder) list(object map[string]any, key string) ([]any, bool, error) {
	value, ok := object[key]
	if !ok || value == nil {
		return nil, false, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false, b.fail()
	}
	return items, true, nil
}

// SchemaWriteEntColumnArgs is clio's CreateEntitySchemaColumnArgs.
type SchemaWriteEntColumnArgs struct {
	Name, ColumnName, Type, DataValueType, ReferenceSchemaName, ReferenceSchema *string
	TitleLocalizations                                                          schemaWriteEntLocMap
	Title, Caption, DefaultValueSource, DefaultValue                            *string
	Required, IsRequired, Masked                                                *bool
	DefaultValueConfig                                                          *schemaWriteEntDefaultConfig
	// Raw keeps the item as the caller sent it, for a resume plan that echoes it back.
	Raw map[string]any
}

func schemaWriteEntPick(primary, alias *string) string {
	if primary != nil && strings.TrimSpace(*primary) != "" {
		return *primary
	}
	if alias != nil {
		return *alias
	}
	return ""
}

func (c SchemaWriteEntColumnArgs) resolveName() string {
	return schemaWriteEntPick(c.Name, c.ColumnName)
}
func (c SchemaWriteEntColumnArgs) resolveType() string {
	return schemaWriteEntPick(c.Type, c.DataValueType)
}
func (c SchemaWriteEntColumnArgs) resolveReference() string {
	return schemaWriteEntPick(c.ReferenceSchemaName, c.ReferenceSchema)
}
func (c SchemaWriteEntColumnArgs) resolveRequired() *bool {
	if c.Required != nil {
		return c.Required
	}
	return c.IsRequired
}

func (b schemaWriteEntBinder) column(value any) (SchemaWriteEntColumnArgs, error) {
	object, err := b.object(value)
	if err != nil {
		return SchemaWriteEntColumnArgs{}, err
	}
	column := SchemaWriteEntColumnArgs{Raw: object}
	for key, target := range map[string]**string{"name": &column.Name, "column-name": &column.ColumnName, "type": &column.Type,
		"data-value-type": &column.DataValueType, "reference-schema-name": &column.ReferenceSchemaName,
		"reference-schema": &column.ReferenceSchema, "title": &column.Title, "caption": &column.Caption,
		"default-value-source": &column.DefaultValueSource, "default-value": &column.DefaultValue} {
		if *target, err = b.str(object, key); err != nil {
			return SchemaWriteEntColumnArgs{}, err
		}
	}
	for key, target := range map[string]**bool{"required": &column.Required, "is-required": &column.IsRequired, "masked": &column.Masked} {
		if *target, err = b.boolean(object, key); err != nil {
			return SchemaWriteEntColumnArgs{}, err
		}
	}
	if column.TitleLocalizations, err = b.locMap(object, "title-localizations"); err != nil {
		return SchemaWriteEntColumnArgs{}, err
	}
	if column.DefaultValueConfig, err = b.defaultConfig(object, "default-value-config"); err != nil {
		return SchemaWriteEntColumnArgs{}, err
	}
	return column, nil
}

func (b schemaWriteEntBinder) columns(object map[string]any, key string) ([]SchemaWriteEntColumnArgs, bool, error) {
	items, present, err := b.list(object, key)
	if err != nil || !present {
		return nil, present, err
	}
	columns := make([]SchemaWriteEntColumnArgs, 0, len(items))
	for _, item := range items {
		column, err := b.column(item)
		if err != nil {
			return nil, true, err
		}
		columns = append(columns, column)
	}
	return columns, true, nil
}

// SchemaWriteEntOperationArgs is clio's UpdateEntitySchemaOperationArgs / ModifyEntitySchemaColumnArgs
// column part (ColumnModificationArgsBase).
type SchemaWriteEntOperationArgs struct {
	Action, ColumnName, NameAlias, NewName, Type, DataValueType, ReferenceSchemaName, ReferenceSchema *string
	TitleLocalizations, DescriptionLocalizations                                                      schemaWriteEntLocMap
	Title, Caption, Description, DefaultValue, DefaultValueSource, CaptionCulture, UsageType          *string
	Required, IsRequired, Indexed, Cloneable, TrackChanges, MultilineText, LocalizableText            *bool
	AccentInsensitive, Masked, FormatValidated, UseSeconds, SimpleLookup, Cascade, DoNotControlInteg  *bool
	DefaultValueConfig                                                                                *schemaWriteEntDefaultConfig
	Raw                                                                                               map[string]any
}

func (o SchemaWriteEntOperationArgs) action() string {
	if o.Action == nil {
		return ""
	}
	return *o.Action
}
func (o SchemaWriteEntOperationArgs) resolveColumnName() string {
	return schemaWriteEntPick(o.ColumnName, o.NameAlias)
}
func (o SchemaWriteEntOperationArgs) resolveType() string {
	return schemaWriteEntPick(o.Type, o.DataValueType)
}
func (o SchemaWriteEntOperationArgs) resolveReference() string {
	return schemaWriteEntPick(o.ReferenceSchemaName, o.ReferenceSchema)
}
func (o SchemaWriteEntOperationArgs) resolveRequired() *bool {
	if o.Required != nil {
		return o.Required
	}
	return o.IsRequired
}

func (b schemaWriteEntBinder) operation(value any) (SchemaWriteEntOperationArgs, error) {
	object, err := b.object(value)
	if err != nil {
		return SchemaWriteEntOperationArgs{}, err
	}
	return b.operationFields(object)
}

func (b schemaWriteEntBinder) operationFields(object map[string]any) (SchemaWriteEntOperationArgs, error) {
	operation := SchemaWriteEntOperationArgs{Raw: object}
	var err error
	for key, target := range map[string]**string{"action": &operation.Action, "column-name": &operation.ColumnName,
		"name": &operation.NameAlias, "new-name": &operation.NewName, "type": &operation.Type,
		"data-value-type": &operation.DataValueType, "reference-schema-name": &operation.ReferenceSchemaName,
		"reference-schema": &operation.ReferenceSchema, "title": &operation.Title, "caption": &operation.Caption,
		"description": &operation.Description, "default-value": &operation.DefaultValue,
		"default-value-source": &operation.DefaultValueSource, "caption-culture": &operation.CaptionCulture,
		"usage-type": &operation.UsageType} {
		if *target, err = b.str(object, key); err != nil {
			return SchemaWriteEntOperationArgs{}, err
		}
	}
	for key, target := range map[string]**bool{"required": &operation.Required, "is-required": &operation.IsRequired,
		"indexed": &operation.Indexed, "cloneable": &operation.Cloneable, "track-changes": &operation.TrackChanges,
		"multiline-text": &operation.MultilineText, "localizable-text": &operation.LocalizableText,
		"accent-insensitive": &operation.AccentInsensitive, "masked": &operation.Masked,
		"format-validated": &operation.FormatValidated, "use-seconds": &operation.UseSeconds,
		"simple-lookup": &operation.SimpleLookup, "cascade": &operation.Cascade,
		"do-not-control-integrity": &operation.DoNotControlInteg} {
		if *target, err = b.boolean(object, key); err != nil {
			return SchemaWriteEntOperationArgs{}, err
		}
	}
	if operation.TitleLocalizations, err = b.locMap(object, "title-localizations"); err != nil {
		return SchemaWriteEntOperationArgs{}, err
	}
	if operation.DescriptionLocalizations, err = b.locMap(object, "description-localizations"); err != nil {
		return SchemaWriteEntOperationArgs{}, err
	}
	if operation.DefaultValueConfig, err = b.defaultConfig(object, "default-value-config"); err != nil {
		return SchemaWriteEntOperationArgs{}, err
	}
	return operation, nil
}
