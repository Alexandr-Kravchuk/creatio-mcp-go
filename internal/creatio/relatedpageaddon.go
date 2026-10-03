package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RelatedPageAddonResponse is clio's get-related-page-addon envelope.
type RelatedPageAddonResponse struct {
	Success          bool                    `json:"success"`
	EntitySchemaName *string                 `json:"entitySchemaName,omitempty"`
	EntitySchemaUID  *string                 `json:"entitySchemaUId,omitempty"`
	PackageName      *string                 `json:"packageName,omitempty"`
	PackageUID       *string                 `json:"packageUId,omitempty"`
	AddonName        *string                 `json:"addonName,omitempty"`
	TypeColumnUID    *string                 `json:"typeColumnUId,omitempty"`
	PageCount        int                     `json:"pageCount"`
	Pages            []RelatedPageAddonEntry `json:"pages,omitzero"`
	Error            *string                 `json:"error,omitempty"`
}

// RelatedPageAddonEntry is one decoded page binding of the add-on.
type RelatedPageAddonEntry struct {
	PageSchemaUID   *string `json:"pageSchemaUId,omitempty"`
	PageSchemaName  *string `json:"pageSchemaName,omitempty"`
	IsDefault       bool    `json:"isDefault"`
	IsAdd           bool    `json:"isAdd"`
	IsSspDefault    bool    `json:"isSspDefault"`
	Role            *string `json:"role,omitempty"`
	RoleName        *string `json:"roleName,omitempty"`
	TypeColumnValue *string `json:"typeColumnValue,omitempty"`
}

// relatedAddonRoleNames are the two platform audiences the designer produces, keyed by SysAdminUnit id.
var relatedAddonRoleNames = map[string]string{
	"a29a3ba5-4b0d-de11-9a51-005056c00008": "All employees",
	"720b771c-e7a7-4f31-9cfb-52cd21c3739f": "All external users",
}

// RelatedPageAddonFailure is clio's failed read: success false, page count 0 and the error.
func RelatedPageAddonFailure(message string) RelatedPageAddonResponse {
	return RelatedPageAddonResponse{Error: &message}
}

// GetRelatedPageAddon reads an object's RelatedPage (web) or MobileRelatedPage (mobile) add-on through
// AddonSchemaDesignerService.GetSchema — a read that saves nothing — and decodes its page bindings, resolving
// page names from SysSchema and the two standard audience names.
func (c *Client) GetRelatedPageAddon(ctx context.Context, entitySchemaName, packageName, schemaType string) RelatedPageAddonResponse {
	if strings.TrimSpace(entitySchemaName) == "" {
		return RelatedPageAddonFailure("entity-schema-name is required.")
	}
	if strings.TrimSpace(packageName) == "" {
		return RelatedPageAddonFailure("package-name is required.")
	}
	addonName := "RelatedPage"
	switch strings.ToLower(strings.TrimSpace(schemaType)) {
	case "", "web":
	case "mobile":
		addonName = "MobileRelatedPage"
	default:
		return RelatedPageAddonFailure(fmt.Sprintf("schema-type '%s' is not valid; use 'web' or 'mobile'.", schemaType))
	}
	response, err := c.relatedAddonRead(ctx, entitySchemaName, packageName, addonName)
	if err != nil {
		return RelatedPageAddonFailure(err.Error())
	}
	return response
}

func (c *Client) relatedAddonRead(ctx context.Context, entitySchemaName, packageName, addonName string) (RelatedPageAddonResponse, error) {
	packageUID, err := c.relatedAddonPackageUID(ctx, packageName)
	if err != nil {
		return RelatedPageAddonResponse{}, err
	}
	packageID := normalizeGUID(packageUID)
	if packageID == "" {
		return RelatedPageAddonResponse{}, fmt.Errorf("Resolved package '%s' UId '%s' is not a valid GUID.", packageName, packageUID)
	}
	schemaUID, parentUID, err := c.relatedAddonEntitySchema(ctx, entitySchemaName, packageID, packageName)
	if err != nil {
		return RelatedPageAddonResponse{}, err
	}
	schema, err := c.relatedAddonSchema(ctx, map[string]any{
		"addonName": addonName, "targetSchemaUId": schemaUID, "targetParentSchemaUId": parentUID,
		"targetPackageUId": packageID, "targetSchemaManagerName": "EntitySchemaManager", "useFullHierarchy": true,
	})
	if err != nil {
		return RelatedPageAddonResponse{}, err
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
		return RelatedPageAddonResponse{}, errors.New("The related-page add-on response is missing a valid targetSchemaUId.")
	}
	metaData := ""
	if value := processDescribeProperty(schema, "metaData"); value != nil && value.kind == jkString {
		metaData = value.text
	}
	pages, typeColumnUID := c.relatedAddonPages(ctx, metaData)
	return RelatedPageAddonResponse{
		Success: true, EntitySchemaName: &entitySchemaName, EntitySchemaUID: &targetUID, PackageName: &packageName,
		PackageUID: &packageUID, AddonName: &addonName, TypeColumnUID: typeColumnUID, PageCount: len(pages), Pages: pages,
	}, nil
}

// relatedAddonPackageUID is clio's QueryPackageUId: the first SysPackage row with that exact name.
func (c *Client) relatedAddonPackageUID(ctx context.Context, packageName string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"UId": "UId"},
		map[string]any{"byName": comparisonFilter("Name", packageName, 1, 3)}, 1))
	if err != nil {
		if strings.HasPrefix(err.Error(), "SelectQuery failed:") {
			return "", errors.New("Failed to query SysPackage")
		}
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("Package '%s' not found in the target environment.", packageName)
	}
	uid := rowText(rows[0], "UId")
	if strings.TrimSpace(uid) == "" {
		return "", fmt.Errorf("Package '%s' has no UId in the SysPackage response.", packageName)
	}
	return uid, nil
}

// relatedAddonEntitySchema reads the object as the package sees it through GetSchemaDesignItem.
func (c *Client) relatedAddonEntitySchema(ctx context.Context, entitySchemaName, packageID, packageName string) (string, string, error) {
	body, _ := json.Marshal(map[string]any{"name": strings.TrimSpace(entitySchemaName), "packageUId": packageID,
		"useFullHierarchy": true, "cultures": []string{businessRuleDefaultCulture}})
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", "", err
	}
	var response struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
		Schema *struct {
			UID          string `json:"uId"`
			ParentSchema *struct {
				UID string `json:"uId"`
			} `json:"parentSchema"`
		} `json:"schema"`
	}
	if strings.TrimSpace(string(payload)) == "" {
		return "", "", errors.New("GetSchemaDesignItem returned an empty response.")
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", "", fmt.Errorf("GetSchemaDesignItem returned invalid JSON: %w", err)
	}
	if !response.Success {
		if response.ErrorInfo != nil && strings.TrimSpace(response.ErrorInfo.Message) != "" {
			return "", "", errors.New(response.ErrorInfo.Message)
		}
		return "", "", errors.New("GetSchemaDesignItem failed.")
	}
	if response.Schema == nil {
		return "", "", fmt.Errorf("Object (entity schema) '%s' not found in package '%s'. The object must be visible from that package — if it lives elsewhere, add a package dependency.", entitySchemaName, packageName)
	}
	parentUID := emptyGUID
	if response.Schema.ParentSchema != nil {
		parentUID = guidOrEmpty(response.Schema.ParentSchema.UID)
	}
	return guidOrEmpty(response.Schema.UID), parentUID, nil
}

// relatedAddonSchema returns the add-on schema object of AddonSchemaDesignerService.GetSchema.
func (c *Client) relatedAddonSchema(ctx context.Context, request map[string]any) (*jnode, error) {
	body, _ := json.Marshal(request)
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/AddonSchemaDesignerService.svc/GetSchema", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return nil, errors.New("AddonSchemaDesignerService returned an empty response.")
	}
	root, err := parseJNode(payload)
	if err != nil {
		return nil, fmt.Errorf("AddonSchemaDesignerService returned invalid JSON: %v", err)
	}
	if success := processDescribeProperty(root, "success"); success == nil || success.kind != jkBool || !success.flag {
		if info := processDescribeProperty(root, "errorInfo"); info.isObject() {
			if message := processDescribeProperty(info, "message"); message != nil && message.kind == jkString {
				return nil, errors.New(message.text)
			}
		}
		return nil, errors.New("AddonSchemaDesignerService.GetSchema failed.")
	}
	schema := processDescribeProperty(root, "schema")
	if !schema.isObject() {
		return nil, errors.New("AddonSchemaDesignerService did not return a schema payload.")
	}
	return schema, nil
}

// relatedAddonPages decodes the add-on MetaData tolerantly: a malformed body yields no pages, and each
// distinct page UId is resolved to its name once.
func (c *Client) relatedAddonPages(ctx context.Context, metaData string) ([]RelatedPageAddonEntry, *string) {
	pages := []RelatedPageAddonEntry{}
	if strings.TrimSpace(metaData) == "" {
		return pages, nil
	}
	root, err := parseJNode([]byte(metaData))
	if err != nil || !root.isObject() {
		root = newObject()
	}
	typeColumnUID := relatedAddonString(root, "TypeColumnUId")
	items := root.get("Pages")
	if !items.isArray() {
		return pages, typeColumnUID
	}
	names := map[string]*string{}
	for _, item := range items.items {
		if !item.isObject() {
			continue
		}
		uid := relatedAddonString(item, "PageSchemaUId")
		if uid == nil || strings.TrimSpace(*uid) == "" {
			continue
		}
		if _, seen := names[strings.ToLower(*uid)]; !seen {
			names[strings.ToLower(*uid)] = c.relatedAddonPageName(ctx, *uid)
		}
	}
	for _, item := range items.items {
		if !item.isObject() {
			continue
		}
		uid := relatedAddonString(item, "PageSchemaUId")
		role := relatedAddonString(item, "Role")
		entry := RelatedPageAddonEntry{PageSchemaUID: uid, IsDefault: relatedAddonBool(item, "IsDefault"),
			IsSspDefault: relatedAddonBool(item, "IsSspDefault"), Role: role, TypeColumnValue: relatedAddonString(item, "TypeColumnValue")}
		if actions := item.get("Actions"); actions.isObject() {
			entry.IsAdd = relatedAddonBool(actions, "Add")
		}
		if uid != nil {
			entry.PageSchemaName = names[strings.ToLower(*uid)]
		}
		if role != nil && strings.TrimSpace(*role) != "" {
			if name, ok := relatedAddonRoleNames[strings.ToLower(*role)]; ok {
				entry.RoleName = &name
			}
		}
		pages = append(pages, entry)
	}
	return pages, typeColumnUID
}

// relatedAddonPageName is clio's QueryPageSchemaNameByUId: the client-unit schema with that UId, or nil.
func (c *Client) relatedAddonPageName(ctx context.Context, uid string) *string {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"Name": "Name"}, map[string]any{
		"byUId":     comparisonFilter("UId", uid, 0, 3),
		"byManager": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
	}, 1))
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rowTextPointer(rows[0], "Name")
}

// relatedAddonString reads a scalar member: a JSON string as itself, another scalar as its JSON text, and an
// absent, null or container member as nil.
func relatedAddonString(object *jnode, key string) *string {
	value := object.get(key)
	if value == nil || value.kind == jkNull || value.kind == jkObject || value.kind == jkArray {
		return nil
	}
	if value.kind == jkString {
		text := value.text
		return &text
	}
	text := string(value.stjJSON())
	return &text
}

// relatedAddonBool reads a flag stored as a JSON bool or as the string "true"/"false"; anything else is false.
func relatedAddonBool(object *jnode, key string) bool {
	value := object.get(key)
	if value == nil {
		return false
	}
	switch value.kind {
	case jkBool:
		return value.flag
	case jkString:
		return strings.EqualFold(strings.TrimSpace(value.text), "true")
	}
	return false
}
