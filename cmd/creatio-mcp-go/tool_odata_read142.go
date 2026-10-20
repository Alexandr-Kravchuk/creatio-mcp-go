package main

// odata-read and odata-read-to-file as clio 8.1.0.142 serves them (ODataReadTool, ODataReadToFileTool): the
// same argument record (entity, filters, select, expand, order-by, top, skip, count, environment-name; plus
// output-file for the twin), one validator, one request builder and one response classification
// (creatio.ODataReadPrepare / ODataRead142 / ODataReadToFile). Every answer carries a correlation-id.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// odataReadMembers is clio's ODataReadArgs. clio's record also requires environment-name; this server keeps
// resolving a call without one to its default target, as for every other tool.
var odataReadMembers = []clioMember{
	{"entity", clioString, true}, {"select", clioAny, false}, {"expand", clioAny, false}, {"order-by", clioString, false},
	{"top", clioInt, false}, {"skip", clioInt, false}, {"count", clioBool, false}, {"filters", clioAny, false},
	{"environment-name", clioString, false},
}

// bindODataRead binds the call as System.Text.Json binds ODataReadArgs / ODataReadToFileArgs, the nested
// filters record included.
func bindODataRead(tool string, args map[string]any, toFile bool) (creatio.ODataReadArgs, *string, error) {
	members := odataReadMembers
	if toFile {
		members = append(append([]clioMember(nil), odataReadMembers...), clioMember{"output-file", clioString, true})
	}
	bound, err := clioBind(tool, args, members)
	if err != nil {
		return creatio.ODataReadArgs{}, nil, err
	}
	var read creatio.ODataReadArgs
	read.Entity, _ = bound.str("entity")
	read.OrderBy, _ = bound.str("order-by")
	read.OutputFile, _ = bound.str("output-file")
	read.Top, read.Skip = bound.integer("top"), bound.integer("skip")
	read.Count, _ = bound.values["count"].(bool)
	if bound.has("select") && bound.values["select"] != nil {
		read.Select = bound.raw("select")
	}
	if bound.has("expand") && bound.values["expand"] != nil {
		read.Expand = bound.raw("expand")
	}
	read.Extension = bound.extension
	if bound.has("filters") {
		read.FiltersProvided = true
		if value := bound.values["filters"]; value != nil {
			object, ok := value.(map[string]any)
			if !ok {
				return creatio.ODataReadArgs{}, nil, dataWriteKindBindingError(tool, "filters", "an object")
			}
			filters, ok := bindODataFilters(object)
			if !ok {
				return creatio.ODataReadArgs{}, nil, dataWriteNestedBindingError(tool, "filters")
			}
			read.Filters = filters
		}
	}
	var environmentName *string
	if bound.has("environment-name") {
		name, _ := bound.str("environment-name")
		environmentName = &name
	}
	return read, environmentName, nil
}

// clioMatch finds the key binding a member: the exact spelling, else a case-insensitive one.
func clioMatch(object map[string]any, member string) (string, bool) {
	if _, ok := object[member]; ok {
		return member, true
	}
	for key := range object {
		if strings.EqualFold(key, member) {
			return key, true
		}
	}
	return "", false
}

func clioExtension(object map[string]any, bound map[string]bool) map[string]json.RawMessage {
	extension := map[string]json.RawMessage{}
	for key, value := range object {
		if !bound[key] {
			raw, _ := json.Marshal(value)
			extension[key] = raw
		}
	}
	return extension
}

func bindODataFilters(object map[string]any) (*creatio.ODataFilters, bool) {
	filters := &creatio.ODataFilters{}
	bound := map[string]bool{}
	for _, group := range []string{"all", "any"} {
		key, ok := clioMatch(object, group)
		if !ok {
			continue
		}
		bound[key] = true
		value := object[key]
		if value == nil {
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return nil, false
		}
		conditions := make([]*creatio.ODataFilterCondition, 0, len(items))
		for _, item := range items {
			if item == nil {
				conditions = append(conditions, nil)
				continue
			}
			condition, ok := bindODataCondition(item)
			if !ok {
				return nil, false
			}
			conditions = append(conditions, condition)
		}
		if group == "all" {
			filters.All = conditions
		} else {
			filters.Any = conditions
		}
	}
	filters.Extension = clioExtension(object, bound)
	return filters, true
}

func bindODataCondition(item any) (*creatio.ODataFilterCondition, bool) {
	object, ok := item.(map[string]any)
	if !ok {
		return nil, false
	}
	condition := &creatio.ODataFilterCondition{}
	bound := map[string]bool{}
	key, ok := clioMatch(object, "field")
	if !ok {
		return nil, false // a required member
	}
	bound[key] = true
	if value := object[key]; value != nil {
		text, isText := value.(string)
		if !isText {
			return nil, false
		}
		condition.Field = text
	}
	if key, ok := clioMatch(object, "op"); ok {
		bound[key] = true
		if value := object[key]; value != nil {
			text, isText := value.(string)
			if !isText {
				return nil, false
			}
			condition.Op = text
		}
	}
	if key, ok := clioMatch(object, "value"); ok {
		bound[key] = true
		condition.Value, _ = json.Marshal(object[key])
	}
	if key, ok := clioMatch(object, "in"); ok {
		bound[key] = true
		condition.In, _ = json.Marshal(object[key])
	}
	condition.Extension = clioExtension(object, bound)
	return condition, true
}

// odataReadResolve resolves the environment the way both read tools report it: a resolution failure is a
// transport-class answer naming the entity.
func odataReadResolve(envs *environments, tool string, environmentName *string, read creatio.ODataReadArgs) (*creatio.Client, *creatio.ODataReadResponse, error) {
	selector := map[string]any{}
	if environmentName != nil {
		selector["environment-name"] = *environmentName
	}
	client, refusal, err := envs.resolve(tool, selector, scopeName)
	if err != nil {
		return nil, nil, err
	}
	if refusal != nil {
		failure := creatio.ODataReadTransportFailure(refusal.Error(), read)
		return nil, &failure, nil
	}
	return client, nil, nil
}

// odataReadAnswer is ODataReadTool.Read for the inline tool.
func odataReadAnswer(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "odata-read"
	read, environmentName, err := bindODataRead(tool, args, false)
	if err != nil {
		return nil, err
	}
	refusal, selectColumns, expandColumns := creatio.ODataReadPrepare(read, false)
	if refusal != nil {
		return toFileResult(creatio.ODataReadStamp(*refusal)), nil
	}
	client, failure, err := odataReadResolve(envs, tool, environmentName, read)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return toFileResult(creatio.ODataReadStamp(*failure)), nil
	}
	return toFileResult(creatio.ODataReadStamp(client.ODataRead142(ctx, read, selectColumns, expandColumns))), nil
}

func init() {
	registerTool(map[string]any{"name": "odata-read-to-file"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "odata-read-to-file"
		read, environmentName, err := bindODataRead(tool, args, true)
		if err != nil {
			return nil, err
		}
		answer := func(response creatio.ODataReadResponse) (*mcp.CallToolResult, error) {
			return toFileResult(creatio.ODataReadStamp(response)), nil
		}
		refusal, selectColumns, expandColumns := creatio.ODataReadPrepare(read, true)
		if refusal != nil {
			return answer(*refusal)
		}
		if strings.TrimSpace(read.OutputFile) == "" {
			return answer(creatio.ODataReadFailure("output-file is required. Use odata-read when the response should be returned inline.",
				"argument", nil, ""))
		}
		client, failure, err := odataReadResolve(envs, tool, environmentName, read)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return answer(*failure)
		}
		path, pathRefusal := creatio.ToFileResolveOutput(read.OutputFile)
		if pathRefusal != "" {
			return answer(creatio.ODataReadFailure(pathRefusal, "argument", nil, ""))
		}
		return answer(client.ODataReadToFile(ctx, read, selectColumns, expandColumns, path))
	}, withAnnotations(toFileAnnotations))
}
