package creatio

// localize-page, ported from clio 8.1.0.142's LocalizePageCommand: the captions of one Freedom UI page are
// written in one culture (never en-US, never another culture), or only their coverage is reported. The culture
// is resolved against SysCulture, the editable schema the way the page designer does, the supplied keys must
// already exist on the page or its ancestors, the schema is saved through ClientUnitSchemaDesignerService, the
// .clio-pages baseline is refreshed, and the stored values are read back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	localizeDefaultCulture   = "en-US"
	localizeCaptionWritten   = "written"
	localizeCaptionUnchanged = "unchanged"
	localizeUnknownKeys      = "Unknown resource key(s) on page '%s': %s. localize-page translates existing keys only; register a new key " +
		"with update-page first. Available keys: %s."
	localizeDataSourceBoundHint = " Key(s) %s are data-source-bound captions provided by the entity column caption: translate them with " +
		"update-entity-schema or modify-entity-schema-column title-localizations."
	localizeNoEditableSchema = "Page '%s' has no editable schema in design package '%s'. localize-page never creates a replacing schema; " +
		"create one first, for example with the first update-page into the design package."
	localizeDefaultCultureRefused = "localize-page does not write the default culture '%s'. Change %s values with update-page `resources` " +
		"(it registers keys and writes %s); localize-page adds the other cultures."
	localizeUnstorableValue = "The value of resource key '%s' contains a character Creatio cannot store in a schema resource (a control " +
		"character other than tab, LF or CR, U+FFFE, U+FFFF or a lone surrogate half). Remove it and retry; nothing was saved."
	localizeUnstorableCaption = "The caption contains a character Creatio cannot store in a schema resource (a control character other than " +
		"tab, LF or CR, U+FFFE, U+FFFF or a lone surrogate half). Remove it and retry; nothing was saved."
	localizeNullValues  = "resources must map every key to a string value; key(s) with a null value: %s."
	localizeBlankValues = "resources must map every key to a non-blank value; key(s) with an empty or whitespace-only value: %s. " +
		"Nothing was saved."
	localizeBlankCaption       = "The caption is whitespace-only; pass the page title text, or omit caption. Nothing was saved."
	localizeSaveOutcomeUnknown = "The save request failed before Creatio confirmed it, so it may or may not have been stored: %s. " +
		"Run localize-page again without resources and caption to see the stored coverage. The .clio-pages " +
		"baseline was not refreshed; run get-page before the next update-page."
	localizeBaselineRefreshFailed = "The page was saved, but refreshing the .clio-pages baseline failed: %s. The next update-page may report " +
		"an external modification; run get-page again to refresh the baseline."
	localizeWorkspaceCaptureWarning = "Page translations were saved on the server; localize-page does not capture your workspace source. " +
		"A push-workspace from stale metadata/resource XML can revert these changes. Preserve local edits, capture " +
		"the affected package with restore-workspace (pull-workspace), and review its schema metadata and culture " +
		"resource XML before pushing. For linked FSM workspaces, follow the workspace's capture instructions."
	localizeReadbackMismatch = "The page was saved, but the stored '%s' value differs from the requested one for: %s."
	localizeStaleBaseline    = "The .clio-pages baseline of '%s' was stale (the page changed after the get-page that wrote it), so it was " +
		"left unchanged; update-page will report the conflict. Re-read the page with get-page and merge before saving."
	localizeUnprovenBaseline = "The .clio-pages baseline of '%s' was left unchanged because the page checksum could not be read before the " +
		"save, so the baseline could not be proven current; update-page may report a conflict for this save. Re-read " +
		"the page with get-page before update-page."
	localizeDroppedBaseline = "The .clio-pages baseline of '%s' was removed because the saved checksum could not be read; run get-page " +
		"again before update-page."
	localizeCultureAbsent = "Culture '%s' is not available in this environment. Add it in the Languages section " +
		"(System Designer → Languages) first. Available: %s."
	localizeCultureInactive = "Culture '%s' exists but is inactive; users cannot select it until it is activated in the Languages section. " +
		"After activating it, run a full configuration compile (clio compile-configuration --all); until then the " +
		"UI does not load in that culture."
	localizeDictionaryType = "System.Collections.Generic.Dictionary`2[System.String,System.String]"
)

// LocalizePageRequest is clio's LocalizePageOptions.
type LocalizePageRequest struct {
	SchemaName      *string
	Culture         *string
	Resources       *string
	Caption         *string
	OutputDirectory string
	EnvironmentName string
	EnvironmentURI  string
}

// LocalizePageCoverage is clio's LocalizePageCoverage.
type LocalizePageCoverage struct {
	Keys                 int      `json:"keys"`
	Translated           int      `json:"translated"`
	Missing              []string `json:"missing"`
	SameAsDefault        []string `json:"sameAsDefault"`
	CaptionSameAsDefault bool     `json:"captionSameAsDefault"`
	CaptionInherited     bool     `json:"captionInherited"`
	CaptionValue         *string  `json:"captionValue,omitempty"`
}

// LocalizePageResponse is clio's LocalizePageResponse as the MCP server writes it (null members left out).
type LocalizePageResponse struct {
	Success        bool                  `json:"success"`
	SchemaName     *string               `json:"schemaName,omitempty"`
	SchemaUID      *string               `json:"schemaUId,omitempty"`
	PackageName    *string               `json:"packageName,omitempty"`
	Culture        *string               `json:"culture,omitempty"`
	CultureActive  *bool                 `json:"cultureActive,omitempty"`
	Saved          bool                  `json:"saved"`
	Written        []string              `json:"written"`
	Unchanged      []string              `json:"unchanged"`
	CaptionOutcome *string               `json:"captionOutcome,omitempty"`
	Coverage       *LocalizePageCoverage `json:"coverage,omitempty"`
	Warnings       []string              `json:"warnings"`
	Error          *string               `json:"error,omitempty"`
}

// LocalizePageFailure is LocalizePageTool.Failure / LocalizePageCommand.Failure without a culture.
func LocalizePageFailure(schemaName *string, message string) LocalizePageResponse {
	return LocalizePageResponse{SchemaName: schemaName, Error: &message, Written: []string{}, Unchanged: []string{}, Warnings: []string{}}
}

type localizeCulture struct {
	name   string
	active bool
}

func (r LocalizePageResponse) withCulture(culture *localizeCulture) LocalizePageResponse {
	if culture != nil {
		name, active := culture.name, culture.active
		r.Culture, r.CultureActive = &name, &active
	}
	return r
}

// localizeEditable is LocalizePageCommand.EditableSchema.
type localizeEditable struct {
	uid         string
	packageName *string
	hierarchy   []pageLayer
}

// localizeKeys is the resolved key map: name -> culture values, in first-seen order.
type localizeKeys struct {
	names  []string
	values map[string]*jnode
}

// LocalizePage is LocalizePageCommand.Localize: a remote failure is reported in the answer, never raised.
func (c *Client) LocalizePage(ctx context.Context, request LocalizePageRequest) LocalizePageResponse {
	response, err := c.localizeCore(ctx, request)
	if err != nil {
		return LocalizePageFailure(request.SchemaName, redact.Text(c.clioRequestFailureText(err)))
	}
	return response
}

// localizeHierarchy is PageDesignerHierarchyClient.GetParentSchemas with the parts localize-page reads; a
// refusal names the server's errorInfo as JToken.ToString() writes it.
func (c *Client) localizeHierarchy(ctx context.Context, schemaUID, packageUID string) ([]pageLayer, error) {
	body := toJNode(orderedFields{{"schemaUId", schemaUID}, {"packageUId", packageUID}, {"useFullHierarchy", true},
		{"userLevelSchema", false}}).newtonsoftJSON()
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas", Body: body,
		Timeout: 120 * time.Second, Limit: pageHierarchyResponseBytes})
	if err != nil {
		return nil, err
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		return nil, errors.New(pageWriteNewtonsoftParseError(payload, err))
	}
	if success := response.get("success"); success == nil || success.kind != jkBool || !success.flag {
		detail := ""
		for _, key := range []string{"errorInfo", "message", "error"} {
			if value := response.get(key); value != nil {
				detail = pageWriteJTokenText(value)
				break
			}
		}
		if strings.TrimSpace(detail) == "" {
			return nil, errors.New("Failed to load page schema hierarchy")
		}
		return nil, errors.New("Failed to load page schema hierarchy: " + detail)
	}
	values := response.get("values")
	if !values.isArray() {
		return nil, errors.New("Page schema hierarchy response does not contain values")
	}
	text := func(node *jnode) string {
		if node == nil || node.kind == jkNull {
			return ""
		}
		return *node.tokenString()
	}
	first := func(nodes ...*jnode) string {
		for _, node := range nodes {
			if node != nil {
				return text(node)
			}
		}
		return ""
	}
	array := func(node *jnode) *jnode {
		if node.isArray() {
			return node
		}
		return newArray()
	}
	layers := make([]pageLayer, 0, len(values.items))
	for _, value := range values.items {
		if !value.isObject() {
			continue
		}
		pkg := value.get("package")
		layers = append(layers, pageLayer{UID: first(value.get("uId"), value.get("id")), Name: text(value.get("name")),
			PackageUID: first(pkg.get("uId"), value.get("packageUId")), PackageName: first(pkg.get("name"), value.get("packageName")),
			LocalizableStrings: array(value.get("localizableStrings")), Caption: array(value.get("caption"))})
	}
	return layers, nil
}

func localizeText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (c *Client) localizeCore(ctx context.Context, request LocalizePageRequest) (LocalizePageResponse, error) {
	schemaName := localizeText(request.SchemaName)
	fail := func(message string, culture *localizeCulture) (LocalizePageResponse, error) {
		return LocalizePageFailure(request.SchemaName, message).withCulture(culture), nil
	}
	if strings.TrimSpace(schemaName) == "" {
		return fail("schema-name is required", nil)
	}
	cultureName := localizeText(request.Culture)
	if strings.TrimSpace(cultureName) == "" {
		return fail("culture is required", nil)
	}
	if strings.EqualFold(dotnetTrim(cultureName), localizeDefaultCulture) {
		return fail(fmt.Sprintf(localizeDefaultCultureRefused, localizeDefaultCulture, localizeDefaultCulture, localizeDefaultCulture), nil)
	}
	resources, problem := localizeParseResources(localizeText(request.Resources))
	if problem != "" {
		return fail(problem, nil)
	}
	captionText := localizeText(request.Caption)
	if captionText != "" && strings.TrimFunc(captionText, unicode.IsSpace) == "" {
		return fail(localizeBlankCaption, nil)
	}
	var caption *string
	if captionText != "" {
		trimmed := dotnetTrim(captionText)
		caption = &trimmed
	}
	for _, key := range resources.keys {
		if localizeUnstorable(*resources.values[key]) {
			return fail(fmt.Sprintf(localizeUnstorableValue, key), nil)
		}
	}
	if caption != nil && localizeUnstorable(*caption) {
		return fail(localizeUnstorableCaption, nil)
	}
	culture, warning, err := c.localizeResolveCulture(ctx, cultureName)
	if err != nil {
		return LocalizePageResponse{}, err
	}
	warnings := []string{}
	if warning != "" {
		warnings = append(warnings, warning)
	}
	editable, problem, err := c.localizeEditableSchema(ctx, schemaName)
	if err != nil {
		return LocalizePageResponse{}, err
	}
	if problem != "" {
		return fail(problem, culture)
	}
	schema, problem, err := c.localizeGetSchema(ctx, editable.uid)
	if err != nil {
		return LocalizePageResponse{}, err
	}
	if problem != "" {
		return fail(problem, culture)
	}
	packageName := editable.packageName
	if name := schema.get("package").get("name").tokenString(); name != nil && *name != "" {
		packageName = name
	}
	localizableStrings := schema.get("localizableStrings")
	if !localizableStrings.isArray() {
		localizableStrings = newArray()
		schema.set("localizableStrings", localizableStrings)
	}
	known := localizeResolveKeys(editable.hierarchy, localizableStrings)
	if message := localizeDescribeUnknownKeys(schemaName, schema, localizableStrings, known, resources); message != "" {
		failure := LocalizePageFailure(request.SchemaName, message).withCulture(culture)
		uid := editable.uid
		failure.SchemaUID, failure.PackageName = &uid, packageName
		return failure, nil
	}
	written, unchanged := []string{}, []string{}
	for _, key := range resources.keys {
		value := *resources.values[key]
		var entry *jnode
		for _, item := range localizableStrings.items {
			if item.isObject() && localizeTokenEquals(item.get("name"), key) {
				entry = item
				break
			}
		}
		changed := false
		if entry != nil {
			values := entry.get("values")
			if !values.isArray() {
				values = newArray()
				entry.set("values", values)
			}
			changed = localizeSetCultureValue(values, culture.name, value)
		} else {
			current := localizeGetCultureValue(known.values[key], culture.name)
			changed = current == nil || *current != value
			if changed {
				added := newObject()
				added.set("uId", newString(newGUID()))
				added.set("name", newString(key))
				values := newArray()
				added.set("values", values)
				localizeSetCultureValue(values, culture.name, value)
				localizableStrings.items = append(localizableStrings.items, added)
			}
		}
		if changed {
			written = append(written, key)
		} else {
			unchanged = append(unchanged, key)
		}
	}
	var captionOutcome *string
	if caption != nil {
		values := schema.get("caption")
		if !values.isArray() {
			values = newArray()
			schema.set("caption", values)
		}
		outcome := localizeCaptionUnchanged
		if localizeSetCultureValue(values, culture.name, *caption) {
			outcome = localizeCaptionWritten
		}
		captionOutcome = &outcome
	}
	mustSave := len(written) > 0 || (captionOutcome != nil && *captionOutcome == localizeCaptionWritten)
	uid := editable.uid
	result := LocalizePageResponse{Success: true, SchemaName: request.SchemaName, SchemaUID: &uid, PackageName: packageName,
		Written: written, Unchanged: unchanged, CaptionOutcome: captionOutcome,
		Coverage: localizeCoverage(localizeResolveKeys(editable.hierarchy, localizableStrings), schema, editable.hierarchy, culture.name),
		Warnings: warnings}.withCulture(culture)
	if !mustSave {
		return result, nil
	}
	return c.localizeSaveAndVerify(ctx, request, caption, schema, editable.hierarchy, result)
}

func (c *Client) localizeSaveAndVerify(ctx context.Context, request LocalizePageRequest, caption *string, schema *jnode,
	hierarchy []pageLayer, result LocalizePageResponse) (LocalizePageResponse, error) {
	uid := *result.SchemaUID
	preSave, _ := c.localizeSchemaMetadata(ctx, uid)
	saved, saveProblem, saveErr := c.localizeSaveSchema(ctx, schema)
	if saveErr != nil {
		if ctx.Err() != nil {
			return LocalizePageResponse{}, ctx.Err()
		}
		c.localizeResetScriptCache(ctx)
		result.Warnings = append(result.Warnings, localizeWorkspaceCaptureWarning)
		message := fmt.Sprintf(localizeSaveOutcomeUnknown, appWriteDescribeServerText(c.clioRequestFailureText(saveErr)))
		result.Success, result.Saved, result.Coverage, result.Error = false, false, nil, &message
		return result, nil
	}
	if !saved {
		result.Success, result.Saved, result.Coverage, result.Error = false, false, nil, &saveProblem
		return result, nil
	}
	c.localizeResetScriptCache(ctx)
	result.Warnings = append(result.Warnings, localizeWorkspaceCaptureWarning)
	if warning := c.localizeRefreshBaseline(ctx, request, uid, preSave); strings.TrimSpace(warning) != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	readbackFailure := func(reason string) (LocalizePageResponse, error) {
		message := "The page was saved, but reading it back failed: " + appWriteDescribeServerText(reason)
		result.Success, result.Saved, result.Error = false, true, &message
		return result, nil
	}
	stored, problem, err := c.localizeGetSchema(ctx, uid)
	if err != nil {
		if ctx.Err() != nil {
			return LocalizePageResponse{}, ctx.Err()
		}
		return readbackFailure(c.clioRequestFailureText(err))
	}
	if problem != "" {
		return readbackFailure(problem)
	}
	var mismatches []string
	storedStrings := stored.get("localizableStrings")
	requestedStrings := schema.get("localizableStrings")
	for _, key := range result.Written {
		expected := localizeGetCultureValue(localizeFindValues(requestedStrings, key), *result.Culture)
		actual := localizeGetCultureValue(localizeFindValues(storedStrings, key), *result.Culture)
		if !localizeSameText(expected, actual) {
			mismatches = append(mismatches, key)
		}
	}
	if result.CaptionOutcome != nil && *result.CaptionOutcome == localizeCaptionWritten &&
		!localizeSameText(caption, localizeGetCultureValue(stored.get("caption"), *result.Culture)) {
		mismatches = append(mismatches, "caption")
	}
	if !storedStrings.isArray() {
		storedStrings = newArray()
	}
	result.Saved = true
	result.Coverage = localizeCoverage(localizeResolveKeys(hierarchy, storedStrings), stored, hierarchy, *result.Culture)
	if len(mismatches) > 0 {
		message := fmt.Sprintf(localizeReadbackMismatch, *result.Culture, strings.Join(mismatches, ", "))
		result.Success, result.Error = false, &message
	}
	return result, nil
}

func localizeSameText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// localizeResources is the parsed `resources` argument: keys in the order Newtonsoft's dictionary keeps them,
// a nil value for a JSON null.
type localizeResources struct {
	keys   []string
	values map[string]*string
}

// localizeParseResources is TryParseResources: JsonConvert.DeserializeObject<Dictionary<string, string>>, then
// the null and blank checks.
func localizeParseResources(text string) (localizeResources, string) {
	resources := localizeResources{values: map[string]*string{}}
	if strings.TrimFunc(text, unicode.IsSpace) == "" {
		return resources, ""
	}
	prefix := "resources must be a JSON object of key → value strings: "
	node, err := pageWriteParseDocument(text)
	if err != nil {
		return resources, prefix + localizeNewtonsoftError(text, err)
	}
	switch node.kind {
	case jkNull:
		return resources, ""
	case jkArray:
		line, position := localizeTextPosition(text, localizeFirstTokenEnd(text))
		return resources, prefix + fmt.Sprintf("Cannot deserialize the current JSON array (e.g. [1,2,3]) into type '%s' because the type "+
			"requires a JSON object (e.g. {\"name\":\"value\"}) to deserialize correctly.\nTo fix this error either change the JSON to a JSON "+
			"object (e.g. {\"name\":\"value\"}) or change the deserialized type to an array or a type that implements a collection interface "+
			"(e.g. ICollection, IList) like List<T> that can be deserialized from a JSON array. JsonArrayAttribute can also be added to "+
			"the type to force it to deserialize from a JSON array.\nPath '', line %d, position %d.", localizeDictionaryType, line, position)
	case jkString, jkInteger, jkFloat, jkBool:
		line, position := localizeTextPosition(text, localizeScalarEnd(text))
		shown := node.text
		if node.kind == jkString {
			shown = "\"" + node.text + "\""
		} else if node.kind == jkBool {
			shown = "False"
			if node.flag {
				shown = "True"
			}
		}
		return resources, prefix + fmt.Sprintf("Error converting value %s to type '%s'. Path '', line %d, position %d.", shown,
			localizeDictionaryType, line, position)
	}
	for _, key := range node.keys {
		value := node.props[key]
		var stored *string
		switch value.kind {
		case jkNull:
		case jkString, jkInteger:
			text := value.text
			stored = &text
		case jkFloat:
			text := value.text
			stored = &text
		case jkBool:
			// JsonReader.ReadAsString writes a boolean as JsonConvert.True / False (measured: "true").
			text := "false"
			if value.flag {
				text = "true"
			}
			stored = &text
		default:
			line, position := localizeValuePosition(text, key, value.kind)
			character := "{"
			if value.kind == jkArray {
				character = "["
			}
			return localizeResources{values: map[string]*string{}}, prefix + fmt.Sprintf(
				"Unexpected character encountered while parsing value: %s. Path '%s', line %d, position %d.", character, key, line, position)
		}
		if _, seen := resources.values[key]; !seen {
			resources.keys = append(resources.keys, key)
		}
		resources.values[key] = stored
	}
	var nullKeys, blankKeys []string
	for _, key := range resources.keys {
		if resources.values[key] == nil {
			nullKeys = append(nullKeys, key)
		}
	}
	if len(nullKeys) > 0 {
		return resources, fmt.Sprintf(localizeNullValues, strings.Join(nullKeys, ", "))
	}
	for _, key := range resources.keys {
		if strings.TrimFunc(*resources.values[key], unicode.IsSpace) == "" {
			blankKeys = append(blankKeys, key)
		}
	}
	if len(blankKeys) > 0 {
		return resources, fmt.Sprintf(localizeBlankValues, strings.Join(blankKeys, ", "))
	}
	return resources, ""
}

// localizeNewtonsoftError words a parse failure of malformed JSON. Newtonsoft's exact wording for every
// malformed input is not reproduced: the Go decoder's message is used (a measured difference).
func localizeNewtonsoftError(text string, err error) string {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		line, position := localizeTextPosition(text, int(syntax.Offset))
		return fmt.Sprintf("%s. Path '', line %d, position %d.", strings.TrimSuffix(syntax.Error(), "."), line, position)
	}
	return err.Error()
}

// localizeTextPosition is Newtonsoft's (line, position) of a reader that has consumed offset bytes.
func localizeTextPosition(text string, offset int) (int, int) {
	if offset > len(text) {
		offset = len(text)
	}
	line, position := 1, 0
	for _, r := range text[:offset] {
		if r == '\n' {
			line, position = line+1, 0
			continue
		}
		position += len(utf16.Encode([]rune{r}))
	}
	return line, position
}

func localizeFirstTokenEnd(text string) int {
	for index, r := range text {
		if !unicode.IsSpace(r) {
			return index + len(string(r))
		}
	}
	return len(text)
}

// localizeScalarEnd is the offset after the root scalar token.
func localizeScalarEnd(text string) int {
	trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
	return len(trimmed)
}

// localizeValuePosition is the position just after the opening bracket of key's value.
func localizeValuePosition(text, key string, kind jsonKind) (int, int) {
	quoted, _ := json.Marshal(key)
	index := strings.Index(text, string(quoted))
	if index < 0 {
		return localizeTextPosition(text, len(text))
	}
	opening := "{"
	if kind == jkArray {
		opening = "["
	}
	rest := text[index+len(quoted):]
	if at := strings.Index(rest, opening); at >= 0 {
		return localizeTextPosition(text, index+len(quoted)+at+1)
	}
	return localizeTextPosition(text, len(text))
}

// localizeUnstorable is XmlAttributeText.ContainsUnstorable: a control character other than tab, LF and CR,
// U+FFFE, U+FFFF or a lone surrogate half (a decoded lone surrogate arrives as U+FFFD and is not seen).
func localizeUnstorable(value string) bool {
	for _, r := range value {
		if r == 0xFFFE || r == 0xFFFF || (r >= 0xD800 && r <= 0xDFFF) {
			return true
		}
		if (r < 0x20 || (r >= 0x7F && r <= 0x9F)) && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}

// localizeResolveCulture is CultureAvailabilityGuard.Resolve over one SysCulture read.
func (c *Client) localizeResolveCulture(ctx context.Context, requested string) (*localizeCulture, string, error) {
	rows, err := c.clioSelectRows(ctx, "SysCulture", []rightsColumn{{path: "Name", alias: "Name"}, {path: "Active", alias: "Active"}}, nil)
	if err != nil {
		return nil, "", err
	}
	var cultures []localizeCulture
	for _, row := range rows {
		name := row.get("Name")
		if name == nil || name.kind != jkString || strings.TrimSpace(name.text) == "" {
			continue
		}
		active := row.get("Active")
		cultures = append(cultures, localizeCulture{name: name.text, active: active != nil && active.kind == jkBool && active.flag})
	}
	trimmed := dotnetTrim(requested)
	for _, culture := range cultures {
		if strings.EqualFold(culture.name, trimmed) {
			found := culture
			if !found.active {
				return &found, fmt.Sprintf(localizeCultureInactive, found.name), nil
			}
			return &found, "", nil
		}
	}
	names := make([]string, 0, len(cultures))
	for _, culture := range cultures {
		names = append(names, culture.name)
	}
	return nil, "", errors.New(fmt.Sprintf(localizeCultureAbsent, trimmed, strings.Join(names, ", ")))
}

// localizeEditableSchema is TryResolveEditableSchema: a problem is the answer's error; err is an exception.
func (c *Client) localizeEditableSchema(ctx context.Context, schemaName string) (localizeEditable, string, error) {
	row, problem := c.pageWriteSysSchemaRow(ctx, schemaName)
	if row == nil {
		return localizeEditable{}, appWriteDescribeServerText(problem), nil
	}
	rawUID := rowText(row, "UId")
	if strings.TrimSpace(rawUID) == "" {
		return localizeEditable{}, fmt.Sprintf("Schema '%s' metadata is missing UId", schemaName), nil
	}
	designPackageUID, err := c.pageWriteDesignPackage(ctx, rawUID)
	if err != nil {
		return localizeEditable{}, "", err
	}
	if strings.TrimSpace(designPackageUID) == "" {
		return localizeEditable{}, fmt.Sprintf("Failed to resolve design package for '%s': no package returned", schemaName), nil
	}
	hierarchy, err := c.localizeHierarchy(ctx, rawUID, designPackageUID)
	if err != nil {
		return localizeEditable{}, "", err
	}
	if len(hierarchy) == 0 {
		return localizeEditable{}, fmt.Sprintf("Schema '%s' hierarchy is empty", schemaName), nil
	}
	head := hierarchy[0]
	if strings.EqualFold(head.PackageUID, designPackageUID) {
		name := head.PackageName
		var packageName *string
		if name != "" {
			packageName = &name
		}
		return localizeEditable{uid: head.UID, packageName: packageName, hierarchy: hierarchy}, "", nil
	}
	existing := c.pageWriteSchemaInPackage(ctx, schemaName, designPackageUID)
	if strings.TrimSpace(existing) == "" {
		return localizeEditable{}, fmt.Sprintf(localizeNoEditableSchema, schemaName, designPackageUID), nil
	}
	return localizeEditable{uid: existing, hierarchy: hierarchy}, "", nil
}

// localizeGetSchema is TryGetSchema: the designer schema without its hierarchy.
func (c *Client) localizeGetSchema(ctx context.Context, schemaUID string) (*jnode, string, error) {
	body := toJNode(orderedFields{{"schemaUId", schemaUID}, {"useFullHierarchy", false}}).newtonsoftJSON()
	payload, err := c.callService(ctx, serviceCall{Route: pageWriteGetDesignerRoute, Body: body, Timeout: schemaWriteDesignerTimeout,
		Limit: schemaWriteResponseBytes})
	if err != nil {
		return nil, "", err
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		return nil, "", errors.New(pageWriteNewtonsoftParseError(payload, err))
	}
	schema := response.get("schema")
	if success := response.get("success"); success == nil || success.kind != jkBool || !success.flag || !schema.isObject() {
		message := response.get("errorInfo").get("message")
		if message == nil {
			return nil, fmt.Sprintf("Failed to load schema '%s'", schemaUID), nil
		}
		return nil, appWriteDescribeServerText(*message.tokenString()), nil
	}
	return schema, "", nil
}

// localizeSaveSchema is TrySaveSchema: (saved, the refusal text) or the exception of a failed request.
func (c *Client) localizeSaveSchema(ctx context.Context, schema *jnode) (bool, string, error) {
	payload, err := c.callService(ctx, serviceCall{Route: pageWriteSaveDesignerRoute, Body: schema.newtonsoftJSON(),
		Timeout: schemaWriteDesignerTimeout, Limit: schemaWriteResponseBytes})
	if err != nil {
		return false, "", err
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		return false, "", errors.New(pageWriteNewtonsoftParseError(payload, err))
	}
	if success := response.get("success"); success != nil && success.kind == jkBool && success.flag {
		return true, "", nil
	}
	return false, appWriteDescribeServerText(schemaWriteSaveErrorMessage(response, "Failed to save page schema")), nil
}

func (c *Client) localizeResetScriptCache(ctx context.Context) {
	_, _ = c.serviceRequest(ctx, serviceCall{Route: pageWriteResetScriptCacheRoute, Body: []byte{}})
}

// localizeSchemaMetadata is ReadSchemaMetadata: the SysSchema checksum and modification date, empty when unreadable.
func (c *Client) localizeSchemaMetadata(ctx context.Context, schemaUID string) (string, string) {
	row, _ := c.pageWriteSchemaRowByUID(ctx, schemaUID)
	if row == nil {
		return "", ""
	}
	return rowText(row, "Checksum"), rowText(row, "ModifiedOn")
}

// localizeRefreshBaseline is PageBaselineGuard.RefreshAfterSave; a failure is a warning, never an error.
func (c *Client) localizeRefreshBaseline(ctx context.Context, request LocalizePageRequest, savedUID, preSaveChecksum string) (warning string) {
	schemaName := localizeText(request.SchemaName)
	defer func() {
		if recovered := recover(); recovered != nil {
			warning = fmt.Sprintf(localizeBaselineRefreshFailed, redact.Text(fmt.Sprint(recovered)))
		}
	}()
	metaPath, _, err := pageWriteMetaPath(strings.TrimSpace(request.OutputDirectory), "", schemaName)
	if err != nil {
		return ""
	}
	baseline, readWarning := pageWriteReadBaseline(metaPath)
	if baseline == nil || !pageWriteMatchesEnvironment(baseline, request.EnvironmentName, request.EnvironmentURI) {
		return strings.TrimSpace(readWarning)
	}
	if strings.TrimSpace(preSaveChecksum) == "" {
		return fmt.Sprintf(localizeUnprovenBaseline, schemaName)
	}
	if baseline.Checksum != strings.TrimSpace(preSaveChecksum) {
		return fmt.Sprintf(localizeStaleBaseline, schemaName)
	}
	checksum, modifiedOn := c.localizeSchemaMetadata(ctx, savedUID)
	if strings.TrimSpace(checksum) == "" {
		if deleted := pageWriteDeleteBaseline(metaPath); strings.TrimSpace(deleted) != "" {
			return strings.TrimSpace(deleted)
		}
		return fmt.Sprintf(localizeDroppedBaseline, schemaName)
	}
	return pageWriteRefreshBaseline(metaPath, &pageWriteBaseline{SchemaName: schemaName,
		EnvironmentName: strings.TrimSpace(request.EnvironmentName), EnvironmentURI: strings.TrimSpace(request.EnvironmentURI),
		EditableSchemaExists: true, EditableSchemaUID: savedUID, Checksum: checksum, ModifiedOn: modifiedOn,
		CapturedAt: pageWriteRoundTripTime(time.Now())})
}

// ---- ResourceStringHelper ----

func localizeTokenEquals(node *jnode, value string) bool {
	text := node.tokenString()
	return text != nil && node.kind != jkNull && *text == value
}

// localizeFindCultureValue is FindCultureValue: the first {cultureName, value} object of the culture.
func localizeFindCultureValue(values *jnode, culture string) *jnode {
	if !values.isArray() {
		return nil
	}
	for _, item := range values.items {
		if !item.isObject() {
			continue
		}
		name := item.get("cultureName")
		if name != nil && name.kind != jkNull && strings.EqualFold(*name.tokenString(), culture) {
			return item
		}
	}
	return nil
}

// localizeGetCultureValue is GetCultureValue: the stored value as JToken.ToString(), nil when the culture or its
// value is absent.
func localizeGetCultureValue(values *jnode, culture string) *string {
	item := localizeFindCultureValue(values, culture)
	if item == nil {
		return nil
	}
	value, ok := item.props["value"]
	if !ok || value == nil {
		return nil
	}
	return value.tokenString()
}

// localizeSetCultureValue is SetCultureValue(JArray, ...): true when the stored value changed.
func localizeSetCultureValue(values *jnode, culture, value string) bool {
	item := localizeFindCultureValue(values, culture)
	if item == nil {
		item = newObject()
		item.set("cultureName", newString(culture))
		values.items = append(values.items, item)
	} else if current := item.get("value"); current != nil && current.kind == jkString && current.text == value {
		return false
	}
	item.set("value", newString(value))
	return true
}

// localizeSetCultureValueOrNull is SetCultureValue with a value that may be null (a hierarchy entry without one).
func localizeSetCultureValueOrNull(values *jnode, culture string, value *string) {
	if value != nil {
		localizeSetCultureValue(values, culture, *value)
		return
	}
	item := localizeFindCultureValue(values, culture)
	if item == nil {
		item = newObject()
		item.set("cultureName", newString(culture))
		values.items = append(values.items, item)
	}
	item.set("value", jsonNullNode())
}

func localizeFindValues(localizableStrings *jnode, key string) *jnode {
	if !localizableStrings.isArray() {
		return nil
	}
	for _, entry := range localizableStrings.items {
		if entry.isObject() && localizeTokenEquals(entry.get("name"), key) {
			if values := entry.get("values"); values.isArray() {
				return values
			}
			return nil
		}
	}
	return nil
}

// localizeResolveKeys is ResolveHierarchyKeys: the hierarchy's localizableStrings merged ancestor-first, the
// page's own list on top.
func localizeResolveKeys(hierarchy []pageLayer, pageStrings *jnode) localizeKeys {
	resolved := localizeKeys{values: map[string]*jnode{}}
	var layers []*jnode
	for index := len(hierarchy) - 1; index >= 0; index-- {
		layers = append(layers, hierarchy[index].LocalizableStrings)
	}
	layers = append(layers, pageStrings)
	for _, layer := range layers {
		if !layer.isArray() {
			continue
		}
		for _, entry := range layer.items {
			if !entry.isObject() {
				continue
			}
			nameNode := entry.get("name")
			if nameNode == nil || nameNode.kind == jkNull {
				continue
			}
			name := *nameNode.tokenString()
			if name == "" {
				continue
			}
			values, ok := resolved.values[name]
			if !ok {
				values = newArray()
				resolved.values[name] = values
				resolved.names = append(resolved.names, name)
			}
			entryValues := entry.get("values")
			if !entryValues.isArray() {
				continue
			}
			for _, value := range entryValues.items {
				if !value.isObject() {
					continue
				}
				cultureNode := value.get("cultureName")
				if cultureNode == nil || cultureNode.kind == jkNull || *cultureNode.tokenString() == "" {
					continue
				}
				var text *string
				if inner, ok := value.props["value"]; ok && inner != nil {
					text = inner.tokenString()
				}
				localizeSetCultureValueOrNull(values, *cultureNode.tokenString(), text)
			}
		}
	}
	return resolved
}

// localizeDescribeUnknownKeys is DescribeUnknownKeys: empty when every supplied key is known.
func localizeDescribeUnknownKeys(schemaName string, schema, localizableStrings *jnode, known localizeKeys, resources localizeResources) string {
	var unknown []string
	for _, key := range resources.keys {
		if _, ok := known.values[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	pageKeys := localizeOwnFirst(schema, localizableStrings)
	available := append([]string{}, pageKeys...)
	inPage := map[string]bool{}
	for _, key := range pageKeys {
		inPage[key] = true
	}
	for _, key := range known.names {
		if !inPage[key] {
			available = append(available, key)
		}
	}
	message := fmt.Sprintf(localizeUnknownKeys, schemaName, strings.Join(unknown, ", "), strings.Join(available, ", "))
	body := ""
	if node := schema.get("body"); node != nil && node.kind != jkNull {
		body = *node.tokenString()
	}
	bodyKeys := pageWriteBodyResourceKeys(body)
	bound := pageWriteViewModelPaths(body, false)
	var boundUnknown []string
	for _, key := range unknown {
		if bodyKeys[key] && bound[strings.ToLower(key)] {
			boundUnknown = append(boundUnknown, key)
		}
	}
	if len(boundUnknown) == 0 {
		return message
	}
	return message + fmt.Sprintf(localizeDataSourceBoundHint, strings.Join(boundUnknown, ", "))
}

// localizeOwnFirst is OrderOwnFirst: the page's own entries before the inherited ones, each group in list order.
func localizeOwnFirst(schema, localizableStrings *jnode) []string {
	schemaUID := ""
	if node := schema.get("uId"); node != nil && node.kind != jkNull {
		schemaUID = *node.tokenString()
	}
	var own, inherited []string
	for _, entry := range localizableStrings.items {
		if !entry.isObject() {
			continue
		}
		nameNode := entry.get("name")
		if nameNode == nil || nameNode.kind == jkNull || *nameNode.tokenString() == "" {
			continue
		}
		parent := ""
		if node := entry.get("parentSchemaUId"); node != nil && node.kind != jkNull {
			parent = *node.tokenString()
		}
		if parent == "" || strings.EqualFold(parent, schemaUID) {
			own = append(own, *nameNode.tokenString())
		} else {
			inherited = append(inherited, *nameNode.tokenString())
		}
	}
	return append(own, inherited...)
}

// localizeCoverage is BuildCoverage.
func localizeCoverage(keys localizeKeys, schema *jnode, hierarchy []pageLayer, culture string) *LocalizePageCoverage {
	coverage := &LocalizePageCoverage{Missing: []string{}, SameAsDefault: []string{}}
	for _, name := range keys.names {
		coverage.Keys++
		value := localizeGetCultureValue(keys.values[name], culture)
		if value == nil || *value == "" {
			coverage.Missing = append(coverage.Missing, name)
			continue
		}
		if localizeSameText(value, localizeGetCultureValue(keys.values[name], localizeDefaultCulture)) {
			coverage.SameAsDefault = append(coverage.SameAsDefault, name)
		}
	}
	coverage.Translated = coverage.Keys - len(coverage.Missing)
	captions := schema.get("caption")
	captionValue := localizeGetCultureValue(captions, culture)
	defaultCaption := localizeGetCultureValue(captions, localizeDefaultCulture)
	hasCaption := captionValue != nil && *captionValue != ""
	coverage.CaptionSameAsDefault = hasCaption && localizeSameText(captionValue, defaultCaption)
	if hasCaption {
		schemaUID := ""
		if node := schema.get("uId"); node != nil && node.kind != jkNull {
			schemaUID = *node.tokenString()
		}
		ancestors := []*jnode{schema.get("parent").get("caption")}
		for _, part := range hierarchy {
			if !strings.EqualFold(part.UID, schemaUID) {
				ancestors = append(ancestors, part.Caption)
			}
		}
		for _, ancestor := range ancestors {
			if !ancestor.isArray() || len(ancestor.items) == 0 {
				continue
			}
			if localizeSameText(captionValue, localizeGetCultureValue(ancestor, culture)) &&
				!localizeSameText(defaultCaption, localizeGetCultureValue(ancestor, localizeDefaultCulture)) {
				coverage.CaptionInherited = true
				break
			}
		}
		coverage.CaptionValue = captionValue
	}
	return coverage
}
