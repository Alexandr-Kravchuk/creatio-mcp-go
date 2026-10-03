package creatio

// create-schema, update-schema, create-sql-schema, update-sql-schema and install-sql-schema: clio's
// SourceCodeSchemaCreate/Update and SqlSchemaCreate/Update/Install commands over the designer services.

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// SourceCodeSchemaCreateRequest is clio's create-schema arguments.
type SourceCodeSchemaCreateRequest struct {
	SchemaName, PackageName string
	Body                    *string
	BodyFile                *string
	Caption, Description    string
}

// SourceCodeSchemaCreateResponse is clio's create-schema answer; empty fields are left out as clio's MCP
// serializer leaves out nulls.
type SourceCodeSchemaCreateResponse struct {
	Success     bool   `json:"success"`
	SchemaName  string `json:"schemaName,omitempty"`
	SchemaUID   string `json:"schemaUId,omitempty"`
	PackageName string `json:"packageName,omitempty"`
	PackageUID  string `json:"packageUId,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Error       string `json:"error,omitempty"`
}

// SchemaBodyUpdateRequest is clio's update-schema / update-sql-schema arguments.
type SchemaBodyUpdateRequest struct {
	SchemaName string
	Body       *string
	BodyFile   string
	DryRun     bool
}

// SchemaBodyUpdateResponse is clio's SourceCodeSchemaUpdateResponse and SqlSchemaUpdateResponse: bodyLength
// and dryRun are always present.
type SchemaBodyUpdateResponse struct {
	Success    bool   `json:"success"`
	SchemaName string `json:"schemaName,omitempty"`
	BodyLength int    `json:"bodyLength"`
	DryRun     bool   `json:"dryRun"`
	Error      string `json:"error,omitempty"`
}

// SQLSchemaCreateRequest is clio's create-sql-schema arguments.
type SQLSchemaCreateRequest struct {
	SchemaName, PackageName string
	DBEngineType            *int
	InstallType             int
	Caption, Description    string
}

// SQLSchemaCreateResponse is clio's SqlSchemaCreateResponse.
type SQLSchemaCreateResponse struct {
	Success     bool   `json:"success"`
	SchemaName  string `json:"schemaName,omitempty"`
	SchemaUID   string `json:"schemaUId,omitempty"`
	PackageName string `json:"packageName,omitempty"`
	PackageUID  string `json:"packageUId,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Error       string `json:"error,omitempty"`
}

// SQLSchemaInstallResponse is clio's SqlSchemaInstallResponse.
type SQLSchemaInstallResponse struct {
	Success    bool   `json:"success"`
	SchemaName string `json:"schemaName,omitempty"`
	SchemaUID  string `json:"schemaUId,omitempty"`
	Error      string `json:"error,omitempty"`
}

const schemaWriteInstallSQLRoute = "ServiceModel/WorkspaceExplorerService.svc/InstallSqlScripts"

// CreateSourceCodeSchema is clio's SourceCodeSchemaCreateCommand.TryCreate: validate, resolve the package,
// require the name to be free, CreateNewSchema, stamp name/caption/description in the profile culture, set
// the body when one was given, SaveSchema.
func (c *Client) CreateSourceCodeSchema(ctx context.Context, input SourceCodeSchemaCreateRequest) SourceCodeSchemaCreateResponse {
	fail := func(message string) SourceCodeSchemaCreateResponse {
		return SourceCodeSchemaCreateResponse{Error: message}
	}
	if problem := schemaWriteValidateCreateInput(input.SchemaName, input.PackageName); problem != "" {
		return fail(problem)
	}
	body := ""
	hasBody := input.Body != nil || input.BodyFile != nil
	if hasBody {
		bodyFile := ""
		if input.BodyFile != nil {
			bodyFile = *input.BodyFile
		}
		resolved, problem := schemaWriteResolveBody(input.Body, bodyFile)
		if problem != "" {
			return fail(problem)
		}
		body = resolved
	}
	packageUID, problem := c.schemaWritePackageUID(ctx, input.PackageName)
	if problem != "" {
		return fail(problem)
	}
	if problem := c.schemaWriteRequireAbsent(ctx, input.SchemaName, schemaWriteSourceCode); problem != "" {
		return fail(problem)
	}
	caption := input.SchemaName
	if strings.TrimSpace(input.Caption) != "" {
		caption = strings.TrimSpace(input.Caption)
	}
	schema, problem := c.schemaWriteCreateNew(ctx, packageUID, schemaWriteSourceCode)
	if problem != "" {
		return fail(problem)
	}
	culture := c.schemaWriteProfileCulture(ctx)
	if err := schemaWriteApplyMetadata(schema, input.SchemaName, caption, input.Description, culture); err != nil {
		return fail(err.Error())
	}
	if hasBody {
		schema.set("body", newString(body))
	}
	if failure, unknown := c.schemaWriteSave(ctx, schema, schemaWriteSourceCode); failure != "" {
		if unknown {
			failure += " " + schemaWriteSaveOutcomeUnknownNote
		}
		return fail(failure)
	}
	uid := ""
	if value := schema.get("uId").tokenString(); value != nil {
		uid = *value
	}
	return SourceCodeSchemaCreateResponse{Success: true, SchemaName: input.SchemaName, SchemaUID: uid,
		PackageName: input.PackageName, PackageUID: packageUID, Caption: caption}
}

// schemaWriteRequireAbsent is clio's CheckSchemaIsAbsent: only an answered "not found" licenses a create.
func (c *Client) schemaWriteRequireAbsent(ctx context.Context, schemaName string, kind schemaWriteKind) string {
	existing := c.schemaWriteResolveUID(ctx, schemaName, kind)
	switch existing.status {
	case schemaWriteResolved:
		return fmt.Sprintf("Schema '%s' already exists in this environment.", schemaName)
	case schemaWriteNotFound:
		return ""
	default:
		return fmt.Sprintf("Could not check whether schema '%s' already exists: %s", schemaName, existing.err)
	}
}

// UpdateSourceCodeSchema is clio's SourceCodeSchemaUpdateCommand.TryUpdateSchema.
func (c *Client) UpdateSourceCodeSchema(ctx context.Context, input SchemaBodyUpdateRequest) SchemaBodyUpdateResponse {
	return c.schemaWriteUpdateBody(ctx, input, schemaWriteSourceCode)
}

// UpdateSQLSchema is clio's SqlSchemaUpdateCommand.TryUpdateSchema.
func (c *Client) UpdateSQLSchema(ctx context.Context, input SchemaBodyUpdateRequest) SchemaBodyUpdateResponse {
	return c.schemaWriteUpdateBody(ctx, input, schemaWriteSQLScript)
}

// schemaWriteUpdateBody resolves the schema, loads it through the designer, replaces its body and saves it
// back whole. dry-run stops after the resolution.
func (c *Client) schemaWriteUpdateBody(ctx context.Context, input SchemaBodyUpdateRequest, kind schemaWriteKind) SchemaBodyUpdateResponse {
	fail := func(message string) SchemaBodyUpdateResponse { return SchemaBodyUpdateResponse{Error: message} }
	if strings.TrimSpace(input.SchemaName) == "" {
		return fail("schema-name is required")
	}
	body, problem := schemaWriteResolveBody(input.Body, input.BodyFile)
	if problem != "" {
		return fail(problem)
	}
	resolution := c.schemaWriteResolveUID(ctx, input.SchemaName, kind)
	if resolution.status != schemaWriteResolved {
		return fail(resolution.err)
	}
	success := SchemaBodyUpdateResponse{Success: true, SchemaName: input.SchemaName, BodyLength: utf16Length(body), DryRun: input.DryRun}
	if input.DryRun {
		return success
	}
	schema, problem := c.schemaWriteLoad(ctx, resolution.uid, kind, input.SchemaName)
	if problem != "" {
		return fail(problem)
	}
	schema.set("body", newString(body))
	if failure, unknown := c.schemaWriteSave(ctx, schema, kind); failure != "" {
		if unknown && !strings.Contains(failure, schemaWriteSaveOutcomeUnknownNote) {
			failure += " " + schemaWriteSaveOutcomeUnknownNote
		}
		return fail(failure)
	}
	return success
}

// CreateSQLSchema is clio's SqlSchemaCreateCommand.TryCreate: SQL scripts are saved straight through
// SqlScriptSchemaDesignerService.SaveSchema with a client-generated UId, a one-space body and the detected
// (or given) database engine. An answer that says nothing is checked by reading the name back.
func (c *Client) CreateSQLSchema(ctx context.Context, input SQLSchemaCreateRequest) SQLSchemaCreateResponse {
	fail := func(message string) SQLSchemaCreateResponse { return SQLSchemaCreateResponse{Error: message} }
	if problem := schemaWriteValidateCreateInput(input.SchemaName, input.PackageName); problem != "" {
		return fail(problem)
	}
	if strings.TrimSpace(input.Caption) != "" || strings.TrimSpace(input.Description) != "" {
		return fail("Package SQL scripts have no caption, description or caption culture. Omit these legacy options; schema-name is the display name.")
	}
	if (input.DBEngineType != nil && (*input.DBEngineType < 0 || *input.DBEngineType > 2)) || input.InstallType < 0 || input.InstallType > 3 {
		return fail("db-engine-type must be 0..2 and install-type must be 0..3.")
	}
	packageUID, problem := c.schemaWritePackageUID(ctx, input.PackageName)
	if problem != "" {
		return fail(problem)
	}
	if problem := c.schemaWriteRequireAbsent(ctx, input.SchemaName, schemaWriteSQLScript); problem != "" {
		return fail(problem)
	}
	engine, problem := c.schemaWriteDatabaseEngine(ctx, input.DBEngineType)
	if problem != "" {
		return fail(problem)
	}
	uid := schemaWriteNewGUID()
	pkg := newObject()
	pkg.set("uId", newString(packageUID))
	pkg.set("name", newString(input.PackageName))
	schema := newObject()
	schema.set("uId", newString(uid))
	schema.set("name", newString(input.SchemaName))
	schema.set("package", pkg)
	schema.set("body", newString(" "))
	schema.set("dbEngineType", newInt(engine))
	schema.set("installType", newInt(input.InstallType))
	schema.set("dependOnSqlScripts", newArray())
	schema.set("backwardCompatibilityConfirmed", newBool(false))
	success := SQLSchemaCreateResponse{Success: true, SchemaName: input.SchemaName, SchemaUID: uid,
		PackageName: input.PackageName, PackageUID: packageUID, Caption: input.SchemaName}
	failure, unknown := c.schemaWriteSave(ctx, schema, schemaWriteSQLScript)
	if failure == "" {
		return success
	}
	if !unknown {
		return fail(failure)
	}
	readBack := c.schemaWriteResolveUID(ctx, input.SchemaName, schemaWriteSQLScript)
	if readBack.status == schemaWriteUnanswerable {
		return fail(fmt.Sprintf("%s The result could not be verified either: %s Check whether schema '%s' exists before retrying.",
			failure, readBack.err, input.SchemaName))
	}
	if readBack.status == schemaWriteResolved && strings.EqualFold(strings.Trim(readBack.uid, "{}"), uid) {
		success.SchemaUID = readBack.uid
		return success
	}
	return fail(failure)
}

// schemaWriteDatabaseEngine is clio's ResolveDatabaseEngine: the given engine, or the one
// GetSystemEnvironmentInfo reports.
func (c *Client) schemaWriteDatabaseEngine(ctx context.Context, requested *int) (int, string) {
	if requested != nil {
		return *requested, ""
	}
	response, failure := c.schemaWritePost(ctx, systemEnvironmentInfoRoute, "GetSystemEnvironmentInfo", []byte("{}"), "")
	engine := -1
	if failure == "" {
		if success := response.get("success"); success != nil && success.kind == jkBool && success.flag {
			switch response.get("dbEngineType").str() {
			case "MSSql":
				engine = 0
			case "Oracle":
				engine = 1
			case "PostgreSql":
				engine = 2
			}
		}
	}
	if engine >= 0 {
		return engine, ""
	}
	detail := failure
	if detail == "" && response != nil {
		if errorInfo := response.get("errorInfo"); errorInfo.isObject() {
			detail = errorInfo.get("message").str()
		}
	}
	return 0, "Could not detect the database engine. Supply db-engine-type explicitly. " + detail
}

// InstallSQLSchema is clio's SqlSchemaInstallCommand.TryInstall: resolve the unique SQL script and execute it
// through WorkspaceExplorerService.InstallSqlScripts. It runs the script's SQL on the database.
func (c *Client) InstallSQLSchema(ctx context.Context, schemaName string) SQLSchemaInstallResponse {
	if strings.TrimSpace(schemaName) == "" {
		return SQLSchemaInstallResponse{Error: "schema-name is required"}
	}
	resolution := c.schemaWriteResolveUID(ctx, schemaName, schemaWriteSQLScript)
	if resolution.status != schemaWriteResolved {
		return SQLSchemaInstallResponse{Error: resolution.err}
	}
	failed := func(message string) SQLSchemaInstallResponse {
		return SQLSchemaInstallResponse{SchemaName: schemaName, SchemaUID: resolution.uid, Error: message}
	}
	body := []byte(`["` + resolution.uid + `"]`)
	response, err := c.serviceRequest(ctx, serviceCall{Route: schemaWriteInstallSQLRoute, Body: body,
		Timeout: 10 * time.Minute, Limit: schemaWriteResponseBytes, Label: "WorkspaceExplorerService InstallSqlScripts"})
	if err != nil {
		if isTransportError(err) {
			return failed("InstallSqlScripts transport failed. Execution outcome is unknown; verify database effects before retrying.")
		}
		return SQLSchemaInstallResponse{Error: err.Error()}
	}
	operation := "WorkspaceExplorerService InstallSqlScripts"
	url := c.serviceURL(schemaWriteInstallSQLRoute)
	unknown := " Execution outcome is unknown; verify database effects before retrying."
	if strings.TrimSpace(string(response.payload)) == "" {
		return failed(schemaWriteEmptyBodyMessage(operation, url) + unknown)
	}
	if looksLikeHTML(response.payload) {
		return failed(schemaWriteMarkupMessage(operation, url) + unknown)
	}
	node, parseErr := parseJNode(response.payload)
	if parseErr != nil || !node.isObject() {
		return failed(fmt.Sprintf("%s returned an unparseable response. URL: %s Parser error: %v. Response preview: %s",
			operation, url, parseErr, schemaWritePreview(string(response.payload))) + unknown)
	}
	if success := node.get("success"); success == nil || success.kind != jkBool || !success.flag {
		if message := node.get("errorInfo").get("message").stringValue(); message != nil {
			return failed(*message)
		}
		return failed("InstallSqlScripts failed")
	}
	return SQLSchemaInstallResponse{Success: true, SchemaName: schemaName, SchemaUID: resolution.uid}
}

// schemaWriteNewGUID is .NET's Guid.NewGuid().ToString(): a random version-4 GUID, lower case.
func schemaWriteNewGUID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
