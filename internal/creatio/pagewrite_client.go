package creatio

import (
	"context"
	"encoding/json"
	"strings"
)

var pageWriteClientKind = schemaWriteKind{"ClientUnitSchemaManager", "ClientUnitSchemaDesignerService",
	"ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema", "ServiceModel/ClientUnitSchemaDesignerService.svc/SaveSchema", ""}

type ClientUnitCreateRequest struct{ SchemaName, PackageName, Caption, Description, CaptionCulture string }

func (c *Client) CreateClientUnitSchema(ctx context.Context, input ClientUnitCreateRequest) SourceCodeSchemaCreateResponse {
	fail := func(text string) SourceCodeSchemaCreateResponse { return SourceCodeSchemaCreateResponse{Error: text} }
	if strings.TrimSpace(input.SchemaName) == "" {
		return fail("schema-name is required")
	}
	if !classicPageValidSchemaName(input.SchemaName) {
		return fail(classicPageSchemaNameError)
	}
	if strings.TrimSpace(input.PackageName) == "" {
		return fail("package-name is required")
	}
	packageUID, problem := c.schemaWritePackageUID(ctx, input.PackageName)
	if problem != "" {
		return fail(problem)
	}
	// Clio's creation checks any SysSchema manager, as helper modules share the global schema namespace.
	rows, err := c.selectRows(ctx, map[string]any{"rootSchemaName": "SysSchema", "operationType": 0, "rowCount": 1, "columns": map[string]any{"items": map[string]any{"UId": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "UId"}}}}, "filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": map[string]any{"byName": schemaWriteTextEquals("Name", input.SchemaName)}}})
	if err != nil {
		return fail(err.Error())
	}
	if len(rows) > 0 {
		return fail("Schema '" + input.SchemaName + "' already exists in this environment.")
	}
	caption := strings.TrimSpace(input.Caption)
	if caption == "" {
		caption = input.SchemaName
	}
	culture := input.CaptionCulture
	if strings.TrimSpace(culture) == "" {
		culture = c.schemaWriteProfileCulture(ctx)
	}
	uid := schemaWriteNewGUID()
	payload := map[string]any{"uId": uid, "name": input.SchemaName, "package": map[string]any{"uId": packageUID, "name": input.PackageName},
		"description": []any{}, "managerName": "ClientUnitSchemaManager", "extendParent": false, "body": "", "localizableStrings": []any{}, "parameters": []any{}, "messages": []any{}, "images": []any{}}
	raw, _ := json.Marshal(payload)
	schema, err := parseJNode(raw)
	if err != nil {
		return fail(err.Error())
	}
	if err := schemaWriteApplyMetadata(schema, input.SchemaName, caption, input.Description, culture); err != nil {
		return fail(err.Error())
	}
	failure, _ := c.schemaWriteSave(ctx, schema, pageWriteClientKind)
	if failure != "" {
		failure = strings.Replace(failure, "Failed to save schema", "Failed to create schema", 1)
		return fail(failure)
	}
	return SourceCodeSchemaCreateResponse{Success: true, SchemaName: input.SchemaName, SchemaUID: uid, PackageName: input.PackageName, PackageUID: packageUID, Caption: caption}
}

func (c *Client) UpdateClientUnitSchema(ctx context.Context, input SchemaBodyUpdateRequest) SchemaBodyUpdateResponse {
	fail := func(text string) SchemaBodyUpdateResponse { return SchemaBodyUpdateResponse{Error: text} }
	if strings.TrimSpace(input.SchemaName) == "" {
		return fail("schema-name is required")
	}
	body, problem := schemaWriteResolveBody(input.Body, input.BodyFile)
	if problem != "" {
		return fail(problem)
	}
	resolved, err := c.schemaGetResolveTopLayer(ctx, input.SchemaName)
	if err != nil {
		return fail(err.Error())
	}
	success := SchemaBodyUpdateResponse{Success: true, SchemaName: input.SchemaName, BodyLength: utf16Length(body), DryRun: input.DryRun}
	if input.DryRun {
		return success
	}
	schema, problem := c.schemaWriteLoad(ctx, resolved, pageWriteClientKind, input.SchemaName)
	if problem != "" {
		return fail(problem)
	}
	schema.set("body", newString(body))
	failure, unknown := c.schemaWriteSave(ctx, schema, pageWriteClientKind)
	if failure != "" {
		if unknown {
			failure += " " + schemaWriteSaveOutcomeUnknownNote
		}
		return fail(failure)
	}
	return success
}
