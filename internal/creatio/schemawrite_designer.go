package creatio

// The shared designer-service plumbing of clio's schema write tools (SchemaDesignerHelper,
// ServiceResponseJsonGuard, CaptionCultureScriptGuard, the profile-culture lookup): resolve a schema name to
// its UId, load a designer schema, create a blank one, save it, and the failure texts clio words for each.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// schemaWriteKind is clio's SchemaDesignerKind: the SysSchema manager a name resolves under and the designer
// service routes.
type schemaWriteKind struct {
	managerName string
	serviceName string
	getRoute    string
	saveRoute   string
	createRoute string
}

var (
	schemaWriteSourceCode = schemaWriteKind{"SourceCodeSchemaManager", "SourceCodeSchemaDesignerService",
		"ServiceModel/SourceCodeSchemaDesignerService.svc/GetSchema",
		"ServiceModel/SourceCodeSchemaDesignerService.svc/SaveSchema",
		"ServiceModel/SourceCodeSchemaDesignerService.svc/CreateNewSchema"}
	schemaWriteSQLScript = schemaWriteKind{"VwSysSqlScriptInPackage", "SqlScriptSchemaDesignerService",
		"ServiceModel/SqlScriptSchemaDesignerService.svc/GetSchema",
		"ServiceModel/SqlScriptSchemaDesignerService.svc/SaveSchema", ""}
)

const (
	schemaWriteSelectRoute = "DataService/json/SyncReply/SelectQuery"
	// schemaWriteDesignerTimeout bounds one designer call; clio's client waits 100 s by default.
	schemaWriteDesignerTimeout = 100 * time.Second
	schemaWriteResponseBytes   = 32 << 20

	// schemaWriteDesignerServiceHint is clio's DesignerServiceHint, appended to an empty or non-JSON designer answer.
	schemaWriteDesignerServiceHint = "If this repeats, the designer route may not be served by this Creatio version at all; otherwise " +
		"verify that the target package exists and is unlocked and editable, that the connected user may " +
		"manage configuration, and that the session is still valid (healthcheck)."
	// schemaWriteSaveOutcomeUnknownNote is clio's SaveOutcomeUnknownNote.
	schemaWriteSaveOutcomeUnknownNote = "The save outcome is unknown - the request may have been applied and only the answer lost - so " +
		"verify the schema in the environment before retrying."
	schemaWriteDefaultCulture = "en-US"
)

// schemaWriteResolveStatus is clio's SchemaResolveStatus: only an answered "no such schema" licenses a create.
type schemaWriteResolveStatus int

const (
	schemaWriteResolved schemaWriteResolveStatus = iota
	schemaWriteNotFound
	schemaWriteUnanswerable
)

type schemaWriteResolution struct {
	uid    string
	status schemaWriteResolveStatus
	err    string
}

// schemaWriteValidateCreateInput is clio's SchemaDesignerHelper.ValidateCreateInput.
func schemaWriteValidateCreateInput(schemaName, packageName string) string {
	var problems []string
	if strings.TrimSpace(schemaName) == "" {
		problems = append(problems, "schema-name is required")
	} else if !classicPageValidSchemaName(schemaName) {
		problems = append(problems, classicPageSchemaNameError)
	}
	if strings.TrimSpace(packageName) == "" {
		problems = append(problems, "package-name is required")
	}
	return strings.Join(problems, "; ")
}

// schemaWriteResolveBody is clio's ResolveBody: body-file wins over body and must exist; the result must not be
// blank.
func schemaWriteResolveBody(body *string, bodyFile string) (string, string) {
	text := ""
	if body != nil {
		text = *body
	}
	if strings.TrimSpace(bodyFile) != "" {
		content, err := os.ReadFile(bodyFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmt.Sprintf("body-file not found: '%s'", bodyFile)
			}
			return "", err.Error()
		}
		text = strings.TrimPrefix(string(content), "\uFEFF")
	}
	if strings.TrimSpace(text) == "" {
		return "", "body (or body-file) is required and must not be empty"
	}
	return text, ""
}

// schemaWritePost sends a JSON body to a fixed service route and parses the answer as clio's
// ServiceResponseJsonGuard.TryParseJObject does. The returned text is clio's failure wording.
func (c *Client) schemaWritePost(ctx context.Context, route, operation string, body []byte, hint string) (*jnode, string) {
	response, err := c.serviceRequest(ctx, serviceCall{Route: route, Body: body, Timeout: schemaWriteDesignerTimeout,
		Limit: schemaWriteResponseBytes, Label: operation})
	if err != nil {
		return nil, err.Error()
	}
	url := c.serviceURL(route)
	text := string(response.payload)
	if strings.TrimSpace(text) == "" {
		return nil, schemaWriteAppendHint(schemaWriteEmptyBodyMessage(operation, url), hint)
	}
	if looksLikeHTML(response.payload) {
		return nil, schemaWriteAppendHint(schemaWriteMarkupMessage(operation, url), hint)
	}
	node, parseErr := parseJNode(response.payload)
	if parseErr != nil || !node.isObject() {
		reason := "Unexpected JSON token."
		if parseErr != nil {
			reason = parseErr.Error()
		}
		return nil, schemaWriteAppendHint(fmt.Sprintf("%s returned an unparseable response. URL: %s Parser error: %s. Response preview: %s",
			operation, url, reason, schemaWritePreview(text)), hint)
	}
	if response.status < 200 || response.status >= 300 {
		// clio's synchronous client raises on an HTTP failure status; the designer's own reason, when the body
		// carries one, is the useful part of that failure.
		if message := node.get("errorInfo").get("message").str(); message != "" {
			return nil, message
		}
		return nil, fmt.Sprintf("%s %s returned HTTP %d", operation, route, response.status)
	}
	return node, ""
}

func schemaWriteAppendHint(message, hint string) string {
	if strings.TrimSpace(hint) == "" {
		return message
	}
	return message + " " + hint
}

func schemaWriteEmptyBodyMessage(operation, url string) string {
	return operation + " returned an empty response. URL: " + url + " " +
		"The response body was empty, and clio's synchronous client does not expose the HTTP status, so " +
		"whether the request was accepted is unknown — an unrouted endpoint answers 404 with an empty body " +
		"just as a served endpoint can answer 200 with one. Retry the request, and if it persists check the " +
		"route, the environment health (healthcheck) and the Creatio server log for that endpoint."
}

func schemaWriteMarkupMessage(operation, url string) string {
	return operation + " returned an HTML page instead of JSON. URL: " + url + " " +
		"The request was most likely redirected to a login page, or the server raised an unhandled " +
		"error and answered with an HTML error page. Verify that: " +
		"1) the environment is registered with valid credentials (reg-web-app, then healthcheck); " +
		"2) the IsNetCore flag matches the target instance (omit for .NET Framework, add --IsNetCore for .NET Core); " +
		"3) the request is retried — a transient server-side failure on this endpoint answers with an HTML error page. " +
		"The HTML body is omitted from this message because an error or login page can carry session tokens."
}

func schemaWritePreview(text string) string {
	collapsed := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(text))
	collapsed = strings.TrimSpace(strings.Trim(collapsed, "\uFEFF"))
	if collapsed == "" {
		return "<empty body>"
	}
	runes := []rune(collapsed)
	if len(runes) > 200 {
		return string(runes[:200]) + "…"
	}
	return collapsed
}

// schemaWriteSelectFailure is clio's DataServiceSelectResponse.TryGetFailure.
func schemaWriteSelectFailure(response *jnode) (string, bool) {
	errorInfo := response.get("errorInfo")
	hasErrorInfo := errorInfo.isObject() && len(errorInfo.keys) > 0
	success := response.get("success")
	responseStatus := response.get("responseStatus")
	if (success != nil && success.kind == jkBool && !success.flag) || hasErrorInfo ||
		responseStatus.get("ErrorCode").str() != "" {
		if message := errorInfo.get("message").stringValue(); message != nil {
			return *message, true
		}
		if message := responseStatus.get("Message").stringValue(); message != nil {
			return *message, true
		}
		return "Creatio DataService returned a failure response with no rows", true
	}
	if rows := response.get("rows"); rows == nil || rows.kind == jkNull {
		return "Creatio DataService returned a response with no rows and no explicit success signal", true
	}
	return "", false
}

// schemaWriteResolveUID is clio's ResolveSchemaUId for the source-code and SQL kinds: one SysSchema row by
// name and manager, or for SQL one VwSysSqlScriptInPackage row by name that must be unique.
func (c *Client) schemaWriteResolveUID(ctx context.Context, schemaName string, kind schemaWriteKind) schemaWriteResolution {
	column := map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}
	filters := map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
		"byName": schemaWriteTextEquals("Name", schemaName),
	}}
	query := map[string]any{"rootSchemaName": "SysSchema", "operationType": 0,
		"columns": map[string]any{"items": map[string]any{"UId": column}}, "filters": filters, "rowCount": 1}
	if kind.managerName == schemaWriteSQLScript.managerName {
		query["rootSchemaName"] = "VwSysSqlScriptInPackage"
		query["rowCount"] = 2
	} else {
		filters["items"].(map[string]any)["byManager"] = schemaWriteTextEquals("ManagerName", kind.managerName)
	}
	body, _ := json.Marshal(query)
	response, failure := c.schemaWritePost(ctx, schemaWriteSelectRoute, "SelectQuery", body, "")
	if failure != "" {
		return schemaWriteResolution{status: schemaWriteUnanswerable, err: failure}
	}
	if message, failed := schemaWriteSelectFailure(response); failed {
		return schemaWriteResolution{status: schemaWriteUnanswerable,
			err: fmt.Sprintf("SelectQuery for schema '%s' failed: %s", schemaName, message)}
	}
	rows := response.get("rows")
	if !rows.isArray() || len(rows.items) == 0 {
		return schemaWriteResolution{status: schemaWriteNotFound,
			err: fmt.Sprintf("Schema '%s' not found (ManagerName='%s')", schemaName, kind.managerName)}
	}
	if kind.managerName == schemaWriteSQLScript.managerName && len(rows.items) > 1 {
		return schemaWriteResolution{status: schemaWriteUnanswerable,
			err: fmt.Sprintf("SQL script name '%s' is ambiguous across packages or database engines. Use a unique name.", schemaName)}
	}
	uid := ""
	if value := rows.items[0].get("UId").tokenString(); value != nil {
		uid = *value
	}
	if strings.TrimSpace(uid) == "" {
		return schemaWriteResolution{status: schemaWriteUnanswerable, err: fmt.Sprintf("Schema '%s' metadata is missing UId", schemaName)}
	}
	return schemaWriteResolution{uid: uid, status: schemaWriteResolved}
}

func schemaWriteTextEquals(column, value string) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
		"leftExpression": map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2,
			"parameter": map[string]any{"dataValueType": 1, "value": value}}}
}

// schemaWritePackageUID is clio's PageSchemaMetadataHelper.QueryPackageUId.
func (c *Client) schemaWritePackageUID(ctx context.Context, packageName string) (string, string) {
	uid, err := c.relatedAddonPackageUID(ctx, packageName)
	if err != nil {
		return "", err.Error()
	}
	return uid, ""
}

// schemaWriteLoad is clio's LoadSchema: designer GetSchema by UId; a missing schema object is a failed load
// that carries the designer's own reason when it gave one.
func (c *Client) schemaWriteLoad(ctx context.Context, schemaUID string, kind schemaWriteKind, schemaName string) (*jnode, string) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "useFullHierarchy": false})
	response, failure := c.schemaWritePost(ctx, kind.getRoute, kind.serviceName+" GetSchema", body, schemaWriteDesignerServiceHint)
	if failure != "" {
		return nil, failure
	}
	schema := response.get("schema")
	if !schema.isObject() {
		label := schemaName
		if label == "" {
			label = schemaUID
		}
		reason := ""
		if errorInfo := response.get("errorInfo"); errorInfo.isObject() {
			reason = errorInfo.get("message").str()
		}
		if strings.TrimSpace(reason) == "" {
			return nil, fmt.Sprintf("Failed to load schema '%s' via %s", label, kind.serviceName)
		}
		return nil, fmt.Sprintf("Failed to load schema '%s' via %s: %s", label, kind.serviceName, reason)
	}
	return schema, ""
}

// schemaWriteCreateNew is clio's CreateNewSchema: a blank designer schema in the package, not yet saved.
func (c *Client) schemaWriteCreateNew(ctx context.Context, packageUID string, kind schemaWriteKind) (*jnode, string) {
	body, _ := json.Marshal(map[string]any{"packageUId": packageUID})
	response, failure := c.schemaWritePost(ctx, kind.createRoute, kind.serviceName+" CreateNewSchema", body, schemaWriteDesignerServiceHint)
	if failure != "" {
		return nil, failure
	}
	if success := response.get("success"); success == nil || success.kind != jkBool || !success.flag {
		if message := response.get("errorInfo").get("message").stringValue(); message != nil {
			return nil, *message
		}
		return nil, "CreateNewSchema failed"
	}
	schema := response.get("schema")
	if !schema.isObject() {
		return nil, "CreateNewSchema did not return a schema payload."
	}
	return schema, ""
}

// schemaWriteSave is clio's SaveSchema. outcomeUnknown is true when the answer said nothing about whether
// the schema was written (empty or non-JSON), as opposed to an observed rejection.
func (c *Client) schemaWriteSave(ctx context.Context, schema *jnode, kind schemaWriteKind) (failure string, outcomeUnknown bool) {
	response, failure := c.schemaWritePost(ctx, kind.saveRoute, kind.serviceName+" SaveSchema", schema.newtonsoftJSON(), schemaWriteDesignerServiceHint)
	if failure != "" {
		return failure, true
	}
	if success := response.get("success"); success != nil && success.kind == jkBool && success.flag {
		return "", false
	}
	return schemaWriteSaveErrorMessage(response, "Failed to save schema"), false
}

// schemaWriteSaveErrorMessage is clio's PageSchemaMetadataHelper.ParseSaveErrorMessage.
func schemaWriteSaveErrorMessage(response *jnode, fallback string) string {
	message := fallback
	if errorInfo := response.get("errorInfo"); errorInfo.isObject() {
		if text := errorInfo.get("message").str(); strings.TrimSpace(text) != "" {
			message = text
		}
	}
	if validation := response.get("validationErrors"); validation.isArray() && len(validation.items) > 0 {
		var messages []string
		for _, item := range validation.items {
			text := item.get("message").tokenString()
			if text == nil {
				text = item.get("caption").tokenString()
			}
			if text != nil && strings.TrimSpace(*text) != "" {
				messages = append(messages, *text)
			}
		}
		message = strings.Join(messages, "; ")
	}
	if addons := response.get("addonsErrors"); addons.isArray() && len(addons.items) > 0 {
		var messages []string
		for _, item := range addons.items {
			if text := item.tokenString(); text != nil {
				messages = append(messages, *text)
			}
		}
		message = strings.Join(messages, "; ")
	}
	return message
}

// schemaWriteApplyMetadata is clio's ApplySchemaMetadata: name, a one-culture caption and an optional
// description, after the caption-script guard.
func schemaWriteApplyMetadata(schema *jnode, name, caption, description, culture string) error {
	if strings.TrimSpace(culture) == "" {
		culture = schemaWriteDefaultCulture
	}
	if err := schemaWriteCaptionMatchesCulture(culture, caption, "caption"); err != nil {
		return err
	}
	if err := schemaWriteCaptionMatchesCulture(culture, description, "description"); err != nil {
		return err
	}
	localized := func(value string) *jnode {
		entry := newObject()
		entry.set("cultureName", newString(culture))
		entry.set("value", newString(value))
		list := newArray()
		list.items = append(list.items, entry)
		return list
	}
	schema.set("name", newString(name))
	schema.set("caption", localized(caption))
	if strings.TrimSpace(description) != "" {
		schema.set("description", localized(description))
	}
	return nil
}

// schemaWriteLatinLanguages is clio's CaptionCultureScriptGuard.LatinScriptLanguages.
var schemaWriteLatinLanguages = func() map[string]bool {
	set := map[string]bool{}
	for _, language := range strings.Fields("en de fr es it pt nl sv da no nb nn fi is ga gd cy gl ca eu oc br co rm fur wa an ast kl " +
		"cs sk pl sl hr bs ro hu et lv lt sq mt lb fo af sw id ms fil tl vi tr az uz tk ku so ha yo ig zu xh st tn mi sm haw") {
		set[language] = true
	}
	return set
}()

// schemaWriteIsLatinCulture is clio's IsLatinScriptCulture: an explicit four-letter script subtag decides,
// otherwise the language allow-list.
func schemaWriteIsLatinCulture(culture string) bool {
	parts := strings.FieldsFunc(strings.TrimSpace(culture), func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return false
	}
	if len(parts) >= 2 && len(parts[1]) == 4 && schemaWriteASCIILetters(parts[1]) {
		return strings.EqualFold(parts[1], "Latn")
	}
	return schemaWriteLatinLanguages[strings.ToLower(parts[0])]
}

func schemaWriteASCIILetters(text string) bool {
	for _, r := range text {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func schemaWriteIsLatinLetter(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == 0xAA || r == 0xB5 || r == 0xBA ||
		r >= 0xC0 && r <= 0x24F || r >= 0x1E00 && r <= 0x1EFF || r >= 0xFB00 && r <= 0xFB06 ||
		r >= 0xFF21 && r <= 0xFF3A || r >= 0xFF41 && r <= 0xFF5A
}

// schemaWriteCaptionMatchesCulture is clio's EnsureCaptionMatchesCulture: a Latin-script culture refuses a
// value with non-Latin letters, naming up to eight of them.
func schemaWriteCaptionMatchesCulture(culture, value, context string) error {
	if strings.TrimSpace(culture) == "" || strings.TrimSpace(value) == "" || !schemaWriteIsLatinCulture(culture) {
		return nil
	}
	var offenders []string
	seen := map[string]bool{}
	for _, r := range value {
		if r == utf8.RuneError || !unicode.IsLetter(r) || schemaWriteIsLatinLetter(r) {
			continue
		}
		text := string(r)
		if !seen[text] {
			seen[text] = true
			offenders = append(offenders, "'"+text+"'")
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	if len(offenders) > 8 {
		offenders = offenders[:8]
	}
	return fmt.Errorf("%s: the '%s' value \"%s\" contains non-Latin characters (%s), but '%s' is a Latin-script (English-style) locale. "+
		"Author the '%s' caption in that language using Latin script, or put the localized text under the matching culture key "+
		"(for example add a 'uk-UA' entry, or pass 'caption-culture' for the language you actually wrote). This usually means "+
		"the connected user's profile language differs from the language the caption was written in — author captions in the "+
		"profile language detected by 'get-user-culture'.",
		context, culture, strings.TrimSpace(value), strings.Join(offenders, ", "), culture, culture)
}

// schemaWriteCultureCache is clio's CurrentUserCultureCache: a resolved profile culture per environment URL,
// kept five minutes; failures are not cached.
var schemaWriteCultureCache = struct {
	sync.Mutex
	entries map[string]schemaWriteCultureEntry
}{entries: map[string]schemaWriteCultureEntry{}}

type schemaWriteCultureEntry struct {
	culture string
	expires time.Time
}

// schemaWriteProfileCulture is clio's CaptionCultureResolver without an override: the connected user's
// profile culture, or en-US when it cannot be read.
func (c *Client) schemaWriteProfileCulture(ctx context.Context) string {
	key := strings.ToLower(strings.TrimRight(c.config.BaseURL, "/"))
	schemaWriteCultureCache.Lock()
	entry, ok := schemaWriteCultureCache.entries[key]
	schemaWriteCultureCache.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.culture
	}
	result := c.GetUserCulture(ctx)
	if !result.Success {
		return schemaWriteDefaultCulture
	}
	schemaWriteCultureCache.Lock()
	schemaWriteCultureCache.entries[key] = schemaWriteCultureEntry{culture: result.Culture, expires: time.Now().Add(5 * time.Minute)}
	schemaWriteCultureCache.Unlock()
	return result.Culture
}
