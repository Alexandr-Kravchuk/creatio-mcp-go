package main

// execute-esq-to-file: clio 8.1.0.142's ExecuteEsqToFileTool over the shared execute-esq run
// (creatio.ESQPrepare / Client.ESQExecute).

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// esqMembers is clio's ExecuteEsqArgs. The record requires environment-name (environments.requireEnvironment):
// a call without it fails the binding, and a blank one reaches ESQPrepare.
var esqMembers = []clioMember{{"query", clioAny, false}, {"environment-name", clioString, false}, {"timeout", clioInt, false}}

// esqRun is ExecuteEsqTool.Run after binding: the local checks, then the environment, then the request.
func esqRun(ctx context.Context, envs *environments, tool string, bound clioBound, maxBytes int, suggestFileTwin bool) (creatio.ExecuteEsqResponse, error) {
	var environmentName *string
	if bound.has("environment-name") {
		name, _ := bound.str("environment-name")
		environmentName = &name
	}
	prepared, failure := creatio.ESQPrepare(bound.raw("query"), bound.extension, environmentName)
	if failure != nil {
		return *failure, nil
	}
	selector := map[string]any{}
	if environmentName != nil {
		selector["environment-name"] = *environmentName
	}
	client, refusal, err := envs.resolve(tool, selector, scopeName)
	if err != nil {
		return creatio.ExecuteEsqResponse{}, err
	}
	if refusal != nil {
		return creatio.ESQFailure142(redacted(refusal)), nil
	}
	return client.ESQExecute(ctx, prepared, bound.integer("timeout"), maxBytes, suggestFileTwin), nil
}

func init() {
	registerTool(map[string]any{"name": "execute-esq-to-file"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "execute-esq-to-file"
		bound, err := clioBind(tool, args, envs.requireEnvironment(append(append([]clioMember(nil), esqMembers...), clioMember{"output-file", clioString, true})))
		if err != nil {
			return nil, err
		}
		outputFile, _ := bound.str("output-file")
		path, refusal := creatio.ToFileResolve(outputFile)
		if refusal != "" {
			return toFileResult(creatio.ESQFailureWithoutGuidance(refusal + " Use execute-esq when the rows should be returned inline.")), nil
		}
		response, err := esqRun(ctx, envs, tool, bound, creatio.ESQToFileMaxResponseBytes, false)
		if err != nil {
			return nil, err
		}
		if !response.Success || response.Rows == nil {
			return toFileResult(response), nil
		}
		if refusal := creatio.ToFileWriteNew(path, response.Rows); refusal != "" {
			return toFileResult(creatio.ESQFailureWithoutGuidance(refusal)), nil
		}
		response.Rows = nil
		response.OutputFile = &path
		return toFileResult(response), nil
	}, withAnnotations(toFileAnnotations))
}
