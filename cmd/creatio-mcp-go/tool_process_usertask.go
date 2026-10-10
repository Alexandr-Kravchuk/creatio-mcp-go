package main

// create-user-task and modify-user-task-parameters: clio's CreateUserTaskTool and
// ModifyUserTaskParametersTool (UserTaskTool.cs). Both bind leniently (unknown keys are ignored), turn the
// parameter objects into clio's "code=..;title=..;type=.." definitions and run the command against the
// resolved environment, answering clio's command envelope.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func processTaskShapeError(tool, name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", name, tool)
}

// processTaskBindParameters binds a list of UserTaskParameterArgs (or, with parent, UserTaskParameterItemArgs).
func processTaskBindParameters(tool, name string, value any, withParent bool) ([]creatio.ProcessWriteTaskParameter, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an array. Received an incompatible JSON value.", name, tool)
	}
	parameters := make([]creatio.ProcessWriteTaskParameter, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, processTaskShapeError(tool, name)
		}
		parameter, err := processTaskBindParameter(tool, name, object, withParent)
		if err != nil {
			return nil, err
		}
		parameters = append(parameters, parameter)
	}
	return parameters, nil
}

func processTaskBindParameter(tool, name string, object map[string]any, withParent bool) (creatio.ProcessWriteTaskParameter, error) {
	var parameter creatio.ProcessWriteTaskParameter
	texts := map[string]*string{"code": &parameter.Code, "title": &parameter.Title, "type": &parameter.Type, "lookup": &parameter.Lookup, "direction": &parameter.Direction}
	if withParent {
		texts["parent-parameter-name"] = &parameter.Parent
	}
	flags := map[string]**bool{"required": &parameter.Required, "resulting": &parameter.Resulting, "serializable": &parameter.Serializable,
		"copy-value": &parameter.CopyValue, "lazy-load": &parameter.LazyLoad, "contains-performer-id": &parameter.ContainsPerformerID}
	for key, raw := range object {
		if raw == nil {
			continue
		}
		if target, ok := texts[key]; ok {
			text, isText := raw.(string)
			if !isText {
				return parameter, processTaskShapeError(tool, name)
			}
			*target = text
			continue
		}
		if target, ok := flags[key]; ok {
			flag, isFlag := raw.(bool)
			if !isFlag {
				return parameter, processTaskShapeError(tool, name)
			}
			*target = &flag
			continue
		}
		if key == "items" {
			children, err := processTaskBindParameters(tool, name, raw, false)
			if err != nil {
				return parameter, processTaskShapeError(tool, name)
			}
			parameter.Items = children
		}
	}
	return parameter, nil
}

// processTaskStrings binds a list of strings (IEnumerable<string>).
func processTaskStrings(tool, name string, value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an array. Received an incompatible JSON value.", name, tool)
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			texts = append(texts, "")
			continue
		}
		text, ok := item.(string)
		if !ok {
			return nil, processTaskShapeError(tool, name)
		}
		texts = append(texts, text)
	}
	return texts, nil
}

// processTaskDirections binds set-parameter-directions into clio's "<name>=<direction>" texts.
func processTaskDirections(tool, name string, value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an array. Received an incompatible JSON value.", name, tool)
	}
	updates := make([]string, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, processTaskShapeError(tool, name)
		}
		var parameter, direction string
		for key, target := range map[string]*string{"parameter-name": &parameter, "direction": &direction} {
			if raw, present := object[key]; present && raw != nil {
				text, isText := raw.(string)
				if !isText {
					return nil, processTaskShapeError(tool, name)
				}
				*target = text
			}
		}
		updates = append(updates, parameter+"="+direction)
	}
	return updates, nil
}

func processTaskCulture(culture string) string {
	if strings.TrimSpace(culture) == "" {
		return "en-US"
	}
	return culture
}

func init() {
	registerTool(map[string]any{"name": "create-user-task"},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "create-user-task"
			values, err := processWriteStrings(tool, args, "code", "package-name", "title", "environment-name", "workspace-path", "description", "culture")
			if err != nil {
				return nil, err
			}
			parameters, err := processTaskBindParameters(tool, "parameters", args["parameters"], false)
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(tool, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			var description *string
			if text, ok := args["description"].(string); ok {
				description = &text
			}
			return structuredToolResult(client.CreateUserTask(ctx, creatio.ProcessWriteCreateTaskRequest{
				Code: values["code"], Package: values["package-name"], Title: values["title"], Description: description,
				Culture: processTaskCulture(values["culture"]), WorkspacePath: values["workspace-path"],
				Parameters: creatio.ProcessWriteTaskDefinitions(parameters), ParameterItems: creatio.ProcessWriteTaskNestedItems(parameters),
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))

	registerTool(map[string]any{"name": "modify-user-task-parameters"},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "modify-user-task-parameters"
			values, err := processWriteStrings(tool, args, "user-task-name", "environment-name", "workspace-path", "culture")
			if err != nil {
				return nil, err
			}
			added, err := processTaskBindParameters(tool, "add-parameters", args["add-parameters"], false)
			if err != nil {
				return nil, err
			}
			items, err := processTaskBindParameters(tool, "add-parameter-items", args["add-parameter-items"], true)
			if err != nil {
				return nil, err
			}
			removals, err := processTaskStrings(tool, "remove-parameter-names", args["remove-parameter-names"])
			if err != nil {
				return nil, err
			}
			directions, err := processTaskDirections(tool, "set-parameter-directions", args["set-parameter-directions"])
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(tool, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			allItems := append(append([]string{}, creatio.ProcessWriteTaskNestedItems(added)...), creatio.ProcessWriteTaskItems(items)...)
			return structuredToolResult(client.ModifyUserTaskParameters(ctx, creatio.ProcessWriteModifyTaskRequest{
				UserTaskName: values["user-task-name"], Culture: processTaskCulture(values["culture"]), WorkspacePath: values["workspace-path"],
				AddParameters: creatio.ProcessWriteTaskDefinitions(added), AddParameterItems: allItems,
				RemoveParameters: removals, SetDirections: directions,
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
