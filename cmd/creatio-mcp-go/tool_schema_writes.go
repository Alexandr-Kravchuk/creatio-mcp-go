package main

import (
	"context"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

func init() {
	for _, name := range []string{"create-schema", "update-schema", "create-sql-schema", "update-sql-schema", "install-sql-schema"} {
		tool := name
		destructive := tool == "update-schema" || tool == "update-sql-schema" || tool == "install-sql-schema"
		registerTool(map[string]any{"name": tool}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			fields := map[string]string{}
			for _, key := range []string{"schema-name", "package-name", "caption", "description", "body", "body-file"} {
				value, err := optionalStringArg(args, tool, key)
				if err != nil {
					return nil, err
				}
				fields[key] = value
			}
			if tool == "create-schema" {
				var problems []string
				if strings.TrimSpace(fields["schema-name"]) == "" {
					problems = append(problems, "schema-name is required")
				} else if !schemaWriteToolNameValid(fields["schema-name"]) {
					problems = append(problems, "schema-name must start with a letter and contain only letters, digits, or underscores")
				}
				if strings.TrimSpace(fields["package-name"]) == "" {
					problems = append(problems, "package-name is required")
				}
				if len(problems) > 0 {
					return structuredToolResult(creatio.SourceCodeSchemaCreateResponse{Error: strings.Join(problems, "; ") + ". Valid arguments: schema-name, package-name (required); body, body-file, caption, description (optional); environment-name or uri/login/password."}), nil
				}
			}
			dry, err := schemaGetBoolArg(args, tool, "dry-run")
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(tool, args, scopeDirect)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				switch tool {
				case "create-schema":
					return structuredToolResult(creatio.SourceCodeSchemaCreateResponse{Error: redacted(failure)}), nil
				case "create-sql-schema":
					return structuredToolResult(creatio.SQLSchemaCreateResponse{Error: redacted(failure)}), nil
				case "install-sql-schema":
					return structuredToolResult(creatio.SQLSchemaInstallResponse{Error: redacted(failure)}), nil
				default:
					return structuredToolResult(creatio.SchemaBodyUpdateResponse{Error: redacted(failure)}), nil
				}
			}
			body := (*string)(nil)
			if args["body"] != nil {
				v := fields["body"]
				body = &v
			}
			switch tool {
			case "create-schema":
				return structuredToolResult(client.CreateSourceCodeSchema(ctx, creatio.SourceCodeSchemaCreateRequest{SchemaName: fields["schema-name"], PackageName: fields["package-name"], Caption: fields["caption"], Description: fields["description"], Body: body, BodyFile: schemaWriteToolOptionalString(args, "body-file", fields["body-file"])})), nil
			case "create-sql-schema":
				var engine *int
				if args["db-engine-type"] != nil {
					n, err := hierarchyIntArg(args, tool, "db-engine-type")
					if err != nil {
						return nil, err
					}
					engine = &n
				}
				phase := 1
				if args["install-type"] != nil {
					phase, err = hierarchyIntArg(args, tool, "install-type")
					if err != nil {
						return nil, err
					}
				}
				return structuredToolResult(client.CreateSQLSchema(ctx, creatio.SQLSchemaCreateRequest{SchemaName: fields["schema-name"], PackageName: fields["package-name"], Caption: fields["caption"], Description: fields["description"], DBEngineType: engine, InstallType: phase})), nil
			case "install-sql-schema":
				return structuredToolResult(client.InstallSQLSchema(ctx, fields["schema-name"])), nil
			default:
				request := creatio.SchemaBodyUpdateRequest{SchemaName: fields["schema-name"], Body: body, BodyFile: fields["body-file"], DryRun: dry}
				if tool == "update-schema" {
					return structuredToolResult(client.UpdateSourceCodeSchema(ctx, request)), nil
				}
				return structuredToolResult(client.UpdateSQLSchema(ctx, request)), nil
			}
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: destructive, Idempotent: false, OpenWorld: false}))
	}
}

func schemaWriteToolOptionalString(args map[string]any, key, value string) *string {
	if args[key] == nil {
		return nil
	}
	return &value
}
func schemaWriteToolNameValid(value string) bool {
	for i, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '_')) {
			return false
		}
	}
	return value != ""
}
