package creatio

// create-related-page-addon: clio's CreateRelatedPageAddonCommand / RelatedPageAddonService.Create. The
// object's RelatedPage (web) or MobileRelatedPage (mobile) add-on is read through AddonSchemaDesignerService,
// its Pages list replaced, the add-on saved, and the client script cache and static content rebuilt.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	relatedAddonEmployees = "All employees"
	relatedAddonPortal    = "All external users"
)

var relatedAddonRoleIDs = map[string]string{
	strings.ToLower(relatedAddonEmployees): "a29a3ba5-4b0d-de11-9a51-005056c00008",
	strings.ToLower(relatedAddonPortal):    "720b771c-e7a7-4f31-9cfb-52cd21c3739f",
}

// RelatedPageSpec is clio's RelatedPageSpec (one page entry of the request).
type RelatedPageSpec struct {
	PageSchemaName  string `json:"page-schema-name"`
	IsDefault       *bool  `json:"is-default"`
	IsAdd           *bool  `json:"is-add"`
	IsSspDefault    *bool  `json:"is-ssp-default"`
	Role            string `json:"role"`
	TypeColumnValue string `json:"type-column-value"`
	RoleName        string `json:"role-name"`
	PageSchemaUID   string `json:"page-schema-uid"`
}

// CreateRelatedPageAddonRequest is clio's CreateRelatedPageAddonArgs.
type CreateRelatedPageAddonRequest struct {
	EntitySchemaName string
	PackageName      string
	Pages            []*RelatedPageSpec
	PagesMissing     bool
	TypeColumnUID    string
	SchemaType       string
}

// CreateRelatedPageAddonResponse is clio's CreateRelatedPageAddonResponse.
type CreateRelatedPageAddonResponse struct {
	Success          bool   `json:"success"`
	EntitySchemaName string `json:"entitySchemaName,omitempty"`
	EntitySchemaUID  string `json:"entitySchemaUId,omitempty"`
	PackageName      string `json:"packageName,omitempty"`
	PackageUID       string `json:"packageUId,omitempty"`
	AddonName        string `json:"addonName,omitempty"`
	PageCount        int    `json:"pageCount"`
	Warning          string `json:"warning,omitempty"`
	Error            string `json:"error,omitempty"`
}

// RelatedPageAddonArgumentFailure is the tool's own argument guard (clio's CreateRelatedPageAddonTool).
func RelatedPageAddonArgumentFailure(request CreateRelatedPageAddonRequest) string {
	switch {
	case strings.TrimSpace(request.EntitySchemaName) == "":
		return "entity-schema-name is required."
	case strings.TrimSpace(request.PackageName) == "":
		return "package-name is required."
	case request.PagesMissing:
		return "pages is required (send an empty list to clear all bindings / reset to inline)"
	}
	for _, page := range request.Pages {
		if page == nil {
			return "each entry in pages is required (a null pages entry was provided)"
		}
	}
	return ""
}

func relatedAddonFlag(value *bool) bool { return value != nil && *value }

func relatedAddonIsGUID(value string) bool { return normalizeGUID(strings.TrimSpace(value)) != "" }

func relatedAddonRoleIs(role, known string) bool {
	parsed := normalizeGUID(strings.TrimSpace(role))
	return parsed != "" && parsed == normalizeGUID(known)
}

func relatedAddonGeneral(page *RelatedPageSpec) bool {
	return (strings.TrimSpace(page.Role) == "" && strings.TrimSpace(page.RoleName) == "") ||
		strings.EqualFold(strings.TrimSpace(page.RoleName), relatedAddonEmployees) ||
		relatedAddonRoleIs(page.Role, relatedAddonRoleIDs[strings.ToLower(relatedAddonEmployees)])
}

func relatedAddonPortalAudience(page *RelatedPageSpec) bool {
	return strings.EqualFold(strings.TrimSpace(page.RoleName), relatedAddonPortal) ||
		relatedAddonRoleIs(page.Role, relatedAddonRoleIDs[strings.ToLower(relatedAddonPortal)])
}

func relatedAddonAudience(page *RelatedPageSpec) string {
	if relatedAddonPortalAudience(page) {
		return relatedAddonPortal
	}
	return relatedAddonEmployees
}

func relatedAddonNormalizeGUID(value string) string {
	if parsed := normalizeGUID(strings.TrimSpace(value)); parsed != "" {
		return parsed
	}
	return strings.TrimSpace(value)
}

func relatedAddonTypeValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return relatedAddonNormalizeGUID(value)
}

// relatedAddonValidate is RelatedPageAddonService.ValidateRequest.
func relatedAddonValidate(request CreateRelatedPageAddonRequest) error {
	if len(request.Pages) == 0 {
		return nil
	}
	if strings.TrimSpace(request.TypeColumnUID) == "" {
		for _, page := range request.Pages {
			if strings.TrimSpace(page.TypeColumnValue) != "" {
				return errors.New("type-column-uid is required when any page sets a type-column-value: a typed page set needs the " +
					"type column it is keyed by, otherwise the platform can never match those pages to a record type.")
			}
		}
	}
	if strings.TrimSpace(request.TypeColumnUID) != "" && !relatedAddonIsGUID(request.TypeColumnUID) {
		return fmt.Errorf("type-column-uid '%s' is not a valid GUID; it must be the entity column's u-id.", request.TypeColumnUID)
	}
	for _, page := range request.Pages {
		hasName, hasUID := strings.TrimSpace(page.PageSchemaName) != "", strings.TrimSpace(page.PageSchemaUID) != ""
		if !hasName && !hasUID {
			return errors.New("Each page entry requires a page-schema-name or a page-schema-uid.")
		}
		if hasUID && !relatedAddonIsGUID(page.PageSchemaUID) {
			return fmt.Errorf("page-schema-uid '%s' is not a valid GUID; it must be the page schema's u-id.", page.PageSchemaUID)
		}
		if strings.TrimSpace(page.TypeColumnValue) != "" && !relatedAddonIsGUID(page.TypeColumnValue) {
			return fmt.Errorf("type-column-value '%s' is not a valid GUID; it must be the lookup record's Id (the record in the "+
				"reference lookup that type-column-uid points at).", page.TypeColumnValue)
		}
	}
	for _, page := range request.Pages {
		if strings.TrimSpace(page.Role) == "" || strings.TrimSpace(page.RoleName) == "" || !relatedAddonIsGUID(page.Role) {
			continue
		}
		known, ok := relatedAddonRoleIDs[strings.ToLower(strings.TrimSpace(page.RoleName))]
		if !ok || !relatedAddonRoleIs(page.Role, known) {
			return fmt.Errorf("A page entry sets both role ('%s') and role-name ('%s') and they point at different audiences; "+
				"provide only one, or make role-name resolve to the same audience as role.", strings.TrimSpace(page.Role), strings.TrimSpace(page.RoleName))
		}
	}
	for _, page := range request.Pages {
		if strings.TrimSpace(page.Role) != "" && !relatedAddonIsGUID(page.Role) {
			return fmt.Errorf("role '%s' is not a valid SysAdminUnit GUID; use role-name to resolve a role by name.", page.Role)
		}
	}
	for _, page := range request.Pages {
		if !relatedAddonGeneral(page) && !relatedAddonPortalAudience(page) {
			return errors.New("A page entry targets an unsupported audience. Related-page bindings support only the general " +
				"audience (no role, or the 'All employees' role) and the portal audience (the 'All external users' role); a custom role is not supported.")
		}
	}
	base := false
	for _, page := range request.Pages {
		if relatedAddonFlag(page.IsDefault) && strings.TrimSpace(page.TypeColumnValue) == "" && relatedAddonGeneral(page) {
			base = true
			break
		}
	}
	if !base {
		return errors.New("A base default page for the general audience is required: include one page with is-default=true, no " +
			"type-column-value, and either no role or the 'All employees' role (the page opened for a record and the fallback for " +
			"any record type without a dedicated set). Portal ('All external users') and type-specific pages are layered on top; " +
			"a portal-only or other audience-scoped-only binding is rejected.")
	}
	defaults, adds := map[string]int{}, map[string]int{}
	cells := []string{}
	for _, page := range request.Pages {
		cell := relatedAddonAudience(page) + "\x00" + relatedAddonTypeValue(page.TypeColumnValue)
		if _, seen := defaults[cell]; !seen {
			cells = append(cells, cell)
			defaults[cell] = 0
			adds[cell] = 0
		}
		if relatedAddonFlag(page.IsDefault) {
			defaults[cell]++
		}
		if relatedAddonFlag(page.IsAdd) {
			adds[cell]++
		}
	}
	for _, cell := range cells {
		if defaults[cell] > 1 {
			return errors.New("More than one default page targets the same audience and type. Each (audience, type) may have " +
				"only one is-default page; remove the duplicate default.")
		}
		if adds[cell] > 1 {
			return errors.New("More than one add page targets the same audience and type. Each (audience, type) may have only " +
				"one is-add page; remove the duplicate add page.")
		}
	}
	audiences := []string{}
	hasAdd, hasDefault := map[string]bool{}, map[string]bool{}
	for _, page := range request.Pages {
		audience := relatedAddonAudience(page)
		if !hasAdd[audience] && !hasDefault[audience] && !containsText(audiences, audience) {
			audiences = append(audiences, audience)
		}
		hasAdd[audience] = hasAdd[audience] || relatedAddonFlag(page.IsAdd)
		hasDefault[audience] = hasDefault[audience] || relatedAddonFlag(page.IsDefault)
	}
	for _, audience := range audiences {
		if hasAdd[audience] && !hasDefault[audience] {
			return fmt.Errorf("The '%s' audience has an add page but no default page. A page used to ADD a record needs a page "+
				"to OPEN one for the same audience; add an is-default page for that audience.", audience)
		}
	}
	return nil
}

// CreateRelatedPageAddon is clio's CreateRelatedPageAddonCommand.TryCreate.
func (c *Client) CreateRelatedPageAddon(ctx context.Context, request CreateRelatedPageAddonRequest) (response CreateRelatedPageAddonResponse) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = CreateRelatedPageAddonResponse{Error: pageWriteRecoveredText(recovered)}
		}
	}()
	fail := func(err error) CreateRelatedPageAddonResponse {
		return CreateRelatedPageAddonResponse{Error: err.Error()}
	}
	addonName := "RelatedPage"
	switch strings.ToLower(strings.TrimSpace(request.SchemaType)) {
	case "", "web":
	case "mobile":
		addonName = "MobileRelatedPage"
	default:
		return fail(fmt.Errorf("schema-type '%s' is not valid; use 'web' or 'mobile'.", request.SchemaType))
	}
	if strings.TrimSpace(request.EntitySchemaName) == "" {
		return fail(errors.New("entity-schema-name is required."))
	}
	if strings.TrimSpace(request.PackageName) == "" {
		return fail(errors.New("package-name is required."))
	}
	if err := relatedAddonValidate(request); err != nil {
		return fail(err)
	}
	packageUID, err := c.relatedAddonPackageUID(ctx, request.PackageName)
	if err != nil {
		return fail(err)
	}
	packageID := normalizeGUID(packageUID)
	if packageID == "" {
		return fail(fmt.Errorf("Resolved package '%s' UId '%s' is not a valid GUID.", request.PackageName, packageUID))
	}
	entity, err := c.relatedAddonEntityDesign(ctx, request.EntitySchemaName, packageID, request.PackageName)
	if err != nil {
		return fail(err)
	}
	warnings := []string{}
	if len(request.Pages) > 0 {
		warning, err := relatedAddonTypeColumnCheck(request.TypeColumnUID, entity)
		if err != nil {
			return fail(err)
		}
		warnings = append(warnings, warning)
	}
	for _, page := range request.Pages {
		if strings.TrimSpace(page.Role) != "" || strings.TrimSpace(page.RoleName) == "" {
			continue
		}
		if _, ok := relatedAddonRoleIDs[strings.ToLower(strings.TrimSpace(page.RoleName))]; !ok {
			return fail(fmt.Errorf("Role name '%s' is not a supported audience; only 'All employees' and 'All external users' are supported.",
				strings.TrimSpace(page.RoleName)))
		}
	}
	pages, err := c.relatedAddonBuildPages(ctx, request.Pages, packageID)
	if err != nil {
		return fail(err)
	}
	schema, err := c.relatedAddonSchema(ctx, map[string]any{
		"addonName": addonName, "targetSchemaUId": entity.uid, "targetParentSchemaUId": entity.parentUID,
		"targetPackageUId": packageID, "targetSchemaManagerName": "EntitySchemaManager", "useFullHierarchy": true,
	})
	if err != nil {
		return fail(err)
	}
	targetUID := ""
	for _, key := range schema.keys {
		if value := schema.props[key]; strings.EqualFold(key, "targetSchemaUId") && value.kind == jkString {
			if uid := normalizeGUID(value.text); uid != "" && uid != emptyGUID {
				targetUID = uid
				break
			}
		}
	}
	if targetUID == "" {
		return fail(errors.New("The related-page add-on response is missing a valid targetSchemaUId."))
	}
	metaData := ""
	if value := processDescribeProperty(schema, "metaData"); value != nil && value.kind == jkString {
		metaData = value.text
	}
	metadata, parseErr := parseJNode([]byte(metaData))
	if strings.TrimSpace(metaData) == "" || parseErr != nil || !metadata.isObject() {
		metadata = newObject()
	}
	metadata.set("Pages", pages)
	if len(request.Pages) == 0 || strings.TrimSpace(request.TypeColumnUID) == "" {
		metadata.set("TypeColumnUId", jsonNullNode())
	} else {
		metadata.set("TypeColumnUId", newString(relatedAddonNormalizeGUID(request.TypeColumnUID)))
	}
	if err := c.relatedAddonSave(ctx, schema, string(metadata.stjJSON())); err != nil {
		return fail(err)
	}
	warnings = append(warnings, c.relatedAddonResetCache(ctx), c.relatedAddonBuildConfiguration(ctx))
	present := []string{}
	for _, warning := range warnings {
		if strings.TrimSpace(warning) != "" {
			present = append(present, warning)
		}
	}
	return CreateRelatedPageAddonResponse{Success: true, EntitySchemaName: request.EntitySchemaName, EntitySchemaUID: targetUID,
		PackageName: request.PackageName, PackageUID: packageUID, AddonName: addonName, PageCount: len(pages.items),
		Warning: strings.Join(present, " ")}
}

type relatedAddonEntity struct {
	name, uid, parentUID string
	columns              []string
}

// relatedAddonEntityDesign is ResolveEntitySchema with the own and inherited column UIds.
func (c *Client) relatedAddonEntityDesign(ctx context.Context, entitySchemaName, packageID, packageName string) (relatedAddonEntity, error) {
	body, _ := json.Marshal(map[string]any{"name": strings.TrimSpace(entitySchemaName), "packageUId": packageID,
		"useFullHierarchy": true, "cultures": []string{businessRuleDefaultCulture}})
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return relatedAddonEntity{}, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return relatedAddonEntity{}, errors.New("GetSchemaDesignItem returned an empty response.")
	}
	root, err := parseJNode(payload)
	if err != nil || !root.isObject() {
		return relatedAddonEntity{}, fmt.Errorf("GetSchemaDesignItem returned invalid JSON: %v", err)
	}
	if success := processDescribeProperty(root, "success"); success == nil || success.kind != jkBool || !success.flag {
		if message := processDescribeProperty(processDescribeProperty(root, "errorInfo"), "message"); message != nil && message.kind == jkString &&
			strings.TrimSpace(message.text) != "" {
			return relatedAddonEntity{}, errors.New(message.text)
		}
		return relatedAddonEntity{}, errors.New("GetSchemaDesignItem failed.")
	}
	schema := processDescribeProperty(root, "schema")
	if !schema.isObject() {
		return relatedAddonEntity{}, fmt.Errorf("Object (entity schema) '%s' not found in package '%s'. The object must be visible "+
			"from that package — if it lives elsewhere, add a package dependency.", entitySchemaName, packageName)
	}
	entity := relatedAddonEntity{name: pageWriteTokenText(processDescribeProperty(schema, "name")),
		uid: guidOrEmpty(pageWriteTokenText(processDescribeProperty(schema, "uId"))), parentUID: emptyGUID}
	if parent := processDescribeProperty(schema, "parentSchema"); parent.isObject() {
		entity.parentUID = guidOrEmpty(pageWriteTokenText(processDescribeProperty(parent, "uId")))
	}
	for _, key := range []string{"columns", "inheritedColumns"} {
		if list := processDescribeProperty(schema, key); list.isArray() {
			for _, column := range list.items {
				entity.columns = append(entity.columns, normalizeGUID(pageWriteTokenText(processDescribeProperty(column, "uId"))))
			}
		}
	}
	return entity, nil
}

// relatedAddonTypeColumnCheck is ValidateTypeColumn.
func relatedAddonTypeColumnCheck(typeColumnUID string, entity relatedAddonEntity) (string, error) {
	id := normalizeGUID(strings.TrimSpace(typeColumnUID))
	if strings.TrimSpace(typeColumnUID) == "" || id == "" {
		return "", nil
	}
	if len(entity.columns) == 0 {
		return fmt.Sprintf("type-column-uid '%s' could not be verified: object '%s' returned no columns (own or inherited), so the "+
			"type-column existence check was skipped and the value was written unverified. If the typed pages do not resolve at "+
			"runtime, re-check the type column — the schema fetch may have been incomplete.", strings.TrimSpace(typeColumnUID), entity.name), nil
	}
	for _, column := range entity.columns {
		if column == id {
			return "", nil
		}
	}
	return "", fmt.Errorf("type-column-uid '%s' is not a column of object '%s'. It must be the u-id of a column on the object — the "+
		"record-type lookup (e.g. Type or Category) the typed page sets are keyed by.", typeColumnUID, entity.name)
}

// relatedAddonBuildPages is BuildPages: one RelatedPagesMetadata entry per spec, page names resolved as the
// package sees them.
func (c *Client) relatedAddonBuildPages(ctx context.Context, specs []*RelatedPageSpec, packageID string) (*jnode, error) {
	resolved := map[string]string{}
	for _, spec := range specs {
		if strings.TrimSpace(spec.PageSchemaUID) != "" || strings.TrimSpace(spec.PageSchemaName) == "" {
			continue
		}
		name := strings.TrimSpace(spec.PageSchemaName)
		if _, seen := resolved[strings.ToLower(name)]; seen {
			continue
		}
		uid, err := c.relatedAddonPageUID(ctx, name, packageID)
		if err != nil {
			return nil, err
		}
		if normalizeGUID(uid) == "" {
			return nil, fmt.Errorf("Resolved page '%s' UId '%s' is not a valid GUID.", name, uid)
		}
		resolved[strings.ToLower(name)] = uid
	}
	pages := newArray()
	for _, spec := range specs {
		uid := strings.TrimSpace(spec.PageSchemaUID)
		if uid == "" {
			uid = resolved[strings.ToLower(strings.TrimSpace(spec.PageSchemaName))]
		}
		role := ""
		if strings.TrimSpace(spec.Role) != "" {
			role = relatedAddonNormalizeGUID(spec.Role)
		} else if strings.TrimSpace(spec.RoleName) != "" {
			role = relatedAddonRoleIDs[strings.ToLower(strings.TrimSpace(spec.RoleName))]
		}
		actions := newObject()
		actions.set("Add", newBool(relatedAddonFlag(spec.IsAdd)))
		entry := newObject()
		entry.set("UId", newString(schemaWriteNewGUID()))
		entry.set("PageSchemaUId", newString(uid))
		entry.set("IsDefault", newBool(relatedAddonFlag(spec.IsDefault)))
		entry.set("IsSspDefault", newBool(relatedAddonFlag(spec.IsSspDefault)))
		entry.set("Actions", actions)
		entry.set("Role", pageWriteNullableString(role))
		entry.set("TypeColumnValue", pageWriteNullableString(relatedAddonTypeValue(spec.TypeColumnValue)))
		pages.items = append(pages.items, entry)
	}
	return pages, nil
}

// relatedAddonPageUID is PageSchemaResolver.ResolveHierarchy(name, package)[0].UId: the replacing schema in
// the package if there is one, otherwise the head of the root schema's hierarchy as the package sees it.
func (c *Client) relatedAddonPageUID(ctx context.Context, name, packageID string) (string, error) {
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
			"byName":    schemaWriteTextEquals("Name", name),
			"byManager": schemaWriteTextEquals("ManagerName", clientUnitSchemaManagerName),
			"byPackage": pageWriteGUIDEquals("SysPackage.UId", packageID),
		}},
		"columns":  map[string]any{"items": map[string]any{"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}}},
		"rowCount": 1,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "SelectQuery failed:") {
			return "", errors.New("Failed to query schema metadata in target package.")
		}
		return "", err
	}
	schemaUID := ""
	if len(rows) > 0 {
		schemaUID = rowText(rows[0], "UId")
	}
	if strings.TrimSpace(schemaUID) == "" {
		row, problem := c.pageWriteSysSchemaRowWithPackage(ctx, name)
		if problem != "" {
			return "", errors.New(problem)
		}
		schemaUID = rowText(row, "UId")
		if strings.TrimSpace(schemaUID) == "" {
			return "", fmt.Errorf("Page schema '%s' metadata is missing schema UId.", name)
		}
		design := c.pageDesignPackageUID(ctx, schemaUID)
		if strings.TrimSpace(design) == "" {
			design = rowText(row, "PackageUId")
			if strings.TrimSpace(design) == "" {
				design = packageID
			}
		}
		initial, err := c.pageLayers(ctx, schemaUID, design)
		if err != nil {
			return "", err
		}
		for index := len(initial) - 1; index >= 0; index-- {
			if strings.EqualFold(initial[index].Name, name) {
				schemaUID = initial[index].UID
				break
			}
		}
	}
	hierarchy, err := c.pageLayers(ctx, schemaUID, packageID)
	if err != nil {
		return "", err
	}
	if len(hierarchy) == 0 {
		return "", fmt.Errorf("Page schema '%s' hierarchy is empty.", name)
	}
	return hierarchy[0].UID, nil
}

// pageWriteSysSchemaRowWithPackage is QuerySysSchemaRow for UId and SysPackage.UId.
func (c *Client) pageWriteSysSchemaRowWithPackage(ctx context.Context, schemaName string) (map[string]json.RawMessage, string) {
	column := func(path string) map[string]any {
		return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
	}
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "trimDateTimeParameterToDate": false,
			"items": map[string]any{
				"filter0": comparisonFilter("Name", schemaName, 1, 3),
				"filter1": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
			}},
		"columns":  map[string]any{"items": map[string]any{"UId": column("UId"), "PackageUId": column("SysPackage.UId")}},
		"rowCount": 1,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "SelectQuery failed:") {
			return nil, "Failed to query schema metadata"
		}
		return nil, err.Error()
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("Schema '%s' not found", schemaName)
	}
	return rows[0], ""
}

// relatedAddonSave is AddonSchemaDesignerClient.SaveSchema: the schema as read, with the new metaData and the
// resources in clio's DTO shape.
func (c *Client) relatedAddonSave(ctx context.Context, schema *jnode, metaData string) error {
	dto := newObject()
	dto.set("metaData", newString(metaData))
	resources := newArray()
	if list := processDescribeProperty(schema, "resources"); list.isArray() {
		for _, item := range list.items {
			resource := newObject()
			resource.set("key", newString(pageWriteTokenText(processDescribeProperty(item, "key"))))
			values := newArray()
			if valueList := processDescribeProperty(item, "value"); valueList.isArray() {
				for _, value := range valueList.items {
					entry := newObject()
					entry.set("key", newString(pageWriteTokenText(processDescribeProperty(value, "key"))))
					entry.set("value", newString(pageWriteTokenText(processDescribeProperty(value, "value"))))
					values.items = append(values.items, entry)
				}
			}
			resource.set("value", values)
			resources.items = append(resources.items, resource)
		}
	}
	dto.set("resources", resources)
	for _, key := range schema.keys {
		if strings.EqualFold(key, "metaData") || strings.EqualFold(key, "resources") {
			continue
		}
		dto.set(key, schema.props[key])
	}
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/AddonSchemaDesignerService.svc/SaveSchema", dto.stjJSON(), 45*time.Second, maxResponseBytes)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return errors.New("AddonSchemaDesignerService.SaveSchema returned an empty response.")
	}
	root, err := parseJNode(payload)
	if err != nil || !root.isObject() {
		return fmt.Errorf("AddonSchemaDesignerService.SaveSchema returned invalid JSON: %v", err)
	}
	success := processDescribeProperty(root, "success")
	value := processDescribeProperty(root, "value")
	if success == nil || success.kind != jkBool || !success.flag || (value != nil && value.kind == jkBool && !value.flag) {
		if message := processDescribeProperty(processDescribeProperty(root, "errorInfo"), "message"); message != nil && message.kind == jkString {
			return errors.New(message.text)
		}
		return errors.New("AddonSchemaDesignerService.SaveSchema failed.")
	}
	return nil
}

// relatedAddonResetCache is ResetClientScriptCache: best effort, a failure is a warning.
func (c *Client) relatedAddonResetCache(ctx context.Context) string {
	if _, err := c.callService(ctx, serviceCall{Route: pageWriteResetScriptCacheRoute, Body: []byte{}}); err != nil {
		return "Client script-cache reset failed after the schema was already saved: " + err.Error() + ". The change is persisted; the cache reset can be retried."
	}
	return ""
}

// relatedAddonBuildConfiguration is BuildConfiguration: the static-content rebuild; only an explicit failure
// or a transport error is a warning.
func (c *Client) relatedAddonBuildConfiguration(ctx context.Context) string {
	response, err := c.serviceRequest(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/BuildConfiguration", Body: []byte{},
		Timeout: 10 * time.Minute})
	if err != nil {
		return "Static-content rebuild (BuildConfiguration) failed after the schema was already saved: " + err.Error() +
			". The change is persisted; the client static-content rebuild may need a manual retry."
	}
	root, parseErr := parseJNode(response.payload)
	if strings.TrimSpace(string(response.payload)) == "" || parseErr != nil || !root.isObject() {
		return ""
	}
	if success := processDescribeProperty(root, "success"); success != nil && success.kind == jkBool && !success.flag {
		message := "static content rebuild failed"
		if text := processDescribeProperty(processDescribeProperty(root, "errorInfo"), "message"); text != nil && text.kind == jkString {
			message = text.text
		}
		return "WorkspaceExplorerService.svc/BuildConfiguration reported a failure AFTER the schema was already saved: " + message +
			". The change is persisted; the client static-content rebuild may need a manual retry."
	}
	return ""
}
