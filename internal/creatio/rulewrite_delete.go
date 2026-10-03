package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type RuleWriteResult struct {
	Name     string `json:"name"`
	Success  bool   `json:"success"`
	RuleName string `json:"ruleName,omitempty"`
	Error    string `json:"error,omitempty"`
}
type RuleWriteResponse struct {
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Results   []RuleWriteResult `json:"results"`
	Error     string            `json:"error,omitempty"`
}

func RuleWriteRequestError(message string) RuleWriteResponse {
	return RuleWriteResponse{Results: []RuleWriteResult{}, Error: message}
}

// DeleteBusinessRules applies a batch to a full-hierarchy add-on and saves it once.
// The platform's SaveSchema computes the layered removal in the requested package.
func (c *Client) DeleteBusinessRules(ctx context.Context, page bool, input BusinessRulesReadRequest, names []string) RuleWriteResponse {
	field := "entity-schema-name"
	if page {
		field = "page-schema-name"
	}
	if message := missingBusinessRuleFields(input.PackageName, input.SchemaName, field); message != "" {
		return RuleWriteRequestError(message)
	}
	if len(names) == 0 {
		return RuleWriteRequestError("rule-names is required and must contain at least one rule name.")
	}
	request, err := c.ruleWriteAddonRequest(ctx, page, input)
	if err != nil {
		return RuleWriteRequestError(err.Error())
	}
	schema, metadata, resources, err := c.ruleWriteLoadAddon(ctx, request)
	if err != nil {
		return RuleWriteRequestError(err.Error())
	}
	rules, ok := metadata["rules"].([]any)
	if !ok {
		return RuleWriteRequestError("Business-rule add-on metadata 'rules' property must be a JSON array.")
	}
	result := RuleWriteResponse{Results: make([]RuleWriteResult, len(names))}
	pending := []int{}
	for i, name := range names {
		item := RuleWriteResult{Name: name}
		index := -1
		if strings.TrimSpace(name) == "" {
			item.Error = "Business rule name is required."
		} else {
			for j, node := range rules {
				rule, _ := node.(map[string]any)
				if rule == nil || ruleWriteString(rule, "parentUId") != "" || !strings.EqualFold(ruleWriteString(rule, "name"), name) {
					continue
				}
				if index >= 0 {
					item.Error = fmt.Sprintf("Business rule name '%s' matches more than one rule on the schema; rename the duplicates in the Creatio designer before using name-based operations.", name)
					break
				}
				index = j
			}
			if item.Error == "" && index < 0 {
				item.Error = fmt.Sprintf("Business rule '%s' was not found.", name)
			}
		}
		if item.Error != "" {
			result.Results[i] = item
			continue
		}
		rule := rules[index].(map[string]any)
		uid := ruleWriteString(rule, "uId")
		rules = append(rules[:index], rules[index+1:]...)
		if uid != "" {
			resources = ruleWriteRemoveCaption(resources, uid)
			retained := []any{}
			for _, node := range rules {
				child, _ := node.(map[string]any)
				if child != nil && strings.EqualFold(ruleWriteString(child, "parentUId"), uid) {
					resources = ruleWriteRemoveCaption(resources, ruleWriteString(child, "uId"))
					continue
				}
				retained = append(retained, node)
			}
			rules = retained
		}
		result.Results[i] = item
		pending = append(pending, i)
	}
	if len(pending) > 0 {
		metadata["rules"] = rules
		err = c.ruleWriteSaveAddon(ctx, schema, metadata, resources)
		for _, i := range pending {
			if err != nil {
				result.Results[i].Error = err.Error()
			} else {
				result.Results[i].Success = true
				result.Results[i].RuleName = names[i]
			}
		}
	}
	for _, item := range result.Results {
		if item.Success {
			result.Succeeded++
		} else {
			result.Failed++
		}
	}
	return result
}
func ruleWriteString(object map[string]any, key string) string {
	s, _ := object[key].(string)
	return s
}
func ruleWriteRemoveCaption(resources []any, uid string) []any {
	result := []any{}
	for _, node := range resources {
		r, _ := node.(map[string]any)
		if r != nil && strings.EqualFold(ruleWriteString(r, "key"), uid+".Caption") {
			continue
		}
		result = append(result, node)
	}
	return result
}
func (c *Client) ruleWriteAddonRequest(ctx context.Context, page bool, input BusinessRulesReadRequest) (map[string]any, error) {
	pkg, err := c.resolvePackageUID(ctx, input.PackageName)
	if err != nil {
		return nil, err
	}
	manager := entitySchemaManagerName
	uid, parent, err := "", "", error(nil)
	if page {
		manager = clientUnitSchemaManagerName
		uid = newGUID()
		parent, err = c.pageSchemaUID(ctx, strings.TrimSpace(input.SchemaName), pkg)
	} else {
		uid, parent, err = c.entityDesignSchemaUIDs(ctx, input.SchemaName, pkg)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"addonName": businessRuleAddonName, "targetSchemaUId": uid, "targetParentSchemaUId": parent, "targetPackageUId": pkg, "targetSchemaManagerName": manager, "useFullHierarchy": true}, nil
}
func (c *Client) ruleWriteLoadAddon(ctx context.Context, request map[string]any) (map[string]any, map[string]any, []any, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, nil, nil, err
	}
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AddonSchemaDesignerService.svc/GetSchema", Body: body, Timeout: 45 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		return nil, nil, nil, err
	}
	var response map[string]any
	if err = ruleWriteDecodeObject(payload, &response); err != nil {
		return nil, nil, nil, err
	}
	if response["success"] != true {
		return nil, nil, nil, ruleWriteResponseError(response, "AddonSchemaDesignerService.GetSchema failed.")
	}
	schema, _ := response["schema"].(map[string]any)
	if schema == nil {
		return nil, nil, nil, fmt.Errorf("AddonSchemaDesignerService did not return a schema payload.")
	}
	metadata := map[string]any{"typeName": "Terrasoft.Core.BusinessRules.BusinessRules", "rules": []any{}}
	if text := ruleWriteString(schema, "metaData"); strings.TrimSpace(text) != "" {
		if err = ruleWriteDecodeObject([]byte(text), &metadata); err != nil {
			return nil, nil, nil, fmt.Errorf("Business-rule add-on metadata is not valid JSON.")
		}
		if metadata == nil {
			return nil, nil, nil, fmt.Errorf("Business-rule add-on metadata root must be a JSON object.")
		}
	}
	if metadata["rules"] == nil {
		metadata["rules"] = []any{}
	}
	resources, _ := schema["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}
	for _, node := range resources {
		r, _ := node.(map[string]any)
		if r == nil {
			continue
		}
		parts := strings.Split(ruleWriteString(r, "key"), ".")
		if len(parts) == 4 && parts[0] == "AddonConfig" && parts[1] == "Rules" {
			r["key"] = parts[2] + "." + parts[3]
		}
	}
	return schema, metadata, resources, nil
}
func ruleWriteResponseError(response map[string]any, fallback string) error {
	info, _ := response["errorInfo"].(map[string]any)
	if message, ok := info["message"].(string); ok {
		return fmt.Errorf("%s", message)
	}
	return fmt.Errorf("%s", fallback)
}
func (c *Client) ruleWriteSaveAddon(ctx context.Context, schema, metadata map[string]any, resources []any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	schema["metaData"] = string(encoded)
	schema["resources"] = resources
	body, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AddonSchemaDesignerService.svc/SaveSchema", Body: body, Timeout: 45 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		return err
	}
	var response map[string]any
	if err = ruleWriteDecodeObject(payload, &response); err != nil {
		return err
	}
	if response["success"] != true || response["value"] == false {
		return ruleWriteResponseError(response, "AddonSchemaDesignerService.SaveSchema failed.")
	}
	// Both refreshes are best effort after the durable save, as in clio. They must
	// not turn an already committed write into a reported failure.
	_, _ = c.serviceRequest(ctx, serviceCall{Route: "rest/WorkplaceService/ResetScriptCache", Timeout: 45 * time.Second, Limit: maxResponseBytes})
	_, _ = c.serviceRequest(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/BuildConfiguration", Timeout: 45 * time.Second, Limit: maxResponseBytes})
	return nil
}

// UseNumber keeps constants and unrelated schema extension values exact while
// the batch removes another rule (float64 would round large integer constants).
func ruleWriteDecodeObject(raw []byte, value *map[string]any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}
	return decoder.Decode(value)
}
