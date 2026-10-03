package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var processWriteSpecs = []struct {
	name                    string
	destructive, idempotent bool
	keys                    []string
}{
	{"create-business-process", true, false, []string{"environment-name", "package-name", "descriptor"}},
	{"modify-business-process", true, false, []string{"environment-name", "process-name", "process-uid", "operations", "confirm-layout-change"}},
	{"modify-business-process-as-new-version", true, false, []string{"environment-name", "process-name", "process-uid", "operations", "package-name"}},
	{"set-active-business-process-version", true, true, []string{"environment-name", "version-name", "version-uid"}},
}

func init() {
	for _, spec := range processWriteSpecs {
		spec := spec
		registerTool(map[string]any{"name": spec.name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			input := map[string]string{}
			for _, key := range spec.keys {
				if key == "confirm-layout-change" {
					continue
				}
				value, err := optionalStringArg(args, spec.name, key)
				if err != nil {
					return nil, err
				}
				input[key] = value
			}
			if spec.name == "modify-business-process" {
				if value, exists := args["confirm-layout-change"]; exists && value != nil {
					if _, ok := value.(bool); !ok {
						return nil, fmt.Errorf("invalid-parameter-type: argument 'confirm-layout-change' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", spec.name)
					}
				}
			}
			if refusal := unknownArgumentError(args, spec.keys...); refusal != "" {
				return structuredToolResult(creatio.CommandFailure(refusal)), nil
			}
			if strings.TrimSpace(input["environment-name"]) == "" {
				return structuredToolResult(creatio.CommandFailure("environment-name is required and cannot be empty.")), nil
			}
			request := creatio.ProcessWriteRequest{Descriptor: input["descriptor"], Package: input["package-name"], Name: input["process-name"], UID: input["process-uid"], Operations: input["operations"]}
			if value, ok := args["confirm-layout-change"].(bool); ok {
				request.ConfirmLayoutChange = value
			}
			// clio validates these before resolving or contacting an environment.
			switch spec.name {
			case "create-business-process":
				if strings.TrimSpace(request.Descriptor) == "" {
					return structuredToolResult(creatio.CommandFailure("descriptor is required and cannot be empty.")), nil
				}
			case "modify-business-process", "modify-business-process-as-new-version":
				if strings.TrimSpace(request.Name) == "" && strings.TrimSpace(request.UID) == "" {
					return structuredToolResult(creatio.CommandFailure("one of process-name or process-uid is required.")), nil
				}
				if strings.TrimSpace(request.Name) != "" && strings.TrimSpace(request.UID) != "" {
					return structuredToolResult(creatio.CommandFailure("Provide only one of process-name or process-uid, not both.")), nil
				}
				if spec.name == "modify-business-process" && strings.TrimSpace(request.Operations) == "" {
					return structuredToolResult(creatio.CommandFailure("operations is required and cannot be empty.")), nil
				}
			case "set-active-business-process-version":
				name, uid := input["version-name"], input["version-uid"]
				if strings.TrimSpace(name) == "" && strings.TrimSpace(uid) == "" {
					return structuredToolResult(creatio.CommandFailure("one of version-name or version-uid is required.")), nil
				}
				if strings.TrimSpace(name) != "" && strings.TrimSpace(uid) != "" {
					return structuredToolResult(creatio.CommandFailure("Provide only one of version-name or version-uid, not both.")), nil
				}
			}
			client, failure, err := envs.resolve(spec.name, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			var result creatio.CommandResult
			switch spec.name {
			case "create-business-process":
				result = client.CreateBusinessProcess(ctx, input["environment-name"], request)
			case "modify-business-process":
				result = client.ModifyBusinessProcess(ctx, input["environment-name"], request)
			case "modify-business-process-as-new-version":
				result = client.ModifyBusinessProcessAsNewVersion(ctx, input["environment-name"], request)
			case "set-active-business-process-version":
				result = client.SetActiveBusinessProcessVersion(ctx, input["environment-name"], input["version-name"], input["version-uid"])
			}
			return structuredToolResult(result), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: spec.destructive, Idempotent: spec.idempotent, OpenWorld: false}))
	}
}
