package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ruleWriteFault is a failure clio raises as an exception inside one rule's processing (validation,
// conversion or a remote call it makes on the way). It is recovered per rule and becomes that rule's error.
type ruleWriteFault struct{ message string }

func ruleWriteFail(format string, args ...any) {
	if len(args) == 0 {
		panic(ruleWriteFault{message: format})
	}
	panic(ruleWriteFault{message: fmt.Sprintf(format, args...)})
}

// ruleWriteCatch runs work and turns a ruleWriteFault into its message.
func ruleWriteCatch(work func()) (message string, failed bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fault, ok := recovered.(ruleWriteFault)
			if !ok {
				panic(recovered)
			}
			message, failed = fault.message, true
		}
	}()
	work()
	return "", false
}

// ruleWriteAttribute is clio's BusinessRuleAttributeDescriptor.
type ruleWriteAttribute struct {
	Path            string
	DataValueType   string
	ReferenceSchema *string
	Scope           string
}

func (a ruleWriteAttribute) scoped() bool { return a.Scope != "" }

func (a ruleWriteAttribute) operand() ruleWriteOperandType {
	return ruleWriteOperandType{dataValueType: a.DataValueType, referenceSchema: a.ReferenceSchema}
}

// ruleWriteAttributes is the attribute map a validator and converter read: keys in insertion order and a
// lookup that may fail (an unsupported column type, or a referenced schema that cannot be read).
type ruleWriteAttributes interface {
	lookup(path string) (ruleWriteAttribute, bool)
	keys() []string
}

// ruleWriteMustGet is clio's attributeMap[path] indexer.
func ruleWriteMustGet(attributes ruleWriteAttributes, path string) ruleWriteAttribute {
	attribute, ok := attributes.lookup(path)
	if !ok {
		ruleWriteFail("Attribute '%s' was not found.", path)
	}
	return attribute
}

// Entity schema as EntitySchemaDesignerService.GetSchemaDesignItem returns it (the fields clio reads).
type ruleWriteColumn struct {
	Name            string
	DataValueType   *int
	ReferenceSchema *string
}

type ruleWriteEntitySchema struct {
	Name           string
	UID            string
	ParentUID      string
	PrimaryDisplay *string
	columnOrder    []string
	columns        map[string]ruleWriteColumn
}

func (c *Client) ruleWriteEntitySchema(ctx context.Context, schemaName, packageUID string) (*ruleWriteEntitySchema, error) {
	body, err := json.Marshal(map[string]any{
		"name": strings.TrimSpace(schemaName), "packageUId": packageUID, "useFullHierarchy": true,
		"cultures": []string{businessRuleDefaultCulture},
	})
	if err != nil {
		return nil, err
	}
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	type column struct {
		Name            string `json:"name"`
		DataValueType   *int   `json:"type"`
		ReferenceSchema *struct {
			Name *string `json:"name"`
		} `json:"referenceSchema"`
	}
	var response struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
		Schema *struct {
			Name         string `json:"name"`
			UID          string `json:"uId"`
			ParentSchema *struct {
				UID string `json:"uId"`
			} `json:"parentSchema"`
			PrimaryDisplayColumn *struct {
				Name *string `json:"name"`
			} `json:"primaryDisplayColumn"`
			Columns          []column `json:"columns"`
			InheritedColumns []column `json:"inheritedColumns"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("GetSchemaDesignItem returned invalid JSON: %w", err)
	}
	if !response.Success {
		if response.ErrorInfo != nil && strings.TrimSpace(response.ErrorInfo.Message) != "" {
			return nil, fmt.Errorf("%s", response.ErrorInfo.Message)
		}
		return nil, fmt.Errorf("GetSchemaDesignItem failed.")
	}
	if response.Schema == nil {
		return nil, fmt.Errorf("Entity schema '%s' was not returned.", schemaName)
	}
	schema := &ruleWriteEntitySchema{Name: response.Schema.Name, UID: guidOrEmpty(response.Schema.UID), ParentUID: emptyGUID, columns: map[string]ruleWriteColumn{}}
	if response.Schema.ParentSchema != nil {
		schema.ParentUID = guidOrEmpty(response.Schema.ParentSchema.UID)
	}
	if response.Schema.PrimaryDisplayColumn != nil {
		schema.PrimaryDisplay = response.Schema.PrimaryDisplayColumn.Name
	}
	// BuildColumnMap: own columns, then inherited ones; a repeated name keeps its first position and the
	// later value, as a Dictionary indexer assignment does.
	for _, list := range [][]column{response.Schema.Columns, response.Schema.InheritedColumns} {
		for _, item := range list {
			if strings.TrimSpace(item.Name) == "" {
				continue
			}
			entry := ruleWriteColumn{Name: item.Name, DataValueType: item.DataValueType}
			if item.ReferenceSchema != nil {
				entry.ReferenceSchema = item.ReferenceSchema.Name
			}
			if _, seen := schema.columns[item.Name]; !seen {
				schema.columnOrder = append(schema.columnOrder, item.Name)
			}
			schema.columns[item.Name] = entry
		}
	}
	return schema, nil
}

// ruleWriteColumnType is clio's MapDataValueTypeName: it throws for a missing or unknown code.
func ruleWriteColumnType(column ruleWriteColumn) string {
	if column.DataValueType == nil {
		ruleWriteFail("Entity schema column dataValueType is required.")
	}
	name, ok := ruleWriteTypeName(*column.DataValueType)
	if !ok {
		ruleWriteFail("Unsupported entity schema dataValueType '%d'.", *column.DataValueType)
	}
	return name
}

func ruleWriteColumnAttribute(path string, column ruleWriteColumn) ruleWriteAttribute {
	return ruleWriteAttribute{Path: path, DataValueType: ruleWriteColumnType(column), ReferenceSchema: column.ReferenceSchema}
}

// ruleWriteSchemaCache fetches each entity schema once per batch, with the target package scope.
type ruleWriteSchemaCache struct {
	ctx        context.Context
	client     *Client
	packageUID string
	schemas    map[string]*ruleWriteEntitySchema
}

func (s *ruleWriteSchemaCache) get(name string) *ruleWriteEntitySchema {
	if schema, ok := s.schemas[name]; ok {
		return schema
	}
	schema, err := s.client.ruleWriteEntitySchema(s.ctx, name, s.packageUID)
	if err != nil {
		ruleWriteFail("%s", err.Error())
	}
	s.schemas[name] = schema
	return schema
}

// ruleWriteEntityAttributes is clio's EntityBusinessRuleAttributeDescriptorMap: direct columns of the root
// schema and forward paths through lookup columns (Account.Country), resolved lazily.
type ruleWriteEntityAttributes struct {
	root  *ruleWriteEntitySchema
	cache *ruleWriteSchemaCache
}

func (m *ruleWriteEntityAttributes) keys() []string { return m.root.columnOrder }

func (m *ruleWriteEntityAttributes) lookup(key string) (ruleWriteAttribute, bool) {
	if strings.TrimSpace(key) == "" {
		return ruleWriteAttribute{}, false
	}
	segments := []string{}
	for _, segment := range strings.Split(key, ".") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	if len(segments) == 0 || strings.Join(segments, ".") != key {
		return ruleWriteAttribute{}, false
	}
	current := m.root
	for index, segment := range segments {
		column, ok := current.columns[segment]
		if !ok {
			return ruleWriteAttribute{}, false
		}
		if index == len(segments)-1 {
			return ruleWriteColumnAttribute(key, column), true
		}
		if ruleWriteBlank(column.ReferenceSchema) {
			return ruleWriteAttribute{}, false
		}
		reference := *column.ReferenceSchema
		if reference == m.root.Name {
			current = m.root
		} else {
			current = m.cache.get(reference)
		}
	}
	return ruleWriteAttribute{}, false
}

// ruleWriteMapAttributes is a plain dictionary map (the page attribute map).
type ruleWriteMapAttributes struct {
	order  []string
	values map[string]ruleWriteAttribute
}

func newRuleWriteMapAttributes() *ruleWriteMapAttributes {
	return &ruleWriteMapAttributes{values: map[string]ruleWriteAttribute{}}
}

func (m *ruleWriteMapAttributes) set(key string, value ruleWriteAttribute) {
	if _, seen := m.values[key]; !seen {
		m.order = append(m.order, key)
	}
	m.values[key] = value
}

func (m *ruleWriteMapAttributes) lookup(key string) (ruleWriteAttribute, bool) {
	value, ok := m.values[key]
	return value, ok
}

func (m *ruleWriteMapAttributes) keys() []string { return m.order }

// Page attributes (clio's PageBusinessRuleAttributeProvider) and element names (PageBusinessRuleElementProvider).

type ruleWritePageContext struct {
	SchemaUID string
	bundle    *jnode
}

func (c *Client) ruleWritePageContext(ctx context.Context, schemaName, packageUID string) (*ruleWritePageContext, error) {
	normalized := strings.TrimSpace(schemaName)
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"UId": "UId"}, map[string]any{
		"byName":    comparisonFilter("Name", normalized, 1, 3),
		"byManager": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
		"byPackage": comparisonFilter("SysPackage.UId", packageUID, 0, 3),
	}, 1))
	if err != nil {
		return nil, fmt.Errorf("Failed to query schema metadata in target package.")
	}
	schemaUID := ""
	if len(rows) > 0 {
		schemaUID = rowString(rows[0], "UId")
	}
	if strings.TrimSpace(schemaUID) == "" {
		if schemaUID, err = c.rootPageSchemaUID(ctx, normalized, packageUID); err != nil {
			return nil, err
		}
	}
	layers, err := c.pageLayers(ctx, schemaUID, packageUID)
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, fmt.Errorf("Page schema '%s' hierarchy is empty.", schemaName)
	}
	parts := []pageBundlePart{}
	for _, layer := range layers {
		if layer.Body == nil {
			continue
		}
		parsed, err := parsePageBody(*layer.Body)
		if err != nil {
			return nil, err
		}
		parts = append(parts, pageBundlePart{schema: layer, parsed: parsed})
	}
	// An empty part list is an empty bundle in clio (PageBundleBuilder.Build).
	bundle := toJNode(orderedFields{{"viewConfig", newArray()}, {"viewModelConfig", newObject()}, {"modelConfig", newObject()}, {"parameters", []any{}}})
	if len(parts) > 0 {
		if bundle, err = safePageBundle(parts, schemaName); err != nil {
			return nil, err
		}
	}
	return &ruleWritePageContext{SchemaUID: guidOrEmpty(layers[0].UID), bundle: bundle}, nil
}

func ruleWriteJString(node *jnode) string {
	if node == nil || node.kind != jkString {
		return ""
	}
	return node.text
}

func (c *Client) ruleWritePageAttributes(page *ruleWritePageContext, cache *ruleWriteSchemaCache) *ruleWriteMapAttributes {
	result := newRuleWriteMapAttributes()
	attributes := objectOrEmpty(page.bundle.get("viewModelConfig").get("attributes"))
	dataSources := objectOrEmpty(page.bundle.get("modelConfig").get("dataSources"))
	// BuildParameterMap keeps the last parameter of a name; TryResolveParameterType then needs a known
	// data value type code.
	type parameter struct {
		dataValueType   string
		referenceSchema *string
		resolved        bool
	}
	parameterOrder := []string{}
	parameters := map[string]parameter{}
	if list := page.bundle.get("parameters"); list.isArray() {
		for _, item := range list.items {
			name := ruleWriteJString(item.get("name"))
			if strings.TrimSpace(name) == "" {
				continue
			}
			if _, seen := parameters[name]; !seen {
				parameterOrder = append(parameterOrder, name)
			}
			entry := parameter{}
			if typeNode := item.get("dataValueType"); typeNode != nil && typeNode.kind == jkInteger {
				if typeName, ok := ruleWriteTypeName(tokenInt(typeNode)); ok {
					entry = parameter{dataValueType: typeName, resolved: true}
					if text := ruleWriteJString(item.get("referenceSchemaName")); strings.TrimSpace(text) != "" {
						entry.referenceSchema = &text
					}
				}
			}
			parameters[name] = entry
		}
	}
	entityMaps := map[string]ruleWriteAttributes{}
	entityAttributes := func(schemaName string) ruleWriteAttributes {
		if attributes, ok := entityMaps[schemaName]; ok {
			return attributes
		}
		attributes := &ruleWriteEntityAttributes{root: cache.get(schemaName), cache: cache}
		entityMaps[schemaName] = attributes
		return attributes
	}
	// TryGetSupportedAttribute: an unsupported column type is skipped, not reported.
	supported := func(attributes ruleWriteAttributes, column string) (ruleWriteAttribute, bool) {
		var found ruleWriteAttribute
		ok := false
		if _, failed := ruleWriteCatch(func() { found, ok = attributes.lookup(column) }); failed {
			return ruleWriteAttribute{}, false
		}
		return found, ok
	}
	entitySchemaOf := func(dataSource string) string {
		return ruleWriteJString(dataSources.get(dataSource).get("config").get("entitySchemaName"))
	}
	for _, name := range attributes.keys {
		attribute := attributes.props[name]
		if strings.TrimSpace(name) == "" || !attribute.isObject() {
			continue
		}
		if collection := attribute.get("isCollection"); collection != nil && collection.kind == jkBool && collection.flag {
			continue
		}
		path := ruleWriteJString(attribute.get("modelConfig").get("path"))
		parts := []string{}
		for _, part := range strings.Split(path, ".") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) != 2 {
			continue
		}
		dataSource, column := parts[0], parts[1]
		if dataSource == "PageParameters" {
			if parameter, ok := parameters[column]; ok && parameter.resolved {
				result.set(name, ruleWriteAttribute{Path: name, DataValueType: parameter.dataValueType, ReferenceSchema: parameter.referenceSchema})
			}
			continue
		}
		entitySchema := entitySchemaOf(dataSource)
		if strings.TrimSpace(entitySchema) == "" {
			continue
		}
		descriptor, ok := supported(entityAttributes(entitySchema), column)
		if !ok {
			continue
		}
		descriptor.Path = name
		result.set(name, descriptor)
	}
	for _, dataSource := range dataSources.keys {
		entitySchema := entitySchemaOf(dataSource)
		if strings.TrimSpace(dataSource) == "" || strings.TrimSpace(entitySchema) == "" {
			continue
		}
		attributes := entityAttributes(entitySchema)
		for _, column := range attributes.keys() {
			descriptor, ok := supported(attributes, column)
			if !ok {
				continue
			}
			descriptor.Path = column
			descriptor.Scope = dataSource
			result.set(dataSource+"."+column, descriptor)
		}
	}
	for _, name := range parameterOrder {
		parameter := parameters[name]
		if !parameter.resolved {
			continue
		}
		result.set("PageParameters."+name, ruleWriteAttribute{Path: name, DataValueType: parameter.dataValueType, ReferenceSchema: parameter.referenceSchema, Scope: "PageParameters"})
	}
	return result
}

// ruleWritePageElements collects every "name" declared anywhere in the merged viewConfig.
func ruleWritePageElements(page *ruleWritePageContext) map[string]bool {
	names := map[string]bool{}
	var walk func(node *jnode)
	walk = func(node *jnode) {
		switch {
		case node.isArray():
			for _, item := range node.items {
				walk(item)
			}
		case node.isObject():
			if name := ruleWriteJString(node.get("name")); strings.TrimSpace(name) != "" {
				names[name] = true
			}
			for _, key := range node.keys {
				walk(node.props[key])
			}
		}
	}
	walk(page.bundle.get("viewConfig"))
	return names
}
