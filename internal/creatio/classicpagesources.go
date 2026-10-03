package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ClassicPageSourcesResult is clio's get-classic-page-sources envelope: the manifest path and per-block counts.
// The bodies themselves are written to the manifest only.
type ClassicPageSourcesResult struct {
	Success             bool     `json:"success"`
	SchemaName          string   `json:"schemaName,omitempty"`
	Entity              string   `json:"entity,omitempty"`
	ManifestPath        string   `json:"manifestPath,omitempty"`
	LayerCount          int      `json:"layerCount"`
	SeedCount           int      `json:"seedCount"`
	ResourceCount       int      `json:"resourceCount"`
	ColumnCount         int      `json:"columnCount"`
	DetailCount         int      `json:"detailCount"`
	SectionLayerCount   int      `json:"sectionLayerCount"`
	ChildPageCount      int      `json:"childPageCount"`
	EnumVocabularyCount int      `json:"enumVocabularyCount"`
	Warnings            []string `json:"warnings,omitempty"`
	Error               string   `json:"error,omitempty"`
}

type ClassicPageSourcesRequest struct {
	SchemaName string
	Entity     string
	OutputFile string
}

const (
	classicPageNoSchemaReturned = "no schema returned"
	classicPageLayerChunkSize   = lookupIDsPerQuery
	classicPageEditRowsPerEnt   = 20
)

// The patterns are clio's, with each (?<![A-Za-z_]) look-behind written as a leading (^|[^A-Za-z_]) group,
// because Go's regexp has no look-behind. The group consumes one character before the match, which never
// overlaps a neighbouring match: two references are always separated by at least a quote and a comma.
var (
	classicPageEntityPattern = regexp.MustCompile(`(?:^|[^A-Za-z_])entitySchemaName["']?\s*:\s*["']([A-Za-z_]\w*)["']`)
	classicPageDetailPattern = regexp.MustCompile(`(?:^|[^A-Za-z_])schemaName["']?\s*:\s*["']([A-Za-z]\w*Detail\w*)["']`)
	classicPageEditPattern   = regexp.MustCompile(`(?:getEditPageName|editPageName|EditPageSchemaName)[\s\S]{0,80}?["']([A-Za-z]\w+)["']`)
	classicPageOverride      = regexp.MustCompile(
		`(?:^|[^A-Za-z_])schemaName["']?\s*:\s*["']([A-Za-z]\w*Detail\w*)["']\s*,\s*entitySchemaName["']?\s*:\s*["']([A-Za-z_]\w*)["']` +
			`|(?:^|[^A-Za-z_])entitySchemaName["']?\s*:\s*["']([A-Za-z_]\w*)["']\s*,\s*schemaName["']?\s*:\s*["']([A-Za-z]\w*Detail\w*)["']`)
)

// classicPageRun is clio's PageSourcesRunContext: designer reads and layer enumerations are memoized for one
// run, and warnings are de-duplicated.
type classicPageRun struct {
	client   *Client
	schemas  map[string]classicPageLoaded
	layers   map[string][]schemaGetLayer
	warnings []string
}

type classicPageLoaded struct {
	schema *schemaGetDesignerSchema
	err    error
}

func (r *classicPageRun) warn(warning string) {
	for _, existing := range r.warnings {
		if existing == warning {
			return
		}
	}
	r.warnings = append(r.warnings, warning)
}

// GetClassicPageSources collects the Classic page sources the migration engine folds and writes the manifest
// the way clio's get-classic-page-sources does: the replacing-layer chain, the parent-template seed, entity
// columns and titles, merged resources, details with their layers, the section, the child pages each detail
// registers in SysModuleEdit, and the stand's own enum vocabulary. It only reads from Creatio.
func (c *Client) GetClassicPageSources(ctx context.Context, input ClassicPageSourcesRequest) ClassicPageSourcesResult {
	if strings.TrimSpace(input.SchemaName) == "" {
		return ClassicPageSourcesResult{Error: "schema-name is required"}
	}
	if !classicPageValidSchemaName(input.SchemaName) {
		return ClassicPageSourcesResult{Error: classicPageSchemaNameError}
	}
	run := &classicPageRun{client: c, schemas: map[string]classicPageLoaded{}, layers: map[string][]schemaGetLayer{}}
	schemas, seed, topLayerUID, err := run.chainAndSeed(ctx, input.SchemaName)
	if err != nil {
		return ClassicPageSourcesResult{Error: err.Error()}
	}
	entity := input.Entity
	if strings.TrimSpace(entity) == "" {
		entity = classicPageInferEntity(append(append([]*jnode{}, schemas.items...), seed.items...))
	}
	merged := run.mergedStrings(ctx, topLayerUID, input.SchemaName,
		fmt.Sprintf("Could not gather merged localizable strings (resources) of '%s'", input.SchemaName),
		"Its manifest carries no resources, so localized captions will be missing from the folded page.")
	resources, resourceStrings := classicPageFlatResources(merged), classicPageResourceStrings(merged)
	entityColumns, columnTitles := run.entityColumns(ctx, entity)
	bodies := append(append([]*jnode{}, schemas.items...), seed.items...)
	detailNames := classicPageDetailNames(bodies)
	overrides := classicPageDetailOverrides(seed.items, schemas.items)
	sectionCandidates := run.sectionCandidates(ctx, input.SchemaName, entity)
	run.primeLayers(ctx, append(append([]string{}, detailNames...), sectionCandidates...))
	detailSchemas := run.detailSchemas(ctx, detailNames)
	section := run.section(ctx, sectionCandidates)
	childPages, childInfo := run.childPageSchemas(ctx, detailSchemas, overrides, input.SchemaName)
	classicPageAnnotateDetails(detailSchemas, childInfo)
	if len(section.items) == 0 {
		subject := fmt.Sprintf("entity '%s'", entity)
		if strings.TrimSpace(entity) == "" {
			subject = fmt.Sprintf("page '%s'", input.SchemaName)
		}
		run.warn("No Classic section resolved for " + subject + fmt.Sprintf(" (tried: %s). The manifest carries no section, so the ", strings.Join(sectionCandidates, ", ")) +
			"List-page side of the migration plan will be empty. Verify whether a section exists before " +
			"treating this as 'nothing to migrate'.")
	}
	enumVocabulary := run.enumVocabulary(ctx)

	manifest := newObject()
	manifest.set("schemas", schemas)
	if strings.TrimSpace(entity) != "" {
		manifest.set("entity", newString(entity))
	}
	for _, block := range []struct {
		name  string
		value *jnode
	}{{"seed", seed}, {"entityColumns", entityColumns}, {"columnTitles", columnTitles}, {"resources", resources},
		{"resourceStrings", resourceStrings}, {"detailSchemas", detailSchemas}, {"section", section},
		{"childPageSchemas", childPages}, {"enumVocabulary", enumVocabulary}} {
		if len(block.value.items) > 0 || len(block.value.keys) > 0 {
			manifest.set(block.name, block.value)
		}
	}
	manifestPath, err := classicPageWriteManifest(input, manifest)
	if err != nil {
		return ClassicPageSourcesResult{Error: err.Error()}
	}
	result := ClassicPageSourcesResult{
		Success: true, SchemaName: input.SchemaName, Entity: entity, ManifestPath: manifestPath,
		LayerCount: len(schemas.items), SeedCount: len(seed.items), ResourceCount: len(resources.keys),
		ColumnCount: len(columnTitles.keys), DetailCount: len(detailSchemas.keys), SectionLayerCount: len(section.items),
		ChildPageCount: len(childPages.keys), EnumVocabularyCount: len(enumVocabulary.keys),
	}
	if len(run.warnings) > 0 {
		result.Warnings = run.warnings
	}
	return result
}

// classicPageWriteManifest writes the manifest as Newtonsoft's indented JSON. An explicit output-file is
// confined and never overwritten; the default <anchor>/.clio-migration/<schema>/manifest.json is replaced.
func classicPageWriteManifest(input ClassicPageSourcesRequest, manifest *jnode) (string, error) {
	content := classicPageNewtonsoftIndented(manifest)
	if strings.TrimSpace(input.OutputFile) != "" {
		path, refusal := schemaGetResolveOutput(input.OutputFile)
		if refusal != "" {
			return "", errors.New(refusal)
		}
		if err := schemaGetWriteAtomic(path, content); err != nil {
			return "", err
		}
		return path, nil
	}
	anchor, err := pageOutputAnchor("")
	if err != nil {
		return "", err
	}
	path := filepath.Join(anchor, ".clio-migration", input.SchemaName, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// load is the memoized designer GetSchema read; the merged (full-hierarchy) and own reads are kept apart.
func (r *classicPageRun) load(ctx context.Context, uid, label string, fullHierarchy bool) (*schemaGetDesignerSchema, error) {
	key := strings.ToLower(uid) + "|own"
	if fullHierarchy {
		key = strings.ToLower(uid) + "|merged"
	}
	if cached, ok := r.schemas[key]; ok {
		return cached.schema, cached.err
	}
	schema, err := r.client.schemaGetLoad(ctx, uid, schemaGetClientUnit, label, fullHierarchy)
	loaded := classicPageLoaded{err: err}
	if err == nil {
		loaded.schema = &schema
	}
	r.schemas[key] = loaded
	return loaded.schema, loaded.err
}

// layersOf is the memoized layer enumeration; a failed enumeration is not cached, as in clio.
func (r *classicPageRun) layersOf(ctx context.Context, schemaName string) ([]schemaGetLayer, error) {
	if cached, ok := r.layers[strings.ToLower(schemaName)]; ok {
		return cached, nil
	}
	layers, err := r.client.schemaGetLayers(ctx, schemaName)
	if err != nil {
		return nil, err
	}
	r.layers[strings.ToLower(schemaName)] = layers
	return layers, nil
}

// primeLayers enumerates the layers of many names in chunked In-filter reads. A failed chunk only means the
// names fall back to per-name lookups.
func (r *classicPageRun) primeLayers(ctx context.Context, names []string) {
	missing := []string{}
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(name)
		if strings.TrimSpace(name) == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, cached := r.layers[key]; !cached {
			missing = append(missing, name)
		}
	}
	for offset := 0; offset < len(missing); offset += classicPageLayerChunkSize {
		chunk := missing[offset:min(offset+classicPageLayerChunkSize, len(missing))]
		layersByName, err := r.client.classicPageLayersBatch(ctx, chunk)
		if err != nil {
			continue
		}
		for key, layers := range layersByName {
			r.layers[key] = layers
		}
	}
}

func (c *Client) classicPageLayersBatch(ctx context.Context, names []string) (map[string][]schemaGetLayer, error) {
	result := map[string][]schemaGetLayer{}
	for _, name := range names {
		result[strings.ToLower(name)] = []schemaGetLayer{}
	}
	column := func(path string) map[string]any {
		return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
	}
	packageName, level := column("SysPackage.Name"), column("SysPackage.HierarchyLevel")
	packageName["orderDirection"], packageName["orderPosition"] = 1, 1
	level["orderDirection"], level["orderPosition"] = 1, 0
	query := map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0, "rowCount": -1,
		"columns": map[string]any{"items": map[string]any{
			"UId": column("UId"), "Name": column("Name"), "PackageName": packageName, "HierarchyLevel": level,
		}},
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
			"byName": inFilter("Name", names, 1), "byManager": eqFilter("ManagerName", schemaGetClientUnit.managerName, 1),
		}},
	}
	rows, err := c.selectRows(ctx, query)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		name := rowText(row, "Name")
		key := strings.ToLower(name)
		if _, requested := result[key]; !requested || name == "" {
			continue
		}
		var level int
		_ = json.Unmarshal(row["HierarchyLevel"], &level)
		result[key] = append(result[key], schemaGetLayer{uid: rowText(row, "UId"), name: name,
			packageName: rowText(row, "PackageName"), hierarchyLevel: level})
	}
	for key := range result {
		layers := result[key]
		sort.SliceStable(layers, func(i, j int) bool {
			if layers[i].hierarchyLevel != layers[j].hierarchyLevel {
				return layers[i].hierarchyLevel < layers[j].hierarchyLevel
			}
			return compareOrdinalIgnoreCase(layers[i].packageName, layers[j].packageName) < 0
		})
	}
	return result, nil
}

// chainAndSeed reads the page's own layers and the parent-template seed from the designer hierarchy, falling
// back to per-layer enumeration and a parent walk when the hierarchy has no layer of the page.
func (r *classicPageRun) chainAndSeed(ctx context.Context, schemaName string) (*jnode, *jnode, string, error) {
	if hierarchy, err := r.client.classicPageHierarchyBaseToTop(ctx, schemaName); err == nil && len(hierarchy) > 0 {
		schemas, seed, topUID := newArray(), newArray(), ""
		for _, layer := range hierarchy {
			body := ""
			if layer.Body != nil {
				body = *layer.Body
			}
			if strings.EqualFold(layer.Name, schemaName) {
				schemas.items = append(schemas.items, classicPageLayerEntry(layer.PackageName, body, true))
				topUID = layer.UID
			} else {
				seed.items = append(seed.items, classicPageLayerEntry(layer.PackageName, body, false))
			}
		}
		if len(schemas.items) > 0 {
			return schemas, seed, topUID, nil
		}
	}
	schemas, top, topUID, err := r.layerChain(ctx, schemaName)
	if err != nil {
		return nil, nil, "", err
	}
	return schemas, r.seedWalk(ctx, top), topUID, nil
}

// classicPageLayerEntry is a {pkg, body} manifest item. A seed entry leaves out a blank package name.
func classicPageLayerEntry(packageName, body string, alwaysPackage bool) *jnode {
	entry := newObject()
	if alwaysPackage {
		entry.set("pkg", classicPageNullableString(packageName))
	} else if strings.TrimSpace(packageName) != "" {
		entry.set("pkg", newString(packageName))
	}
	entry.set("body", newString(body))
	return entry
}

// classicPageNullableString renders a missing package name as JSON null, as a null JToken does.
func classicPageNullableString(value string) *jnode {
	if value == "" {
		return jsonNullNode()
	}
	return newString(value)
}

func (c *Client) classicPageHierarchyBaseToTop(ctx context.Context, schemaName string) ([]pageLayer, error) {
	metadata, err := c.pageSchemaRow(ctx, schemaName)
	if err != nil {
		return nil, nil
	}
	schemaUID, packageUID := rowText(metadata, "UId"), rowText(metadata, "PackageUId")
	if strings.TrimSpace(schemaUID) == "" || strings.TrimSpace(packageUID) == "" {
		return nil, nil
	}
	designPackageUID, err := c.classicPageDesignPackageUID(ctx, schemaUID)
	if err != nil || strings.TrimSpace(designPackageUID) == "" {
		designPackageUID = packageUID
	}
	leafFirst, err := c.pageLayers(ctx, schemaUID, designPackageUID)
	if err != nil {
		return nil, err
	}
	if len(leafFirst) == 0 {
		return nil, nil
	}
	rootSchemaUID := schemaUID
	for index := len(leafFirst) - 1; index >= 0; index-- {
		if strings.EqualFold(leafFirst[index].Name, schemaName) {
			rootSchemaUID = leafFirst[index].UID
			break
		}
	}
	if !strings.EqualFold(rootSchemaUID, schemaUID) {
		full, err := c.pageLayers(ctx, rootSchemaUID, designPackageUID)
		if err != nil {
			return nil, err
		}
		if len(full) > 0 {
			leafFirst = full
		}
	}
	baseToTop := make([]pageLayer, len(leafFirst))
	for index, layer := range leafFirst {
		baseToTop[len(leafFirst)-1-index] = layer
	}
	return baseToTop, nil
}

// layerChain loads every replacing layer of a name, base to top; any layer that fails to load fails the chain.
func (r *classicPageRun) layerChain(ctx context.Context, schemaName string) (*jnode, *schemaGetDesignerSchema, string, error) {
	layers, err := r.layersOf(ctx, schemaName)
	if err != nil {
		return nil, nil, "", err
	}
	if len(layers) == 0 {
		return nil, nil, "", fmt.Errorf("Schema '%s' not found (ManagerName='%s')", schemaName, schemaGetClientUnit.managerName)
	}
	schemas := newArray()
	var top *schemaGetDesignerSchema
	topUID := ""
	for _, layer := range layers {
		item, schema, err := r.layerItem(ctx, layer, schemaName)
		if err != nil {
			return nil, nil, "", err
		}
		schemas.items = append(schemas.items, item)
		top, topUID = schema, layer.uid
	}
	return schemas, top, topUID, nil
}

func (r *classicPageRun) layerItem(ctx context.Context, layer schemaGetLayer, schemaName string) (*jnode, *schemaGetDesignerSchema, error) {
	schema, err := r.load(ctx, layer.uid, schemaName, false)
	if err != nil || schema == nil {
		reason := classicPageNoSchemaReturned
		if err != nil {
			reason = err.Error()
		}
		return nil, nil, fmt.Errorf("Failed to load layer '%s' (%s): %s", layer.packageName, layer.uid, reason)
	}
	return classicPageLayerEntry(layer.packageName, schema.body(), true), schema, nil
}

// seedWalk follows parent links from the top layer to the base template and seeds every layer of each parent
// template, base first. Each parent UId is followed once, so a cycle stops the walk with a warning.
func (r *classicPageRun) seedWalk(ctx context.Context, top *schemaGetDesignerSchema) *jnode {
	levels := [][]*jnode{}
	visited, seededNames, seededUIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	current := top
	for current != nil {
		var parent map[string]json.RawMessage
		_ = json.Unmarshal(current.values["parent"], &parent)
		parentUID := rowText(parent, "uId")
		if strings.TrimSpace(parentUID) == "" || strings.EqualFold(parentUID, emptyGUID) {
			break
		}
		if visited[strings.ToLower(parentUID)] {
			r.warn(fmt.Sprintf("Parent-template walk stopped on a cycle at '%s'; the seed may be truncated.", parentUID))
			break
		}
		visited[strings.ToLower(parentUID)] = true
		parentLayer, err := r.load(ctx, parentUID, "", false)
		if err != nil || parentLayer == nil {
			reason := classicPageNoSchemaReturned
			if err != nil {
				reason = err.Error()
			}
			r.warn(fmt.Sprintf("Parent-template walk stopped at '%s' (%s); the seed is ", parentUID, reason) +
				"truncated and the base containers defined above this point are missing from the manifest.")
			break
		}
		levels = append(levels, r.parentLevel(ctx, parentUID, parentLayer, seededNames, seededUIDs))
		current = parentLayer
	}
	seed := newArray()
	for index := len(levels) - 1; index >= 0; index-- {
		seed.items = append(seed.items, levels[index]...)
	}
	return seed
}

func (r *classicPageRun) parentLevel(ctx context.Context, parentUID string, parentLayer *schemaGetDesignerSchema,
	seededNames, seededUIDs map[string]bool) []*jnode {
	linkedOnly := func() []*jnode {
		if seededUIDs[strings.ToLower(parentUID)] {
			return nil
		}
		seededUIDs[strings.ToLower(parentUID)] = true
		return []*jnode{classicPageLayerEntry(parentLayer.packageName(), parentLayer.body(), false)}
	}
	parentName := parentLayer.name()
	if strings.TrimSpace(parentName) == "" || seededNames[strings.ToLower(parentName)] {
		return linkedOnly()
	}
	seededNames[strings.ToLower(parentName)] = true
	layers, err := r.layersOf(ctx, parentName)
	if err != nil {
		r.warn(fmt.Sprintf("Could not enumerate parent template '%s' layers (%s); only the linked layer ", parentName, err.Error()) +
			"is seeded, so containers defined in a sibling layer of that template are missing from the manifest.")
		return linkedOnly()
	}
	if len(layers) == 0 {
		return linkedOnly()
	}
	entries := []*jnode{}
	for _, layer := range layers {
		key := strings.ToLower(layer.uid)
		if seededUIDs[key] {
			continue
		}
		seededUIDs[key] = true
		if strings.EqualFold(layer.uid, parentUID) {
			entries = append(entries, classicPageLayerEntry(layer.packageName, parentLayer.body(), false))
			continue
		}
		schema, err := r.load(ctx, layer.uid, parentName, false)
		if err != nil || schema == nil {
			reason := classicPageNoSchemaReturned
			if err != nil {
				reason = err.Error()
			}
			r.warn(fmt.Sprintf("Could not load parent-template layer '%s' (%s): %s. ", parentName, layer.uid, reason) +
				"That layer's body is missing from the seed.")
			continue
		}
		entries = append(entries, classicPageLayerEntry(layer.packageName, schema.body(), false))
	}
	if len(entries) == 0 {
		return linkedOnly()
	}
	return entries
}

func classicPageEntryBody(entry *jnode) string {
	if body := entry.get("body"); body != nil && body.kind == jkString {
		return body.text
	}
	return ""
}

// classicPageInferEntity returns the first entitySchemaName any of the bodies declares, in order.
func classicPageInferEntity(entries []*jnode) string {
	for _, entry := range entries {
		if entity := classicPageEntityFromBody(classicPageEntryBody(entry)); entity != "" {
			return entity
		}
	}
	return ""
}

func classicPageEntityFromBody(body string) string {
	if body == "" {
		return ""
	}
	if match := classicPageEntityPattern.FindStringSubmatch(body); match != nil {
		return match[1]
	}
	return ""
}

func classicPageDetailNames(entries []*jnode) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		for _, match := range classicPageDetailPattern.FindAllStringSubmatch(classicPageEntryBody(entry), -1) {
			if key := strings.ToLower(match[1]); !seen[key] {
				seen[key] = true
				names = append(names, match[1])
			}
		}
	}
	return names
}

// classicPageDetailOverrides reads `schemaName: "XDetail", entitySchemaName: "Y"` pairs from the page bodies.
// The seed is read first so the page's own layers overwrite it. Keys are lower-case detail names.
func classicPageDetailOverrides(seed, schemas []*jnode) map[string]string {
	overrides := map[string]string{}
	for _, entries := range [][]*jnode{seed, schemas} {
		for _, entry := range entries {
			for _, match := range classicPageOverride.FindAllStringSubmatch(classicPageEntryBody(entry), -1) {
				detail, entity := match[1], match[2]
				if detail == "" {
					entity, detail = match[3], match[4]
				}
				if strings.TrimSpace(detail) != "" && strings.TrimSpace(entity) != "" {
					overrides[strings.ToLower(detail)] = entity
				}
			}
		}
	}
	return overrides
}

func (r *classicPageRun) mergedStrings(ctx context.Context, topUID, schemaName, failurePrefix, missingNote string) []MergedLocalizableString {
	schema, err := r.load(ctx, topUID, schemaName, true)
	if err == nil && schema != nil {
		return schemaGetMergedStrings(schema.values["localizableStrings"])
	}
	reason := classicPageNoSchemaReturned
	if err != nil {
		reason = err.Error()
	}
	r.warn(fmt.Sprintf("%s: %s. %s", failurePrefix, reason, missingNote))
	return nil
}

// classicPageFlatResources keeps one text per key: the en-US text, else the first culture's, of the key's
// first entry.
func classicPageFlatResources(localized []MergedLocalizableString) *jnode {
	resources := newObject()
	for _, item := range localized {
		name := derefString(item.Name)
		if strings.TrimSpace(name) == "" || len(item.Values) == 0 {
			continue
		}
		value := item.Values[0].Value
		for _, culture := range item.Values {
			if strings.EqualFold(derefString(culture.CultureName), "en-US") {
				value = culture.Value
				break
			}
		}
		if derefString(value) != "" && resources.get(name) == nil {
			resources.set(name, newString(*value))
		}
	}
	return resources
}

// classicPageResourceStrings keeps every culture of every key, first value per culture winning.
func classicPageResourceStrings(localized []MergedLocalizableString) *jnode {
	result := newObject()
	for _, item := range localized {
		name := derefString(item.Name)
		if strings.TrimSpace(name) == "" {
			continue
		}
		cultures := result.get(name)
		if !cultures.isObject() {
			cultures = newObject()
		}
		for _, value := range item.Values {
			if strings.TrimSpace(derefString(value.CultureName)) == "" || derefString(value.Value) == "" {
				continue
			}
			culture := classicPageCultureName(*value.CultureName)
			if cultures.get(culture) == nil {
				cultures.set(culture, newString(*value.Value))
			}
		}
		if len(cultures.keys) > 0 {
			result.set(name, cultures)
		}
	}
	return result
}

// classicPageCultureName canonicalises a culture name as CultureInfo.GetCultureInfo(name).Name does for the
// language-region form (en-us -> en-US); any other shape is only trimmed.
func classicPageCultureName(name string) string {
	trimmed := strings.TrimSpace(name)
	parts := strings.Split(trimmed, "-")
	if len(parts) == 2 && len(parts[0]) >= 2 && len(parts[0]) <= 3 && len(parts[1]) == 2 {
		return strings.ToLower(parts[0]) + "-" + strings.ToUpper(parts[1])
	}
	return trimmed
}

func (r *classicPageRun) entityColumns(ctx context.Context, entity string) (*jnode, *jnode) {
	columns, titles := newObject(), newObject()
	if strings.TrimSpace(entity) == "" {
		return columns, titles
	}
	properties, err := r.client.GetEntitySchemaProperties(ctx, EntitySchemaPropertiesRequest{SchemaName: entity})
	if err != nil {
		r.warn(fmt.Sprintf("Could not gather entity columns for '%s': %s", entity, err.Error()))
		return columns, titles
	}
	for _, column := range properties.Columns {
		if strings.TrimSpace(column.Name) == "" {
			continue
		}
		meta := newObject()
		if strings.TrimSpace(column.Type) != "" {
			meta.set("type", newString(column.Type))
		}
		if reference := derefString(column.ReferenceSchemaName); strings.TrimSpace(reference) != "" {
			meta.set("ref", newString(reference))
		}
		if len(meta.keys) > 0 {
			columns.set(column.Name, meta)
		}
		if title := derefString(column.Title); strings.TrimSpace(title) != "" {
			titles.set(column.Name, newString(title))
		}
	}
	return columns, titles
}

func (r *classicPageRun) detailSchemas(ctx context.Context, names []string) *jnode {
	details := newObject()
	for _, name := range names {
		layers, err := r.layersOf(ctx, name)
		if err != nil {
			r.warn(fmt.Sprintf("Could not gather detail schema '%s': %s", name, err.Error()))
			continue
		}
		if len(layers) == 0 {
			continue
		}
		entry := r.detailEntry(ctx, layers[len(layers)-1].uid, name)
		if len(entry.keys) == 0 {
			continue
		}
		r.detailBodies(ctx, entry, layers, name)
		details.set(name, entry)
	}
	return details
}

func (r *classicPageRun) detailEntry(ctx context.Context, topUID, name string) *jnode {
	schema, mergedErr := r.load(ctx, topUID, name, true)
	merged := mergedErr == nil && schema != nil
	if !merged {
		own, ownErr := r.load(ctx, topUID, name, false)
		if ownErr != nil || own == nil {
			reason := classicPageNoSchemaReturned
			if ownErr != nil {
				reason = ownErr.Error()
			}
			r.warn(fmt.Sprintf("Could not gather detail schema '%s': %s", name, reason))
			return newObject()
		}
		reason := classicPageNoSchemaReturned
		if mergedErr != nil {
			reason = mergedErr.Error()
		}
		r.warn(fmt.Sprintf("Could not gather merged localizable strings (resourceStrings) of detail '%s': %s. Its entry carries no resourceStrings.", name, reason))
		schema = own
	}
	entry := newObject()
	entry.set("body", newString(schema.body()))
	if title := schemaCaption(schema.values["caption"]); strings.TrimSpace(title) != "" {
		entry.set("title", newString(title))
	}
	if merged {
		if localized := classicPageResourceStrings(schemaGetMergedStrings(schema.values["localizableStrings"])); len(localized.keys) > 0 {
			entry.set("resourceStrings", localized)
		}
	}
	return entry
}

// detailBodies adds every replacing layer of the detail, base to top; one failed layer drops the whole list.
func (r *classicPageRun) detailBodies(ctx context.Context, entry *jnode, layers []schemaGetLayer, name string) {
	bodies := newArray()
	for _, layer := range layers {
		item, _, err := r.layerItem(ctx, layer, name)
		if err != nil {
			r.warn(fmt.Sprintf("Could not gather the layer chain (bodies) of detail '%s': %s. Its entry carries no bodies; body is the top layer only.", name, err.Error()))
			return
		}
		bodies.items = append(bodies.items, item)
	}
	entry.set("bodies", bodies)
}

func (r *classicPageRun) sectionCandidates(ctx context.Context, schemaName, entity string) []string {
	candidates := []string{}
	if strings.TrimSpace(entity) != "" {
		names, err := r.client.classicPageSectionNames(ctx, entity)
		if err != "" {
			r.warn(fmt.Sprintf("Section metadata lookup failed (%s); fell back to name conventions, which cannot ", err) +
				"reach a renamed section or one whose schema name carries a UId infix.")
		}
		candidates = append(candidates, names...)
	}
	for _, prefix := range []string{classicPageStripPageSuffix(schemaName), entity} {
		if strings.TrimSpace(prefix) != "" {
			candidates = append(candidates, prefix+"SectionV2", prefix+"Section")
		}
	}
	return classicPageDistinct(candidates)
}

func classicPageDistinct(values []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if key := strings.ToLower(value); !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

func classicPageStripPageSuffix(schemaName string) string {
	for _, suffix := range []string{"PageV2", "Page"} {
		if len(schemaName) > len(suffix) && strings.EqualFold(schemaName[len(schemaName)-len(suffix):], suffix) {
			return schemaName[:len(schemaName)-len(suffix)]
		}
	}
	return ""
}

// section returns the layer chain of the first candidate that resolves.
func (r *classicPageRun) section(ctx context.Context, candidates []string) *jnode {
	for _, candidate := range candidates {
		layers, err := r.layersOf(ctx, candidate)
		if err != nil || len(layers) == 0 {
			continue
		}
		schemas, _, _, err := r.layerChain(ctx, candidate)
		if err != nil {
			continue
		}
		if len(schemas.items) > 0 {
			return schemas
		}
	}
	return newArray()
}

// classicPageChildInfo is what a detail entry is annotated with: its entity and its edit card, or a verified
// absence of one.
type classicPageChildInfo struct {
	entity, editPage string
	verifiedNone     bool
}

func (r *classicPageRun) childPageSchemas(ctx context.Context, details *jnode, overrides map[string]string, pageSchemaName string) (*jnode, map[string]classicPageChildInfo) {
	info := map[string]classicPageChildInfo{}
	names := r.childPageNames(ctx, details, overrides, pageSchemaName, info)
	r.primeLayers(ctx, names)
	childPages := newObject()
	for _, name := range names {
		manifest, err := r.childManifest(ctx, name)
		if err != nil {
			r.warn(fmt.Sprintf("Could not assemble child page '%s': %s", name, err.Error()))
			continue
		}
		if manifest != nil {
			childPages.set(name, manifest)
		}
	}
	return childPages, info
}

func (r *classicPageRun) childPageNames(ctx context.Context, details *jnode, overrides map[string]string, pageSchemaName string,
	info map[string]classicPageChildInfo) []string {
	entityByDetail := map[string]string{}
	entities := []string{}
	unresolved := []string{}
	for _, detail := range details.keys {
		entity, ok := overrides[strings.ToLower(detail)]
		if !ok {
			entity = classicPageEntityFromBody(classicPageEntryBody(details.get(detail)))
		}
		if strings.TrimSpace(entity) == "" {
			unresolved = append(unresolved, detail)
			continue
		}
		entityByDetail[strings.ToLower(detail)] = entity
		entities = append(entities, entity)
	}
	if len(unresolved) > 0 {
		single := len(unresolved) == 1
		pick := func(one, many string) string {
			if single {
				return one
			}
			return many
		}
		r.warn(fmt.Sprintf("Could not determine the bound entity for %s %s, so %s child pages were not looked up in "+
			"SysModuleEdit. That is NOT the same as '%s no child page'.", pick("detail", "details"), strings.Join(unresolved, ", "),
			pick("its", "their"), pick("it has", "they have")))
	}
	pagesByEntity, resolved := r.childPagesByEntity(ctx, classicPageDistinct(entities))
	names := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		if strings.TrimSpace(name) == "" || strings.EqualFold(name, pageSchemaName) || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	for _, detail := range details.keys {
		entity, hasEntity := entityByDetail[strings.ToLower(detail)]
		if pages := pagesByEntity[strings.ToLower(entity)]; hasEntity && len(pages) > 0 {
			for _, page := range pages {
				add(page.schemaName)
			}
			card := classicPagePrimaryCard(pages, entity, pageSchemaName)
			info[detail] = classicPageChildInfo{entity: entity, editPage: card, verifiedNone: card == ""}
			continue
		}
		verifiedNone := hasEntity && resolved[strings.ToLower(entity)]
		match := classicPageEditPattern.FindStringSubmatch(classicPageEntryBody(details.get(detail)))
		if match == nil {
			info[detail] = classicPageChildInfo{entity: entity, verifiedNone: verifiedNone}
			continue
		}
		info[detail] = classicPageChildInfo{entity: entity, editPage: match[1]}
		add(match[1])
	}
	return names
}

func classicPagePrimaryCard(pages []classicPageChildPage, entity, pageSchemaName string) string {
	cards := []classicPageChildPage{}
	for _, page := range pages {
		if !page.isMiniPage && !strings.EqualFold(page.schemaName, pageSchemaName) {
			cards = append(cards, page)
		}
	}
	if len(cards) == 0 {
		return ""
	}
	if strings.TrimSpace(entity) == "" {
		return cards[0].schemaName
	}
	for _, name := range []string{entity + "PageV2", entity + "Page"} {
		for _, card := range cards {
			if strings.EqualFold(card.schemaName, name) {
				return card.schemaName
			}
		}
	}
	for _, card := range cards {
		if len(card.schemaName) >= len(entity) && strings.EqualFold(card.schemaName[:len(entity)], entity) {
			return card.schemaName
		}
	}
	return cards[0].schemaName
}

func classicPageAnnotateDetails(details *jnode, info map[string]classicPageChildInfo) {
	for _, detail := range details.keys {
		entry, ok := info[detail]
		if !ok {
			continue
		}
		node := details.get(detail)
		if strings.TrimSpace(entry.entity) != "" {
			node.set("entity", newString(entry.entity))
		}
		if strings.TrimSpace(entry.editPage) != "" {
			node.set("editPage", newString(entry.editPage))
		} else if entry.verifiedNone {
			node.set("editPage", newBool(false))
		}
	}
}

// childPagesByEntity groups the SysModuleEdit pages by lower-case entity name and reports which entities
// resolved. A failed lookup is a warning, and the detail bodies are then scanned for an edit-page token.
func (r *classicPageRun) childPagesByEntity(ctx context.Context, entities []string) (map[string][]classicPageChildPage, map[string]bool) {
	pagesByEntity, resolved := map[string][]classicPageChildPage{}, map[string]bool{}
	if len(entities) == 0 {
		return pagesByEntity, resolved
	}
	lookup, err := r.client.classicPageChildPages(ctx, entities)
	if err != nil {
		r.warn(fmt.Sprintf("Child-page lookup from SysModuleEdit failed (%s); fell back to scanning the detail bodies for an ", err.Error()) +
			"edit-page token, which resolves almost nothing on a stock product (measured 0 of 845 page-detail pairs). " +
			"An empty childPageSchemas here does NOT mean the details have no child pages.")
		return pagesByEntity, resolved
	}
	for _, warning := range lookup.warnings {
		r.warn(warning)
	}
	for _, page := range lookup.pages {
		key := strings.ToLower(page.entityName)
		if strings.TrimSpace(page.schemaName) == "" {
			continue
		}
		duplicate := false
		for _, existing := range pagesByEntity[key] {
			if strings.EqualFold(existing.schemaName, page.schemaName) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			pagesByEntity[key] = append(pagesByEntity[key], page)
		}
	}
	for _, entity := range lookup.resolved {
		resolved[strings.ToLower(entity)] = true
	}
	return pagesByEntity, resolved
}

// childManifest is a nested manifest: the child page's own chain and seed, its entity and merged strings.
func (r *classicPageRun) childManifest(ctx context.Context, name string) (*jnode, error) {
	layers, err := r.layersOf(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, nil
	}
	schemas, seed, topUID, err := r.chainAndSeed(ctx, name)
	if err != nil {
		return nil, err
	}
	manifest := newObject()
	manifest.set("schemas", schemas)
	if entity := classicPageInferEntity(append(append([]*jnode{}, schemas.items...), seed.items...)); strings.TrimSpace(entity) != "" {
		manifest.set("entity", newString(entity))
	}
	if len(seed.items) > 0 {
		manifest.set("seed", seed)
	}
	localized := classicPageResourceStrings(r.mergedStrings(ctx, topUID, name,
		fmt.Sprintf("Could not gather merged localizable strings (resourceStrings) of child page '%s'", name),
		"Its nested manifest carries no resourceStrings, so the child page's localized captions will be missing."))
	if len(localized.keys) > 0 {
		manifest.set("resourceStrings", localized)
	}
	return manifest, nil
}

// classicPageNewtonsoftIndented writes the node as Newtonsoft's Formatting.Indented: two-space indentation,
// ": " after a key, empty containers inline, the platform newline, non-ASCII text left as is.
func classicPageNewtonsoftIndented(n *jnode) []byte {
	newline := "\n"
	if os.PathSeparator == '\\' {
		newline = "\r\n"
	}
	var buffer strings.Builder
	classicPageWriteIndented(&buffer, n, 0, newline)
	return []byte(buffer.String())
}

func classicPageWriteIndented(buffer *strings.Builder, n *jnode, depth int, newline string) {
	indent := func(level int) {
		buffer.WriteString(newline)
		buffer.WriteString(strings.Repeat("  ", level))
	}
	switch {
	case n == nil || n.kind == jkNull:
		buffer.WriteString("null")
	case n.kind == jkBool:
		if n.flag {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case n.kind == jkInteger || n.kind == jkFloat:
		buffer.WriteString(n.text)
	case n.kind == jkString:
		classicPageNewtonsoftString(buffer, n.text)
	case n.kind == jkArray:
		if len(n.items) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteByte('[')
		for index, item := range n.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			classicPageWriteIndented(buffer, item, depth+1, newline)
		}
		indent(depth)
		buffer.WriteByte(']')
	case n.kind == jkObject:
		if len(n.keys) == 0 {
			buffer.WriteString("{}")
			return
		}
		buffer.WriteByte('{')
		for index, key := range n.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			classicPageNewtonsoftString(buffer, key)
			buffer.WriteString(": ")
			classicPageWriteIndented(buffer, n.props[key], depth+1, newline)
		}
		indent(depth)
		buffer.WriteByte('}')
	}
}

// classicPageNewtonsoftString escapes as Newtonsoft's default StringEscapeHandling: control characters,
// quote, backslash, U+0085, U+2028 and U+2029, with lower-case hex.
func classicPageNewtonsoftString(buffer *strings.Builder, value string) {
	buffer.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			buffer.WriteString(`\"`)
		case '\\':
			buffer.WriteString(`\\`)
		case '\n':
			buffer.WriteString(`\n`)
		case '\r':
			buffer.WriteString(`\r`)
		case '\t':
			buffer.WriteString(`\t`)
		case '\b':
			buffer.WriteString(`\b`)
		case '\f':
			buffer.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x85 || r == 0x2028 || r == 0x2029 {
				fmt.Fprintf(buffer, `\u%04x`, r)
				continue
			}
			buffer.WriteRune(r)
		}
	}
	buffer.WriteByte('"')
}
