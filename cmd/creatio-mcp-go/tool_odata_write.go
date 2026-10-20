package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// odataArgKind is how clio's argument record types one member: the binder refuses another JSON kind.
type odataArgKind int

const (
	odataString odataArgKind = iota
	odataBool
	odataJSON
	odataStrings
)

type odataArg struct {
	name string
	kind odataArgKind
}

// odataWriteArgs are clio 8.1.0.142's argument records of the three OData write tools. required are the
// members clio's record declares `required`: a missing one fails the whole binding. clio's own records
// also require environment-name; this server keeps resolving a call without one to its default target.
var odataWriteArgs = map[string]struct {
	args     []odataArg
	required []string
}{
	"odata-create": {[]odataArg{{"entity", odataString}, {"rows", odataJSON}, {"stop-on-error", odataBool}, {"rows-file", odataString},
		{"environment-name", odataString}}, []string{"entity"}},
	"odata-update": {[]odataArg{{"entity", odataString}, {"id", odataString}, {"data", odataJSON}, {"environment-name", odataString},
		{"confirm", odataBool}, {"rows-file", odataString}}, []string{"entity", "id"}},
	"odata-delete": {[]odataArg{{"entity", odataString}, {"id", odataString}, {"ids", odataStrings}, {"cascade", odataBool},
		{"cascade-plan-hash", odataString}, {"dry-run", odataBool}, {"environment-name", odataString}, {"confirm", odataBool}}, []string{"entity"}},
}

// odataBound is one call's arguments after clio's binder: members matched by name (case-insensitively, as
// System.Text.Json is configured), the rest kept as the overflow bag.
type odataBound struct {
	strings map[string]string
	bools   map[string]bool
	json    map[string]json.RawMessage
	lists   map[string][]string
	extra   map[string]json.RawMessage
}

func bindODataArgs(tool string, args map[string]any) (odataBound, error) {
	spec := odataWriteArgs[tool]
	bound := odataBound{strings: map[string]string{}, bools: map[string]bool{}, json: map[string]json.RawMessage{},
		lists: map[string][]string{}, extra: map[string]json.RawMessage{}}
	matched := map[string]string{} // member -> the key that bound it
	for key := range args {
		member := ""
		for _, arg := range spec.args {
			if key == arg.name {
				member = arg.name
				break
			}
		}
		if member == "" {
			for _, arg := range spec.args {
				if strings.EqualFold(key, arg.name) {
					if _, exact := args[arg.name]; !exact {
						member = arg.name
					}
					break
				}
			}
		}
		if member == "" {
			raw, _ := json.Marshal(args[key])
			bound.extra[key] = raw
			continue
		}
		matched[member] = key
	}
	for _, name := range spec.required {
		if _, ok := matched[name]; !ok {
			return bound, dataWriteBindingError(tool)
		}
	}
	for _, arg := range spec.args {
		key, ok := matched[arg.name]
		if !ok {
			continue
		}
		value := args[key]
		switch arg.kind {
		case odataString:
			if value == nil {
				continue
			}
			text, isText := value.(string)
			if !isText {
				return bound, dataWriteKindBindingError(tool, arg.name, "a string")
			}
			bound.strings[arg.name] = text
		case odataBool:
			flag, isBool := value.(bool)
			if !isBool {
				return bound, dataWriteKindBindingError(tool, arg.name, "a boolean")
			}
			bound.bools[arg.name] = flag
		case odataJSON:
			if value == nil {
				continue // a JSON null binds as an absent JsonElement?
			}
			raw, _ := json.Marshal(value)
			bound.json[arg.name] = raw
		case odataStrings:
			if value == nil {
				continue
			}
			items, isArray := value.([]any)
			if !isArray {
				return bound, dataWriteKindBindingError(tool, arg.name, "an array")
			}
			list := make([]string, 0, len(items))
			for _, item := range items {
				text, isText := item.(string)
				if !isText && item != nil {
					return bound, dataWriteNestedBindingError(tool, arg.name)
				}
				list = append(list, text)
			}
			bound.lists[arg.name] = list
		}
	}
	return bound, nil
}

func init() {
	for _, name := range []string{"odata-create", "odata-update", "odata-delete"} {
		registerTool(map[string]any{"name": name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			input, err := bindODataArgs(name, args)
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(name, args, scopeName)
			if err != nil {
				return nil, err
			}
			switch name {
			case "odata-create":
				return structuredToolResult(creatio.ODataCreate(ctx, client, failure, creatio.ODataCreateRequest{Entity: input.strings["entity"],
					Rows: input.json["rows"], RowsFile: input.strings["rows-file"], StopOnError: input.bools["stop-on-error"], Extra: input.extra})), nil
			case "odata-update":
				return structuredToolResult(creatio.ODataUpdate(ctx, client, failure, creatio.ODataKeyedRequest{Entity: input.strings["entity"],
					ID: input.strings["id"], Data: input.json["data"], RowsFile: input.strings["rows-file"], Confirm: input.bools["confirm"],
					Extra: input.extra})), nil
			}
			// odata-delete's record has no overflow bag: clio ignores a key it does not bind.
			return structuredToolResult(creatio.ODataDelete(ctx, client, failure, creatio.ODataDeleteRequest{Entity: input.strings["entity"],
				ID: input.strings["id"], IDs: input.lists["ids"], Cascade: input.bools["cascade"], CascadePlanHash: input.strings["cascade-plan-hash"],
				DryRun: input.bools["dry-run"], Confirm: input.bools["confirm"]})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: name != "odata-create", Idempotent: false, OpenWorld: false}))
	}
}
