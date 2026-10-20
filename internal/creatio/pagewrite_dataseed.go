package creatio

// create-page --entity-schema-name data source seeding: clio 8.1.0.142's PageEntityDataSourceSeeder (GH-1839).
// A web page created for an entity gets a body whose modelConfigDiff binds a data source to it, shaped by the
// template's merged modelConfig; before GH-1839 the entity reached only dependsOn.

import (
	"context"
	"fmt"
	"strings"
)

const pageWriteDefaultDataSourceName = "PDS"

// pageEntityDataSourceSeed is clio's PageEntityDataSourceSeed.
type pageEntityDataSourceSeed struct {
	modelConfigDiff *jnode
	dataSourceName  string
	isPrimary       bool
	warning         string
}

// pageWriteUnboundSeed is PageEntityDataSourceSeeder.Unbound: a template whose shape is unknown binds nothing.
func pageWriteUnboundSeed(entitySchemaName, reason string) pageEntityDataSourceSeed {
	return pageEntityDataSourceSeed{modelConfigDiff: newArray(),
		warning: fmt.Sprintf("%s; no data source was bound to '%s' - the entity is recorded in the page "+
			"dependencies only. Bind it with update-page.", reason, entitySchemaName)}
}

// pageWriteBuildDataSourceSeed is PageEntityDataSourceSeeder.BuildSeed: the template hierarchy read in the
// target package, merged into the bundle, and its modelConfig decides the shape. Any failure binds nothing.
func (c *Client) pageWriteBuildDataSourceSeed(ctx context.Context, templateUID, packageUID, entitySchemaName string) (seed pageEntityDataSourceSeed) {
	defer func() {
		if recovered := recover(); recovered != nil {
			message := fmt.Sprint(recovered)
			if fault, ok := recovered.(applierFault); ok {
				message = fault.message
			}
			seed = pageWriteUnboundSeed(entitySchemaName, "Could not read the template's model config ("+message+")")
		}
	}()
	hierarchy, err := c.pageLayers(ctx, templateUID, packageUID)
	if err != nil {
		return pageWriteUnboundSeed(entitySchemaName, "Could not read the template's model config ("+err.Error()+")")
	}
	if len(hierarchy) == 0 {
		return pageWriteUnboundSeed(entitySchemaName, "The template hierarchy is empty")
	}
	parts := make([]pageBundlePart, 0, len(hierarchy))
	for _, schema := range hierarchy {
		parsed := emptyParsedPageBody()
		if schema.Body != nil && strings.TrimSpace(*schema.Body) != "" {
			if parsed, err = parsePageBody(*schema.Body); err != nil {
				return pageWriteUnboundSeed(entitySchemaName, "Could not read the template's model config ("+err.Error()+")")
			}
		}
		parts = append(parts, pageBundlePart{schema: schema, parsed: parsed})
	}
	return pageWriteSeedFromModelConfig(buildPageBundle(parts).get("modelConfig"), entitySchemaName)
}

// pageWriteSeedFromModelConfig is PageEntityDataSourceSeeder.BuildFromTemplateModelConfig.
func pageWriteSeedFromModelConfig(modelConfig *jnode, entitySchemaName string) pageEntityDataSourceSeed {
	if modelConfig == nil || !modelConfig.isObject() {
		return pageWriteUnboundSeed(entitySchemaName, "The template's model config is unknown")
	}
	var dataSources *jnode
	if value := modelConfig.get("dataSources"); value.isObject() {
		dataSources = value
	}
	primary := ""
	if value := modelConfig.get("primaryDataSourceName"); value != nil && value.kind == jkString {
		primary = value.text
	}
	sourceNamed := func(name string) *jnode {
		if dataSources == nil {
			return nil
		}
		if source := dataSources.get(name); source.isObject() {
			return source
		}
		return nil
	}
	if strings.TrimSpace(primary) != "" {
		if source := sourceNamed(primary); source != nil {
			return pageEntityDataSourceSeed{modelConfigDiff: pageWriteBindExisting(primary, source, entitySchemaName),
				dataSourceName: primary, isPrimary: true}
		}
	} else if source := sourceNamed(pageWriteDefaultDataSourceName); source != nil {
		return pageEntityDataSourceSeed{modelConfigDiff: pageWriteBindExisting(pageWriteDefaultDataSourceName, source, entitySchemaName),
			dataSourceName: pageWriteDefaultDataSourceName}
	}
	warning := ""
	if strings.TrimSpace(primary) != "" {
		warning = fmt.Sprintf("The template names primary data source '%s' but does not declare it; '%s' was added and made primary instead.",
			primary, pageWriteDefaultDataSourceName)
	}
	pageDataSource := toJNode(orderedFields{
		{"type", "crt.EntityDataSource"},
		{"scope", "page"},
		{"config", orderedFields{{"entitySchemaName", entitySchemaName}}},
	})
	operations := newArray()
	if dataSources == nil {
		// A merge into a missing path is skipped, so a template with no dataSources object (the mini page
		// template) gets the data source from the root merge, which merges deep.
		operations.items = append(operations.items, pageWriteMergeOperation([]string{}, toJNode(orderedFields{
			{"primaryDataSourceName", pageWriteDefaultDataSourceName},
			{"dataSources", orderedFields{{pageWriteDefaultDataSourceName, pageDataSource}}},
		})))
	} else {
		operations.items = append(operations.items,
			pageWriteMergeOperation([]string{}, toJNode(orderedFields{{"primaryDataSourceName", pageWriteDefaultDataSourceName}})),
			pageWriteMergeOperation([]string{"dataSources"}, toJNode(orderedFields{{pageWriteDefaultDataSourceName, pageDataSource}})))
	}
	return pageEntityDataSourceSeed{modelConfigDiff: operations, dataSourceName: pageWriteDefaultDataSourceName, isPrimary: true, warning: warning}
}

// pageWriteBindExisting is BindExisting: the entity goes into the source's config when it has one.
func pageWriteBindExisting(name string, source *jnode, entitySchemaName string) *jnode {
	operations := newArray()
	if source.get("config").isObject() {
		operations.items = append(operations.items, pageWriteMergeOperation([]string{"dataSources", name, "config"},
			toJNode(orderedFields{{"entitySchemaName", entitySchemaName}})))
	} else {
		operations.items = append(operations.items, pageWriteMergeOperation([]string{"dataSources", name},
			toJNode(orderedFields{{"config", orderedFields{{"entitySchemaName", entitySchemaName}}}})))
	}
	return operations
}

func pageWriteMergeOperation(path []string, values *jnode) *jnode {
	pathNode := newArray()
	for _, segment := range path {
		pathNode.items = append(pathNode.items, newString(segment))
	}
	operation := newObject()
	operation.set("operation", newString("merge"))
	operation.set("path", pathNode)
	operation.set("values", values)
	return operation
}

// pageWriteEntityBoundBody is PageCreateCommand.BuildEntityBoundBody: the empty web page body with the seed's
// modelConfigDiff, or "" when there is no entity, the template is a mobile one, or the seed binds nothing.
func (c *Client) pageWriteEntityBoundBody(ctx context.Context, input PageCreateRequest, template PageTemplateItem, packageUID string) (string, *pageEntityDataSourceSeed) {
	if strings.TrimSpace(input.EntitySchemaName) == "" || template.SchemaType == pageSchemaTypeMobile {
		return "", nil
	}
	seed := c.pageWriteBuildDataSourceSeed(ctx, template.UID, packageUID, input.EntitySchemaName)
	if len(seed.modelConfigDiff.items) == 0 {
		return "", &seed
	}
	return pageWriteSeededBody(input.SchemaName, seed), &seed
}

func pageWriteSeededBody(schemaName string, seed pageEntityDataSourceSeed) string {
	return strings.Replace(pageWriteEmptyReplacingBody(schemaName, false),
		"/**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/",
		"/**SCHEMA_MODEL_CONFIG_DIFF*/"+newtonsoftIndented(seed.modelConfigDiff)+"/**SCHEMA_MODEL_CONFIG_DIFF*/", 1)
}
