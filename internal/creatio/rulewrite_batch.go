package creatio

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The BusinessRule add-on as AddonSchemaDesignerService returns it: the schema payload (metaData,
// resources, then any other property kept as it came), the parsed metadata and its rules array.
type ruleWriteAddon struct {
	extra     *orderedObject // schema properties other than metaData and resources, in their order
	metadata  *orderedObject
	rules     []any
	resources []*orderedObject // {key, value: [{key, value}]} as clio's AddonResourceDto keeps them
}

func (c *Client) ruleWriteLoadAddon(ctx context.Context, request *orderedObject) (*ruleWriteAddon, error) {
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AddonSchemaDesignerService.svc/GetSchema",
		Body: []byte(ruleWriteSTJ(request, true)), Timeout: 45 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return nil, fmt.Errorf("AddonSchemaDesignerService returned an empty response.")
	}
	parsed, err := parseOrderedJSON(payload)
	if err != nil {
		return nil, err
	}
	response, ok := parsed.(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("AddonSchemaDesignerService returned an empty response.")
	}
	if !ruleWriteFoldBool(response, "success") {
		return nil, ruleWriteErrorInfo(response, "AddonSchemaDesignerService.GetSchema failed.")
	}
	schema, ok := ruleWriteFoldGet(response, "schema").(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("AddonSchemaDesignerService did not return a schema payload.")
	}
	addon := &ruleWriteAddon{extra: newOrdered(), resources: []*orderedObject{}}
	metaData := ""
	for _, key := range schema.keys {
		value := schema.values[key]
		switch {
		case strings.EqualFold(key, "metaData"):
			metaData, _ = value.(string)
		case strings.EqualFold(key, "resources"):
			items, _ := value.([]any)
			for _, item := range items {
				resource, ok := item.(*orderedObject)
				if !ok {
					continue
				}
				addon.resources = append(addon.resources, ruleWriteResource(resource))
			}
		default:
			addon.extra.set(key, value)
		}
	}
	if strings.TrimSpace(metaData) == "" {
		addon.metadata = ruleWriteObject("typeName", ruleWriteMetadataTypeName, "rules", []any{})
	} else {
		root, err := parseOrderedJSON([]byte(metaData))
		if err != nil {
			return nil, fmt.Errorf("Business-rule add-on metadata is not valid JSON.")
		}
		object, ok := root.(*orderedObject)
		if !ok {
			return nil, fmt.Errorf("Business-rule add-on metadata root must be a JSON object.")
		}
		addon.metadata = object
	}
	rules, present := addon.metadata.values["rules"]
	switch typed := rules.(type) {
	case nil:
		_ = present
		addon.rules = []any{}
	case []any:
		addon.rules = typed
	default:
		return nil, fmt.Errorf("Business-rule add-on metadata 'rules' property must be a JSON array.")
	}
	// NormalizeResourceKeys: AddonConfig.Rules.{uid}.Caption -> {uid}.Caption.
	for _, resource := range addon.resources {
		key, _ := jsonString(resource, "key")
		parts := strings.Split(key, ".")
		if len(parts) == 4 && parts[0] == "AddonConfig" && parts[1] == "Rules" {
			resource.set("key", parts[2]+"."+parts[3])
		}
	}
	return addon, nil
}

// ruleWriteResource keeps what clio's AddonResourceDto binds: key and the culture/value pairs.
func ruleWriteResource(source *orderedObject) *orderedObject {
	key, _ := ruleWriteFoldGet(source, "key").(string)
	values := []any{}
	if items, ok := ruleWriteFoldGet(source, "value").([]any); ok {
		for _, item := range items {
			entry, ok := item.(*orderedObject)
			if !ok {
				continue
			}
			culture, _ := ruleWriteFoldGet(entry, "key").(string)
			var text *string
			if value, ok := ruleWriteFoldGet(entry, "value").(string); ok {
				text = &value
			}
			values = append(values, ruleWriteObject("key", culture, "value", text))
		}
	}
	return ruleWriteObject("key", key, "value", values)
}

func ruleWriteFoldGet(object *orderedObject, name string) any {
	if object == nil {
		return nil
	}
	if value, ok := object.values[name]; ok {
		return value
	}
	for _, key := range object.keys {
		if strings.EqualFold(key, name) {
			return object.values[key]
		}
	}
	return nil
}

func ruleWriteFoldBool(object *orderedObject, name string) bool {
	flag, _ := ruleWriteFoldGet(object, name).(bool)
	return flag
}

func ruleWriteErrorInfo(response *orderedObject, fallback string) error {
	if info, ok := ruleWriteFoldGet(response, "errorInfo").(*orderedObject); ok {
		if message, ok := ruleWriteFoldGet(info, "message").(string); ok {
			return fmt.Errorf("%s", message)
		}
	}
	return fmt.Errorf("%s", fallback)
}

// findRule is FindSingleRuleIndexByName: top-level rules only (no parentUId), name matched ignoring case.
// duplicate carries clio's refusal when the name matches more than one rule.
func (a *ruleWriteAddon) findRule(name string) (index int, duplicate string) {
	index = -1
	for position, node := range a.rules {
		rule, ok := node.(*orderedObject)
		if !ok {
			continue
		}
		if parent, ok := jsonString(rule, "parentUId"); ok && strings.TrimSpace(parent) != "" {
			continue
		}
		candidate, ok := jsonString(rule, "name")
		if !ok || !strings.EqualFold(candidate, name) {
			continue
		}
		if index >= 0 {
			return -1, fmt.Sprintf("Business rule name '%s' matches more than one rule on the schema; rename the duplicates in the Creatio designer before using name-based operations.", name)
		}
		index = position
	}
	return index, ""
}

func (a *ruleWriteAddon) removeCaption(uid string) {
	kept := a.resources[:0]
	for _, resource := range a.resources {
		if key, _ := jsonString(resource, "key"); strings.EqualFold(key, uid+".Caption") {
			continue
		}
		kept = append(kept, resource)
	}
	a.resources = kept
}

// removeChildren is RemoveChildRules: autogenerated helper rules of a parent and their captions.
func (a *ruleWriteAddon) removeChildren(parentUID string) {
	for index := len(a.rules) - 1; index >= 0; index-- {
		rule, ok := a.rules[index].(*orderedObject)
		if !ok {
			continue
		}
		parent, ok := jsonString(rule, "parentUId")
		if !ok || !strings.EqualFold(parent, parentUID) {
			continue
		}
		uid, hasUID := jsonString(rule, "uId")
		a.rules = append(a.rules[:index], a.rules[index+1:]...)
		if hasUID && strings.TrimSpace(uid) != "" {
			a.removeCaption(uid)
		}
	}
}

func (a *ruleWriteAddon) upsertCaption(uid, caption string) {
	value := []any{ruleWriteObject("key", businessRuleDefaultCulture, "value", caption)}
	for _, resource := range a.resources {
		if key, _ := jsonString(resource, "key"); strings.EqualFold(key, uid+".Caption") {
			resource.set("value", value)
			return
		}
	}
	a.resources = append(a.resources, ruleWriteObject("key", uid+".Caption", "value", value))
}

// ruleWriteSaveAddon is SaveSchema plus the two post-save refreshes. The refreshes are best effort after
// the durable save, as in clio: they cannot turn a committed write into a reported failure.
func (c *Client) ruleWriteSaveAddon(ctx context.Context, addon *ruleWriteAddon) error {
	addon.metadata.set("rules", addon.rules)
	schema := newOrdered()
	schema.set("metaData", ruleWriteSTJ(addon.metadata, true))
	resources := make([]any, len(addon.resources))
	for index, resource := range addon.resources {
		resources[index] = resource
	}
	schema.set("resources", resources)
	for _, key := range addon.extra.keys {
		schema.set(key, addon.extra.values[key])
	}
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AddonSchemaDesignerService.svc/SaveSchema",
		Body: []byte(ruleWriteSTJ(schema, true)), Timeout: 45 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return fmt.Errorf("AddonSchemaDesignerService.SaveSchema returned an empty response.")
	}
	parsed, err := parseOrderedJSON(payload)
	if err != nil {
		return err
	}
	response, _ := parsed.(*orderedObject)
	if !ruleWriteFoldBool(response, "success") || ruleWriteFoldGet(response, "value") == false {
		return ruleWriteErrorInfo(response, "AddonSchemaDesignerService.SaveSchema failed.")
	}
	_, _ = c.serviceRequest(ctx, serviceCall{Route: "rest/WorkplaceService/ResetScriptCache", Timeout: 45 * time.Second, Limit: maxResponseBytes})
	_, _ = c.serviceRequest(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/BuildConfiguration", Timeout: 45 * time.Second, Limit: maxResponseBytes})
	return nil
}

// Batch create and update (clio's BaseBusinessRuleService.CreateBatch / UpdateBatch).

// RuleWriteBatchRequest is clio's BusinessRulesBatchRequest.
type RuleWriteBatchRequest struct {
	PackageName string
	SchemaName  string
	Rules       []*RuleWriteRule
}

// ruleWriteTarget is what a resolved batch needs: the add-on request and a scope factory.
type ruleWriteTarget struct {
	request *orderedObject
	scope   *ruleWriteScope
}

// prepare resolves the package and the entity or page the batch targets. A failure here is what clio's
// service throws before any rule is looked at.
func (c *Client) ruleWritePrepare(ctx context.Context, page bool, request RuleWriteBatchRequest) (target *ruleWriteTarget, failure string) {
	field := "entity-schema-name"
	if page {
		field = "page-schema-name"
	}
	if message := missingBusinessRuleFields(request.PackageName, request.SchemaName, field); message != "" {
		return nil, message
	}
	if len(request.Rules) == 0 {
		return nil, "rules is required and must contain at least one rule."
	}
	packageUID, err := c.resolvePackageUID(ctx, request.PackageName)
	if err != nil {
		return nil, err.Error()
	}
	cache := &ruleWriteSchemaCache{ctx: ctx, client: c, packageUID: packageUID, schemas: map[string]*ruleWriteEntitySchema{}}
	scope := &ruleWriteScope{ctx: ctx, client: c, page: page, cache: cache,
		lookupIDs: map[[2]string]string{}, lookupNames: map[[2]string]*string{}}
	if !page {
		schema, err := c.ruleWriteEntitySchema(ctx, request.SchemaName, packageUID)
		if err != nil {
			return nil, err.Error()
		}
		cache.schemas[strings.TrimSpace(request.SchemaName)] = schema
		cache.schemas[schema.Name] = schema
		scope.entitySchema = schema.Name
		scope.attributes = &ruleWriteEntityAttributes{root: schema, cache: cache}
		return &ruleWriteTarget{request: ruleWriteEntityAddonRequest(schema, packageUID), scope: scope}, ""
	}
	pageContext, err := c.ruleWritePageContext(ctx, request.SchemaName, packageUID)
	if err != nil {
		return nil, err.Error()
	}
	message, failed := ruleWriteCatch(func() {
		scope.attributes = c.ruleWritePageAttributes(pageContext, cache)
	})
	if failed {
		return nil, message
	}
	scope.elements = ruleWritePageElements(pageContext)
	return &ruleWriteTarget{request: ruleWritePageAddonRequest(pageContext.SchemaUID, packageUID), scope: scope}, ""
}

// convert validates one rule and builds its metadata (the parent rule first, then generated child rules).
func (s *ruleWriteScope) convert(rule *RuleWriteRule, existing *orderedObject) []ruleWriteMetadataRule {
	if rule == nil {
		ruleWriteFail("Value cannot be null. (Parameter 'rule')")
	}
	s.staticFilters = false
	for _, action := range rule.Actions {
		if action != nil && strings.EqualFold(action.Type, "apply-static-filter") {
			s.staticFilters = true
		}
	}
	s.resolveSysSettings(rule)
	s.validate(rule)
	if s.page {
		return []ruleWriteMetadataRule{s.toPageMetadata(rule, existing)}
	}
	s.validateFormulas(rule)
	return s.toEntityMetadata(rule, existing)
}

func ruleWriteCaption(rule *RuleWriteRule) string {
	if rule == nil {
		return ""
	}
	return ruleWriteText(rule.Caption)
}

// CreateBusinessRules is the create batch: every rule is validated and converted on its own, then all
// converted rules are appended to the add-on in one save. A failure before the batch starts fails every
// rule with that message, as clio's create tools report it.
func (c *Client) CreateBusinessRules(ctx context.Context, page bool, request RuleWriteBatchRequest) RuleWriteResponse {
	target, failure := c.ruleWritePrepare(ctx, page, request)
	if failure != "" {
		results := make([]RuleWriteResult, len(request.Rules))
		for index, rule := range request.Rules {
			results[index] = ruleWriteFailure(ruleWriteCaption(rule), failure)
		}
		return ruleWriteResponseFrom(results)
	}
	results := make([]RuleWriteResult, len(request.Rules))
	type pendingRule struct {
		index            int
		caption, rule    string
		generatedObjects []ruleWriteMetadataRule
	}
	pending := []pendingRule{}
	for index, rule := range request.Rules {
		caption := ruleWriteCaption(rule)
		var created []ruleWriteMetadataRule
		message, failed := ruleWriteCatch(func() { created = target.scope.convert(rule, nil) })
		if failed {
			results[index] = ruleWriteFailure(caption, message)
			continue
		}
		if len(created) == 0 {
			results[index] = ruleWriteFailure(caption, "Rule produced no metadata.")
			continue
		}
		pending = append(pending, pendingRule{index, caption, created[0].name, created})
	}
	if len(pending) == 0 {
		return ruleWriteResponseFrom(results)
	}
	err := c.ruleWriteAppend(ctx, target.request, func() []ruleWriteMetadataRule {
		all := []ruleWriteMetadataRule{}
		for _, entry := range pending {
			all = append(all, entry.generatedObjects...)
		}
		return all
	}())
	for _, entry := range pending {
		if err != nil {
			results[entry.index] = ruleWriteFailure(entry.caption, err.Error())
		} else {
			results[entry.index] = ruleWriteSuccess(entry.caption, entry.rule)
		}
	}
	return ruleWriteResponseFrom(results)
}

// ruleWriteAppend is AppendRules: one GetSchema, the unique-name check, the appended rules and captions,
// one save.
func (c *Client) ruleWriteAppend(ctx context.Context, request *orderedObject, created []ruleWriteMetadataRule) error {
	addon, err := c.ruleWriteLoadAddon(ctx, request)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, node := range addon.rules {
		if rule, ok := node.(*orderedObject); ok {
			if name, ok := jsonString(rule, "name"); ok && strings.TrimSpace(name) != "" {
				names[strings.ToLower(name)] = true
			}
		}
	}
	for _, rule := range created {
		if strings.TrimSpace(rule.name) == "" {
			continue
		}
		if names[strings.ToLower(rule.name)] {
			return fmt.Errorf("Business rule name '%s' already exists on the target schema. Rule names must be unique; use the update tool to change an existing rule.", rule.name)
		}
		names[strings.ToLower(rule.name)] = true
	}
	for _, rule := range created {
		addon.rules = append(addon.rules, rule.object)
		if strings.TrimSpace(rule.caption) != "" {
			addon.upsertCaption(rule.uid, strings.TrimSpace(rule.caption))
		}
	}
	return c.ruleWriteSaveAddon(ctx, addon)
}

// UpdateBusinessRules is the update batch: the add-on is read once, every rule replaces the rule of the
// same name (keeping its uId, case, group and matching trigger uIds), and the add-on is saved once. A
// failure before the rules are processed is a request-level error.
func (c *Client) UpdateBusinessRules(ctx context.Context, page bool, request RuleWriteBatchRequest) RuleWriteResponse {
	target, failure := c.ruleWritePrepare(ctx, page, request)
	if failure != "" {
		return RuleWriteRequestError(failure)
	}
	addon, err := c.ruleWriteLoadAddon(ctx, target.request)
	if err != nil {
		return RuleWriteRequestError(err.Error())
	}
	results := make([]RuleWriteResult, len(request.Rules))
	type pendingRule struct {
		index int
		name  string
	}
	pending := []pendingRule{}
	batchNames := map[string]bool{}
	for index, rule := range request.Rules {
		identifier := ruleWriteCaption(rule)
		if rule != nil && !ruleWriteBlank(rule.Name) {
			identifier = strings.TrimSpace(*rule.Name)
		}
		message, failed := ruleWriteCatch(func() {
			if rule == nil {
				ruleWriteFail("Value cannot be null. (Parameter 'rule')")
			}
			if ruleWriteBlank(rule.Name) {
				ruleWriteFail("name is required to update a business rule.")
			}
			name := strings.TrimSpace(*rule.Name)
			if batchNames[strings.ToLower(name)] {
				ruleWriteFail("Business rule '%s' appears more than once in the update batch.", name)
			}
			batchNames[strings.ToLower(name)] = true
			position, duplicate := addon.findRule(name)
			if duplicate != "" {
				ruleWriteFail("%s", duplicate)
			}
			if position < 0 {
				ruleWriteFail("Business rule '%s' was not found.", name)
			}
			existing, _ := addon.rules[position].(*orderedObject)
			effective := *rule
			if effective.Enabled == nil {
				enabled := jsonBool(existing, "enabled", true)
				effective.Enabled = &enabled
			}
			generated := target.scope.convert(&effective, existing)
			parent := generated[0]
			addon.removeChildren(parent.uid)
			position, _ = addon.findRule(name)
			addon.rules[position] = parent.object
			for _, child := range generated[1:] {
				addon.rules = append(addon.rules, child.object)
			}
			for _, item := range generated {
				if strings.TrimSpace(item.caption) != "" {
					addon.upsertCaption(item.uid, strings.TrimSpace(item.caption))
				}
			}
			pending = append(pending, pendingRule{index, name})
		})
		if failed {
			results[index] = ruleWriteFailure(identifier, message)
		}
	}
	if len(pending) == 0 {
		return ruleWriteResponseFrom(results)
	}
	err = c.ruleWriteSaveAddon(ctx, addon)
	for _, entry := range pending {
		if err != nil {
			results[entry.index] = ruleWriteFailure(entry.name, err.Error())
		} else {
			results[entry.index] = ruleWriteSuccess(entry.name, entry.name)
		}
	}
	return ruleWriteResponseFrom(results)
}
