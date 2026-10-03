package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	for _, name := range []string{"odata-create", "odata-update", "odata-delete"} {
		registerTool(map[string]any{"name": name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			required := []string{"entity"}
			if name == "odata-create" {
				if _, fromFile := args["rows-file"]; !fromFile {
					required = append(required, "rows")
				}
			} else {
				required = append(required, "id")
			}
			for _, key := range required {
				if _, ok := args[key]; !ok {
					return nil, fmt.Errorf("invalid-parameter-type: argument 'args' for MCP tool '%s' must be an object. Received an incompatible JSON value.", name)
				}
			}
			var input struct {
				Entity      string          `json:"entity"`
				ID          string          `json:"id"`
				Rows        json.RawMessage `json:"rows"`
				Data        json.RawMessage `json:"data"`
				RowsFile    string          `json:"rows-file"`
				StopOnError bool            `json:"stop-on-error"`
				Confirm     bool            `json:"confirm"`
			}
			if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
				return nil, fmt.Errorf("decode %s arguments: %w", name, err)
			}
			client, failure, err := envs.resolve(name, args, scopeName)
			if err != nil {
				return nil, err
			}
			if name == "odata-create" {
				return structuredToolResult(creatio.ODataCreate(ctx, client, failure, creatio.ODataCreateRequest{Entity: input.Entity, Rows: input.Rows, RowsFile: input.RowsFile, StopOnError: input.StopOnError})), nil
			}
			request := creatio.ODataKeyedRequest{Entity: input.Entity, ID: input.ID, Data: input.Data, RowsFile: input.RowsFile, Confirm: input.Confirm}
			if name == "odata-update" {
				return structuredToolResult(creatio.ODataUpdate(ctx, client, failure, request)), nil
			}
			return structuredToolResult(creatio.ODataDelete(ctx, client, failure, request)), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: name != "odata-create", Idempotent: false, OpenWorld: false}))
	}
}
