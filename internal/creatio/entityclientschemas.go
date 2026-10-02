package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	entityRowCount         = 50
	sectionRowCount        = 100
	editPageRowCount       = 100
	lookupIDsPerQuery      = 400
	entityClientSchemaNote = "One level only. Details on each card and Freedom counterparts are read from the card body/page model " +
		"(pure merge module); recurse into detail entities by calling list-entity-client-schemas per detail entity."
	emptyEntityClientSchemaNote = "No SysModule sections or SysModuleEdit pages matched this entity — it may have no Classic UI " +
		"section, or the entity name/UId is off. This is NOT the same as 'nothing to migrate'; verify before skipping. "
)

var freedomPageTemplates = lowerSet("PageWithTabsAndProgressBarTemplate", "PageWithTabsFreedomTemplate",
	"PageWithRightAreaAndTabsFreedomTemplate", "PageWithTopAreaAndTabsFreedomTemplate", "BaseMiniPageTemplate",
	"PageWithAreaFreedomTemplate", "BaseHomePage", "BaseDashboardTemplate", "BaseSidebarTemplate", "ListPageV3Template",
	"ListPageV2Template", "BlankPageTemplate", "FormPageTemplate", "MobilePageWithTabsFreedomTemplate",
	"BaseMobilePageTemplate", "BaseMobileListTemplate", "BlankMobilePageTemplate")

var classicPageTemplates = lowerSet("BaseModulePageV2", "BasePageV2")

type EntityClientSchemasResult struct {
	Success   bool                `json:"success"`
	Entity    string              `json:"entity,omitempty"`
	EntityUID string              `json:"entityUId,omitempty"`
	Sections  []MigrationSection  `json:"sections,omitempty"`
	EditPages []MigrationEditPage `json:"editPages,omitempty"`
	Warnings  []string            `json:"warnings,omitempty"`
	Note      string              `json:"note,omitempty"`
	Error     string              `json:"error,omitempty"`
}

type MigrationSection struct {
	Caption       *string `json:"caption,omitempty"`
	Code          *string `json:"code,omitempty"`
	SectionSchema *string `json:"sectionSchema,omitempty"`
	CardSchema    *string `json:"cardSchema,omitempty"`
	CardSchemaUID *string `json:"cardSchemaUId,omitempty"`
	Template      *string `json:"template,omitempty"`
	Kind          string  `json:"kind"`
	IsTyped       bool    `json:"isTyped"`
}

type MigrationEditPage struct {
	TypeColumnValue        *string `json:"typeColumnValue,omitempty"`
	TypeColumnDisplayValue *string `json:"typeColumnDisplayValue,omitempty"`
	CardSchema             *string `json:"cardSchema,omitempty"`
	CardSchemaUID          *string `json:"cardSchemaUId,omitempty"`
	Template               *string `json:"template,omitempty"`
	Kind                   string  `json:"kind"`
	MiniPageSchema         *string `json:"miniPageSchema,omitempty"`
	MiniPageSchemaUID      *string `json:"miniPageSchemaUId,omitempty"`
	MiniPageTemplate       *string `json:"miniPageTemplate,omitempty"`
	MiniPageKind           string  `json:"miniPageKind"`
	MiniPageModes          *string `json:"miniPageModes,omitempty"`
}

type schemaMeta struct {
	name, template *string
}

// ListEntityClientSchemas resolves the Classic UI page-role graph of one entity: its SysModule sections and
// SysModuleEdit pages with their add mini pages, each classified classic, freedom or unknown by parent template.
// Rows keep the order DataService returns them in, as clio does.
func (c *Client) ListEntityClientSchemas(ctx context.Context, entityName string) EntityClientSchemasResult {
	if strings.TrimSpace(entityName) == "" {
		return EntityClientSchemasResult{Error: "entity-name is required"}
	}
	warnings := []string{}
	entityRows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"UId": "UId", "ExtendParent": "ExtendParent"},
		map[string]any{"byName": eqFilter("Name", entityName, 1), "byManager": eqFilter("ManagerName", "EntitySchemaManager", 1)}, entityRowCount))
	if err != nil {
		return EntityClientSchemasResult{Error: err.Error()}
	}
	if len(entityRows) == entityRowCount {
		warnings = append(warnings, fmt.Sprintf("Entity schema lookup reached the rowCount cap (%d); verify the entity result before using it.", entityRowCount))
	}
	entityUID, resolveErr := resolveEntityUID(entityName, entityRows)
	if resolveErr != "" {
		return EntityClientSchemasResult{Error: resolveErr}
	}
	moduleRows, err := c.selectRows(ctx, entityQuery("SysModule", map[string]string{
		"Caption": "Caption", "Code": "Code", "SectionSchemaUId": "SectionSchemaUId", "CardSchemaUId": "CardSchemaUId",
		"TypeColumnUId": "SysModuleEntity.TypeColumnUId",
	}, map[string]any{"byEntity": eqFilter("SysModuleEntity.SysEntitySchemaUId", entityUID, 0)}, sectionRowCount))
	if err != nil {
		return EntityClientSchemasResult{Error: err.Error()}
	}
	editRows, err := c.selectRows(ctx, entityQuery("SysModuleEdit", map[string]string{
		"TypeColumnValue": "TypeColumnValue", "CardSchemaUId": "CardSchemaUId", "MiniPageSchemaUId": "MiniPageSchemaUId",
		"MiniPageModes": "MiniPageModes", "TypeColumnUId": "SysModuleEntity.TypeColumnUId",
	}, map[string]any{"byEntity": eqFilter("SysModuleEntity.SysEntitySchemaUId", entityUID, 0)}, editPageRowCount))
	if err != nil {
		return EntityClientSchemasResult{Error: err.Error()}
	}
	if len(moduleRows) == sectionRowCount {
		warnings = append(warnings, fmt.Sprintf("Section lookup reached the rowCount cap (%d); the section list may be truncated.", sectionRowCount))
	}
	if len(editRows) == editPageRowCount {
		warnings = append(warnings, fmt.Sprintf("Edit-page lookup reached the rowCount cap (%d); the edit-page list may be truncated.", editPageRowCount))
	}

	uids := make([]string, 0, 2*(len(moduleRows)+len(editRows)))
	for _, row := range moduleRows {
		uids = append(uids, rowText(row, "SectionSchemaUId"), rowText(row, "CardSchemaUId"))
	}
	for _, row := range editRows {
		uids = append(uids, rowText(row, "CardSchemaUId"), rowText(row, "MiniPageSchemaUId"))
	}
	metas, err := c.schemaMetaBatch(ctx, uids)
	if err != nil {
		return EntityClientSchemasResult{Error: err.Error()}
	}
	meta := func(uid string) schemaMeta {
		if strings.TrimSpace(uid) == "" || uid == emptyGUID {
			return schemaMeta{}
		}
		return metas[strings.ToLower(uid)]
	}

	sections := make([]MigrationSection, 0, len(moduleRows))
	for _, row := range moduleRows {
		card := meta(rowText(row, "CardSchemaUId"))
		typeColumn := rowText(row, "TypeColumnUId")
		sections = append(sections, MigrationSection{
			Caption: rowTextPointer(row, "Caption"), Code: rowTextPointer(row, "Code"),
			SectionSchema: meta(rowText(row, "SectionSchemaUId")).name, CardSchema: card.name,
			CardSchemaUID: rowTextPointer(row, "CardSchemaUId"), Template: card.template, Kind: classifyPageKind(card.template),
			IsTyped: strings.TrimSpace(typeColumn) != "" && typeColumn != emptyGUID,
		})
	}
	editPages := make([]MigrationEditPage, 0, len(editRows))
	typeColumns := make([]string, 0, len(editRows))
	for _, row := range editRows {
		card := meta(rowText(row, "CardSchemaUId"))
		mini := meta(rowText(row, "MiniPageSchemaUId"))
		editPages = append(editPages, MigrationEditPage{
			TypeColumnValue: rowTextPointer(row, "TypeColumnValue"), CardSchema: card.name,
			CardSchemaUID: rowTextPointer(row, "CardSchemaUId"), Template: card.template, Kind: classifyPageKind(card.template),
			MiniPageSchema: mini.name, MiniPageSchemaUID: rowTextPointer(row, "MiniPageSchemaUId"),
			MiniPageTemplate: mini.template, MiniPageKind: classifyPageKind(mini.template),
			MiniPageModes: rowTextPointer(row, "MiniPageModes"),
		})
		typeColumns = append(typeColumns, rowText(row, "TypeColumnUId"))
	}
	c.enrichTypeDisplayNames(ctx, entityName, editPages, typeColumns)

	note := entityClientSchemaNote
	if len(sections) == 0 && len(editPages) == 0 {
		note = emptyEntityClientSchemaNote + note
	}
	result := EntityClientSchemasResult{Success: true, Entity: entityName, EntityUID: entityUID,
		Sections: sections, EditPages: editPages, Note: note}
	if len(warnings) > 0 {
		result.Warnings = warnings
	}
	return result
}

func resolveEntityUID(entityName string, rows []map[string]json.RawMessage) (string, string) {
	if len(rows) == 0 {
		return "", fmt.Sprintf("Entity '%s' not found (ManagerName='EntitySchemaManager')", entityName)
	}
	for _, row := range rows {
		var extendParent *bool
		if json.Unmarshal(row["ExtendParent"], &extendParent) != nil || extendParent == nil || *extendParent {
			continue
		}
		uid := rowText(row, "UId")
		if strings.TrimSpace(uid) == "" {
			return "", fmt.Sprintf("Entity '%s' base schema metadata is missing UId", entityName)
		}
		return uid, ""
	}
	return "", fmt.Sprintf("Entity '%s' metadata did not include a base row (ExtendParent=false); cannot safely resolve the entity's schema.", entityName)
}

func (c *Client) schemaMetaBatch(ctx context.Context, uids []string) (map[string]schemaMeta, error) {
	distinct := distinctGUIDText(uids)
	result := map[string]schemaMeta{}
	if len(distinct) == 0 {
		return result, nil
	}
	rows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"UId": "UId", "Name": "Name", "ParentName": "Parent.Name"},
		map[string]any{"byUId": inFilter("UId", distinct, 0)}, len(distinct)))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if uid := rowText(row, "UId"); strings.TrimSpace(uid) != "" {
			result[strings.ToLower(uid)] = schemaMeta{name: rowTextPointer(row, "Name"), template: rowTextPointer(row, "ParentName")}
		}
	}
	return result, nil
}

// enrichTypeDisplayNames sets the Type lookup caption on each per-type edit page. It is best effort, as in
// clio: any failure leaves the page with its raw GUID only and never fails the call.
func (c *Client) enrichTypeDisplayNames(ctx context.Context, entityName string, pages []MigrationEditPage, typeColumns []string) {
	valuesByColumn := map[string][]string{}
	columnOrder := []string{}
	for index, page := range pages {
		value, ok := parseGUID(derefString(page.TypeColumnValue))
		column := typeColumns[index]
		if !ok || strings.TrimSpace(column) == "" || column == emptyGUID {
			continue
		}
		key := strings.ToLower(column)
		if _, seen := valuesByColumn[key]; !seen {
			columnOrder = append(columnOrder, key)
		}
		valuesByColumn[key] = append(valuesByColumn[key], value)
	}
	if len(valuesByColumn) == 0 {
		return
	}
	entity, err := c.GetEntitySchemaProperties(ctx, EntitySchemaPropertiesRequest{SchemaName: entityName})
	if err != nil {
		return
	}
	captions := map[string]map[string]string{}
	displayColumns := map[string]string{}
	for _, column := range columnOrder {
		columnGUID, ok := parseGUID(column)
		if !ok {
			continue
		}
		reference := ""
		for _, schemaColumn := range entity.Columns {
			if uid, ok := parseGUID(schemaColumn.UID); ok && uid == columnGUID && schemaColumn.ReferenceSchemaName != nil {
				reference = strings.TrimSpace(*schemaColumn.ReferenceSchemaName)
				break
			}
		}
		if reference == "" {
			continue
		}
		displayColumn, cached := displayColumns[strings.ToLower(reference)]
		if !cached {
			if schema, err := c.GetEntitySchemaProperties(ctx, EntitySchemaPropertiesRequest{SchemaName: reference}); err == nil && schema.PrimaryDisplayColumnName != nil {
				displayColumn = *schema.PrimaryDisplayColumnName
			}
			displayColumns[strings.ToLower(reference)] = displayColumn
		}
		if strings.TrimSpace(displayColumn) == "" {
			continue
		}
		captions[column] = c.lookupDisplayValues(ctx, reference, displayColumn, distinctGUIDText(valuesByColumn[column]))
	}
	for index := range pages {
		value, ok := parseGUID(derefString(pages[index].TypeColumnValue))
		if !ok {
			continue
		}
		if caption, found := captions[strings.ToLower(typeColumns[index])][value]; found {
			pages[index].TypeColumnDisplayValue = stringPointer(caption)
		}
	}
}

func (c *Client) lookupDisplayValues(ctx context.Context, schemaName, displayColumn string, ids []string) map[string]string {
	found := map[string]string{}
	for start := 0; start < len(ids); start += lookupIDsPerQuery {
		chunk := ids[start:min(start+lookupIDsPerQuery, len(ids))]
		rows, err := c.selectRows(ctx, buildSelectQuery(schemaName, map[string]string{"Id": "Id", "DisplayValue": displayColumn},
			map[string]any{"Id": inFilter("Id", chunk, 0)}, len(chunk)))
		if err != nil {
			continue
		}
		for _, row := range rows {
			display := rowString(row, "DisplayValue")
			if id, ok := parseGUID(rowString(row, "Id")); ok && strings.TrimSpace(display) != "" {
				found[id] = display
			}
		}
	}
	return found
}

func classifyPageKind(template *string) string {
	if template == nil || strings.TrimSpace(*template) == "" {
		return "unknown"
	}
	name := strings.ToLower(strings.TrimSpace(*template))
	if freedomPageTemplates[name] {
		return "freedom"
	}
	if classicPageTemplates[name] {
		return "classic"
	}
	return "unknown"
}

// entityQuery is the minimal SelectQuery clio's ClassicEntitySchemaQuery sends: no paging or ordering keys.
func entityQuery(root string, columns map[string]string, filters map[string]any, rowCount int) map[string]any {
	items := make(map[string]any, len(columns))
	for alias, path := range columns {
		items[alias] = map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
	}
	return map[string]any{"rootSchemaName": root, "operationType": 0, "columns": map[string]any{"items": items},
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": filters}, "rowCount": rowCount}
}

func eqFilter(column string, value any, dataValueType int) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
		"leftExpression":  map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}},
	}
}

func inFilter(column string, values []string, dataValueType int) map[string]any {
	expressions := make([]any, 0, len(values))
	for _, value := range values {
		expressions = append(expressions, map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}})
	}
	return map[string]any{"filterType": 4, "comparisonType": 3, "isEnabled": true,
		"leftExpression": map[string]any{"expressionType": 0, "columnPath": column}, "rightExpressions": expressions}
}

// rowText renders a DataService cell the way Newtonsoft JToken.ToString does for scalars: a string as is,
// null as empty, and any other value as its JSON text.
func rowText(row map[string]json.RawMessage, key string) string {
	raw, ok := row[key]
	if !ok || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

// rowTextPointer is rowText that keeps an absent column absent; a JSON null renders as an empty string.
func rowTextPointer(row map[string]json.RawMessage, key string) *string {
	if _, ok := row[key]; !ok {
		return nil
	}
	return stringPointer(rowText(row, key))
}

func distinctGUIDText(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		key := strings.ToLower(value)
		if strings.TrimSpace(value) == "" || value == emptyGUID || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func lowerSet(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[strings.ToLower(value)] = true
	}
	return set
}
