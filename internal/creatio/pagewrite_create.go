package creatio

// create-page: clio's PageCreateCommand.TryCreatePage. A new Freedom UI page schema is saved through
// ClientUnitSchemaDesignerService.SaveSchema with the template as parent and the template's own localizable
// strings, in the package the caller names.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// pageWriteSchemaNameError is clio's PageSchemaMetadataHelper.SchemaNameFormatError.
	pageWriteSchemaNameError = "schema-name must start with a letter and contain only letters, digits, or underscores"
	// pageWriteOptionalPropertiesError is clio's PageOptionalPropertiesHelper.InvalidOptionalPropertiesError.
	pageWriteOptionalPropertiesError = "optional-properties must be a valid JSON array of {key, value} objects"
	// PageWriteCompileNotRequiredNote is clio's CommandExecutionResult.CompileNotRequiredNote.
	PageWriteCompileNotRequiredNote = "compile-creatio not required"

	pageWriteGetDesignerRoute  = "ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema"
	pageWriteSaveDesignerRoute = "ServiceModel/ClientUnitSchemaDesignerService.svc/SaveSchema"
)

// PageCreateRequest is clio's create-page arguments.
type PageCreateRequest struct {
	SchemaName, Template, PackageName, Caption, Description, EntitySchemaName, CaptionCulture, OptionalProperties string
}

// PageCreateResponse is clio's PageCreateResponse; null members are left out, schemaType is always written.
type PageCreateResponse struct {
	Success                            bool   `json:"success"`
	SchemaName                         string `json:"schemaName,omitempty"`
	SchemaUID                          string `json:"schemaUId,omitempty"`
	PackageName                        string `json:"packageName,omitempty"`
	PackageUID                         string `json:"packageUId,omitempty"`
	TemplateName                       string `json:"templateName,omitempty"`
	TemplateUID                        string `json:"templateUId,omitempty"`
	Caption                            string `json:"caption,omitempty"`
	EntitySchemaName                   string `json:"entitySchemaName,omitempty"`
	EntitySchemaUID                    string `json:"entitySchemaUId,omitempty"`
	SchemaType                         int    `json:"schemaType"`
	Error                              string `json:"error,omitempty"`
	DesignPackageUID                   string `json:"designPackageUId,omitempty"`
	WillCreateReplacingInDesignPackage *bool  `json:"willCreateReplacingInDesignPackage,omitempty"`
	Note                               string `json:"note,omitempty"`
}

// PageWriteValidSchemaName is clio's PageSchemaMetadataHelper.IsValidSchemaName.
func PageWriteValidSchemaName(name string) bool {
	return name != "" && classicPageValidSchemaName(name)
}

// PageWriteSchemaNameError is the text clio answers for a schema name that fails PageWriteValidSchemaName.
func PageWriteSchemaNameError() string { return pageWriteSchemaNameError }

// pageWriteParseOptionalProperties is clio's PageOptionalPropertiesHelper.TryParse: absent is fine, otherwise a
// JSON array of objects that each carry a non-blank key.
func pageWriteParseOptionalProperties(text string) (*jnode, string) {
	if strings.TrimSpace(text) == "" {
		return nil, ""
	}
	parsed, err := parseJNode([]byte(text))
	if err != nil || !parsed.isArray() {
		return nil, pageWriteOptionalPropertiesError
	}
	for _, item := range parsed.items {
		if !item.isObject() {
			return nil, pageWriteOptionalPropertiesError
		}
		key := item.get("key")
		if key == nil || key.kind == jkNull || strings.TrimSpace(pageWriteTokenText(key)) == "" {
			return nil, pageWriteOptionalPropertiesError
		}
	}
	return parsed, ""
}

// pageWriteTokenText is Newtonsoft's JToken.ToString() for a scalar: a string as itself, other scalars as JSON.
func pageWriteTokenText(node *jnode) string {
	if node == nil || node.kind == jkNull {
		return ""
	}
	if node.kind == jkString {
		return node.text
	}
	return string(node.newtonsoftJSON())
}

// CreatePage is clio's PageCreateCommand.TryCreatePage.
func (c *Client) CreatePage(ctx context.Context, input PageCreateRequest) PageCreateResponse {
	fail := func(text string) PageCreateResponse { return PageCreateResponse{Error: text} }
	switch {
	case strings.TrimSpace(input.SchemaName) == "":
		return fail("schema-name is required")
	case !PageWriteValidSchemaName(input.SchemaName):
		return fail(pageWriteSchemaNameError)
	case strings.TrimSpace(input.Template) == "":
		return fail("template is required")
	case strings.TrimSpace(input.PackageName) == "":
		return fail("package-name is required")
	}
	optionalProperties, problem := pageWriteParseOptionalProperties(input.OptionalProperties)
	if problem != "" {
		return fail(problem)
	}
	template, err := c.pageWriteFindTemplate(ctx, input.Template)
	if err != nil {
		return fail("Failed to resolve template catalog: " + err.Error())
	}
	if template == nil {
		return fail(fmt.Sprintf("Template '%s' is not supported. Use list-page-templates to discover valid values.", input.Template))
	}
	packageUID, problem := c.schemaWritePackageUID(ctx, input.PackageName)
	if problem != "" {
		return fail(problem)
	}
	exists, err := c.pageWriteClientUnitExists(ctx, input.SchemaName)
	if err != nil {
		return fail(err.Error())
	}
	if exists {
		return fail(fmt.Sprintf("Page schema '%s' already exists in this environment.", input.SchemaName))
	}
	caption := strings.TrimSpace(input.Caption)
	if caption == "" {
		caption = input.SchemaName
	}
	entitySchemaUID := ""
	if strings.TrimSpace(input.EntitySchemaName) != "" {
		entitySchemaUID, problem = c.pageWriteEntitySchemaUID(ctx, input.EntitySchemaName)
		if problem != "" {
			return fail(problem)
		}
	}
	uid := schemaWriteNewGUID()
	strings_, problem := c.pageWriteTemplateStrings(ctx, template.UID)
	if problem != "" {
		return fail(problem)
	}
	culture, problem := pageWriteCaptionCulture(input.CaptionCulture)
	if problem != "" {
		return fail(problem)
	}
	if culture == "" {
		culture = c.schemaWriteProfileCulture(ctx)
	}
	if err := schemaWriteCaptionMatchesCulture(culture, caption, "caption"); err != nil {
		return fail(err.Error())
	}
	if err := schemaWriteCaptionMatchesCulture(culture, input.Description, "description"); err != nil {
		return fail(err.Error())
	}
	payload := pageWriteCreatePayload(uid, input.SchemaName, caption, input.Description, *template, packageUID,
		input.PackageName, entitySchemaUID, strings_, culture, optionalProperties)
	if problem := c.pageWriteSaveNew(ctx, payload); problem != "" {
		return fail(problem)
	}
	response := PageCreateResponse{Success: true, SchemaName: input.SchemaName, SchemaUID: uid, PackageName: input.PackageName,
		PackageUID: packageUID, TemplateName: template.Name, TemplateUID: template.UID, SchemaType: template.SchemaType,
		Caption: caption, EntitySchemaName: input.EntitySchemaName, EntitySchemaUID: entitySchemaUID}
	c.pageWriteWarnIfNotDesignPackage(ctx, &response)
	return response
}

// pageWriteFindTemplate is clio's SchemaTemplateCatalog.FindTemplate: web catalog, then mobile, by name or UId.
func (c *Client) pageWriteFindTemplate(ctx context.Context, nameOrUID string) (*PageTemplateItem, error) {
	if strings.TrimSpace(nameOrUID) == "" {
		return nil, nil
	}
	for _, schemaType := range []int{pageSchemaTypeWeb, pageSchemaTypeMobile} {
		items, err := c.loadPageTemplates(ctx, schemaType)
		if err != nil {
			return nil, err
		}
		for index := range items {
			if strings.EqualFold(items[index].Name, nameOrUID) || strings.EqualFold(items[index].UID, nameOrUID) {
				found := items[index]
				return &found, nil
			}
		}
	}
	return nil, nil
}

// pageWriteClientUnitExists is clio's PageSchemaMetadataHelper.SchemaNameExists: a client-unit SysSchema row
// with that name. clio reads any lookup failure as "does not exist" and goes on.
func (c *Client) pageWriteClientUnitExists(ctx context.Context, schemaName string) (bool, error) {
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "trimDateTimeParameterToDate": false,
			"items": map[string]any{
				"filter0": comparisonFilter("Name", schemaName, 1, 3),
				"filter1": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
			}},
		"columns":  map[string]any{"items": map[string]any{"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}}},
		"rowCount": 1,
	})
	if err != nil {
		return false, nil
	}
	return len(rows) > 0, nil
}

// pageWriteEntitySchemaUID is clio's PageSchemaMetadataHelper.QueryEntitySchemaUId.
func (c *Client) pageWriteEntitySchemaUID(ctx context.Context, entitySchemaName string) (string, string) {
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
			"byName":    schemaWriteTextEquals("Name", entitySchemaName),
			"byManager": schemaWriteTextEquals("ManagerName", "EntitySchemaManager"),
		}},
		"columns":  map[string]any{"items": map[string]any{"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}}},
		"rowCount": 1,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "SelectQuery failed:") {
			return "", "Failed to query entity schema metadata"
		}
		return "", err.Error()
	}
	if len(rows) == 0 {
		return "", fmt.Sprintf("Entity schema '%s' not found.", entitySchemaName)
	}
	return rowText(rows[0], "UId"), ""
}

// pageWriteTemplateStrings is clio's TryLoadTemplateLocalizableStrings: the template's own localizable strings
// (not its hierarchy's), copied into the new page.
func (c *Client) pageWriteTemplateStrings(ctx context.Context, templateUID string) (*jnode, string) {
	body, _ := json.Marshal(map[string]any{"schemaUId": templateUID, "useFullHierarchy": false})
	payload, err := c.callService(ctx, serviceCall{Route: pageWriteGetDesignerRoute, Body: body, Timeout: schemaWriteDesignerTimeout,
		Limit: schemaWriteResponseBytes})
	if err != nil {
		return nil, err.Error()
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		return nil, pageWriteNewtonsoftParseError(payload, err)
	}
	schema := response.get("schema")
	if success := response.get("success"); success == nil || success.kind != jkBool || !success.flag || !schema.isObject() {
		if message := response.get("errorInfo").get("message"); message != nil && message.kind != jkNull {
			return nil, pageWriteTokenText(message)
		}
		return nil, fmt.Sprintf("Failed to load template schema '%s' resources", templateUID)
	}
	strings_ := schema.get("localizableStrings")
	if !strings_.isArray() {
		return newArray(), ""
	}
	return strings_.clone(), ""
}

// pageWriteNewtonsoftParseError is the text clio surfaces when JObject.Parse fails on a designer answer.
func pageWriteNewtonsoftParseError(payload []byte, err error) string {
	if strings.TrimSpace(string(payload)) == "" {
		return "Error reading JObject from JsonReader. Path '', line 0, position 0."
	}
	if err != nil {
		return err.Error()
	}
	return "Error reading JObject from JsonReader. Current JsonReader item is not an object."
}

// pageWriteCreatePayload is clio's BuildSaveSchemaPayload, member for member and in clio's order.
func pageWriteCreatePayload(uid, schemaName, caption, description string, template PageTemplateItem, packageUID, packageName,
	entitySchemaUID string, localizableStrings *jnode, culture string, optionalProperties *jnode) *jnode {
	if strings.TrimSpace(culture) == "" {
		culture = schemaWriteDefaultCulture
	}
	localized := func(value string) *jnode {
		entry := newObject()
		entry.set("cultureName", newString(culture))
		entry.set("value", newString(value))
		list := newArray()
		list.items = append(list.items, entry)
		return list
	}
	reference := func(uid, name string) *jnode {
		node := newObject()
		node.set("uId", newString(uid))
		node.set("name", newString(name))
		return node
	}
	addonTypes := newArray()
	for _, name := range []string{"AppearanceSettings", "Sidebar", "BusinessRule"} {
		addonTypes.items = append(addonTypes.items, newString(name))
	}
	schema := newObject()
	schema.set("uId", newString(uid))
	schema.set("name", newString(schemaName))
	schema.set("isReadOnly", newBool(false))
	schema.set("useFullHierarchy", newBool(false))
	schema.set("userLevelSchema", newBool(false))
	schema.set("addonTypes", addonTypes)
	schema.set("package", reference(packageUID, packageName))
	schema.set("body", newString(""))
	schema.set("extendParent", newBool(false))
	schema.set("caption", localized(caption))
	if strings.TrimSpace(description) == "" {
		schema.set("description", newArray())
	} else {
		schema.set("description", localized(description))
	}
	if localizableStrings == nil {
		localizableStrings = newArray()
	}
	schema.set("localizableStrings", localizableStrings)
	schema.set("parameters", newArray())
	schema.set("messages", newArray())
	schema.set("images", newArray())
	if optionalProperties == nil {
		optionalProperties = newArray()
	}
	schema.set("optionalProperties", optionalProperties)
	schema.set("group", pageWriteNullableString(template.GroupName))
	schema.set("schemaType", newInt(template.SchemaType))
	schema.set("schemaVersion", newInt(1))
	schema.set("parent", reference(template.UID, template.Name))
	schema.set("less", newString(""))
	if strings.TrimSpace(entitySchemaUID) != "" {
		dependency := newObject()
		dependency.set("uId", newString(entitySchemaUID))
		dependsOn := newArray()
		dependsOn.items = append(dependsOn.items, dependency)
		schema.set("dependsOn", dependsOn)
	}
	return schema
}

var pageWriteCulturePattern = regexp.MustCompile(`^([A-Za-z]{2,3})(?:-([A-Za-z]{4}))?(?:-([A-Za-z]{2}|[0-9]{3}))?$`)

// pageWriteCaptionCulture is clio's CaptionCultureResolver.NormalizeOverrideCulture: a blank override means the
// profile culture; anything else must be a culture name, returned in .NET's canonical casing. .NET checks
// against the predefined ICU cultures; this checks the shape of the tag.
func pageWriteCaptionCulture(override string) (string, string) {
	trimmed := strings.TrimSpace(override)
	if trimmed == "" {
		return "", ""
	}
	parts := pageWriteCulturePattern.FindStringSubmatch(trimmed)
	if parts == nil {
		return "", fmt.Sprintf("--caption-culture '%s' is not a valid culture name (e.g. en-US, uk-UA).", trimmed)
	}
	name := strings.ToLower(parts[1])
	if parts[2] != "" {
		name += "-" + strings.ToUpper(parts[2][:1]) + strings.ToLower(parts[2][1:])
	}
	if parts[3] != "" {
		name += "-" + strings.ToUpper(parts[3])
	}
	return name, ""
}

func pageWriteNullableString(value string) *jnode {
	if value == "" {
		return jsonNullNode()
	}
	return newString(value)
}

// pageWriteSaveNew is clio's PageCreateCommand.TrySaveSchema: one SaveSchema, failure worded by
// ParseSaveErrorMessage with "Failed to create page schema" as the fallback.
func (c *Client) pageWriteSaveNew(ctx context.Context, schema *jnode) string {
	payload, err := c.callService(ctx, serviceCall{Route: pageWriteSaveDesignerRoute, Body: schema.newtonsoftJSON(),
		Timeout: schemaWriteDesignerTimeout, Limit: schemaWriteResponseBytes})
	if err != nil {
		return err.Error()
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		return pageWriteNewtonsoftParseError(payload, err)
	}
	if success := response.get("success"); success != nil && success.kind == jkBool && success.flag {
		return ""
	}
	return schemaWriteSaveErrorMessage(response, "Failed to create page schema")
}

// pageWriteWarnIfNotDesignPackage is clio's WarnIfNotDesignPackage: best effort, never fails the create.
func (c *Client) pageWriteWarnIfNotDesignPackage(ctx context.Context, response *PageCreateResponse) {
	design := c.pageDesignPackageUID(ctx, response.SchemaUID)
	if strings.TrimSpace(design) == "" || strings.EqualFold(design, response.PackageUID) {
		return
	}
	response.DesignPackageUID = design
	flag := true
	response.WillCreateReplacingInDesignPackage = &flag
	mobile := ""
	if response.SchemaType == pageSchemaTypeMobile {
		mobile = " (an empty mobile page crashes the Creatio Mobile app)"
	}
	note := fmt.Sprintf("Chosen package '%s' is not the app's design package (uId=%s). A subsequent update-page WITHOUT "+
		"target-schema-uid would create a replacing schema in the design package and leave this schema empty%s. "+
		"Pass target-schema-uid=%s on update-page to write into this schema.", response.PackageName, design, mobile, response.SchemaUID)
	if strings.TrimSpace(response.Note) == "" {
		response.Note = note
	} else {
		response.Note += " " + note
	}
}
