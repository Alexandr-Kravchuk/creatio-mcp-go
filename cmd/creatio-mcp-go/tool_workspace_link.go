package main

// link-from-repository-by-environment, link-from-repository-by-env-package-path and
// link-from-repository-unlocked: clio's LinkFromRepositoryTool, three methods with flat camelCase
// parameters (repoPath, packages, environmentName, envPkgPath, dryRun, skipPreparation) bound by clio's
// method binder: exact key names, unknown keys ignored, parameters checked in declaration order. The command
// is internal/workspace.Link.
// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)] for all.
//
// Deliberate difference: clio runs these tools with the Link4RepoCommand of its bootstrap container, whose
// Creatio client points at the placeholder http://localhost, so the preparation (Maintainer, unlock, 2fs)
// and the unlocked-package query never reach the environment the call names ("Connection refused
// (localhost:80)", measured against clio 8.1.0.142 with EnvironmentPath set). Here they go to the registered
// environment the call names. A call that names no registered environment (by-env-package-path, or a
// folder path given as environmentName) is refused where clio would first reach Creatio: this server runs no
// destructive call against an environment the caller did not name.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	linkByEnvironmentTool    = "link-from-repository-by-environment"
	linkByEnvPackagePathTool = "link-from-repository-by-env-package-path"
	linkUnlockedTool         = "link-from-repository-unlocked"
)

func init() {
	for _, tool := range []string{linkByEnvironmentTool, linkByEnvPackagePathTool, linkUnlockedTool} {
		tool := tool
		registerTool(map[string]any{"name": tool}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			return invokeWorkspaceLink(ctx, envs, args, tool)
		}, withAnnotations(toolAnnotations{Destructive: true}))
	}
}

// linkParameter is one parameter of a LinkFromRepositoryTool method: a [Required] string, an optional
// string, or a bool?.
type linkParameter struct {
	name     string
	required bool
	flag     bool
}

var linkParameters = map[string][]linkParameter{
	linkByEnvironmentTool: {{name: "repoPath", required: true}, {name: "packages", required: true},
		{name: "environmentName"}, {name: "dryRun", flag: true}, {name: "skipPreparation", flag: true}},
	linkByEnvPackagePathTool: {{name: "envPkgPath", required: true}, {name: "repoPath", required: true},
		{name: "packages", required: true}, {name: "dryRun", flag: true}, {name: "skipPreparation", flag: true}},
	linkUnlockedTool: {{name: "repoPath", required: true}, {name: "environmentName"}, {name: "dryRun", flag: true}},
}

// bindLinkArguments binds the parameters as clio does (measured): first every supplied value in declaration
// order, where a null for a required string and a value of the wrong JSON type are clio's
// invalid-parameter-type refusals and a null optional is its default; then the absent required parameters in
// declaration order, each the binder's "missing a value" failure.
func bindLinkArguments(tool string, args map[string]any) (map[string]string, map[string]bool, error) {
	texts, flags := map[string]string{}, map[string]bool{}
	for _, parameter := range linkParameters[tool] {
		value, present := args[parameter.name]
		if !present {
			continue
		}
		if value == nil {
			if parameter.required {
				return nil, nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a string. "+
					"Received a JSON null, and the argument is required.", parameter.name, tool)
			}
			continue
		}
		if parameter.flag {
			flag, ok := value.(bool)
			if !ok {
				return nil, nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. "+
					"Received an incompatible JSON value.", parameter.name, tool)
			}
			flags[parameter.name] = flag
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a string. "+
				"Received an incompatible JSON value.", parameter.name, tool)
		}
		texts[parameter.name] = text
	}
	for _, parameter := range linkParameters[tool] {
		if _, present := args[parameter.name]; !present && parameter.required {
			return nil, nil, pkgWriteMissingFlat(parameter.name)
		}
	}
	return texts, flags, nil
}

func invokeWorkspaceLink(ctx context.Context, envs *environments, args map[string]any, tool string) (*mcp.CallToolResult, error) {
	texts, flags, err := bindLinkArguments(tool, args)
	if err != nil {
		return nil, err
	}
	request := workspace.LinkRequest{RepoPath: texts["repoPath"], DryRun: flags["dryRun"]}
	switch tool {
	case linkByEnvironmentTool, linkUnlockedTool:
		if strings.TrimSpace(texts["environmentName"]) == "" {
			return structuredToolResult(creatio.CommandFailure(
				fmt.Sprintf("environment-name is required for %s outside credential passthrough.", tool))), nil
		}
		request.Environment = texts["environmentName"]
		if tool == linkUnlockedTool {
			request.Unlocked = true
		} else {
			request.Packages, request.SkipPreparation = texts["packages"], flags["skipPreparation"]
		}
	case linkByEnvPackagePathTool:
		request.EnvPkgPath, request.Packages, request.SkipPreparation = texts["envPkgPath"], texts["packages"], flags["skipPreparation"]
	}
	deps := workspace.LinkDeps{Environment: linkEnvironmentLookup(envs), Site: linkSite(envs, tool, request.Environment)}
	result, _, err := runLongOperation(ctx, tool, appWriteNoDeadline,
		func(ctx context.Context, _ func(string)) creatio.CommandResult {
			return workspace.Link(ctx, request, deps)
		})
	if err != nil {
		return nil, err
	}
	return structuredToolResult(result), nil
}

// linkEnvironmentLookup is ISettingsRepository.IsEnvironmentExists plus GetEnvironment over clio's settings:
// the name matched ordinally without case and without trimming.
func linkEnvironmentLookup(envs *environments) func(string) (workspace.LinkEnvironment, bool) {
	return func(name string) (workspace.LinkEnvironment, bool) {
		if envs == nil || strings.TrimSpace(name) == "" {
			return workspace.LinkEnvironment{}, false
		}
		settings, _ := envs.snapshot()
		for _, environment := range settings.Environments {
			if strings.ToUpper(environment.Name) == strings.ToUpper(name) {
				return workspace.LinkEnvironment{EnvironmentPath: environment.EnvironmentPath, IsNetCore: environment.IsNetCore,
					URI: environment.URI}, true
			}
		}
		return workspace.LinkEnvironment{}, false
	}
}

// linkSite connects the command to the registered environment the call names. named is the environment the
// command resolved its Pkg folder through; it is empty when the call names none.
func linkSite(envs *environments, tool, environmentName string) func(context.Context, string) (workspace.LinkSite, error) {
	return func(_ context.Context, named string) (workspace.LinkSite, error) {
		if named == "" {
			return nil, errors.New(linkNoSiteText(tool, environmentName))
		}
		if envs == nil {
			return nil, errors.New(linkNoSiteText(tool, environmentName))
		}
		client, err := envs.client(named, creatio.ConnectionOverrides{})
		if err != nil {
			return nil, errors.New(redacted(err))
		}
		return client.WorkspaceLinkSite(), nil
	}
}

// linkNoSiteText is this server's refusal where clio would first reach Creatio for a call that names no
// registered environment.
func linkNoSiteText(tool, environmentName string) string {
	switch tool {
	case linkUnlockedTool:
		return fmt.Sprintf("environmentName '%s' is a folder path, not a registered environment, so there is no Creatio "+
			"site to ask for unlocked packages. Pass a registered environment name.", environmentName)
	case linkByEnvironmentTool:
		return fmt.Sprintf("environmentName '%s' is a folder path, not a registered environment, so this server does not "+
			"run the preparation (Maintainer check, unlock, 2fs) against a Creatio site. Prepare the packages and call "+
			"%s again with skipPreparation=true, or pass a registered environment name.", environmentName, tool)
	}
	return fmt.Sprintf("%s names no environment, so this server does not run the preparation (Maintainer check, "+
		"unlock, 2fs) against a Creatio site. Prepare the packages and call it again with skipPreparation=true, or use "+
		"%s with a registered environment name.", tool, linkByEnvironmentTool)
}
