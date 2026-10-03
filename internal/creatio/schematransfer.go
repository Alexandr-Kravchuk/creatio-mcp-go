package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SchemaTransferRequest struct {
	SchemaName, PackageName, ManagerName, Destination, Path string
	DryRun, AllowNewLayer                                   bool
}

func (c *Client) schemaWriteTransferPost(ctx context.Context, route string, body map[string]any, fallback string) (*jnode, error) {
	raw, _ := json.Marshal(body)
	payload, err := c.callService(ctx, serviceCall{Route: route, Body: raw, Limit: schemaWriteResponseBytes})
	if err != nil {
		return nil, err
	}
	node, err := parseJNode(payload)
	if err != nil {
		return nil, fmt.Errorf("%s did not return a JSON response (%v). Check that cliogate 2.0.0.46 or newer is installed on the environment.", c.serviceURL(route), err)
	}
	if !node.isObject() {
		return nil, fmt.Errorf("%s returned an empty response.", c.serviceURL(route))
	}
	if success := node.get("success"); success == nil || success.kind != jkBool || !success.flag {
		message := node.get("errorInfo").get("message").str()
		if strings.TrimSpace(message) == "" {
			message = fallback
		}
		return nil, fmt.Errorf("%s", message)
	}
	return node, nil
}
func (c *Client) ExportSchema(ctx context.Context, input SchemaTransferRequest) CommandResult {
	name := strings.TrimSpace(input.SchemaName)
	if name == "" {
		return CommandFailure("Schema name cannot be empty.")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return CommandFailure(fmt.Sprintf("'%s' cannot be used as a schema name: it becomes the bundle folder name, so it must not contain a path separator, be a '.' or '..' segment, or carry a character that is invalid in a file name. Pass the plain schema name, and use --destination to choose where the bundle goes.", name))
	}
	anchor, err := pageOutputAnchor(input.Destination)
	if err != nil {
		return CommandFailure(err.Error())
	}
	directory := filepath.Join(anchor, name)
	if strings.TrimSpace(input.Destination) != "" {
		var problem string
		directory, problem = schemaGetResolveOutput(directory)
		if problem != "" {
			return CommandFailure(problem)
		}
	}
	messages := []LogMessage{{MessageType: "Info", Value: fmt.Sprintf("Exporting schema '%s'...", name)}}
	fail := func(err error) CommandResult {
		return CommandResult{ExitCode: 1, Messages: append(messages, LogMessage{MessageType: "Error", Value: err.Error()})}
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	node, err := c.schemaWriteTransferPost(ctx, "rest/CreatioApiGateway/ExportSchema", map[string]any{"schemaName": name, "packageName": nullable(input.PackageName), "managerName": nullable(input.ManagerName)}, fmt.Sprintf("Could not export schema '%s'.", name))
	if err != nil {
		return fail(err)
	}
	data := node.get("schemaData").str()
	if data == "" {
		return fail(fmt.Errorf("The environment reported success but returned no payload for schema '%s'.", name))
	}
	if _, err = os.Stat(directory); err == nil {
		return fail(fmt.Errorf("'%s' already exists. Choose another destination, or remove it first — export never overwrites an existing bundle.", directory))
	}
	if err = os.MkdirAll(filepath.Dir(directory), 0755); err != nil {
		return fail(err)
	}
	if err = os.Mkdir(directory, 0755); err != nil {
		return fail(err)
	}
	schema := node.get("schema")
	descriptor := map[string]any{"schemaName": schema.get("schemaName").str(), "schemaUId": schema.get("schemaUId").str(), "caption": schema.get("caption").str(), "managerName": schema.get("managerName").str(), "sourcePackageName": schema.get("packageName").str(), "sourceEnvironmentUrl": c.config.BaseURL, "exportedOnUtc": time.Now().UTC(), "clioVersion": "8.1.0.134"}
	raw, _ := json.MarshalIndent(descriptor, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "descriptor.json"), raw, 0600); err == nil {
		err = os.WriteFile(filepath.Join(directory, "schema-data.json"), []byte(data), 0600)
	}
	if err != nil {
		os.RemoveAll(directory)
		return fail(err)
	}
	// Projection failures do not invalidate the authoritative bundle.
	if payload, parseErr := parseJNode([]byte(data)); parseErr == nil && payload.isObject() {
		for _, projection := range []struct{ key, file string }{{"MetaData", "metadata.json"}, {"Properties", "properties.json"}} {
			value := payload.get(projection.key)
			if value == nil {
				continue
			}
			raw := schemaWriteTransferIndent(value)
			if projection.key == "MetaData" {
				raw = []byte(value.str())
				if n, e := parseJNode(raw); e == nil {
					raw = schemaWriteTransferIndent(n)
				}
			}
			if e := os.WriteFile(filepath.Join(directory, projection.file), raw, 0600); e != nil {
				messages = append(messages, LogMessage{MessageType: "Warning", Value: e.Error()})
			}
		}
		buckets := map[string][]*jnode{}
		values := payload.get("LocalizableValues")
		if values.isArray() {
			for _, v := range values.items {
				culture := v.get("Culture").str()
				if culture == "" {
					culture = "unknown"
				}
				buckets[culture] = append(buckets[culture], v)
			}
		}
		if len(buckets) > 0 {
			os.Mkdir(filepath.Join(directory, "resources"), 0755)
			for culture, items := range buckets {
				if strings.ContainsAny(culture, "/\\") || culture == ".." {
					continue
				}
				a := newArray()
				a.items = items
				os.WriteFile(filepath.Join(directory, "resources", "resource."+culture+".json"), schemaWriteTransferIndent(a), 0600)
			}
		}
	}
	messages = append(messages, LogMessage{MessageType: "Info", Value: fmt.Sprintf("Exported '%s' (uId=%s, manager=%s) from package '%s' to '%s'.", schema.get("schemaName").str(), schema.get("schemaUId").str(), schema.get("managerName").str(), schema.get("packageName").str(), directory)})
	return CommandResult{Messages: messages}
}

func schemaWriteTransferIndent(node *jnode) []byte {
	raw := node.newtonsoftJSON()
	var buffer bytes.Buffer
	if json.Indent(&buffer, raw, "", "  ") == nil {
		return buffer.Bytes()
	}
	return raw
}

func (c *Client) ImportSchema(ctx context.Context, input SchemaTransferRequest) CommandResult {
	pkg := strings.TrimSpace(input.PackageName)
	if pkg == "" {
		return CommandFailure("Target package name cannot be empty.")
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return CommandFailure("A bundle path is required.")
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "schema-data.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return CommandFailure(fmt.Sprintf("'%s' was not found. Point import at a bundle folder produced by 'clio export-schema', or directly at its schema-data.json.", path))
	}
	if strings.TrimSpace(string(raw)) == "" {
		return CommandFailure(fmt.Sprintf("'%s' is empty.", path))
	}
	payload, err := parseJNode(raw)
	if err != nil || !payload.isObject() {
		return CommandFailure("The bundle does not name a schema; it is not a bundle produced by 'clio export-schema'.")
	}
	name, uid, manager := payload.get("Name").str(), payload.get("UId").str(), payload.get("ManagerName").str()
	descriptorRaw, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "descriptor.json"))
	if readErr == nil {
		descriptor, parseErr := parseJNode(descriptorRaw)
		if parseErr == nil && descriptor.isObject() {
			var differences []string
			for _, field := range []struct{ key, value string }{{"schemaName", name}, {"schemaUId", uid}, {"managerName", manager}} {
				old := descriptor.get(field.key).str()
				if old != "" && field.value != "" && !strings.EqualFold(normalizeGUIDText(old), normalizeGUIDText(field.value)) {
					differences = append(differences, fmt.Sprintf("%s is '%s' in descriptor.json but '%s' in schema-data.json", field.key, old, field.value))
				}
			}
			if len(differences) > 0 {
				return CommandFailure(fmt.Sprintf("The descriptor.json of this bundle describes a different schema than its schema-data.json: %s. '%s' is what the import writes, so the mismatch is refused rather than importing under one identity while reporting another. Remove or correct descriptor.json — it is provenance only and import reads the payload without it.", strings.Join(differences, "; "), path))
			}
			if name == "" {
				name = descriptor.get("schemaName").str()
			}
			if uid == "" {
				uid = descriptor.get("schemaUId").str()
			}
			if manager == "" {
				manager = descriptor.get("managerName").str()
			}
		}
	}
	if strings.TrimSpace(name) == "" {
		return CommandFailure("The bundle does not name a schema; it is not a bundle produced by 'clio export-schema'.")
	}
	var managerArg any
	if manager != "" {
		managerArg = manager
	}
	response, err := c.schemaWriteTransferPost(ctx, "rest/CreatioApiGateway/FindSchemaLayers", map[string]any{"schemaName": name, "managerName": managerArg}, fmt.Sprintf("Could not look up schema '%s'.", name))
	if err != nil {
		return CommandFailure(err.Error())
	}
	layers := response.get("layers")
	var owners []string
	owned := false
	identityMatch := false
	if layers.isArray() {
		for _, layer := range layers.items {
			owner := layer.get("packageName").str()
			quoted := "'" + owner + "'"
			if !schemaWriteTransferContains(owners, quoted) {
				owners = append(owners, quoted)
			}
			if strings.EqualFold(owner, pkg) {
				owned = true
				layerUID := layer.get("schemaUId").str()
				layerManager := layer.get("managerName").str()
				if (layerUID == "" || strings.EqualFold(normalizeGUIDText(uid), normalizeGUIDText(layerUID))) && (manager == "" || layerManager == "" || strings.EqualFold(manager, layerManager)) {
					identityMatch = true
				}
			}
		}
	}
	plan := fmt.Sprintf("Plan: CREATE schema '%s' in package '%s' (it does not exist yet).", name, pkg)
	if owned {
		if uid == "" {
			return CommandFailure(fmt.Sprintf("Package '%s' already owns a schema named '%s', but this bundle carries no uId, so it cannot be confirmed to be that same schema. A REPLACE is only safe once the identity matches, so it is refused rather than reported as one. Re-export the schema from the source environment so its schema-data.json carries a UId, or import into a package that does not own this name.", pkg, name))
		}
		if !identityMatch {
			var identities []string
			for _, layer := range layers.items {
				if strings.EqualFold(layer.get("packageName").str(), pkg) {
					identities = append(identities, fmt.Sprintf("uId=%s (manager %s)", schemaWriteTransferOrUnknown(layer.get("schemaUId").str()), schemaWriteTransferOrUnknown(layer.get("managerName").str())))
				}
			}
			return CommandFailure(fmt.Sprintf("Package '%s' already owns a schema named '%s', but it is not the one in this bundle: the bundle carries uId=%s (manager %s) while '%s' owns %s. Importing would not replace that layer — the platform preserves the bundle's uId, so it would add a second row with the same name in the same package, which the IU_Name_Manager_Package index rejects. Import into a package that does not own this name, or delete the conflicting schema first.", pkg, name, schemaWriteTransferOrUnknown(uid), schemaWriteTransferOrUnknown(manager), pkg, strings.Join(identities, ", ")))
		}
		plan = fmt.Sprintf("Plan: REPLACE schema '%s' in package '%s'.", name, pkg)
	} else if len(owners) > 0 {
		if !input.AllowNewLayer {
			return CommandFailure(fmt.Sprintf("Schema '%s' already exists in package(s) %s, not in '%s'. Importing it here would create an additional layer. Re-run with --package-name of the owning package to replace it, or with --allow-new-layer to create the layer deliberately.", name, strings.Join(owners, ", "), pkg))
		}
		plan = fmt.Sprintf("Plan: add a NEW LAYER of schema '%s' in package '%s'; it already exists in %s.", name, pkg, strings.Join(owners, ", "))
	}
	result := NewCommandResult(0, "Info", plan)
	if input.DryRun {
		result.Messages = append(result.Messages, LogMessage{MessageType: "Info", Value: "Dry run: nothing was written."})
		return result
	}
	response, err = c.schemaWriteTransferPost(ctx, "rest/CreatioApiGateway/ImportSchema", map[string]any{"schemaData": string(raw), "packageName": pkg}, fmt.Sprintf("Could not import the schema into package '%s'.", pkg))
	if err != nil {
		result.ExitCode = 1
		result.Messages = append(result.Messages, LogMessage{MessageType: "Error", Value: err.Error()})
		return result
	}
	result.Messages = append(result.Messages, LogMessage{MessageType: "Info", Value: fmt.Sprintf("Imported schema '%s' (uId=%s) into package '%s'.", name, uid, pkg)})
	if importer := response.get("importResult").str(); strings.TrimSpace(importer) != "" {
		result.Messages = append(result.Messages, LogMessage{MessageType: "Info", Value: "Platform importer: " + importer})
	}
	result.Messages = append(result.Messages, LogMessage{MessageType: "Warning", Value: "The schema is saved but not built. Run 'clio compile-configuration' when it carries source code, and 'clio update-db-structure' when it changes the database structure."})
	return result
}

func schemaWriteTransferContains(values []string, value string) bool {
	for _, item := range values {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func schemaWriteTransferOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<unknown>"
	}
	return value
}

// SchemaTransferRequirement checks the minimum ClioGate version before command validation, as clio dispatch does.
func (c *Client) SchemaTransferRequirement(ctx context.Context) string {
	version, found, err := c.lowestClioGateVersion(ctx)
	if err == nil && found && describeCompareVersions(version, "2.0.0.46") >= 0 {
		return ""
	}
	return "To use this command, you need to install the cliogate package version 2.0.0.46 or higher. Install or update the package in the target environment and retry.\nRun 'clio install-gate -e <environment>' (or call the install-gate MCP tool) to install/update cliogate."
}
