package creatio

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	businessRuleAddonName       = "BusinessRule"
	entitySchemaManagerName     = "EntitySchemaManager"
	clientUnitSchemaManagerName = "ClientUnitSchemaManager"
	emptyGUID                   = "00000000-0000-0000-0000-000000000000"
	businessRuleDefaultCulture  = "en-US"

	brConditionTypeName      = "Terrasoft.Core.BusinessRules.Models.Conditions.BusinessRuleCondition"
	brGroupConditionTypeName = "Terrasoft.Core.BusinessRules.Models.Conditions.BusinessRuleGroupCondition"
	brAttributeExpression    = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleAttributeExpression"
	brValueExpression        = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleValueExpression"
	brEmptyValueExpression   = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleEmptyValueExpression"
	brSysValueExpression     = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleSysValueExpression"
	brSysSettingExpression   = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleSysSettingExpression"
	brFormulaExpression      = "Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleFormulaExpression"
	brActionPrefix           = "Terrasoft.Core.BusinessRules.Models.Actions."
)

// Field-selection actions keyed by their persisted typeName suffix.
var brFieldSelectionActions = map[string]string{
	brActionPrefix + "BusinessRuleActionEditableElement": "make-editable",
	brActionPrefix + "BusinessRuleActionReadonlyElement": "make-read-only",
	brActionPrefix + "BusinessRuleActionRequiredElement": "make-required",
	brActionPrefix + "BusinessRuleActionOptionalElement": "make-optional",
	brActionPrefix + "BusinessRuleActionHideElement":     "hide-element",
	brActionPrefix + "BusinessRuleActionShowElement":     "show-element",
}

// brComparisonNames is clio's SupportedComparisonTypeValues inverted.
var brComparisonNames = map[int]string{
	0: "is-not-filled-in", 1: "is-filled-in", 2: "equal", 3: "not-equal", 5: "less-than",
	6: "less-than-or-equal", 7: "greater-than", 8: "greater-than-or-equal", 11: "contain", 12: "not-contain",
}

// BusinessRulesReadResponse is clio's read envelope. It deliberately has no success flag: clio reports a
// failed read only through error.
type BusinessRulesReadResponse struct {
	Count int            `json:"count"`
	Rules []BusinessRule `json:"rules"`
	Error string         `json:"error,omitempty"`
}

type BusinessRule struct {
	Caption   string                     `json:"caption"`
	Condition BusinessRuleConditionGroup `json:"condition"`
	Actions   []any                      `json:"actions"`
	Name      string                     `json:"name"`
	Enabled   bool                       `json:"enabled"`
}

type BusinessRuleConditionGroup struct {
	LogicalOperation string                  `json:"logicalOperation"`
	Conditions       []BusinessRuleCondition `json:"conditions"`
}

type BusinessRuleCondition struct {
	LeftExpression  BusinessRuleExpression  `json:"leftExpression"`
	ComparisonType  string                  `json:"comparisonType"`
	RightExpression *BusinessRuleExpression `json:"rightExpression,omitempty"`
	UID             *string                 `json:"uId,omitempty"`
}

type BusinessRuleExpression struct {
	Type           string          `json:"type"`
	Path           *string         `json:"path,omitempty"`
	Value          json.RawMessage `json:"value,omitempty"`
	Expression     *string         `json:"expression,omitempty"`
	SysValueName   *string         `json:"sysValueName,omitempty"`
	SysSettingName *string         `json:"sysSettingName,omitempty"`
	UID            *string         `json:"uId,omitempty"`
}

type businessRuleFieldAction struct {
	Type  string   `json:"type"`
	Items []string `json:"items"`
	UID   *string  `json:"uId,omitempty"`
}

type businessRuleSetValuesAction struct {
	Type  string                     `json:"type"`
	Items []businessRuleSetValueItem `json:"items"`
	UID   *string                    `json:"uId,omitempty"`
}

type businessRuleSetValueItem struct {
	Expression BusinessRuleExpression `json:"expression"`
	Value      BusinessRuleExpression `json:"value"`
	UID        *string                `json:"uId,omitempty"`
}

type businessRuleApplyFilterAction struct {
	Type             string  `json:"type"`
	Target           string  `json:"target"`
	TargetFilterPath string  `json:"targetFilterPath"`
	Source           string  `json:"source"`
	SourceFilterPath *string `json:"sourceFilterPath,omitempty"`
	ClearValue       bool    `json:"clearValue"`
	PopulateValue    bool    `json:"populateValue"`
	UID              *string `json:"uId,omitempty"`
}

type businessRuleApplyStaticFilterAction struct {
	Type            string         `json:"type"`
	TargetAttribute string         `json:"targetAttribute"`
	Filter          *orderedObject `json:"filter"`
	UID             *string        `json:"uId,omitempty"`
}

type BusinessRulesReadRequest struct {
	PackageName string
	SchemaName  string
}

// ReadEntityBusinessRules reads the BusinessRule add-on of an entity schema with the full package
// hierarchy and converts every persisted rule to clio's create/update contract.
func (c *Client) ReadEntityBusinessRules(ctx context.Context, input BusinessRulesReadRequest) BusinessRulesReadResponse {
	if message := missingBusinessRuleFields(input.PackageName, input.SchemaName, "entity-schema-name"); message != "" {
		return businessRulesReadError(message)
	}
	packageUID, err := c.resolvePackageUID(ctx, input.PackageName)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	schemaUID, parentUID, err := c.entityDesignSchemaUIDs(ctx, input.SchemaName, packageUID)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	return c.readBusinessRuleAddon(ctx, map[string]any{
		"addonName": businessRuleAddonName, "targetSchemaUId": schemaUID, "targetParentSchemaUId": parentUID,
		"targetPackageUId": packageUID, "targetSchemaManagerName": entitySchemaManagerName, "useFullHierarchy": true,
	})
}

// ReadPageBusinessRules reads the BusinessRule add-on of a Freedom UI page. Like clio, the page is passed
// as the add-on PARENT with a fresh target uId, so the platform resolves the add-on through the page.
func (c *Client) ReadPageBusinessRules(ctx context.Context, input BusinessRulesReadRequest) BusinessRulesReadResponse {
	if message := missingBusinessRuleFields(input.PackageName, input.SchemaName, "page-schema-name"); message != "" {
		return businessRulesReadError(message)
	}
	packageUID, err := c.resolvePackageUID(ctx, input.PackageName)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	pageUID, err := c.pageSchemaUID(ctx, strings.TrimSpace(input.SchemaName), packageUID)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	return c.readBusinessRuleAddon(ctx, map[string]any{
		"addonName": businessRuleAddonName, "targetSchemaUId": newGUID(), "targetParentSchemaUId": pageUID,
		"targetPackageUId": packageUID, "targetSchemaManagerName": clientUnitSchemaManagerName, "useFullHierarchy": true,
	})
}

func businessRulesReadError(message string) BusinessRulesReadResponse {
	return BusinessRulesReadResponse{Rules: []BusinessRule{}, Error: message}
}

// missingBusinessRuleFields names every missing target field in one message, as clio does.
func missingBusinessRuleFields(packageName, schemaName, schemaField string) string {
	missing := []string{}
	if strings.TrimSpace(packageName) == "" {
		missing = append(missing, "package-name")
	}
	if strings.TrimSpace(schemaName) == "" {
		missing = append(missing, schemaField)
	}
	switch len(missing) {
	case 0:
		return ""
	case 1:
		return missing[0] + " is required."
	default:
		return strings.Join(missing, ", ") + " are required."
	}
}

// resolvePackageUID mirrors clio's BusinessRulePackageResolver: the first SysPackage whose name equals the
// trimmed input, ignoring case.
func (c *Client) resolvePackageUID(ctx context.Context, packageName string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"Name": "Name", "UId": "UId"}, nil, -1))
	if err != nil {
		return "", err
	}
	wanted := strings.TrimSpace(packageName)
	for _, row := range rows {
		if strings.EqualFold(rowString(row, "Name"), wanted) {
			if uid := normalizeGUID(rowString(row, "UId")); uid != "" {
				return uid, nil
			}
			return emptyGUID, nil
		}
	}
	return "", fmt.Errorf("Package '%s' was not found.", packageName)
}

func (c *Client) entityDesignSchemaUIDs(ctx context.Context, schemaName, packageUID string) (string, string, error) {
	body, err := json.Marshal(map[string]any{
		"name": strings.TrimSpace(schemaName), "packageUId": packageUID, "useFullHierarchy": true,
		"cultures": []string{businessRuleDefaultCulture},
	})
	if err != nil {
		return "", "", err
	}
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
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", "", fmt.Errorf("GetSchemaDesignItem returned invalid JSON: %w", err)
	}
	if !response.Success {
		if response.ErrorInfo != nil && strings.TrimSpace(response.ErrorInfo.Message) != "" {
			return "", "", fmt.Errorf("%s", response.ErrorInfo.Message)
		}
		return "", "", fmt.Errorf("GetSchemaDesignItem failed.")
	}
	if response.Schema == nil {
		return "", "", fmt.Errorf("Entity schema '%s' was not returned.", schemaName)
	}
	parentUID := emptyGUID
	if response.Schema.ParentSchema != nil {
		parentUID = guidOrEmpty(response.Schema.ParentSchema.UID)
	}
	return guidOrEmpty(response.Schema.UID), parentUID, nil
}

// pageSchemaUID resolves the page uId exactly as clio's PageBusinessRuleSchemaProvider: the schema in the
// target package, else the root of the page's design hierarchy, then the first entry of the hierarchy as
// seen from the target package.
func (c *Client) pageSchemaUID(ctx context.Context, schemaName, packageUID string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"UId": "UId"}, map[string]any{
		"byName":    comparisonFilter("Name", schemaName, 1, 3),
		"byManager": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
		"byPackage": comparisonFilter("SysPackage.UId", packageUID, 0, 3),
	}, 1))
	if err != nil {
		return "", fmt.Errorf("Failed to query schema metadata in target package.")
	}
	schemaUID := ""
	if len(rows) > 0 {
		schemaUID = rowString(rows[0], "UId")
	}
	if strings.TrimSpace(schemaUID) == "" {
		if schemaUID, err = c.rootPageSchemaUID(ctx, schemaName, packageUID); err != nil {
			return "", err
		}
	}
	hierarchy, err := c.pageParentSchemas(ctx, schemaUID, packageUID)
	if err != nil {
		return "", err
	}
	if len(hierarchy) == 0 {
		return "", fmt.Errorf("Page schema '%s' hierarchy is empty.", schemaName)
	}
	return guidOrEmpty(hierarchy[0].UID), nil
}

func (c *Client) rootPageSchemaUID(ctx context.Context, schemaName, packageUID string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"UId": "UId", "PackageUId": "SysPackage.UId"}, map[string]any{
		"filter0": comparisonFilter("Name", schemaName, 1, 3),
		"filter1": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
	}, 1))
	if err != nil {
		return "", fmt.Errorf("Failed to query schema metadata")
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("Schema '%s' not found", schemaName)
	}
	schemaUID := rowString(rows[0], "UId")
	if strings.TrimSpace(schemaUID) == "" {
		return "", fmt.Errorf("Page schema '%s' metadata is missing schema UId.", schemaName)
	}
	designPackageUID, err := c.designPackageUID(ctx, schemaUID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(designPackageUID) == "" {
		designPackageUID = rowString(rows[0], "PackageUId")
		if strings.TrimSpace(designPackageUID) == "" {
			designPackageUID = packageUID
		}
	}
	hierarchy, err := c.pageParentSchemas(ctx, schemaUID, designPackageUID)
	if err != nil {
		return "", err
	}
	for index := len(hierarchy) - 1; index >= 0; index-- {
		if strings.EqualFold(hierarchy[index].Name, schemaName) {
			return hierarchy[index].UID, nil
		}
	}
	return schemaUID, nil
}

type pageHierarchySchema struct {
	UID  string
	Name string
}

func (c *Client) designPackageUID(ctx context.Context, schemaUID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "userLevelSchema": false})
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", fmt.Errorf("GetDesignPackageUId returned invalid JSON: %w", err)
	}
	if !rawBool(response["success"]) {
		return "", fmt.Errorf("%s", designerFailure("Failed to resolve design package", response))
	}
	uid := rawText(response["uId"])
	if strings.TrimSpace(uid) == "" {
		return "", fmt.Errorf("Design package response did not return a uId")
	}
	return uid, nil
}

func (c *Client) pageParentSchemas(ctx context.Context, schemaUID, packageUID string) ([]pageHierarchySchema, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "packageUId": packageUID, "useFullHierarchy": true, "userLevelSchema": false})
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("GetParentSchemas returned invalid JSON: %w", err)
	}
	if !rawBool(response["success"]) {
		return nil, fmt.Errorf("%s", designerFailure("Failed to load page schema hierarchy", response))
	}
	var values []map[string]json.RawMessage
	if err := json.Unmarshal(response["values"], &values); err != nil || response["values"] == nil || string(response["values"]) == "null" {
		return nil, fmt.Errorf("Page schema hierarchy response does not contain values")
	}
	result := make([]pageHierarchySchema, 0, len(values))
	for _, value := range values {
		uid := rawText(value["uId"])
		if uid == "" {
			uid = rawText(value["id"])
		}
		result = append(result, pageHierarchySchema{UID: uid, Name: rawText(value["name"])})
	}
	return result, nil
}

func designerFailure(prefix string, response map[string]json.RawMessage) string {
	for _, key := range []string{"errorInfo", "message", "error"} {
		if raw := response[key]; len(raw) > 0 && string(raw) != "null" {
			detail := rawText(raw)
			if detail == "" {
				detail = string(raw)
			}
			return prefix + ": " + detail
		}
	}
	return prefix
}

func rawBool(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

// rawText renders a scalar the way Newtonsoft's JToken.ToString does for strings and numbers.
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func (c *Client) readBusinessRuleAddon(ctx context.Context, request map[string]any) BusinessRulesReadResponse {
	body, err := json.Marshal(request)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/AddonSchemaDesignerService.svc/GetSchema", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	var response struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
		Schema *struct {
			MetaData  string `json:"metaData"`
			Resources []struct {
				Key   string `json:"key"`
				Value []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"value"`
			} `json:"resources"`
		} `json:"schema"`
	}
	if strings.TrimSpace(string(payload)) == "" {
		return businessRulesReadError("AddonSchemaDesignerService returned an empty response.")
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return businessRulesReadError(fmt.Sprintf("AddonSchemaDesignerService returned invalid JSON: %v", err))
	}
	if !response.Success {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			return businessRulesReadError(*response.ErrorInfo.Message)
		}
		return businessRulesReadError("AddonSchemaDesignerService.GetSchema failed.")
	}
	if response.Schema == nil {
		return businessRulesReadError("AddonSchemaDesignerService did not return a schema payload.")
	}
	resources := make([]addonResource, 0, len(response.Schema.Resources))
	for _, resource := range response.Schema.Resources {
		item := addonResource{key: normalizeAddonResourceKey(resource.Key)}
		for _, value := range resource.Value {
			item.values = append(item.values, [2]string{value.Key, value.Value})
		}
		resources = append(resources, item)
	}
	rules, err := convertBusinessRules(response.Schema.MetaData, resources)
	if err != nil {
		return businessRulesReadError(err.Error())
	}
	return BusinessRulesReadResponse{Count: len(rules), Rules: rules}
}

type addonResource struct {
	key    string
	values [][2]string
}

// normalizeAddonResourceKey turns "AddonConfig.Rules.{guid}.Caption" into "{guid}.Caption", as clio does.
func normalizeAddonResourceKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) == 4 && parts[0] == "AddonConfig" && parts[1] == "Rules" {
		return parts[2] + "." + parts[3]
	}
	return key
}

// convertBusinessRules is clio's FullToSimpleBusinessRuleConverter over the add-on metadata JSON.
func convertBusinessRules(metaData string, resources []addonResource) ([]BusinessRule, error) {
	rules := []BusinessRule{}
	if strings.TrimSpace(metaData) == "" {
		return rules, nil
	}
	parsed, err := parseOrderedJSON([]byte(metaData))
	if err != nil {
		return nil, fmt.Errorf("Business-rule add-on metadata is not valid JSON.")
	}
	metadata, ok := parsed.(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("Business-rule add-on metadata root must be a JSON object.")
	}
	rawRules := metadata.get("rules")
	if rawRules == nil {
		return rules, nil
	}
	ruleNodes, ok := rawRules.([]any)
	if !ok {
		return nil, fmt.Errorf("Business-rule add-on metadata 'rules' property must be a JSON array.")
	}
	children := map[string][]*orderedObject{}
	parents := []*orderedObject{}
	for _, node := range ruleNodes {
		rule, ok := node.(*orderedObject)
		if !ok {
			continue
		}
		if parent, _ := jsonString(rule, "parentUId"); strings.TrimSpace(parent) != "" {
			key := strings.ToUpper(parent)
			children[key] = append(children[key], rule)
			continue
		}
		parents = append(parents, rule)
	}
	for _, rule := range parents {
		name, _ := jsonString(rule, "name")
		uid, _ := jsonString(rule, "uId")
		caption := jsonStringOrNil(rule, "caption")
		if caption == nil {
			caption = captionResource(resources, uid)
		}
		converted, err := convertBusinessRule(rule, caption, name, jsonBool(rule, "enabled", true), children[strings.ToUpper(uid)])
		if err != nil {
			return nil, fmt.Errorf("Business rule '%s' cannot be represented in the rule contract: %v", name, err)
		}
		rules = append(rules, converted)
	}
	return rules, nil
}

func captionResource(resources []addonResource, ruleUID string) *string {
	if strings.TrimSpace(ruleUID) == "" {
		return nil
	}
	key := ruleUID + ".Caption"
	for _, resource := range resources {
		if !strings.EqualFold(resource.key, key) {
			continue
		}
		if len(resource.values) == 0 {
			return nil
		}
		for _, value := range resource.values {
			if strings.EqualFold(value[0], businessRuleDefaultCulture) {
				return &value[1]
			}
		}
		return &resource.values[0][1]
	}
	return nil
}

func convertBusinessRule(rule *orderedObject, caption *string, name string, enabled bool, children []*orderedObject) (BusinessRule, error) {
	cases, ok := jsonArrayAt(rule, "cases")
	if !ok || len(cases) != 1 {
		return BusinessRule{}, fmt.Errorf("only single-case rules are supported.")
	}
	caseObject, ok := cases[0].(*orderedObject)
	if !ok {
		return BusinessRule{}, fmt.Errorf("only single-case rules are supported.")
	}
	condition, err := convertConditionGroup(caseObject.get("condition"))
	if err != nil {
		return BusinessRule{}, err
	}
	actionNodes, ok := jsonArrayAt(caseObject, "actions")
	if !ok {
		return BusinessRule{}, fmt.Errorf("Business-rule case has no actions array.")
	}
	actions := make([]any, 0, len(actionNodes))
	for _, node := range actionNodes {
		action, ok := node.(*orderedObject)
		if !ok {
			return BusinessRule{}, fmt.Errorf("Business-rule action must be a JSON object.")
		}
		converted, err := convertBusinessRuleAction(action, children)
		if err != nil {
			return BusinessRule{}, err
		}
		actions = append(actions, converted)
	}
	result := BusinessRule{Condition: condition, Actions: actions, Name: name, Enabled: enabled}
	if caption != nil {
		result.Caption = *caption
	}
	return result, nil
}

func convertConditionGroup(node any) (BusinessRuleConditionGroup, error) {
	if node == nil {
		return BusinessRuleConditionGroup{LogicalOperation: "AND", Conditions: []BusinessRuleCondition{}}, nil
	}
	object, ok := node.(*orderedObject)
	if !ok {
		return BusinessRuleConditionGroup{}, fmt.Errorf("Business-rule condition must be a JSON object.")
	}
	typeName, _ := jsonString(object, "typeName")
	if typeName == brConditionTypeName {
		condition, err := convertCondition(object)
		if err != nil {
			return BusinessRuleConditionGroup{}, err
		}
		return BusinessRuleConditionGroup{LogicalOperation: "AND", Conditions: []BusinessRuleCondition{condition}}, nil
	}
	if typeName != brGroupConditionTypeName {
		return BusinessRuleConditionGroup{}, fmt.Errorf("Unsupported business-rule condition typeName '%s'.", typeName)
	}
	logical := jsonInt(object, "logicalOperation", 1)
	var logicalName string
	switch logical {
	case 1:
		logicalName = "AND"
	case 2:
		logicalName = "OR"
	default:
		return BusinessRuleConditionGroup{}, fmt.Errorf("Unsupported logicalOperation '%d'.", logical)
	}
	conditions := []BusinessRuleCondition{}
	if nested, ok := jsonArrayAt(object, "conditions"); ok {
		for _, item := range nested {
			nestedObject, ok := item.(*orderedObject)
			if !ok {
				return BusinessRuleConditionGroup{}, fmt.Errorf("Business-rule group condition entries must be JSON objects.")
			}
			nestedType, _ := jsonString(nestedObject, "typeName")
			if nestedType != brConditionTypeName {
				return BusinessRuleConditionGroup{}, fmt.Errorf("Unsupported nested condition typeName '%s'.", nestedType)
			}
			condition, err := convertCondition(nestedObject)
			if err != nil {
				return BusinessRuleConditionGroup{}, err
			}
			conditions = append(conditions, condition)
		}
	}
	return BusinessRuleConditionGroup{LogicalOperation: logicalName, Conditions: conditions}, nil
}

func convertCondition(object *orderedObject) (BusinessRuleCondition, error) {
	comparison := jsonInt(object, "comparisonType", 0)
	comparisonName, ok := brComparisonNames[comparison]
	if !ok {
		return BusinessRuleCondition{}, fmt.Errorf("Unsupported comparisonType '%d'.", comparison)
	}
	leftObject := jsonObjectAt(object, "leftExpression")
	if leftObject == nil {
		return BusinessRuleCondition{}, fmt.Errorf("Business-rule condition has no leftExpression.")
	}
	left, err := convertExpression(leftObject)
	if err != nil {
		return BusinessRuleCondition{}, err
	}
	condition := BusinessRuleCondition{LeftExpression: left, ComparisonType: comparisonName, UID: jsonStringOrNil(object, "uId")}
	if rightObject := jsonObjectAt(object, "rightExpression"); rightObject != nil {
		right, err := convertExpression(rightObject)
		if err != nil {
			return BusinessRuleCondition{}, err
		}
		condition.RightExpression = &right
	}
	return condition, nil
}

func convertExpression(object *orderedObject) (BusinessRuleExpression, error) {
	typeName, _ := jsonString(object, "typeName")
	kind, _ := jsonString(object, "type")
	uid := jsonStringOrNil(object, "uId")
	switch {
	case typeName == brAttributeExpression || strings.EqualFold(kind, "AttributeValue"):
		return BusinessRuleExpression{Type: "AttributeValue", Path: attributePath(object), UID: uid}, nil
	case typeName == brSysValueExpression || strings.EqualFold(kind, "SysValue"):
		return BusinessRuleExpression{Type: "SysValue", SysValueName: jsonStringOrNil(object, "sysValueName"), UID: uid}, nil
	case typeName == brSysSettingExpression || strings.EqualFold(kind, "SysSetting"):
		return BusinessRuleExpression{Type: "SysSetting", SysSettingName: jsonStringOrNil(object, "sysSettingName"), UID: uid}, nil
	case typeName == brFormulaExpression || strings.EqualFold(kind, "Formula"):
		return BusinessRuleExpression{Type: "Formula", Expression: jsonStringOrNil(jsonObjectAt(object, "expressionSchema"), "expression"), UID: uid}, nil
	case typeName == brValueExpression || typeName == brEmptyValueExpression || strings.EqualFold(kind, "Const"):
		expression := BusinessRuleExpression{Type: "Const", UID: uid}
		if value := object.get("value"); value != nil {
			raw, err := rawJSON(value)
			if err != nil {
				return BusinessRuleExpression{}, err
			}
			expression.Value = raw
		}
		return expression, nil
	}
	return BusinessRuleExpression{}, fmt.Errorf("Unsupported business-rule expression shape (typeName '%s', type '%s').", typeName, kind)
}

func attributePath(object *orderedObject) *string {
	path := jsonStringOrNil(object, "path")
	scope, _ := jsonString(object, "scopeId")
	if scope == "" || path == nil || *path == "" {
		return path
	}
	combined := scope + "." + *path
	return &combined
}

func convertBusinessRuleAction(action *orderedObject, children []*orderedObject) (any, error) {
	typeName, _ := jsonString(action, "typeName")
	uid := jsonStringOrNil(action, "uId")
	if actionType, ok := brFieldSelectionActions[typeName]; ok {
		items, err := actionItemNames(action)
		if err != nil {
			return nil, err
		}
		return businessRuleFieldAction{Type: actionType, Items: items, UID: uid}, nil
	}
	switch typeName {
	case brActionPrefix + "BusinessRuleActionSetValues":
		itemNodes, ok := jsonArrayAt(action, "items")
		if !ok {
			return nil, fmt.Errorf("set-values action has no items array.")
		}
		items := make([]businessRuleSetValueItem, 0, len(itemNodes))
		for _, node := range itemNodes {
			item, ok := node.(*orderedObject)
			if !ok {
				return nil, fmt.Errorf("set-values items must be JSON objects.")
			}
			target := jsonObjectAt(item, "expression")
			if target == nil {
				return nil, fmt.Errorf("set-values item has no expression.")
			}
			expression, err := convertExpression(target)
			if err != nil {
				return nil, err
			}
			source := jsonObjectAt(item, "value")
			if source == nil {
				return nil, fmt.Errorf("set-values item has no value.")
			}
			value, err := convertExpression(source)
			if err != nil {
				return nil, err
			}
			items = append(items, businessRuleSetValueItem{Expression: expression, Value: value, UID: jsonStringOrNil(item, "uId")})
		}
		return businessRuleSetValuesAction{Type: "set-values", Items: items, UID: uid}, nil
	case brActionPrefix + "BusinessRuleActionFilterLookup":
		left := jsonObjectAt(action, "leftExpression")
		if left == nil {
			return nil, fmt.Errorf("apply-filter action has no leftExpression.")
		}
		right := jsonObjectAt(action, "rightExpression")
		if right == nil {
			return nil, fmt.Errorf("apply-filter action has no rightExpression.")
		}
		target, _ := jsonString(left, "path")
		source, _ := jsonString(right, "path")
		targetFilter := filterLookupExpression(left)
		result := businessRuleApplyFilterAction{
			Type: "apply-filter", Target: target, Source: source, SourceFilterPath: filterLookupExpression(right),
			ClearValue:    jsonBool(action, "clearValue", false) || hasChildRuleSuffix(children, "_ClearValue"),
			PopulateValue: jsonBool(action, "populateValue", false) || hasChildRuleSuffix(children, "_PopulateValue"),
			UID:           uid,
		}
		if targetFilter != nil {
			result.TargetFilterPath = *targetFilter
		}
		return result, nil
	case brActionPrefix + "BusinessRuleActionSetFilter":
		expression := jsonObjectAt(action, "expression")
		if expression == nil {
			return nil, fmt.Errorf("apply-static-filter action has no expression.")
		}
		envelope, _ := jsonString(jsonObjectAt(action, "value"), "value")
		filter, err := decompileStaticFilter(envelope)
		if err != nil {
			return nil, err
		}
		target, _ := jsonString(expression, "path")
		return businessRuleApplyStaticFilterAction{Type: "apply-static-filter", TargetAttribute: target, Filter: filter, UID: uid}, nil
	}
	return nil, fmt.Errorf("Unsupported business-rule action typeName '%s'.", typeName)
}

func filterLookupExpression(expression *orderedObject) *string {
	value, _ := jsonString(expression, "filterExpression")
	if strings.TrimSpace(value) == "" || strings.EqualFold(value, "null") {
		return nil
	}
	return &value
}

func hasChildRuleSuffix(children []*orderedObject, suffix string) bool {
	for _, child := range children {
		name, _ := jsonString(child, "name")
		if strings.HasSuffix(strings.ToUpper(name), strings.ToUpper(suffix)) {
			return true
		}
	}
	return false
}

func actionItemNames(action *orderedObject) ([]string, error) {
	switch items := action.get("items").(type) {
	case nil:
		return []string{}, nil
	case string:
		names := []string{}
		for _, part := range strings.Split(items, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				names = append(names, trimmed)
			}
		}
		return names, nil
	case []any:
		names := []string{}
		for _, entry := range items {
			if entry == nil {
				continue
			}
			text, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("Unsupported business-rule action items shape.")
			}
			if strings.TrimSpace(text) != "" {
				names = append(names, text)
			}
		}
		return names, nil
	default:
		return nil, fmt.Errorf("Unsupported business-rule action items shape.")
	}
}

// normalizeGUID renders a GUID the way .NET Guid.ToString() does: lower case, no braces. Text that is
// not a GUID returns "".
func normalizeGUID(value string) string {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "{}"))
	if len(value) == 32 {
		value = value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
	}
	if len(value) != 36 {
		return ""
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return ""
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", char) {
			return ""
		}
	}
	return value
}

func guidOrEmpty(value string) string {
	if normalized := normalizeGUID(value); normalized != "" {
		return normalized
	}
	return emptyGUID
}

func newGUID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
