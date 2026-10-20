package main

// create-package: clio 8.1.0.142's CreatePackageTool over CreatePackageCommand / RemotePackageCreator.

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var createPackageArguments = []string{"environment-name", "package-name", "description", "dependencies", "application-code"}

// createPackageAliases is CreatePackageTool.LegacyAliases (Ordinal).
var createPackageAliases = t21aWithAliases(map[string]string{
	"packageName": "package-name", "package_name": "package-name", "package": "package-name",
	"applicationCode": "application-code", "application_code": "application-code", "app-code": "application-code",
})

func init() {
	registerTool(map[string]any{"name": "create-package"}, invokeCreatePackage,
		withAnnotations(toolAnnotations{Destructive: true}))
}

func invokeCreatePackage(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := t21aBinder{tool: "create-package", args: args}
	environmentName, err := bind.text("environment-name")
	if err != nil {
		return nil, err
	}
	var input creatio.CreatePackageRequest
	if input.PackageName, err = bind.text("package-name"); err != nil {
		return nil, err
	}
	if input.Description, err = bind.str("description"); err != nil {
		return nil, err
	}
	if input.Dependencies, _, err = bind.strings("dependencies"); err != nil {
		return nil, err
	}
	if input.ApplicationCode, err = bind.str("application-code"); err != nil {
		return nil, err
	}
	if message := t21aAliasError(args, createPackageArguments, createPackageAliases, false, ".",
		"Valid: environment-name, package-name, description, dependencies, application-code."); message != "" {
		return t21aResult(creatio.CreatePackageFailure(message)), nil
	}
	if strings.TrimSpace(environmentName) == "" {
		return t21aResult(creatio.CreatePackageFailure("environment-name is required and cannot be empty.")), nil
	}
	if strings.TrimSpace(input.PackageName) == "" {
		return t21aResult(creatio.CreatePackageFailure("package-name is required and cannot be empty.")), nil
	}
	client, failure, err := t21aResolve(envs, bind.tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != "" {
		return t21aResult(creatio.CreatePackageFailure(failure)), nil
	}
	response := client.CreatePackage(ctx, input)
	if response.Error != nil {
		text := redact.Text(*response.Error)
		response.Error = &text
	}
	return t21aResult(response), nil
}
