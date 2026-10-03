package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ClassicListColumnsResult is clio's get-classic-list-columns envelope. Columns and Notes are arrays on
// every path, including a failure.
type ClassicListColumnsResult struct {
	Success       bool                    `json:"success"`
	SectionSchema string                  `json:"sectionSchema,omitempty"`
	Entity        string                  `json:"entity,omitempty"`
	Source        string                  `json:"source,omitempty"`
	View          string                  `json:"view,omitempty"`
	ViewType      string                  `json:"viewType,omitempty"`
	ProfileScope  string                  `json:"profileScope,omitempty"`
	Columns       []ClassicListColumnInfo `json:"columns"`
	Notes         []string                `json:"notes"`
	Error         string                  `json:"error,omitempty"`
}

// ClassicListColumnInfo is one resolved column. Caption is absent for a dotted traversal path; Origin names
// the Classic method that declared the path and is absent when no method did.
type ClassicListColumnInfo struct {
	Name    string  `json:"name"`
	Caption *string `json:"caption,omitempty"`
	Origin  string  `json:"origin,omitempty"`
}

const (
	classicPageProfileSource       = "profile"
	classicPageSchemaDefaultSource = "schema-default"
	classicPageEntityDefaultSource = "entity-default"
	classicPageNoneSource          = "none"
	classicPageSchemaNameError     = "schema-name must start with a letter and contain only letters, digits, or underscores"
	classicPageBothMethods         = "both"
	classicPageDefaultView         = "GridDataView"
)

// classicPageColumnMethods is clio's merge order: getGridDataColumns paths come first.
var classicPageColumnMethods = []string{"getGridDataColumns", "initColumnsConfig"}

// classicPageSchemaMarkers identify a Classic schema object.
var classicPageSchemaMarkers = []string{"entitySchemaName", "methods", "diff", "getGridDataColumns", "initColumnsConfig"}

// GetClassicListColumns resolves the effective default list columns of a Classic section the way clio's
// get-classic-list-columns does: the saved grid profile first (unless ignoreProfile), then the columns the
// section bodies declare statically, then the entity's primary display column. It only reads.
func (c *Client) GetClassicListColumns(ctx context.Context, schemaName string, ignoreProfile bool) ClassicListColumnsResult {
	result, err := c.classicPageResolveColumns(ctx, schemaName, ignoreProfile)
	if err != nil {
		return ClassicListColumnsResult{SectionSchema: schemaName, Columns: []ClassicListColumnInfo{}, Notes: []string{}, Error: err.Error()}
	}
	return result
}

func (c *Client) classicPageResolveColumns(ctx context.Context, schemaName string, ignoreProfile bool) (ClassicListColumnsResult, error) {
	if strings.TrimSpace(schemaName) == "" {
		return ClassicListColumnsResult{}, errors.New("schema-name is required")
	}
	name := strings.TrimSpace(schemaName)
	if !classicPageValidSchemaName(name) {
		return ClassicListColumnsResult{}, errors.New(classicPageSchemaNameError)
	}
	notes := []string{}
	hierarchy, err := c.classicPageSectionHierarchy(ctx, name, &notes)
	if err != nil {
		return ClassicListColumnsResult{}, err
	}
	bodies := []string{}
	for index := len(hierarchy) - 1; index >= 0; index-- {
		if body := hierarchy[index].Body; body != nil && strings.TrimSpace(*body) != "" {
			bodies = append(bodies, *body)
		}
	}
	parsed := classicPageParseColumns(bodies)
	entity := classicPageParseEntityName(bodies)
	if strings.TrimSpace(entity) == "" {
		skipped := ""
		if parsed.unparsed > 0 {
			skipped = fmt.Sprintf(" %d of %d schema layers were skipped, so the declaration may be in a layer that could not be read.", parsed.unparsed, len(bodies))
		}
		return ClassicListColumnsResult{}, fmt.Errorf("Classic section '%s' does not declare entitySchemaName.%s", name, skipped)
	}
	properties, err := c.GetEntitySchemaProperties(ctx, EntitySchemaPropertiesRequest{SchemaName: entity})
	if err != nil {
		return ClassicListColumnsResult{}, err
	}
	captions := classicPageCaptionMap(properties.Columns)
	skippedNote := classicPageSkippedLayerNote(parsed, len(bodies))
	if !ignoreProfile {
		profile := c.classicPageReadProfile(ctx, name)
		notes = append(notes, profile.notes...)
		if len(profile.columns) > 0 {
			if profile.scope == "user" {
				notes = append(notes, "The calling user has a personal profile for this list, so the reported set may be that "+
					"user's own customization rather than the section's shared default.")
			}
			columns := make([]ClassicListColumnInfo, 0, len(profile.columns))
			for _, column := range profile.columns {
				caption := column.caption
				if caption == nil || strings.TrimSpace(*caption) == "" {
					caption = captions[strings.ToLower(column.path)]
				}
				columns = append(columns, ClassicListColumnInfo{Name: column.path, Caption: caption})
			}
			return ClassicListColumnsResult{Success: true, SectionSchema: name, Entity: entity, Source: classicPageProfileSource,
				View: profile.view, ViewType: profile.viewType, ProfileScope: profile.scope, Columns: columns, Notes: notes}, nil
		}
	}
	if skippedNote != "" {
		notes = append(notes, skippedNote)
	}
	if len(parsed.columns) > 0 {
		if parsed.declaresBoth {
			notes = append(notes, "The section declares both getGridDataColumns and initColumnsConfig; the reported set "+
				"merges them, so it can include columns the section loads but never renders, and overlapping "+
				"columns take their order from getGridDataColumns. Use each column's 'origin' to select the "+
				"rendered set, the loaded set, or the union.")
		}
		if parsed.subtractive > 0 {
			notes = append(notes, fmt.Sprintf("%d section schema layer(s) remove inherited columns with "+
				"'delete'; subtraction is not applied, so the reported set may include columns the section hides.", parsed.subtractive))
		}
		columns := make([]ClassicListColumnInfo, 0, len(parsed.columns))
		for _, path := range parsed.columns {
			columns = append(columns, ClassicListColumnInfo{Name: path, Caption: captions[strings.ToLower(path)],
				Origin: parsed.origins[strings.ToLower(path)]})
		}
		return ClassicListColumnsResult{Success: true, SectionSchema: name, Entity: entity, Source: classicPageSchemaDefaultSource,
			Columns: columns, Notes: notes}, nil
	}
	if primary := derefString(properties.PrimaryDisplayColumnName); strings.TrimSpace(primary) != "" {
		notes = append(notes, "The section schema does not define static list columns; using the entity primary display column.")
		return ClassicListColumnsResult{Success: true, SectionSchema: name, Entity: entity, Source: classicPageEntityDefaultSource,
			Columns: []ClassicListColumnInfo{{Name: primary, Caption: captions[strings.ToLower(primary)]}}, Notes: notes}, nil
	}
	notes = append(notes, "The section schema does not define static list columns and the entity has no primary display column.")
	return ClassicListColumnsResult{Success: true, SectionSchema: name, Entity: entity, Source: classicPageNoneSource,
		Columns: []ClassicListColumnInfo{}, Notes: notes}, nil
}

// classicPageValidSchemaName is clio's PageSchemaMetadataHelper.IsValidSchemaName.
func classicPageValidSchemaName(name string) bool {
	for index, ch := range name {
		if index == 0 && !unicode.IsLetter(ch) {
			return false
		}
		if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' {
			return false
		}
	}
	return name != ""
}

// classicPageCaptionMap keys entity column titles case-insensitively; the first column of a name wins.
func classicPageCaptionMap(columns []EntitySchemaPropertyColumn) map[string]*string {
	captions := map[string]*string{}
	for _, column := range columns {
		key := strings.ToLower(column.Name)
		if strings.TrimSpace(column.Name) == "" {
			continue
		}
		if _, seen := captions[key]; !seen {
			captions[key] = column.Title
		}
	}
	return captions
}

// classicPageSectionHierarchy is clio's name -> UId -> design package -> re-anchored designer chain, most
// derived layer first. A failed design-package lookup is a note, and the schema's own package is the anchor.
func (c *Client) classicPageSectionHierarchy(ctx context.Context, schemaName string, notes *[]string) ([]pageLayer, error) {
	metadata, err := c.pageSchemaRow(ctx, schemaName)
	if err != nil {
		return nil, err
	}
	schemaUID, packageUID := rowText(metadata, "UId"), rowText(metadata, "PackageUId")
	if strings.TrimSpace(schemaUID) == "" || strings.TrimSpace(packageUID) == "" {
		return nil, fmt.Errorf("Classic section schema '%s' metadata is incomplete.", schemaName)
	}
	designPackageUID, err := c.classicPageDesignPackageUID(ctx, schemaUID)
	if err != nil {
		*notes = append(*notes, fmt.Sprintf("GetDesignPackageUId failed for '%s' (%s); anchoring on the schema's own package.", schemaName, err.Error()))
		designPackageUID = packageUID
	}
	initial, err := c.pageLayers(ctx, schemaUID, designPackageUID)
	if err != nil {
		return nil, err
	}
	if len(initial) == 0 {
		return nil, fmt.Errorf("Classic section schema '%s' hierarchy is empty.", schemaName)
	}
	rootSchemaUID := ""
	for index := len(initial) - 1; index >= 0; index-- {
		if strings.EqualFold(initial[index].Name, schemaName) {
			rootSchemaUID = initial[index].UID
			break
		}
	}
	if strings.TrimSpace(rootSchemaUID) == "" || strings.EqualFold(rootSchemaUID, schemaUID) {
		return initial, nil
	}
	full, err := c.pageLayers(ctx, rootSchemaUID, designPackageUID)
	if err != nil {
		return nil, err
	}
	if len(full) > 0 {
		return full, nil
	}
	return initial, nil
}

// classicPageDesignPackageUID is clio's PageDesignerHierarchyClient.GetDesignPackageUId with its failure texts.
func (c *Client) classicPageDesignPackageUID(ctx context.Context, schemaUID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "userLevelSchema": false})
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", err
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(response, &decoded); err != nil {
		return "", err
	}
	var success bool
	if json.Unmarshal(decoded["success"], &success) != nil || !success {
		detail := ""
		for _, key := range []string{"errorInfo", "message", "error"} {
			if raw, ok := decoded[key]; ok && string(raw) != "null" {
				detail = rowText(decoded, key)
				break
			}
		}
		if strings.TrimSpace(detail) == "" {
			return "", errors.New("Failed to resolve design package")
		}
		return "", fmt.Errorf("Failed to resolve design package: %s", detail)
	}
	uid := rowText(decoded, "uId")
	if strings.TrimSpace(uid) == "" {
		return "", errors.New("Design package response did not return a uId")
	}
	return uid, nil
}

// classicPageColumnParse is clio's ClassicListColumnParseResult.
type classicPageColumnParse struct {
	columns      []string
	origins      map[string]string // lower-case path -> origin
	unparsed     int
	unanchored   int
	declaresBoth bool
	subtractive  int
}

// classicPageLayer is one parsed body: its anchored schema object, or why there is none.
type classicPageLayer struct {
	schema      *classicPageObject
	syntaxError bool
}

// classicPageParseLayer anchors the Classic schema object of one body: the object a define() factory returns,
// otherwise the first schema-shaped object of the body read as a bare property list.
func classicPageParseLayer(source string) classicPageLayer {
	script, err := classicPageParse(source)
	if err != nil {
		return classicPageLayer{syntaxError: true}
	}
	if schema := classicPageDefineSchemaObject(script); schema != nil {
		return classicPageLayer{schema: schema}
	}
	// clio re-parses the body as "({" + body + "})", which succeeds only for a bare property list.
	wrapped, err := classicPageParse("({" + source + "})")
	if err != nil || wrapped.object(1) == nil {
		return classicPageLayer{}
	}
	return classicPageLayer{schema: classicPageFirstSchemaObject(wrapped)}
}

func classicPageDefineSchemaObject(script *classicPageScript) *classicPageObject {
	for index, token := range script.tokens {
		if token.kind != classicPageIdent || token.text != "define" || !script.punct(index+1, "(") ||
			script.punct(index-1, ".") || script.punct(index-1, "?.") {
			continue
		}
		var factory *classicPageFunction
		for _, argument := range script.splitTopLevel(index+2, script.match[index+1]) {
			if factory = script.functionAt(argument[0], argument[1]); factory != nil {
				break
			}
		}
		if factory == nil {
			return nil
		}
		var found *classicPageObject
		script.walk(factory.bodyStart, factory.bodyEnd, func(at int) {
			if found != nil || !script.is(at, classicPageIdent, "return") {
				return
			}
			open := at + 1
			for script.punct(open, "(") && script.match[open] > open {
				open++
			}
			if !script.punct(open, "{") {
				return
			}
			after := script.match[open] + 1
			for close := at + 1; close < open; close++ {
				if !script.punct(after, ")") {
					return
				}
				after++
			}
			if script.punct(after, ".") || script.punct(after, "[") || script.punct(after, "(") || script.punct(after, "?.") {
				return
			}
			if object := script.object(open); object != nil && classicPageIsSchemaObject(object) {
				found = object
			}
		})
		return found
	}
	return nil
}

func classicPageFirstSchemaObject(script *classicPageScript) *classicPageObject {
	for index := range script.tokens {
		if index != 1 && !script.objectLiteralAt(index) {
			continue
		}
		if object := script.object(index); object != nil && classicPageIsSchemaObject(object) {
			return object
		}
	}
	return nil
}

func classicPageIsSchemaObject(object *classicPageObject) bool {
	for _, marker := range classicPageSchemaMarkers {
		if object.property(marker) != nil {
			return true
		}
	}
	return false
}

// classicPageFunctionProperty finds a function-valued property directly on the schema or inside its methods.
func classicPageFunctionProperty(schema *classicPageObject, name string) *classicPageFunction {
	property := schema.property(name)
	if property == nil {
		if methods := schema.objectValue(schema.property("methods")); methods != nil {
			property = methods.property(name)
		}
	}
	if property == nil {
		return nil
	}
	return property.function
}

func classicPageParseColumns(bodies []string) classicPageColumnParse {
	result := classicPageColumnParse{columns: []string{}, origins: map[string]string{}}
	schemas := []*classicPageObject{}
	for _, body := range bodies {
		if strings.TrimSpace(body) == "" {
			continue
		}
		layer := classicPageParseLayer(body)
		if layer.schema != nil {
			schemas = append(schemas, layer.schema)
			continue
		}
		result.unparsed++
		if !layer.syntaxError {
			result.unanchored++
		}
	}
	declaring := 0
	for _, methodName := range classicPageColumnMethods {
		type method struct {
			schema   *classicPageObject
			function *classicPageFunction
		}
		methods := []method{}
		for _, schema := range schemas {
			if function := classicPageFunctionProperty(schema, methodName); function != nil {
				methods = append(methods, method{schema, function})
			}
		}
		if len(methods) == 0 {
			continue
		}
		// The walk down stops at the most-derived layer that does not compose its parent: a full override
		// makes everything below it unreachable.
		first := len(methods) - 1
		for first > 0 && classicPageCallsParent(methods[first].schema.script, methods[first].function) {
			first--
		}
		declaring++
		for _, effective := range methods[first:] {
			script := effective.schema.script
			if classicPageRemovesColumns(script, effective.function) {
				result.subtractive++
			}
			for _, path := range classicPageStaticPaths(script, effective.function) {
				key := strings.ToLower(path)
				existing, declared := result.origins[key]
				if !declared {
					result.columns = append(result.columns, path)
				}
				if declared && existing != methodName {
					result.origins[key] = classicPageBothMethods
				} else {
					result.origins[key] = methodName
				}
			}
		}
	}
	result.declaresBoth = declaring == len(classicPageColumnMethods)
	return result
}

// classicPageParseEntityName returns the most-derived entitySchemaName literal of the hierarchy.
func classicPageParseEntityName(bodies []string) string {
	entity := ""
	for _, body := range bodies {
		if strings.TrimSpace(body) == "" {
			continue
		}
		schema := classicPageParseLayer(body).schema
		if schema == nil {
			continue
		}
		if name, ok := schema.stringValue(schema.property("entitySchemaName")); ok && classicPageSchemaPath(name) {
			entity = name
		}
	}
	return entity
}

// classicPageStaticPaths collects path/bindTo string literals of object properties inside a column method,
// skipping nested functions.
func classicPageStaticPaths(script *classicPageScript, function *classicPageFunction) []string {
	paths := []string{}
	script.walk(function.bodyStart, function.bodyEnd, func(at int) {
		token := script.tokens[at]
		if (token.kind != classicPageIdent && token.kind != classicPageString) || (token.text != "path" && token.text != "bindTo") {
			return
		}
		if !(script.punct(at-1, "{") || script.punct(at-1, ",")) || !script.punct(at+1, ":") {
			return
		}
		value := at + 2
		if value >= len(script.tokens) || script.tokens[value].kind != classicPageString {
			return
		}
		if !(script.punct(value+1, ",") || script.punct(value+1, "}")) {
			return
		}
		if classicPageSchemaPath(script.tokens[value].text) {
			paths = append(paths, script.tokens[value].text)
		}
	})
	return paths
}

// classicPageCallsParent reports a callParent call (bare or as a non-computed member) in the method.
func classicPageCallsParent(script *classicPageScript, function *classicPageFunction) bool {
	found := false
	script.walk(function.bodyStart, function.bodyEnd, func(at int) {
		if script.is(at, classicPageIdent, "callParent") && script.punct(at+1, "(") && !script.punct(at-1, "?.") {
			found = true
		}
	})
	return found
}

// classicPageRemovesColumns reports a delete of a member expression in the method.
func classicPageRemovesColumns(script *classicPageScript, function *classicPageFunction) bool {
	found := false
	script.walk(function.bodyStart, function.bodyEnd, func(at int) {
		if !script.is(at, classicPageIdent, "delete") || at+2 >= function.bodyEnd {
			return
		}
		if next := script.tokens[at+1]; next.kind == classicPageIdent &&
			(script.punct(at+2, ".") || script.punct(at+2, "[") || script.punct(at+2, "?.")) {
			found = true
		}
	})
	return found
}

func classicPageSchemaPath(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for index, ch := range value {
		if index == 0 && !unicode.IsLetter(ch) {
			return false
		}
		if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' && ch != '.' {
			return false
		}
	}
	return true
}

func classicPageSkippedLayerNote(parsed classicPageColumnParse, bodyCount int) string {
	if parsed.unparsed == 0 {
		return ""
	}
	invalid := parsed.unparsed - parsed.unanchored
	reason := ""
	switch {
	case invalid == 0:
		reason = "did not expose a Classic schema object"
	case parsed.unanchored == 0:
		reason = "could not be parsed as JavaScript"
	default:
		reason = fmt.Sprintf("were skipped (%d could not be parsed as JavaScript, %d exposed no Classic schema object)", invalid, parsed.unanchored)
	}
	return fmt.Sprintf("%d of %d section schema layers %s and were skipped; the resolved columns may be incomplete.", parsed.unparsed, bodyCount, reason)
}
