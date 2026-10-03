package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// processDescribeKnownArgs is clio's accepted list, in the order its unknown-argument hint names it.
var processDescribeKnownArgs = []string{"environment-name", "process-name", "process-uid", "process-caption", "culture"}

func init() {
	registerTool(map[string]any{
		"name": "describe-business-process",
		"description": "Reads an existing Creatio process from the target Creatio environment and returns a STRUCTURED graph: elements (runtime type, " +
			"user-task schema, per-element configuration blocks, diagram placement), flows (name, source, target, kind, label, condition, geometry), " +
			"process parameters, usings and methods, plus the process's VERSION standing read from the process library (version, isActiveVersion, " +
			"activeVersionName/activeVersionSchemaUId, versions[], versionReadWarning). isActiveVersion false means the graph is NOT the one that runs: " +
			"re-describe by activeVersionSchemaUId. Returns clio's command envelope { exit-code, execution-log-messages } with the graph as one indented " +
			"JSON Info line. Identify the process by exactly one of process-name / process-uid / process-caption; a caption resolves to the ACTIVE " +
			"version of its family. Requires the CrtProcessBuilder package (ProcessDesignService) on the target environment.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"process-name":    map[string]string{"type": "string", "description": "Process code (schema Name), e.g. UsrProcess_493d4c9. Provide exactly one identity."},
			"process-uid":     map[string]string{"type": "string", "description": "Process UId (GUID). Provide exactly one identity."},
			"process-caption": map[string]string{"type": "string", "description": "Process caption (display name). Provide exactly one identity."},
			"culture":         map[string]string{"type": "string", "description": "Optional culture used to resolve localized captions (default en-US)."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		request := creatio.ProcessDescribeRequest{}
		for name, target := range map[string]*string{"process-name": &request.ProcessName, "process-uid": &request.ProcessUID, "process-caption": &request.ProcessCaption} {
			value, err := optionalStringArg(args, "describe-business-process", name)
			if err != nil {
				return nil, err
			}
			*target = value
		}
		culture, err := optionalStringArg(args, "describe-business-process", "culture")
		if err != nil {
			return nil, err
		}
		// clio defaults only an absent or null culture; an explicit empty one is sent as none.
		if _, ok := args["culture"].(string); ok {
			request.Culture = &culture
		}
		// clio refuses unknown keys inside its command envelope, before the package gate.
		if refusal := unknownArgumentError(args, processDescribeKnownArgs...); refusal != "" {
			return structuredToolResult(creatio.NewUserTasksResult(1, "Error", refusal)), nil
		}
		client, refusal, err := envs.resolve("describe-business-process", args, scopeName)
		if err != nil {
			return nil, err
		}
		if refusal != nil {
			return resolverFailureEnvelope(refusal), nil
		}
		return structuredToolResult(client.DescribeBusinessProcess(ctx, request)), nil
	})
}
