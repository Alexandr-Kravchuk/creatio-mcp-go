package creatio

// update-page's command: clio's PageUpdateCommand.TryUpdatePage. The editable schema is resolved the way the
// page designer does (design package, parent hierarchy, a replacing schema created in the design package
// when the app does not own the page yet), the external-modification baseline is checked, the body is merged
// (append mode) and its parents checked against the inherited view, localizable strings are registered, and
// the schema is saved through ClientUnitSchemaDesignerService.SaveSchema.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	pageWriteResetScriptCacheRoute = "rest/WorkplaceService/ResetScriptCache"

	// pageWriteInvalidResources is clio's PageUpdateCommand.InvalidResourcesError.
	pageWriteInvalidResources = "resources must be a valid JSON object string"
	// PageWriteEscapeHatchHint is clio's ValidationEscapeHatchHint, appended by the MCP tools to a content failure.
	PageWriteEscapeHatchHint = " If this defect pre-exists on the page and is unrelated to your edit, re-run with validate=false."
	// pageWriteResourceCaptureWarning is clio's ResourceWorkspaceCaptureWarning.
	pageWriteResourceCaptureWarning = "Page resources were saved on the server; update-page does not capture your workspace source. " +
		"A push-workspace from stale metadata/resource XML can revert these changes. Preserve local edits, " +
		"capture the affected package with restore-workspace (pull-workspace), and review its schema metadata " +
		"and culture resource XML before pushing. For linked FSM workspaces, follow the workspace's capture instructions."
	pageWriteRecognizedSections = "SCHEMA_VIEW_CONFIG_DIFF, SCHEMA_VIEW_MODEL_CONFIG_DIFF, SCHEMA_MODEL_CONFIG_DIFF, SCHEMA_HANDLERS, " +
		"SCHEMA_CONVERTERS, SCHEMA_VALIDATORS, SCHEMA_VIEW_MODEL_CONFIG, SCHEMA_MODEL_CONFIG"
)

// PageUpdateRequest is clio's PageUpdateOptions.
type PageUpdateRequest struct {
	SchemaName         string
	Body               string
	BodyFile           string
	DryRun             bool
	Resources          string
	OptionalProperties string
	Mode               string
	TargetPackageUID   string
	TargetSchemaUID    string
	Force              bool
	Validate           bool
	ExpectedChecksum   string
	// EnvironmentName / EnvironmentURI identify the target in the .clio-pages baseline, as clio's options do.
	EnvironmentName string
	EnvironmentURI  string

	expectedSchemaUID       string
	expectedSchemaAbsent    bool
	conditionalSchemaUID    string
	conditionalChecksum     string
	conditionalSchemaAbsent bool
	conditionalApplied      bool
}

// ConditionalBaselineApplied reports that a selector-targeted save landed on the schema its baseline
// describes, so the baseline is refreshed after the save.
func (r *PageUpdateRequest) ConditionalBaselineApplied() bool { return r.conditionalApplied }

// PageUpdateResponse is clio's PageUpdateResponse.
type PageUpdateResponse struct {
	Success                bool                  `json:"success"`
	SchemaName             string                `json:"schemaName,omitempty"`
	BodyLength             int                   `json:"bodyLength"`
	DryRun                 bool                  `json:"dryRun"`
	Error                  string                `json:"error,omitempty"`
	ResourcesRegistered    int                   `json:"resourcesRegistered"`
	RegisteredResourceKeys []string              `json:"registeredResourceKeys,omitempty"`
	Warnings               []string              `json:"warnings,omitempty"`
	AppendProjection       *PageAppendProjection `json:"appendProjection,omitempty"`
	Page                   *PageMetadata         `json:"page,omitempty"`
	Conflict               bool                  `json:"conflict,omitempty"`
	ConflictDetails        *PageConflictDetails  `json:"conflictDetails,omitempty"`
	NewChecksum            string                `json:"newChecksum,omitempty"`
	NewModifiedOn          string                `json:"newModifiedOn,omitempty"`
	SavedSchemaUID         string                `json:"savedSchemaUId,omitempty"`

	// ContentValidationFailure marks a failure of the skippable content rules (clio's flag of the same name).
	ContentValidationFailure bool `json:"-"`
}

// PageConflictDetails is clio's PageConflictDetails.
type PageConflictDetails struct {
	Reason            string `json:"reason"`
	ExpectedChecksum  string `json:"expectedChecksum,omitempty"`
	ActualChecksum    string `json:"actualChecksum,omitempty"`
	ExpectedSchemaUID string `json:"expectedSchemaUId,omitempty"`
	ActualSchemaUID   string `json:"actualSchemaUId,omitempty"`
	ModifiedOn        string `json:"modifiedOn,omitempty"`
}

// MarkDryRunFailure is clio's PageUpdateResponse.MarkDryRunFailure.
func (r *PageUpdateResponse) MarkDryRunFailure(dryRun bool, schemaName string) {
	if r.Success || !dryRun {
		return
	}
	r.DryRun = true
	if r.SchemaName == "" {
		r.SchemaName = schemaName
	}
}

type pageWriteContext struct {
	schemaName        string
	editableUID       string
	designPackageUID  string
	createReplacing   bool
	parentSchemaUID   string
	parentSchemaName  string
	templateSchemaUID string
	mobile            bool
	knownType         bool
	hierarchy         []pageLayer
}

func pageUpdateFailure(text string) PageUpdateResponse { return PageUpdateResponse{Error: text} }

// PageWriteLoadBody is clio's PageUpdateBodyLoader.TryResolveBody: body-file is read only when body is blank.
func PageWriteLoadBody(body, bodyFile string) (string, string) {
	if strings.TrimSpace(body) != "" || strings.TrimSpace(bodyFile) == "" {
		return body, ""
	}
	info, err := os.Stat(bodyFile)
	if err != nil || info.IsDir() {
		return "", "File not found: " + bodyFile
	}
	content, err := os.ReadFile(bodyFile)
	if err != nil {
		return "", fmt.Sprintf("Cannot read %s: %s", bodyFile, err.Error())
	}
	return pageWriteDecodeText(content), ""
}

// pageWriteDecodeText is File.ReadAllText: a UTF-8 (or UTF-16 with BOM) file without its byte-order mark.
func pageWriteDecodeText(content []byte) string {
	return pageValidateDecode(content)
}

// UpdatePage is clio's PageUpdateCommand.TryUpdatePage.
func (c *Client) UpdatePage(ctx context.Context, request *PageUpdateRequest) PageUpdateResponse {
	response := c.updatePageCore(ctx, request)
	response.MarkDryRunFailure(request.DryRun, request.SchemaName)
	return response
}

func (c *Client) updatePageCore(ctx context.Context, request *PageUpdateRequest) (response PageUpdateResponse) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = pageUpdateFailure(pageWriteRecoveredText(recovered))
		}
	}()
	body, problem := PageWriteLoadBody(request.Body, request.BodyFile)
	if problem != "" {
		return pageUpdateFailure(problem)
	}
	request.Body = pageWriteChartPreprocess(body)
	if strings.TrimSpace(request.SchemaName) == "" {
		return pageUpdateFailure("schemaName is required")
	}
	if strings.TrimSpace(request.Body) == "" {
		return pageUpdateFailure("body is required and must not be empty. Reuse the get-page body (CLI: raw.body; MCP: the contents of the file at files.bodyFile) instead of bundle or viewConfig fragments.")
	}
	resources, ok := pageWriteParseResources(request.Resources)
	if !ok {
		return pageUpdateFailure(pageWriteInvalidResources)
	}
	optionalProperties, problem := pageWriteParseOptionalProperties(request.OptionalProperties)
	if problem != "" {
		return pageUpdateFailure(problem)
	}
	context_, failure := c.pageWriteResolveContext(ctx, request)
	if failure != nil {
		return *failure
	}
	if failure := c.pageWriteCheckExternal(ctx, request, context_); failure != nil {
		return *failure
	}
	if failure := pageWriteValidateInput(request, context_.mobile); failure != nil {
		return *failure
	}
	if request.DryRun {
		return c.pageWriteDryRun(ctx, request, context_, resources, optionalProperties)
	}
	return c.pageWriteSave(ctx, request, context_, resources, optionalProperties)
}

// pageWriteRecoveredText turns a recovered applier fault or panic into the exception text clio surfaces.
func pageWriteRecoveredText(recovered any) string {
	switch value := recovered.(type) {
	case applierFault:
		return value.message
	case error:
		return value.Error()
	}
	return fmt.Sprint(recovered)
}

func pageWriteIsAppend(request *PageUpdateRequest) bool {
	return strings.EqualFold(request.Mode, "append")
}

// pageWriteValidateInput is PageUpdateCommand.ValidateInput: the structural floor that runs even with
// validate=false. clio's content rules on this layer (handler structure, field label resources, validator
// placement, mobile content) are not ported; the tools name them in a warning.
func pageWriteValidateInput(request *PageUpdateRequest, mobile bool) *PageUpdateResponse {
	if mobile {
		if problem := pageWriteMobileStructure(request.Body); problem != "" {
			failure := pageUpdateFailure("Mobile page validation failed: " + problem)
			return &failure
		}
		return nil
	}
	if pageWriteIsAppend(request) {
		if !pageWriteRecognizableFragment(request.Body) {
			failure := pageUpdateFailure("Append body carries no recognizable page section: an append body is a FRAGMENT of a page body, " +
				"not a bare list of operations, so it must carry at least one section marker pair - for example " +
				"/**SCHEMA_VIEW_CONFIG_DIFF*/[ ... ]/**SCHEMA_VIEW_CONFIG_DIFF*/. Without one, every section reads as empty and the merge " +
				"would silently discard everything you sent. Recognized sections: " + pageWriteRecognizedSections)
			return &failure
		}
	} else if missing := pageValidateMarkerIntegrity(request.Body); len(missing) > 0 {
		failure := pageUpdateFailure("Body is missing required marker pairs: " + strings.Join(missing, "; "))
		return &failure
	}
	if problem := pageValidateBracketSyntax(request.Body); problem != "" {
		failure := pageUpdateFailure("Body contains invalid JavaScript syntax: " + problem)
		return &failure
	}
	return nil
}

func pageWriteRecognizableFragment(body string) bool {
	for _, marker := range strings.Split(pageWriteRecognizedSections, ", ") {
		if pageWriteSectionPattern(marker).MatchString(body) {
			return true
		}
	}
	return false
}

// pageWriteMobileStructure is SchemaValidationService.ValidateMobileBodyStructure.
func pageWriteMobileStructure(body string) string {
	if strings.TrimSpace(body) == "" {
		return "Mobile page body is null or empty."
	}
	var probe any
	decoder := json.NewDecoder(strings.NewReader(body))
	if err := decoder.Decode(&probe); err != nil {
		return "Mobile page body is not valid JSON: " + err.Error()
	}
	if _, isObject := probe.(map[string]any); !isObject {
		return "Mobile page body must be a JSON object."
	}
	return ""
}

// pageWriteResolveContext is TryResolveContext / TryResolveEditableSchemaContext.
func (c *Client) pageWriteResolveContext(ctx context.Context, request *PageUpdateRequest) (*pageWriteContext, *PageUpdateResponse) {
	fail := func(text string) (*pageWriteContext, *PageUpdateResponse) {
		failure := pageUpdateFailure(text)
		return nil, &failure
	}
	if strings.TrimSpace(request.TargetSchemaUID) != "" {
		return &pageWriteContext{schemaName: request.SchemaName, editableUID: request.TargetSchemaUID,
			templateSchemaUID: request.TargetSchemaUID, mobile: pageWriteIsMobileBody(request.Body), knownType: true}, nil
	}
	row, problem := c.pageWriteSysSchemaRow(ctx, request.SchemaName)
	if problem != "" {
		return fail(problem)
	}
	rawUID := rowText(row, "UId")
	if strings.TrimSpace(rawUID) == "" {
		return fail(fmt.Sprintf("Schema '%s' metadata is missing UId", request.SchemaName))
	}
	designPackageUID := strings.TrimSpace(request.TargetPackageUID)
	if designPackageUID == "" {
		uid, err := c.pageWriteDesignPackage(ctx, rawUID)
		if err != nil {
			return fail(fmt.Sprintf("Failed to resolve design package for '%s': %s", request.SchemaName, err.Error()))
		}
		if strings.TrimSpace(uid) == "" {
			return fail(fmt.Sprintf("Failed to resolve design package for '%s': no package returned", request.SchemaName))
		}
		designPackageUID = uid
	} else {
		designPackageUID = request.TargetPackageUID
	}
	hierarchy, err := c.pageLayers(ctx, rawUID, designPackageUID)
	if err != nil {
		return fail(pageWriteHierarchyHint(fmt.Sprintf("Failed to load hierarchy for '%s': %s", request.SchemaName, err.Error())))
	}
	if len(hierarchy) == 0 {
		return fail(fmt.Sprintf("Schema '%s' hierarchy is empty", request.SchemaName))
	}
	head := hierarchy[0]
	mobile, known := false, false
	if head.SchemaType != nil && (*head.SchemaType == pageSchemaTypeWeb || *head.SchemaType == pageSchemaTypeMobile) {
		mobile, known = *head.SchemaType == pageSchemaTypeMobile, true
	} else {
		mobile = pageWriteIsMobileBody(request.Body)
	}
	root := head
	for index := len(hierarchy) - 1; index >= 0; index-- {
		if strings.EqualFold(hierarchy[index].Name, request.SchemaName) {
			for _, candidate := range hierarchy {
				if strings.EqualFold(candidate.UID, hierarchy[index].UID) {
					root = candidate
					break
				}
			}
			break
		}
	}
	editableUID, createReplacing := head.UID, false
	if !strings.EqualFold(head.PackageUID, designPackageUID) {
		existing := c.pageWriteSchemaInPackage(ctx, request.SchemaName, designPackageUID)
		if strings.TrimSpace(existing) == "" {
			editableUID, createReplacing = schemaWriteNewGUID(), true
		} else {
			editableUID = existing
		}
	}
	if createReplacing && mobile && head.Body == nil {
		return fail(fmt.Sprintf("Refusing to write mobile page '%s': this would create a REPLACING schema in design package '%s' and leave the "+
			"empty base schema '%s' in package '%s' unrendered — the Creatio Mobile app loads that empty base and crashes. Pass "+
			"target-schema-uid=%s to write the body into the base schema, or create the page directly in the design package.",
			request.SchemaName, designPackageUID, head.UID, head.PackageName, head.UID))
	}
	result := &pageWriteContext{schemaName: request.SchemaName, editableUID: editableUID, designPackageUID: designPackageUID,
		createReplacing: createReplacing, parentSchemaName: root.Name, mobile: mobile, knownType: known}
	if createReplacing {
		result.parentSchemaUID = root.UID
		result.templateSchemaUID = root.UID
	} else {
		result.templateSchemaUID = editableUID
		result.hierarchy = hierarchy
	}
	return result, nil
}

func pageWriteHierarchyHint(message string) string {
	if strings.Contains(strings.ToLower(message), "eng-94418") {
		return message
	}
	if strings.Contains(strings.ToLower(message), strings.ToLower("Incorrect syntax near ')'")) {
		return message + pageHierarchyRecoveryHint
	}
	return message
}

// pageWriteSysSchemaRow is PageSchemaMetadataHelper.QuerySysSchemaRow for the UId of a client-unit schema.
func (c *Client) pageWriteSysSchemaRow(ctx context.Context, schemaName string) (map[string]json.RawMessage, string) {
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "trimDateTimeParameterToDate": false,
			"items": map[string]any{
				"filter0": comparisonFilter("Name", schemaName, 1, 3),
				"filter1": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
			}},
		"columns":  map[string]any{"items": map[string]any{"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}}},
		"rowCount": 1,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "SelectQuery failed:") {
			return nil, "Failed to query schema metadata"
		}
		return nil, err.Error()
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("Schema '%s' not found", schemaName)
	}
	return rows[0], ""
}

// pageWriteSchemaRowByUID is QuerySysSchemaRowByUId with the Checksum and ModifiedOn columns.
func (c *Client) pageWriteSchemaRowByUID(ctx context.Context, schemaUID string) (map[string]json.RawMessage, string) {
	column := func(path string) map[string]any {
		return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
	}
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
			"byUId":     pageWriteGUIDEquals("UId", schemaUID),
			"byManager": schemaWriteTextEquals("ManagerName", clientUnitSchemaManagerName),
		}},
		"columns":  map[string]any{"items": map[string]any{"Checksum": column("Checksum"), "ModifiedOn": column("ModifiedOn")}},
		"rowCount": 1,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "SelectQuery failed:") {
			return nil, "Failed to query schema metadata"
		}
		return nil, err.Error()
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("Schema '%s' not found", schemaUID)
	}
	return rows[0], ""
}

func pageWriteGUIDEquals(column, value string) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
		"leftExpression": map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2,
			"parameter": map[string]any{"dataValueType": 0, "value": value}}}
}

// pageWriteSchemaInPackage is PageSchemaMetadataHelper.FindExistingSchemaInPackage: a failed lookup counts as
// absent.
func (c *Client) pageWriteSchemaInPackage(ctx context.Context, schemaName, packageUID string) string {
	if strings.TrimSpace(schemaName) == "" || strings.TrimSpace(packageUID) == "" {
		return ""
	}
	rows, err := c.selectRows(ctx, map[string]any{
		"rootSchemaName": "SysSchema", "operationType": 0,
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{
			"byName":    schemaWriteTextEquals("Name", schemaName),
			"byManager": schemaWriteTextEquals("ManagerName", clientUnitSchemaManagerName),
			"byPackage": pageWriteGUIDEquals("SysPackage.UId", packageUID),
		}},
		"columns":  map[string]any{"items": map[string]any{"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}}},
		"rowCount": 1,
	})
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rowText(rows[0], "UId")
}

// pageWriteDesignPackage is PageDesignerHierarchyClient.GetDesignPackageUId, which fails loudly.
func (c *Client) pageWriteDesignPackage(ctx context.Context, schemaUID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "userLevelSchema": false})
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId", Body: body})
	if err != nil {
		return "", err
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		return "", errors.New(pageWriteNewtonsoftParseError(payload, err))
	}
	if success := response.get("success"); success == nil || success.kind != jkBool || !success.flag {
		detail := ""
		for _, key := range []string{"errorInfo", "message", "error"} {
			if value := response.get(key); value != nil && value.kind != jkNull {
				detail = pageWriteJTokenText(value)
				break
			}
		}
		if strings.TrimSpace(detail) == "" {
			return "", errors.New("Failed to resolve design package")
		}
		return "", fmt.Errorf("Failed to resolve design package: %s", detail)
	}
	uid := pageWriteTokenText(response.get("uId"))
	if strings.TrimSpace(uid) == "" {
		return "", errors.New("Design package response did not return a uId")
	}
	return uid, nil
}

// pageWriteJTokenText is Newtonsoft's JToken.ToString(): a string as itself, a container indented.
func pageWriteJTokenText(node *jnode) string {
	if node.isObject() || node.isArray() {
		return newtonsoftIndented(node)
	}
	return pageWriteTokenText(node)
}

func pageWriteSchemaUIDsMatch(recorded, resolved string) bool {
	if strings.TrimSpace(recorded) == "" || strings.TrimSpace(resolved) == "" {
		return false
	}
	left, right := normalizeGUID(recorded), normalizeGUID(resolved)
	if left != "" && right != "" {
		return left == right
	}
	return strings.EqualFold(recorded, resolved)
}

// pageWriteCheckExternal is TryCheckForExternalModification (with PromoteConditionalBaselineWhenTargetMatches).
func (c *Client) pageWriteCheckExternal(ctx context.Context, request *PageUpdateRequest, context_ *pageWriteContext) *PageUpdateResponse {
	if pageWriteSchemaUIDsMatch(request.conditionalSchemaUID, context_.editableUID) {
		request.conditionalApplied = true
		if strings.TrimSpace(request.ExpectedChecksum) == "" {
			request.ExpectedChecksum = request.conditionalChecksum
			request.expectedSchemaUID = request.conditionalSchemaUID
			request.expectedSchemaAbsent = request.conditionalSchemaAbsent
		}
	}
	if request.Force {
		return nil
	}
	hasChecksum := strings.TrimSpace(request.ExpectedChecksum) != ""
	if !hasChecksum && !request.expectedSchemaAbsent {
		return nil
	}
	conflict := func(details PageConflictDetails) *PageUpdateResponse {
		return &PageUpdateResponse{Conflict: true, ConflictDetails: &details, SchemaName: request.SchemaName,
			Error: fmt.Sprintf("Page schema '%s' was modified outside this session (external modification detected). Do NOT retry with the same body. "+
				"Re-run get-page for this schema, re-apply your change on top of the fresh body, then retry. Re-sending this response's actualChecksum "+
				"as the checksum argument is NOT a resolution - it discards the external change exactly like force=true and needs the same explicit "+
				"user confirmation. Use force=true ONLY after the user explicitly confirms overwriting the external changes.", request.SchemaName)}
	}
	if request.expectedSchemaAbsent {
		if context_.createReplacing {
			return nil
		}
		return conflict(PageConflictDetails{Reason: "schema-created-externally", ExpectedChecksum: request.ExpectedChecksum, ActualSchemaUID: context_.editableUID})
	}
	if context_.createReplacing {
		return conflict(PageConflictDetails{Reason: "schema-deleted-externally", ExpectedChecksum: request.ExpectedChecksum, ExpectedSchemaUID: request.expectedSchemaUID})
	}
	if strings.TrimSpace(request.expectedSchemaUID) != "" && !pageWriteSchemaUIDsMatch(request.expectedSchemaUID, context_.editableUID) {
		return conflict(PageConflictDetails{Reason: "schema-uid-mismatch", ExpectedChecksum: request.ExpectedChecksum,
			ExpectedSchemaUID: request.expectedSchemaUID, ActualSchemaUID: context_.editableUID})
	}
	expectedUID := request.expectedSchemaUID
	if expectedUID == "" {
		expectedUID = context_.editableUID
	}
	row, _ := c.pageWriteSchemaRowByUID(ctx, context_.editableUID)
	if row == nil {
		return conflict(PageConflictDetails{Reason: "schema-deleted-externally", ExpectedChecksum: request.ExpectedChecksum, ExpectedSchemaUID: expectedUID})
	}
	actual := rowText(row, "Checksum")
	if strings.TrimSpace(actual) == "" {
		return nil
	}
	if actual != request.ExpectedChecksum {
		return conflict(PageConflictDetails{Reason: "checksum-mismatch", ExpectedChecksum: request.ExpectedChecksum, ActualChecksum: actual,
			ExpectedSchemaUID: expectedUID, ActualSchemaUID: context_.editableUID, ModifiedOn: rowText(row, "ModifiedOn")})
	}
	return nil
}

// pageWriteGetSchema is TryGetSchema: the designer schema without its hierarchy.
func (c *Client) pageWriteGetSchema(ctx context.Context, schemaUID string) (*jnode, string) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "useFullHierarchy": false})
	payload, err := c.callService(ctx, serviceCall{Route: pageWriteGetDesignerRoute, Body: body, Timeout: schemaWriteDesignerTimeout,
		Limit: schemaWriteResponseBytes})
	if err != nil {
		panic(err)
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		panic(errors.New(pageWriteNewtonsoftParseError(payload, err)))
	}
	schema := response.get("schema")
	if success := response.get("success"); success == nil || success.kind != jkBool || !success.flag || !schema.isObject() {
		if message := response.get("errorInfo").get("message"); message != nil && message.kind != jkNull {
			return nil, pageWriteJTokenText(message)
		}
		return nil, fmt.Sprintf("Failed to load schema '%s'", schemaUID)
	}
	return schema, ""
}

type pageWritePrepared struct {
	schema     *jnode
	body       string
	projection *PageAppendProjection
	registered []string
}

// pageWritePrepare is TryPrepareWrite: load the schema to save, resolve the body, check its parents and
// merge the localizable strings.
func (c *Client) pageWritePrepare(ctx context.Context, request *PageUpdateRequest, context_ *pageWriteContext,
	resources *pageWriteResources, optionalProperties *jnode) (*pageWritePrepared, *PageUpdateResponse) {
	template, problem := c.pageWriteGetSchema(ctx, context_.templateSchemaUID)
	if problem != "" {
		failure := pageUpdateFailure(problem)
		return nil, &failure
	}
	schema := template
	if context_.createReplacing {
		schema = pageWriteReplacingSchema(template, context_)
	}
	body := request.Body
	var projection *PageAppendProjection
	if pageWriteIsAppend(request) {
		if current := pageWriteTokenText(schema.get("body")); strings.TrimSpace(current) != "" {
			merged, mergedProjection, message, fullConfig := pageWriteMergeBodies(current, request.Body)
			if message != "" {
				text := "Append merge failed: " + message + " [hint: the body must contain valid marker pairs with new viewConfigDiff/handlers operations. See docs://mcp/guides/page-modification.]"
				if fullConfig {
					text = message + " [hint: see docs://mcp/guides/page-modification for the append diff-form contract.]"
				}
				failure := pageUpdateFailure(text)
				return nil, &failure
			}
			body, projection = merged, mergedProjection
		}
	}
	if failure := c.pageWriteValidateParents(ctx, body, context_); failure != nil {
		return nil, failure
	}
	schema.set("body", newString(body))
	if optionalProperties != nil {
		pageWriteMergeOptionalProperties(schema, optionalProperties)
	}
	cleaned, registered := pageWriteCleanAndMerge(schema.get("localizableStrings"), resources, pageWriteOrderedBodyKeys(body),
		pageWriteViewModelPaths(body, context_.mobile))
	schema.set("localizableStrings", cleaned)
	if len(registered) == 0 {
		registered = nil
	}
	return &pageWritePrepared{schema: schema, body: body, projection: projection, registered: registered}, nil
}

// pageWriteReplacingSchema is BuildNewReplacingSchemaDto: the parent's schema cloned into a new replacing
// schema in the design package with an empty body.
func pageWriteReplacingSchema(template *jnode, context_ *pageWriteContext) *jnode {
	original := pageWriteTokenText(template.get("name"))
	if template.get("name") == nil || template.get("name").kind == jkNull {
		original = context_.schemaName
	}
	dto := template.clone()
	dto.set("uId", newString(context_.editableUID))
	dto.set("name", newString(original))
	dto.set("isReadOnly", newBool(false))
	dto.set("extendParent", newBool(true))
	if strings_ := template.get("localizableStrings"); strings_ != nil && strings_.kind != jkNull {
		dto.set("localizableStrings", strings_.clone())
	} else {
		dto.set("localizableStrings", newArray())
	}
	pkg := newObject()
	pkg.set("uId", pageWriteNullableString(context_.designPackageUID))
	pkg.set("name", newString(""))
	dto.set("package", pkg)
	parent := newObject()
	parent.set("uId", pageWriteNullableString(context_.parentSchemaUID))
	parentName := context_.parentSchemaName
	if parentName == "" {
		parentName = original
	}
	parent.set("name", newString(parentName))
	dto.set("parent", parent)
	dto.set("body", newString(pageWriteEmptyReplacingBody(original, context_.mobile)))
	return dto
}

func pageWriteEmptyReplacingBody(schemaName string, mobile bool) string {
	if mobile {
		return "{\n\t\"viewConfigDiff\": [],\n\t\"viewModelConfigDiff\": [],\n\t\"modelConfigDiff\": []\n}"
	}
	return "define(\"" + schemaName + "\", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ {\n" +
		"\treturn {\n" +
		"\t\tviewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[]/**SCHEMA_VIEW_CONFIG_DIFF*/,\n" +
		"\t\tviewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/,\n" +
		"\t\tmodelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/,\n" +
		"\t\thandlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/,\n" +
		"\t\tconverters: /**SCHEMA_CONVERTERS*/{}/**SCHEMA_CONVERTERS*/,\n" +
		"\t\tvalidators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/\n" +
		"\t};\n" +
		"});"
}

// pageWriteMergeOptionalProperties is MergeOptionalProperties: keyed case-insensitively, incoming wins, the
// first occurrence keeps its position.
func pageWriteMergeOptionalProperties(schema, incoming *jnode) {
	order := []string{}
	merged := map[string]*jnode{}
	add := func(list *jnode) {
		if !list.isArray() {
			return
		}
		for _, item := range list.items {
			key := ""
			if item.isObject() {
				key = pageWriteTokenText(item.get("key"))
			}
			if strings.TrimSpace(key) == "" {
				continue
			}
			folded := strings.ToLower(key)
			if _, seen := merged[folded]; !seen {
				order = append(order, folded)
			}
			merged[folded] = item
		}
	}
	add(schema.get("optionalProperties"))
	add(incoming)
	result := newArray()
	for _, key := range order {
		result.items = append(result.items, merged[key])
	}
	schema.set("optionalProperties", result)
}

// pageWriteValidateParents is TryValidateParents: an insert, move or set naming a parent is applied over the
// view inherited from the parent schemas, and an unresolved parent fails the write.
func (c *Client) pageWriteValidateParents(ctx context.Context, body string, context_ *pageWriteContext) *PageUpdateResponse {
	if context_.mobile {
		return nil
	}
	candidate, err := pageWriteReadDiff(body)
	if err != nil {
		panic(err)
	}
	needs := false
	for _, item := range candidate.items {
		if !item.isObject() {
			continue
		}
		operation := pageWriteTokenText(item.get("operation"))
		if (operation == "insert" || operation == "move" || operation == "set") &&
			(pageWriteTokenText(item.get("parentName")) != "" || pageWriteTokenText(item.get("nameTo")) != "") {
			needs = true
			break
		}
	}
	if !needs {
		return nil
	}
	uid := context_.editableUID
	if context_.createReplacing {
		uid = context_.templateSchemaUID
	}
	hierarchy := context_.hierarchy
	if hierarchy == nil {
		packageUID := context_.designPackageUID
		if packageUID == "" {
			resolved, err := c.pageWriteDesignPackage(ctx, uid)
			if err != nil {
				panic(err)
			}
			packageUID = resolved
		}
		layers, err := c.pageLayers(ctx, uid, packageUID)
		if err != nil {
			panic(err)
		}
		hierarchy = layers
	}
	if len(hierarchy) == 0 {
		panic(errors.New("Cannot validate parentName: page hierarchy is unavailable."))
	}
	inherited := hierarchy
	if !context_.createReplacing {
		own := -1
		for index := range hierarchy {
			if pageWriteSchemaUIDsMatch(hierarchy[index].UID, uid) {
				own = index
				break
			}
		}
		if own < 0 {
			panic(errors.New("Cannot validate parentName: target schema is missing from the hierarchy."))
		}
		inherited = hierarchy[own+1:]
	}
	applier := newJSONDiffApplier(false)
	view := newArray()
	for index := len(inherited) - 1; index >= 0; index-- {
		part := inherited[index]
		if part.Body == nil || strings.TrimSpace(*part.Body) == "" {
			continue
		}
		diff, err := pageWriteReadDiff(*part.Body)
		if err != nil {
			panic(err)
		}
		view = applier.apply(view, diff, &applierOptions{applyMoveIfIndirectParentMoved: part.SchemaVersion >= 1})
	}
	applier.apply(view, candidate, &applierOptions{rejectUnresolvedParents: true})
	return nil
}

func (c *Client) pageWriteDryRun(ctx context.Context, request *PageUpdateRequest, context_ *pageWriteContext,
	resources *pageWriteResources, optionalProperties *jnode) PageUpdateResponse {
	if !pageWriteIsAppend(request) {
		if failure := c.pageWriteValidateParents(ctx, request.Body, context_); failure != nil {
			return *failure
		}
		return pageWriteSuccess(request, true, nil)
	}
	prepared, failure := c.pageWritePrepare(ctx, request, context_, resources, optionalProperties)
	if failure != nil {
		return *failure
	}
	response := pageWriteSuccess(request, true, nil)
	response.AppendProjection = prepared.projection
	response.Warnings = pageWriteProjectedLoss(prepared.projection)
	return response
}

func (c *Client) pageWriteSave(ctx context.Context, request *PageUpdateRequest, context_ *pageWriteContext,
	resources *pageWriteResources, optionalProperties *jnode) PageUpdateResponse {
	prepared, failure := c.pageWritePrepare(ctx, request, context_, resources, optionalProperties)
	if failure != nil {
		return *failure
	}
	if problem := c.pageWriteSaveSchema(ctx, prepared.schema); problem != "" {
		return pageUpdateFailure(problem)
	}
	response := pageWriteSuccess(request, false, prepared.registered)
	response.AppendProjection = prepared.projection
	warnings := pageWriteProjectedLoss(prepared.projection)
	if resources.count() > 0 || len(prepared.registered) > 0 {
		warnings = append(warnings, pageWriteResourceCaptureWarning)
	}
	if len(warnings) > 0 {
		response.Warnings = warnings
	}
	c.pageWritePostSaveChecksum(ctx, request, context_, &response)
	return response
}

func pageWriteSuccess(request *PageUpdateRequest, dryRun bool, registered []string) PageUpdateResponse {
	return PageUpdateResponse{Success: true, SchemaName: request.SchemaName, BodyLength: utf16Length(request.Body), DryRun: dryRun,
		ResourcesRegistered: len(registered), RegisteredResourceKeys: registered}
}

// pageWriteProjectedLoss is BuildProjectedLossWarnings.
func pageWriteProjectedLoss(projection *PageAppendProjection) []string {
	if projection == nil {
		return nil
	}
	var warnings []string
	warnings = append(warnings, projection.supersededDropWarnings...)
	if !projection.ViewConfigDiffApplied && projection.IncomingOperationCount > 0 {
		warnings = append(warnings, "The page's current body has no SCHEMA_VIEW_CONFIG_DIFF marker pair, so the merged viewConfigDiff array "+
			"cannot be written back and EVERY viewConfigDiff operation in the fragment is discarded - the counts in appendProjection describe "+
			"an array the write throws away. Use --mode replace with a body that carries the marker pair. See docs://mcp/guides/page-modification.")
	}
	return warnings
}

// pageWriteSaveSchema is TrySaveSchema: SaveSchema, then the best-effort ResetScriptCache the page designer
// sends after a save.
func (c *Client) pageWriteSaveSchema(ctx context.Context, schema *jnode) string {
	payload, err := c.callService(ctx, serviceCall{Route: pageWriteSaveDesignerRoute, Body: schema.newtonsoftJSON(),
		Timeout: schemaWriteDesignerTimeout, Limit: schemaWriteResponseBytes})
	if err != nil {
		panic(err)
	}
	response, err := parseJNode(payload)
	if err != nil || !response.isObject() {
		panic(errors.New(pageWriteNewtonsoftParseError(payload, err)))
	}
	if success := response.get("success"); success != nil && success.kind == jkBool && success.flag {
		_, _ = c.serviceRequest(ctx, serviceCall{Route: pageWriteResetScriptCacheRoute, Body: []byte{}})
		return ""
	}
	return pageWriteActionableHint(schemaWriteSaveErrorMessage(response, "Failed to save page schema"))
}

// pageWriteActionableHint is AppendActionableHint.
func pageWriteActionableHint(serverError string) string {
	lower := strings.ToLower(serverError)
	switch {
	case serverError == "":
		return serverError
	case strings.Contains(lower, "requires an element of type 'object'") && strings.Contains(lower, "type 'array'"):
		return serverError + " [hint: this typically happens when re-sending the full get-page body verbatim in mode='replace' — the mode " +
			"in which the body reaches the server; backend re-applies existing merges that now conflict with parent hierarchy. Send only NEW " +
			"viewConfigDiff/handlers operations (the new component insert + matching handler), not the entire inherited body. See " +
			"docs://mcp/guides/page-modification for the minimal-diff pattern.]"
	case strings.Contains(lower, "item with name") && strings.Contains(lower, "not found"):
		return serverError + " [hint: the schema manager cache may be holding a stale phantom replacing schema from an earlier failed save. " +
			"Restart Creatio to clear the cache, or verify the schema UId via list-pages.]"
	case strings.Contains(lower, "third-party publisher") || strings.Contains(lower, "installed from the file archive"):
		return serverError + " [hint: the schema is owned by a package whose maintainer differs from the current workspace maintainer, so " +
			"Creatio blocks direct in-place edits. Fix by saving into a replacing schema in your design package: call update-page with " +
			"mode=append (auto-detects design package and creates a replacement there) or pass target-package-uid explicitly to the app's " +
			"design package. See docs://mcp/guides/page-modification section 'multi-app replacements'.]"
	}
	return serverError
}

// pageWritePostSaveChecksum is PopulatePostSaveChecksum: only when a baseline is in play.
func (c *Client) pageWritePostSaveChecksum(ctx context.Context, request *PageUpdateRequest, context_ *pageWriteContext, response *PageUpdateResponse) {
	if !request.Force && !request.expectedSchemaAbsent && strings.TrimSpace(request.ExpectedChecksum) == "" {
		return
	}
	response.SavedSchemaUID = context_.editableUID
	row, _ := c.pageWriteSchemaRowByUID(ctx, context_.editableUID)
	if row == nil {
		return
	}
	response.NewChecksum = rowText(row, "Checksum")
	response.NewModifiedOn = rowText(row, "ModifiedOn")
}
