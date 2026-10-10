package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataWriteBindingDbTools are clio's CreateDataBindingDbTool, UpsertDataBindingRowDbTool and
// RemoveDataBindingRowDbTool: string arguments bound as clio's records bind them, the environment resolved per
// call (a failure reported in the command envelope), then the command's own log.
var dataWriteBindingDbTools = []struct {
	name string
	keys []string // the arguments of clio's record; other keys are ignored, as its binder ignores them
	run  func(*creatio.Client, context.Context, creatio.DataWriteBindingDbRequest) creatio.CommandResult
}{
	{"create-data-binding-db", []string{"package-name", "schema-name", "binding-name", "rows"}, (*creatio.Client).CreateDataBindingDb},
	{"upsert-data-binding-row-db", []string{"package-name", "binding-name", "values"}, (*creatio.Client).UpsertDataBindingRowDb},
	{"remove-data-binding-row-db", []string{"package-name", "binding-name", "key-value"}, (*creatio.Client).RemoveDataBindingRowDb},
}

func init() {
	for _, tool := range dataWriteBindingDbTools {
		tool := tool
		registerTool(map[string]any{"name": tool.name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			var request creatio.DataWriteBindingDbRequest
			targets := map[string]*string{"package-name": &request.PackageName, "schema-name": &request.SchemaName,
				"binding-name": &request.BindingName, "rows": &request.RowsJSON, "values": &request.ValuesJSON, "key-value": &request.KeyValue}
			for _, key := range tool.keys {
				value, err := optionalStringArg(args, tool.name, key)
				if err != nil {
					return nil, err
				}
				*targets[key] = value
			}
			client, failure, err := envs.resolve(tool.name, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			return structuredToolResult(tool.run(client, ctx, request)), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
	}
}
