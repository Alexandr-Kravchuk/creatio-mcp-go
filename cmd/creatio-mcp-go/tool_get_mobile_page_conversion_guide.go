package main

// get-mobile-page-conversion-guide: clio 8.1.0.142's MobilePageConversionGuideTool (internal/mobileguide, the stand
// reads in internal/creatio/mobileguide_bridge.go). Read-only and idempotent; it writes nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/mobileguide"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mobileGuideTool = "get-mobile-page-conversion-guide"

var mobileGuideMembers = []string{"schema-name", "target-schema-name", "version", "environment-name", "uri", "login", "password"}

func init() {
	registerTool(map[string]any{"name": mobileGuideTool}, invokeMobileGuide,
		withAnnotations(toolAnnotations{ReadOnly: true, Idempotent: true}))
}

// mobileGuideBind is clio's binder with the MCP serializer's web defaults: a member name matches ignoring case
// (the exact spelling wins over another, else the last in sorted order); any other key is ignored.
func mobileGuideBind(args map[string]any) map[string]any {
	bound := map[string]any{}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	exact := map[string]bool{}
	for _, key := range keys {
		for _, member := range mobileGuideMembers {
			if strings.EqualFold(key, member) && !exact[member] {
				bound[member] = args[key]
				exact[member] = key == member
			}
		}
	}
	return bound
}

func invokeMobileGuide(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	// clio's tool takes one required record argument; a call without it fails the binding. Through clio-run an
	// absent "args" arrives as nil; a direct call without the wrapper cannot be told from an empty record.
	if args == nil {
		return nil, errors.New("The arguments dictionary is missing a value for the required parameter 'args'. (Parameter 'arguments')")
	}
	bound := mobileGuideBind(args)
	bind := t21aBinder{tool: mobileGuideTool, args: bound}
	var input mobileguide.Args
	var err error
	for _, member := range []struct {
		name   string
		target **string
	}{{"schema-name", &input.SchemaName}, {"target-schema-name", &input.TargetSchemaName}, {"version", &input.Version},
		{"environment-name", &input.EnvironmentName}, {"uri", &input.URI}} {
		if *member.target, err = bind.str(member.name); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"login", "password"} {
		if _, err := bind.str(name); err != nil {
			return nil, err
		}
	}
	connection := map[string]any{}
	for _, name := range []string{"environment-name", "uri", "login", "password"} {
		if value, ok := bound[name]; ok {
			connection[name] = value
		}
	}
	// clio's ToolCommandResolver needs an environment name or an explicit URI; it does not fall back to the
	// active environment.
	var client *creatio.Client
	var failure error
	if (input.EnvironmentName == nil || strings.TrimSpace(*input.EnvironmentName) == "") && (input.URI == nil || strings.TrimSpace(*input.URI) == "") {
		failure = &environmentError{message: missingTargetMessage}
	} else if client, failure, err = envs.resolve(mobileGuideTool, connection, scopeDirect); err != nil && failure == nil {
		failure = err
	}
	response, err := mobileguide.Run(ctx, creatio.NewMobileGuideStand(client, failure), input, creatio.MobileGuideThreePartVersion)
	if err != nil {
		return nil, err
	}
	text, err := creatio.ClioMcpResultText(response)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: json.RawMessage(text)}, nil
}
