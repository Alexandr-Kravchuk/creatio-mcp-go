package main

// Application write tools (T7): create-app, create-app-section, update-app-section, delete-app-section —
// clio's ApplicationCreateTool, ApplicationSectionCreateTool, ApplicationSectionUpdateTool and
// ApplicationSectionDeleteTool. Every failure after argument binding is reported in the tool's own envelope,
// as clio's tools catch and report it. create-app, update-app-section and delete-app-section wait for the
// work to finish (McpProgressHeartbeat.RunWithProgressAsync); create-app-section answers clio's in-progress
// envelope once the response deadline passes (RunWithProgressAndDeadlineAsync) while the work goes on.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// appWriteAnnotations are the [McpServerTool] hints of every application write tool.
var appWriteAnnotations = toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}

// appWriteNoDeadline is RunWithProgressAsync: the call waits for the work, however long it runs.
const appWriteNoDeadline = 100 * 365 * 24 * time.Hour

// appWriteLocalizationKeys are the localization-map arguments every application write tool refuses.
var appWriteLocalizationKeys = []string{"title-localizations", "description-localizations", "caption-localizations", "name-localizations"}

// appWriteStrings reads clio's string arguments; a non-string is clio's binding refusal.
func appWriteStrings(tool string, args map[string]any, names ...string) (map[string]string, error) {
	values := map[string]string{}
	for _, name := range names {
		value, err := optionalStringArg(args, tool, name)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

// appWriteOptional returns the argument as a pointer: nil when it was absent or null.
func appWriteOptional(args map[string]any, name, value string) *string {
	if args[name] == nil {
		return nil
	}
	return &value
}

// appWriteBoolArg reads a bool argument that defaults to fallback when absent or null.
func appWriteBoolArg(tool string, args map[string]any, name string, fallback bool) (bool, error) {
	switch value := args[name].(type) {
	case nil:
		return fallback, nil
	case bool:
		return value, nil
	default:
		return false, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
	}
}

// appWriteLocalizationMaps reports whether any localization map was sent; a value that is not an object is
// clio's binding refusal.
func appWriteLocalizationMaps(tool string, args map[string]any) (bool, error) {
	sent := false
	for _, name := range appWriteLocalizationKeys {
		switch args[name].(type) {
		case nil:
		case map[string]any:
			sent = true
		default:
			return false, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an object. Received an incompatible JSON value.", name, tool)
		}
	}
	return sent, nil
}

func init() {
	registerTool(map[string]any{"name": "create-app"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "create-app"
		values, err := appWriteStrings(tool, args, "name", "code", "template-code", "description", "icon-background", "icon-id", "client-type-id", "optional-template-data-json")
		if err != nil {
			return nil, err
		}
		mobile, err := appWriteBoolArg(tool, args, "with-mobile-pages", true)
		if err != nil {
			return nil, err
		}
		maps, err := appWriteLocalizationMaps(tool, args)
		if err != nil {
			return nil, err
		}
		request := creatio.AppCreateRequest{Name: values["name"], Code: values["code"],
			TemplateCode:   appWriteOptional(args, "template-code", values["template-code"]),
			Description:    appWriteOptional(args, "description", values["description"]),
			IconBackground: values["icon-background"], IconID: values["icon-id"], ClientTypeID: values["client-type-id"],
			WithMobilePages: mobile, OptionalTemplateDataJSON: values["optional-template-data-json"], LocalizationMaps: maps}
		templateData, err := creatio.ValidateAppCreate(request)
		if err != nil {
			return structuredToolResult(creatio.AppCreateFailure(err.Error())), nil
		}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppCreateFailure(redacted(failure))), nil
		}
		result, _, err := runLongOperation(ctx, tool, appWriteNoDeadline, func(ctx context.Context, stage func(string)) creatio.AppCreateResponse {
			return client.CreateApp(ctx, request, templateData, stage)
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(appWriteAnnotations))

	registerTool(map[string]any{"name": "create-app-section"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "create-app-section"
		values, err := appWriteStrings(tool, args, "application-code", "caption", "description", "entity-schema-name", "code", "icon-background", "caption-culture")
		if err != nil {
			return nil, err
		}
		mobile, err := appWriteBoolArg(tool, args, "with-mobile-pages", true)
		if err != nil {
			return nil, err
		}
		maps, err := appWriteLocalizationMaps(tool, args)
		if err != nil {
			return nil, err
		}
		if err := creatio.ValidateAppSectionCreate(values["application-code"], values["caption"], maps); err != nil {
			return structuredToolResult(creatio.AppSectionCreateFailure(err.Error())), nil
		}
		color := appWriteOptional(args, "icon-background", values["icon-background"])
		// A supplied color is resolved with elicitation disabled; a blank one is left for the service to refuse.
		if strings.TrimSpace(values["icon-background"]) != "" {
			resolved, err := creatio.AppWriteResolveIconColor(values["icon-background"])
			if err != nil {
				return structuredToolResult(creatio.AppSectionCreateFailure(err.Error())), nil
			}
			color = &resolved
		}
		request := creatio.AppSectionCreateRequest{ApplicationCode: values["application-code"], Caption: values["caption"],
			Description:      appWriteOptional(args, "description", values["description"]),
			EntitySchemaName: appWriteOptional(args, "entity-schema-name", values["entity-schema-name"]),
			WithMobilePages:  mobile, IconBackground: color, CaptionCulture: values["caption-culture"], Code: values["code"]}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppSectionCreateFailure(redacted(failure))), nil
		}
		result, finished, err := runLongOperation(ctx, tool, 0, func(ctx context.Context, stage func(string)) creatio.AppSectionCreateResponse {
			return client.CreateAppSection(ctx, request, stage)
		})
		if err != nil {
			return nil, err
		}
		if !finished {
			return structuredToolResult(creatio.AppSectionInProgress(values["caption"], values["code"])), nil
		}
		return structuredToolResult(result), nil
	}, withAnnotations(appWriteAnnotations))

	registerTool(map[string]any{"name": "update-app-section"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "update-app-section"
		values, err := appWriteStrings(tool, args, "application-code", "section-code", "caption", "description", "icon-id", "icon-background", "caption-culture")
		if err != nil {
			return nil, err
		}
		maps, err := appWriteLocalizationMaps(tool, args)
		if err != nil {
			return nil, err
		}
		request := creatio.AppSectionUpdateRequest{ApplicationCode: values["application-code"], SectionCode: values["section-code"],
			Caption:        appWriteOptional(args, "caption", values["caption"]),
			Description:    appWriteOptional(args, "description", values["description"]),
			IconID:         appWriteOptional(args, "icon-id", values["icon-id"]),
			IconBackground: appWriteOptional(args, "icon-background", values["icon-background"]),
			CaptionCulture: values["caption-culture"]}
		if err := creatio.ValidateAppSectionUpdate(request, maps); err != nil {
			return structuredToolResult(creatio.AppSectionUpdateFailure(err.Error())), nil
		}
		if request.IconBackground != nil && strings.TrimSpace(*request.IconBackground) != "" {
			resolved, err := creatio.AppWriteResolveIconColor(*request.IconBackground)
			if err != nil {
				return structuredToolResult(creatio.AppSectionUpdateFailure(err.Error())), nil
			}
			request.IconBackground = &resolved
		}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppSectionUpdateFailure(redacted(failure))), nil
		}
		result, _, err := runLongOperation(ctx, tool, appWriteNoDeadline, func(ctx context.Context, _ func(string)) creatio.AppSectionUpdateResponse {
			return client.UpdateAppSection(ctx, request)
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(appWriteAnnotations))

	registerTool(map[string]any{"name": "delete-app-section"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "delete-app-section"
		values, err := appWriteStrings(tool, args, "application-code", "section-code")
		if err != nil {
			return nil, err
		}
		deleteEntity, err := appWriteBoolArg(tool, args, "delete-entity-schema", false)
		if err != nil {
			return nil, err
		}
		if err := creatio.ValidateAppSectionDelete(values["application-code"], values["section-code"]); err != nil {
			return structuredToolResult(creatio.AppSectionDeleteFailure(err.Error())), nil
		}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppSectionDeleteFailure(redacted(failure))), nil
		}
		result, _, err := runLongOperation(ctx, tool, appWriteNoDeadline, func(ctx context.Context, _ func(string)) creatio.AppSectionDeleteResponse {
			return client.DeleteAppSection(ctx, values["application-code"], values["section-code"], deleteEntity)
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(appWriteAnnotations))
}
