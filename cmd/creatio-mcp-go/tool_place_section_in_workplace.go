package main

// place-section-in-workplace: clio 8.1.0.142's PlaceSectionInWorkplaceTool over WorkplacePlacementService.

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var placeSectionArguments = []string{"environment-name", "section-code", "workplace-name", "package-name", "client-type", "roles",
	"remove-from-my-applications", "position", "mode"}

// placeSectionAliases is PlaceSectionInWorkplaceTool.LegacyAliases (Ordinal).
var placeSectionAliases = t21aWithAliases(map[string]string{
	"packageName": "package-name", "package_name": "package-name", "package": "package-name",
	"sectionCode": "section-code", "section_code": "section-code", "code": "section-code", "app-code": "section-code",
	"workplaceName": "workplace-name", "workplace_name": "workplace-name", "workplace": "workplace-name",
	"clientType": "client-type", "client_type": "client-type",
	"removeFromMyApplications": "remove-from-my-applications", "remove_from_my_applications": "remove-from-my-applications",
})

func init() {
	registerTool(map[string]any{"name": "place-section-in-workplace"}, invokePlaceSectionInWorkplace,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

func invokePlaceSectionInWorkplace(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := t21aBinder{tool: "place-section-in-workplace", args: args}
	var request creatio.WorkplacePlacementRequest
	var err error
	for _, field := range []struct {
		name   string
		target *string
	}{{"environment-name", &request.EnvironmentName}, {"section-code", &request.SectionCode},
		{"workplace-name", &request.WorkplaceName}, {"package-name", &request.PackageName}, {"client-type", &request.ClientType}} {
		if *field.target, err = bind.text(field.name); err != nil {
			return nil, err
		}
	}
	if request.Roles, _, err = bind.strings("roles"); err != nil {
		return nil, err
	}
	remove, err := bind.boolean("remove-from-my-applications")
	if err != nil {
		return nil, err
	}
	if request.Position, err = bind.integer("position"); err != nil {
		return nil, err
	}
	if request.Mode, err = bind.text("mode"); err != nil {
		return nil, err
	}
	failure := func(message string) (*mcp.CallToolResult, error) {
		return t21aResult(creatio.PlaceSectionFailure(message)), nil
	}
	if message := t21aAliasError(args, placeSectionArguments, placeSectionAliases, false, ".",
		"Valid: environment-name, package-name, section-code, workplace-name, client-type, roles, remove-from-my-applications, position, mode."); message != "" {
		return failure(message)
	}
	if strings.TrimSpace(request.EnvironmentName) == "" {
		return failure("environment-name is required and cannot be empty.")
	}
	if strings.TrimSpace(request.SectionCode) == "" {
		return failure("section-code is required and cannot be empty.")
	}
	if !strings.EqualFold(strings.TrimSpace(request.Mode), "none") && strings.TrimSpace(request.WorkplaceName) == "" {
		return failure("workplace-name is required unless mode is 'none'.")
	}
	if strings.TrimSpace(request.ClientType) == "" {
		request.ClientType = "web"
	}
	request.RemoveFromMyApplications = remove == nil || *remove
	client, resolution, err := t21aResolve(envs, bind.tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if resolution != "" {
		return failure(resolution)
	}
	response, err := client.PlaceSectionInWorkplace(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return failure(redact.Text(err.Error()))
	}
	if response.Error != nil {
		text := redact.Text(*response.Error)
		response.Error = &text
	}
	return t21aResult(response), nil
}
