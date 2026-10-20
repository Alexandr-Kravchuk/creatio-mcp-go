package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataWriteRequestInfoArgNames are the arguments of clio's RequestInfoArgs, in the order its hint names them.
var dataWriteRequestInfoArgNames = []string{"request-type", "search", "schema-type", "environment-name", "version", "uri", "login", "password"}

// dataWriteRequestInfoAliases is RequestInfoTool.LegacyAliases (matched case-sensitively).
var dataWriteRequestInfoAliases = map[string]string{
	"requestType": "request-type", "request_type": "request-type", "request-name": "request-type",
	"requestName": "request-type", "request_name": "request-type",
	"schemaType": "schema-type", "schema_type": "schema-type",
	"environmentName": "environment-name", "environment_name": "environment-name",
}

// get-request-info: clio's read-only RequestInfoTool. A value of the wrong type fails binding (raised); an
// alias or unknown key is answered in the tool's own envelope.
func init() {
	registerTool(map[string]any{"name": "get-request-info"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		response, err := requestInfoAnswer(ctx, envs, "get-request-info", args)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(response), nil
	})
}

// requestInfoAnswer is RequestInfoTool.GetRequestInfo for the named tool (get-request-info, or its -to-file
// twin, whose binding failures name the twin): a binding failure is raised, everything else is answered in
// the tool's envelope.
func requestInfoAnswer(ctx context.Context, envs *environments, tool string, args map[string]any) (creatio.DataWriteRequestInfoResponse, error) {
	values := make(map[string]string, len(dataWriteRequestInfoArgNames))
	for _, name := range dataWriteRequestInfoArgNames {
		value, err := optionalStringArg(args, tool, name)
		if err != nil {
			return creatio.DataWriteRequestInfoResponse{}, err
		}
		values[name] = value
	}
	if message := dataWriteRequestInfoAliasError(args); message != "" {
		return creatio.DataWriteRequestInfoFailure(message), nil
	}
	request := creatio.DataWriteRequestInfoRequest{
		RequestType:     values["request-type"],
		Search:          values["search"],
		SchemaType:      values["schema-type"],
		Version:         values["version"],
		EnvironmentName: values["environment-name"],
		URI:             values["uri"],
		Resolve: func() (*creatio.Client, error) {
			client, failure, err := envs.resolve(tool, args, scopeDirect)
			if failure != nil {
				return nil, failure
			}
			return client, err
		},
	}
	return creatio.DataWriteRequestInfoGet(ctx, request), nil
}

// dataWriteRequestInfoAliasError is clio's BuildLegacyAliasError over the keys RequestInfoArgs does not bind.
// Go maps carry no request order, so the keys are reported sorted.
func dataWriteRequestInfoAliasError(args map[string]any) string {
	known := make(map[string]bool, len(dataWriteRequestInfoArgNames))
	for _, name := range dataWriteRequestInfoArgNames {
		known[name] = true
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		if !known[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var renamed, unknown []string
	for _, key := range keys {
		if canonical, ok := dataWriteRequestInfoAliases[key]; ok {
			renamed = append(renamed, fmt.Sprintf("'%s' -> '%s'", describeCallerKey(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", describeCallerKey(key)))
		}
	}
	message := ""
	if len(renamed) > 0 {
		message = "Rename: " + joinCallerKeys(renamed) + "."
	}
	if len(unknown) > 0 {
		if message != "" {
			message += " "
		}
		message += "Unknown args: " + joinCallerKeys(unknown) + ". Valid: request-type, search, schema-type, environment-name, version, uri, login, password."
	}
	return message
}
