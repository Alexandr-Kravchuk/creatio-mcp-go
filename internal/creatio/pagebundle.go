package creatio

import (
	"fmt"
	"strings"
)

// parsedPageBody is clio's PageParsedSchemaBody: the JSON sections as trees and the code sections as text.
type parsedPageBody struct {
	viewConfigDiff      *jnode
	viewModelConfig     *jnode
	viewModelConfigDiff *jnode
	modelConfig         *jnode
	modelConfigDiff     *jnode
	deps, args          string
	handlers            string
	converters          string
	validators          string
}

func emptyParsedPageBody() parsedPageBody {
	return parsedPageBody{viewConfigDiff: newArray(), viewModelConfig: newObject(), viewModelConfigDiff: newArray(),
		modelConfig: newObject(), modelConfigDiff: newArray(), deps: "[]", args: "()", handlers: "[]", converters: "{}", validators: "{}"}
}

// parsePageBody reads a page body: a mobile body is one JSON object, a web body is an AMD module whose sections
// sit between /**MARKER*/ comments. Section text is lenient JSON, as clio's JSONH reader accepts it.
func parsePageBody(body string) (parsedPageBody, error) {
	if strings.TrimSpace(body) == "" {
		return parsedPageBody{}, fmt.Errorf("Page schema body is empty")
	}
	parsed := emptyParsedPageBody()
	if strings.HasPrefix(strings.TrimLeft(body, " \t\r\n"), "{") {
		mobile, err := parseJNode([]byte(body))
		if err != nil || !mobile.isObject() {
			message := "the body is not a JSON object"
			if err != nil {
				message = err.Error()
			}
			return parsedPageBody{}, fmt.Errorf("Failed to parse mobile page body as JSON: %s", message)
		}
		parsed.viewConfigDiff = arrayOrEmpty(mobile.get("viewConfigDiff"))
		parsed.viewModelConfigDiff = arrayOrEmpty(mobile.get("viewModelConfigDiff"))
		parsed.modelConfigDiff = arrayOrEmpty(mobile.get("modelConfigDiff"))
		return parsed, nil
	}
	sections := []struct {
		markers  []string
		target   **jnode
		fallback func() *jnode
	}{
		{[]string{"SCHEMA_VIEW_CONFIG_DIFF", "SCHEMA_DIFF"}, &parsed.viewConfigDiff, newArray},
		{[]string{"SCHEMA_VIEW_MODEL_CONFIG"}, &parsed.viewModelConfig, newObject},
		{[]string{"SCHEMA_VIEW_MODEL_CONFIG_DIFF"}, &parsed.viewModelConfigDiff, newArray},
		{[]string{"SCHEMA_MODEL_CONFIG"}, &parsed.modelConfig, newObject},
		{[]string{"SCHEMA_MODEL_CONFIG_DIFF"}, &parsed.modelConfigDiff, newArray},
	}
	for _, section := range sections {
		content, ok := readPageSection(body, section.markers...)
		// A present but empty section is a parse error in clio too: JSONH reports "Expected token".
		if !ok {
			*section.target = section.fallback()
			continue
		}
		raw, err := lenientJSON(content)
		if err == nil {
			*section.target, err = parseJNode(raw)
		}
		if err != nil {
			return parsedPageBody{}, fmt.Errorf("Failed to parse schema section '%s': %s", section.markers[0], err.Error())
		}
	}
	readRaw := func(fallback string, markers ...string) string {
		if content, ok := readPageSection(body, markers...); ok {
			return strings.TrimSpace(content)
		}
		return fallback
	}
	parsed.deps = readRaw("[]", "SCHEMA_DEPS")
	parsed.args = readRaw("()", "SCHEMA_ARGS")
	parsed.handlers = readRaw("[]", "SCHEMA_HANDLERS", "SCHEMA_HANDLERS_CONFIG")
	parsed.converters = readRaw("{}", "SCHEMA_CONVERTERS")
	parsed.validators = readRaw("{}", "SCHEMA_VALIDATORS")
	return parsed, nil
}

func arrayOrEmpty(n *jnode) *jnode {
	if n.isArray() {
		return n
	}
	return newArray()
}

func objectOrEmpty(n *jnode) *jnode {
	if n.isObject() {
		return n
	}
	return newObject()
}

type pageBundlePart struct {
	schema pageLayer
	parsed parsedPageBody
}

// buildPageBundle merges the hierarchy (HEAD first) into clio's bundle.json view: the view config through the
// view-config diff applier, the view-model and model configs through the path applier, plus resources,
// parameters and optional properties. A merge the platform would reject panics with an applierFault.
func buildPageBundle(parts []pageBundlePart) *jnode {
	current := parts[0]
	mergeOrder := make([]pageBundlePart, 0, len(parts))
	for index := len(parts) - 1; index >= 0; index-- {
		mergeOrder = append(mergeOrder, parts[index])
	}
	diffs := make([]*jnode, 0, len(mergeOrder))
	options := make([]*applierOptions, 0, len(mergeOrder))
	for _, part := range mergeOrder {
		diffs = append(diffs, arrayOrEmpty(part.parsed.viewConfigDiff))
		options = append(options, &applierOptions{applyMoveIfIndirectParentMoved: part.schema.SchemaVersion >= 1})
	}
	viewConfig := expectKind(newJSONDiffApplier(false).applyDiff(newArray(), diffs, options), jkArray, "view config")
	viewModelConfig := buildPageConfig(mergeOrder, func(p parsedPageBody) (*jnode, *jnode) { return p.viewModelConfig, p.viewModelConfigDiff })
	modelConfig := buildPageConfig(mergeOrder, func(p parsedPageBody) (*jnode, *jnode) { return p.modelConfig, p.modelConfigDiff })
	return toJNode(orderedFields{
		{"name", current.schema.Name},
		{"viewConfig", viewConfig},
		{"viewModelConfig", viewModelConfig},
		{"modelConfig", modelConfig},
		{"resources", orderedFields{{"strings", pageResources(mergeOrder)}}},
		{"handlers", current.parsed.handlers},
		{"converters", current.parsed.converters},
		{"validators", current.parsed.validators},
		{"parameters", pageParameters(mergeOrder, current.schema.UID)},
		{"deps", current.parsed.deps},
		{"args", current.parsed.args},
		{"optionalProperties", pageOptionalProperties(mergeOrder)},
		{"containers", pageContainers(viewConfig)},
		{"schemas", pageSchemaChain(parts)},
	})
}

func expectKind(n *jnode, kind jsonKind, name string) *jnode {
	if n == nil || n.kind != kind {
		expected := map[jsonKind]string{jkArray: "JArray", jkObject: "JObject"}[kind]
		diffFault("Resolved %s was %s, expected %s.", name, tokenTypeName(n), expected)
	}
	return n
}

func tokenTypeName(n *jnode) string {
	if n == nil {
		return "null"
	}
	return map[jsonKind]string{jkNull: "Null", jkBool: "Boolean", jkInteger: "Integer", jkFloat: "Float",
		jkString: "String", jkArray: "Array", jkObject: "Object"}[n.kind]
}

// buildPageConfig folds one config chain: a layer with diff operations goes through the path applier (one
// instance per chain, so aliases carry across layers), a layer without them deep-merges its plain config.
func buildPageConfig(parts []pageBundlePart, selector func(parsedPageBody) (*jnode, *jnode)) *jnode {
	result := newObject()
	applier := newJSONDiffApplier(true)
	for _, part := range parts {
		config, diff := selector(part.parsed)
		diff = arrayOrEmpty(diff)
		if len(diff.items) > 0 {
			result = expectKind(applier.apply(result, diff, nil), jkObject, "config")
			continue
		}
		merged := result.clone()
		if config.isObject() {
			newtonsoftMerge(merged, config)
		}
		result = merged
	}
	return result
}

// newtonsoftMerge is JObject.Merge with MergeArrayHandling.Replace and MergeNullValueHandling.Merge.
func newtonsoftMerge(target, source *jnode) {
	for _, key := range source.keys {
		incoming := source.props[key]
		existing := target.get(key)
		switch {
		case existing == nil:
			target.set(key, incoming.clone())
		case existing.isObject() && incoming.isObject():
			newtonsoftMerge(existing, incoming)
		case existing.isArray() && incoming.isArray():
			existing.items = incoming.clone().items
		default:
			target.set(key, incoming.clone())
		}
	}
}

func pageResources(parts []pageBundlePart) *jnode {
	result := newObject()
	for _, part := range parts {
		for _, localizable := range part.schema.LocalizableStrings.items {
			if !localizable.isObject() {
				continue
			}
			name := textOrEmpty(localizable.get("name").tokenString())
			if strings.TrimSpace(name) == "" {
				continue
			}
			existing := result.get(name)
			if existing != nil && existing.kind != jkNull && !existing.isObject() {
				continue
			}
			values := existing
			if !values.isObject() {
				values = newObject()
			}
			if localized := localizable.get("values"); localized.isArray() {
				for _, value := range localized.items {
					if !value.isObject() {
						continue
					}
					culture := textOrEmpty(value.get("cultureName").tokenString())
					if strings.TrimSpace(culture) == "" {
						continue
					}
					values.set(culture, toJNode(value.get("value").tokenString()))
				}
			}
			result.set(name, values)
		}
	}
	return result
}

func pageParameters(parts []pageBundlePart, currentSchemaUID string) []any {
	order := []string{}
	byName := map[string]*jnode{}
	for _, part := range parts {
		for _, parameter := range part.schema.Parameters.items {
			if !parameter.isObject() {
				continue
			}
			name := textOrEmpty(parameter.get("name").tokenString())
			if strings.TrimSpace(name) == "" {
				continue
			}
			var dataValueType *int
			if token := parameter.get("type"); token != nil && token.kind != jkNull {
				value := tokenInt(token)
				dataValueType = &value
			}
			required := false
			if token := parameter.get("required"); token != nil && token.kind == jkBool {
				required = token.flag
			}
			var caption *jnode
			if token := parameter.get("caption"); token != nil {
				caption = token.clone()
			}
			if _, seen := byName[name]; !seen {
				order = append(order, name)
			}
			byName[name] = toJNode(orderedFields{
				{"uId", parameter.get("uId").tokenString()},
				{"name", name},
				{"caption", caption},
				{"dataValueType", dataValueType},
				{"required", required},
				{"isOwnParameter", strings.EqualFold(textOrEmpty(parameter.get("parentSchemaUId").tokenString()), currentSchemaUID) &&
					parameter.get("parentSchemaUId") != nil},
				{"referenceSchemaUId", parameter.get("lookup").tokenString()},
				{"referenceSchemaName", parameter.get("schema").tokenString()},
			})
		}
	}
	result := make([]any, 0, len(order))
	for _, name := range order {
		result = append(result, byName[name])
	}
	return result
}

func pageOptionalProperties(parts []pageBundlePart) []any {
	order := []string{}
	byKey := map[string]*jnode{}
	for _, part := range parts {
		for _, property := range part.schema.OptionalProperties.items {
			if !property.isObject() {
				continue
			}
			key := textOrEmpty(property.get("key").tokenString())
			if strings.TrimSpace(key) == "" {
				continue
			}
			if _, seen := byKey[key]; !seen {
				order = append(order, key)
			}
			byKey[key] = property.clone()
		}
	}
	result := make([]any, 0, len(order))
	for _, key := range order {
		result = append(result, byKey[key])
	}
	return result
}

func pageContainers(viewConfig *jnode) []any {
	result := []any{}
	var collect func(node *jnode, parentPath string)
	collect = func(node *jnode, parentPath string) {
		switch {
		case node.isArray():
			for _, item := range node.items {
				collect(item, parentPath)
			}
		case node.isObject():
			name := node.get("name").tokenString()
			items := node.get("items")
			if items.isArray() && name != nil && strings.TrimSpace(*name) != "" {
				path := *name
				if parentPath != "" {
					path = parentPath + "/" + *name
				}
				result = append(result, orderedFields{{"name", *name}, {"type", node.get("type").tokenString()},
					{"childCount", len(items.items)}, {"path", path}})
				collect(items, path)
			} else if items.isArray() {
				collect(items, parentPath)
			}
		}
	}
	collect(viewConfig, "")
	return result
}

func pageSchemaChain(parts []pageBundlePart) []any {
	result := make([]any, 0, len(parts))
	for _, part := range parts {
		result = append(result, orderedFields{{"schemaUId", part.schema.UID}, {"schemaName", part.schema.Name},
			{"packageUId", part.schema.PackageUID}, {"packageName", part.schema.PackageName}, {"hasBody", part.schema.Body != nil}})
	}
	return result
}
