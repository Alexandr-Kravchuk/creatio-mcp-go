package main

// Business-rule write tools: create-, update- and delete-entity-business-rules and their page variants
// (clio's BusinessRuleTool.cs). clio binds them leniently: unknown keys are ignored, a value of the wrong JSON
// type is an invalid-parameter-type refusal. Entity tools check only their collection argument before the
// environment is resolved; page tools first name every missing field (environment included) in one message.
// A failure inside the service is one failed item per rule on create and a request-level error on update
// and delete.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ruleWriteToolArgs struct {
	environment string
	pkg         string
	schema      string
	rules       []*creatio.RuleWriteRule
	names       []string
}

func ruleWriteStringArg(args map[string]any, tool, name string) (string, error) {
	return optionalStringArg(args, tool, name)
}

func ruleWriteBindArgs(tool string, args map[string]any, page, names bool) (ruleWriteToolArgs, error) {
	var bound ruleWriteToolArgs
	var err error
	if bound.environment, err = ruleWriteStringArg(args, tool, "environment-name"); err != nil {
		return bound, err
	}
	if bound.pkg, err = ruleWriteStringArg(args, tool, "package-name"); err != nil {
		return bound, err
	}
	if page {
		canonical, err := ruleWriteStringArg(args, tool, "page-schema-name")
		if err != nil {
			return bound, err
		}
		alias, err := ruleWriteStringArg(args, tool, "schema-name")
		if err != nil {
			return bound, err
		}
		bound.schema = canonical
		if strings.TrimSpace(canonical) == "" {
			bound.schema = alias
		}
	} else if bound.schema, err = ruleWriteStringArg(args, tool, "entity-schema-name"); err != nil {
		return bound, err
	}
	if names {
		raw, ok := args["rule-names"]
		if ok && raw != nil {
			items, isArray := raw.([]any)
			if !isArray {
				return bound, fmt.Errorf("invalid-parameter-type: argument 'rule-names' for MCP tool '%s' must be an array. Received an incompatible JSON value.", tool)
			}
			bound.names = make([]string, 0, len(items))
			for _, item := range items {
				if item == nil {
					bound.names = append(bound.names, "")
					continue
				}
				text, isString := item.(string)
				if !isString {
					return bound, fmt.Errorf("invalid-parameter-type: argument 'rule-names' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", tool)
				}
				bound.names = append(bound.names, text)
			}
		}
		return bound, nil
	}
	bound.rules, err = creatio.RuleWriteParseRules(tool, args["rules"], page)
	return bound, err
}

// ruleWritePageRequestError is BusinessRuleBatchValidation.MissingRequestFieldsError.
func ruleWritePageRequestError(bound ruleWriteToolArgs, collection string, count int) string {
	missing := []string{}
	if strings.TrimSpace(bound.environment) == "" {
		missing = append(missing, "environment-name")
	}
	if strings.TrimSpace(bound.pkg) == "" {
		missing = append(missing, "package-name")
	}
	if strings.TrimSpace(bound.schema) == "" {
		missing = append(missing, "page-schema-name")
	}
	noun := "rule name."
	if collection == "rules" {
		noun = "rule."
	}
	empty := ""
	if count <= 0 {
		empty = collection + " is required and must contain at least one " + noun
	}
	message := ""
	switch len(missing) {
	case 0:
		return empty
	case 1:
		message = missing[0] + " is required."
	default:
		message = strings.Join(missing, ", ") + " are required."
	}
	if empty != "" {
		message += " " + empty
	}
	return message
}

func init() {
	type operation struct {
		verb       string
		idempotent bool
	}
	for _, op := range []operation{{"create", false}, {"update", true}, {"delete", true}} {
		for _, page := range []bool{false, true} {
			op, page := op, page
			name := op.verb + "-entity-business-rules"
			if page {
				name = op.verb + "-page-business-rules"
			}
			registerTool(map[string]any{"name": name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
				deleting := op.verb == "delete"
				bound, err := ruleWriteBindArgs(name, args, page, deleting)
				if err != nil {
					return nil, err
				}
				collection, count := "rules", len(bound.rules)
				if deleting {
					collection, count = "rule-names", len(bound.names)
				}
				if page {
					if message := ruleWritePageRequestError(bound, collection, count); message != "" {
						return structuredToolResult(creatio.RuleWriteRequestError(message)), nil
					}
				} else if count == 0 {
					noun := "rule."
					if deleting {
						noun = "rule name."
					}
					return structuredToolResult(creatio.RuleWriteRequestError(collection + " is required and must contain at least one " + noun)), nil
				}
				client, failure, err := envs.resolve(name, args, scopeName)
				if err != nil {
					return nil, err
				}
				if failure != nil {
					return resolverFailureEnvelope(failure), nil
				}
				request := creatio.RuleWriteBatchRequest{PackageName: bound.pkg, SchemaName: bound.schema, Rules: bound.rules}
				switch op.verb {
				case "create":
					return structuredToolResult(client.CreateBusinessRules(ctx, page, request)), nil
				case "update":
					return structuredToolResult(client.UpdateBusinessRules(ctx, page, request)), nil
				}
				return structuredToolResult(client.DeleteBusinessRules(ctx, page, creatio.BusinessRulesReadRequest{PackageName: bound.pkg, SchemaName: bound.schema}, bound.names)), nil
			}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: op.idempotent, OpenWorld: false}))
		}
	}
}
