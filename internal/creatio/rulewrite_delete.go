package creatio

import (
	"context"
	"fmt"
	"strings"
)

// RuleWriteResult is one item of clio's BusinessRuleBatchResponse.
type RuleWriteResult struct {
	Name     string  `json:"name"`
	Success  bool    `json:"success"`
	RuleName *string `json:"ruleName,omitempty"`
	Error    *string `json:"error,omitempty"`
}

// RuleWriteResponse is clio's BusinessRuleBatchResponse: per-item results, or a request-level error with
// no items.
type RuleWriteResponse struct {
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Results   []RuleWriteResult `json:"results"`
	Error     string            `json:"error,omitempty"`
}

func RuleWriteRequestError(message string) RuleWriteResponse {
	return RuleWriteResponse{Results: []RuleWriteResult{}, Error: message}
}

func ruleWriteSuccess(name, ruleName string) RuleWriteResult {
	return RuleWriteResult{Name: name, Success: true, RuleName: &ruleName}
}

func ruleWriteFailure(name, message string) RuleWriteResult {
	return RuleWriteResult{Name: name, Error: &message}
}

// ruleWriteResponseFrom is BusinessRuleBatchResponse.From.
func ruleWriteResponseFrom(results []RuleWriteResult) RuleWriteResponse {
	response := RuleWriteResponse{Results: results}
	for _, item := range results {
		if item.Success {
			response.Succeeded++
		} else {
			response.Failed++
		}
	}
	return response
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
	addon, err := c.ruleWriteLoadAddon(ctx, request)
	if err != nil {
		return RuleWriteRequestError(err.Error())
	}
	results := make([]RuleWriteResult, len(names))
	pending := []int{}
	for i, name := range names {
		if strings.TrimSpace(name) == "" {
			results[i] = ruleWriteFailure(name, "Business rule name is required.")
			continue
		}
		index, duplicate := addon.findRule(name)
		if duplicate != "" {
			results[i] = ruleWriteFailure(name, duplicate)
			continue
		}
		if index < 0 {
			results[i] = ruleWriteFailure(name, fmt.Sprintf("Business rule '%s' was not found.", name))
			continue
		}
		rule, _ := addon.rules[index].(*orderedObject)
		uid, hasUID := jsonString(rule, "uId")
		addon.rules = append(addon.rules[:index], addon.rules[index+1:]...)
		if hasUID && strings.TrimSpace(uid) != "" {
			addon.removeCaption(uid)
			addon.removeChildren(uid)
		}
		pending = append(pending, i)
	}
	if len(pending) > 0 {
		err = c.ruleWriteSaveAddon(ctx, addon)
		for _, i := range pending {
			if err != nil {
				results[i] = ruleWriteFailure(names[i], err.Error())
			} else {
				results[i] = ruleWriteSuccess(names[i], names[i])
			}
		}
	}
	return ruleWriteResponseFrom(results)
}

// ruleWriteAddonRequest builds clio's AddonGetRequestDto for an entity (the design schema and its parent)
// or a page (a fresh target uId with the page as the parent).
func (c *Client) ruleWriteAddonRequest(ctx context.Context, page bool, input BusinessRulesReadRequest) (*orderedObject, error) {
	pkg, err := c.resolvePackageUID(ctx, input.PackageName)
	if err != nil {
		return nil, err
	}
	if page {
		context, err := c.ruleWritePageContext(ctx, input.SchemaName, pkg)
		if err != nil {
			return nil, err
		}
		return ruleWritePageAddonRequest(context.SchemaUID, pkg), nil
	}
	schema, err := c.ruleWriteEntitySchema(ctx, input.SchemaName, pkg)
	if err != nil {
		return nil, err
	}
	return ruleWriteEntityAddonRequest(schema, pkg), nil
}

func ruleWriteEntityAddonRequest(schema *ruleWriteEntitySchema, packageUID string) *orderedObject {
	return ruleWriteObject("addonName", businessRuleAddonName, "targetSchemaUId", schema.UID, "targetParentSchemaUId", schema.ParentUID,
		"targetPackageUId", packageUID, "targetSchemaManagerName", entitySchemaManagerName, "useFullHierarchy", true)
}

// ruleWritePageAddonRequest passes the page as the PARENT with a fresh target uId, so the add-on lands in
// the requested package rather than the page's own (possibly locked) package.
func ruleWritePageAddonRequest(pageUID, packageUID string) *orderedObject {
	return ruleWriteObject("addonName", businessRuleAddonName, "targetSchemaUId", newGUID(), "targetParentSchemaUId", pageUID,
		"targetPackageUId", packageUID, "targetSchemaManagerName", clientUnitSchemaManagerName, "useFullHierarchy", true)
}
