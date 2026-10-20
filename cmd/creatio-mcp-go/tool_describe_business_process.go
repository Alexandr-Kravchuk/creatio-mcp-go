package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// processDescribeKnownArgs is clio's accepted list, in the order its unknown-argument hint names it.
var processDescribeKnownArgs = []string{"environment-name", "process-name", "process-uid", "process-caption", "culture", "mode", "element"}

// processDescribeSpecificAliases are clio 8.1.0.142's value-independent synonyms of the process identity
// (ProcessToolArgumentAliases, ENG-103025), keyed lower-case without '-' and '_'.
var processDescribeSpecificAliases = map[string]string{
	"processname": "process-name", "processcode": "process-name", "schemaname": "process-name", "code": "process-name",
	"processuid": "process-uid", "uid": "process-uid", "processid": "process-uid", "schemauid": "process-uid", "id": "process-uid",
	"processcaption": "process-caption", "processtitle": "process-caption", "title": "process-caption", "caption": "process-caption",
	"displayname": "process-caption",
}

var (
	processDescribeGUIDPattern       = regexp.MustCompile(`^\{?[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}?$`)
	processDescribeIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func processDescribeNormalizeKey(key string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
}

// processDescribeResolveAliases is ProcessToolArgumentAliases.ResolveDescribe: an unambiguous synonym of the
// process identity is read as its canonical argument and named in a warning; a synonym beside its canonical
// argument, or two synonyms of one, is refused. It returns the arguments with the synonyms applied.
// Go maps keep no request order, so warnings follow the sorted keys where clio follows the request.
func processDescribeResolveAliases(args map[string]any) (map[string]any, []string, string) {
	known := map[string]bool{}
	for _, name := range processDescribeKnownArgs {
		known[name] = true
	}
	type use struct {
		key, value string
		byShape    bool
	}
	found := map[string][]use{}
	targets := []string{}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if known[key] {
			continue
		}
		normalized := processDescribeNormalizeKey(key)
		byShape := normalized == "name" || normalized == "process"
		target := ""
		if !byShape {
			if target = processDescribeSpecificAliases[normalized]; target == "" {
				continue
			}
		}
		text, isText := args[key].(string)
		if !isText || strings.TrimSpace(text) == "" {
			continue // left for the ordinary unknown-argument refusal
		}
		text = strings.TrimSpace(text)
		if byShape {
			switch {
			case processDescribeGUIDPattern.MatchString(text):
				target = "process-uid"
			case processDescribeIdentifierPattern.MatchString(text):
				target = "process-name"
			default:
				target = "process-caption"
			}
		}
		if _, seen := found[target]; !seen {
			targets = append(targets, target)
		}
		found[target] = append(found[target], use{key, text, byShape})
	}
	if len(targets) == 0 {
		return args, nil, ""
	}
	for _, target := range targets {
		list := found[target]
		canonical, _ := args[target].(string)
		if strings.TrimSpace(canonical) != "" || len(list) > 1 {
			shown := make([]string, len(list))
			for i, item := range list {
				shown[i] = "'" + describeCallerKey(item.key) + "'"
			}
			return args, nil, fmt.Sprintf("Ambiguous process identity: %s and '%s' all name the process. Send the identity once, as '%s'. Nothing was read.",
				strings.Join(shown, " and "), target, target)
		}
	}
	resolved := make(map[string]any, len(args))
	for key, value := range args {
		resolved[key] = value
	}
	warnings := []string{}
	for _, target := range targets {
		item := found[target][0]
		delete(resolved, item.key)
		resolved[target] = item.value
		if item.byShape {
			warnings = append(warnings, fmt.Sprintf("Argument '%s' was read as '%s' because of its value; send 'process-name', 'process-uid' or "+
				"'process-caption' explicitly in future calls.", describeCallerKey(item.key), target))
		} else {
			warnings = append(warnings, fmt.Sprintf("Argument '%s' was accepted as '%s'; use '%s' in future calls.", describeCallerKey(item.key), target, target))
		}
	}
	return resolved, warnings, ""
}

// withProcessWarnings is ProcessToolArgumentAliases.WithWarnings: the warnings ahead of the command's own output.
func withProcessWarnings(result creatio.CommandResult, warnings []string) creatio.CommandResult {
	if len(warnings) == 0 {
		return result
	}
	messages := make([]creatio.LogMessage, 0, len(warnings)+len(result.Messages))
	for _, warning := range warnings {
		messages = append(messages, creatio.LogMessage{MessageType: "Warning", Value: warning})
	}
	result.Messages = append(messages, result.Messages...)
	return result
}

func init() {
	registerTool(map[string]any{
		"name": "describe-business-process",
		"description": "Reads an existing Creatio process from the target Creatio environment and returns a STRUCTURED graph: elements (runtime type, " +
			"user-task schema, per-element configuration blocks, diagram placement), flows (name, source, target, kind, label, condition, geometry), " +
			"process parameters, usings and methods, plus the process's VERSION standing read from the process library (version, isActiveVersion, " +
			"activeVersionName/activeVersionSchemaUId, versions[], versionReadWarning). isActiveVersion false means the graph is NOT the one that runs: " +
			"re-describe by activeVersionSchemaUId. Returns clio's command envelope { exit-code, execution-log-messages } with the graph as one compact " +
			"JSON Info line. Identify the process by exactly one of process-name / process-uid / process-caption; a caption resolves to the ACTIVE " +
			"version of its family. Requires the CrtProcessBuilder package (ProcessDesignService) on the target environment.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"process-name":    map[string]string{"type": "string", "description": "Process code (schema Name), e.g. UsrProcess_493d4c9. Provide exactly one identity."},
			"process-uid":     map[string]string{"type": "string", "description": "Process UId (GUID). Provide exactly one identity."},
			"process-caption": map[string]string{"type": "string", "description": "Process caption (display name). Provide exactly one identity."},
			"culture":         map[string]string{"type": "string", "description": "Optional culture used to resolve localized captions (default en-US)."},
			"mode":            map[string]string{"type": "string", "description": "summary | full | versions | element (+element=). Omit: full if the reply fits 18000 B, else summary."},
			"element":         map[string]string{"type": "string", "description": "Element name: returns that one element in full with its flows."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "describe-business-process"
		// clio 8.1.0.142 reads identity synonyms first: an ambiguous one is refused before anything is read.
		args, warnings, refusal := processDescribeResolveAliases(args)
		if refusal != "" {
			return structuredToolResult(creatio.NewCommandResult(1, "Error", refusal)), nil
		}
		request := creatio.ProcessDescribeRequest{}
		for name, target := range map[string]*string{"process-name": &request.ProcessName, "process-uid": &request.ProcessUID,
			"process-caption": &request.ProcessCaption, "mode": &request.Mode, "element": &request.Element} {
			value, err := optionalStringArg(args, tool, name)
			if err != nil {
				return nil, err
			}
			*target = value
		}
		culture, err := optionalStringArg(args, tool, "culture")
		if err != nil {
			return nil, err
		}
		// clio defaults only an absent or null culture; an explicit empty one is sent as none.
		if _, ok := args["culture"].(string); ok {
			request.Culture = &culture
		}
		// clio refuses unknown keys inside its command envelope, before the package gate.
		if refusal := unknownArgumentError(args, processDescribeKnownArgs...); refusal != "" {
			// Refused before the command runs, so clio adds no synonym warning to it.
			return structuredToolResult(creatio.NewCommandResult(1, "Error", refusal)), nil
		}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			if len(warnings) == 0 {
				return resolverFailureEnvelope(failure), nil
			}
			// clio resolves inside the command it wraps with the synonym warnings, so they precede this refusal too.
			exitCode := 1
			var target *environmentError
			if errors.As(failure, &target) {
				exitCode = target.exitCode()
			}
			return structuredToolResult(withProcessWarnings(creatio.NewCommandResult(exitCode, "Error", resolverErrorText(failure)), warnings)), nil
		}
		return structuredToolResult(withProcessWarnings(client.DescribeBusinessProcess(ctx, request), warnings)), nil
	})
}
