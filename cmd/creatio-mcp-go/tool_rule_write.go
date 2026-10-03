package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	for _, page := range []bool{false, true} {
		page := page
		name := "delete-entity-business-rules"
		if page {
			name = "delete-page-business-rules"
		}
		registerTool(map[string]any{"name": name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			var input struct {
				Package string   `json:"package-name"`
				Entity  string   `json:"entity-schema-name"`
				Page    string   `json:"page-schema-name"`
				Alias   string   `json:"schema-name"`
				Names   []string `json:"rule-names"`
			}
			if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
				return nil, fmt.Errorf("decode %s arguments: %w", name, err)
			}
			schema := input.Entity
			if page {
				schema = input.Page
				if strings.TrimSpace(schema) == "" {
					schema = input.Alias
				}
			}
			if page {
				missing := []string{}
				if value, _ := args["environment-name"].(string); strings.TrimSpace(value) == "" {
					missing = append(missing, "environment-name")
				}
				if strings.TrimSpace(input.Package) == "" {
					missing = append(missing, "package-name")
				}
				if strings.TrimSpace(schema) == "" {
					missing = append(missing, "page-schema-name")
				}
				message := ""
				if len(missing) == 1 {
					message = missing[0] + " is required."
				} else if len(missing) > 1 {
					message = strings.Join(missing, ", ") + " are required."
				}
				if len(input.Names) == 0 {
					if message != "" {
						message += " "
					}
					message += "rule-names is required and must contain at least one rule name."
				}
				if message != "" {
					return structuredToolResult(creatio.RuleWriteRequestError(message)), nil
				}
			}
			if len(input.Names) == 0 {
				return structuredToolResult(creatio.RuleWriteRequestError("rule-names is required and must contain at least one rule name.")), nil
			}
			client, failure, err := envs.resolve(name, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			return structuredToolResult(client.DeleteBusinessRules(ctx, page, creatio.BusinessRulesReadRequest{PackageName: input.Package, SchemaName: schema}, input.Names)), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
	}
}
