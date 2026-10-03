package main

// clio's MCP prompts are compiled C# formatters (clio/Command/McpServer/Prompts). They are reproduced here
// as data: knowledge_prompt_table.go holds, for every prompt, the text clio renders with a placeholder in
// each argument, plus what changes when an argument is absent, empty or blank. The table is generated from
// clio's live prompts/get answers and the generator checked every captured combination before emitting it.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// knowledgePromptRule says what one argument state does to the template: "value" substitutes the given
// value, "subst" substitutes fixed text, "absent" behaves as if the argument were missing, "region"
// replaces template bytes [start,end) with text, "error" fails the call.
type knowledgePromptRule struct {
	mode       string
	start, end int
	text       string
}

type knowledgePromptArg struct {
	name, description string
	required          bool
	kind              string // string, int or bool
	sentinel          string // the placeholder this argument has in the template
	absent            knowledgePromptRule
	empty             knowledgePromptRule
	space             knowledgePromptRule
}

// knowledgePromptPair replaces both arguments' rules when both are absent (clio's (hasValue, hasFile) switch).
type knowledgePromptPair struct {
	a, b   string
	region knowledgePromptRule
}

type knowledgePrompt struct {
	name, description string
	template          string
	alwaysFails       bool
	args              []knowledgePromptArg
	pairs             []knowledgePromptPair
}

// knowledgePrompts is filled by knowledge_prompt_table.go in clio's listing order.
var knowledgePrompts []knowledgePrompt

func knowledgePromptList() []*mcp.Prompt {
	prompts := make([]*mcp.Prompt, 0, len(knowledgePrompts))
	for _, prompt := range knowledgePrompts {
		var arguments []*mcp.PromptArgument
		for _, arg := range prompt.args {
			arguments = append(arguments, &mcp.PromptArgument{Name: arg.name, Description: arg.description, Required: arg.required})
		}
		prompts = append(prompts, &mcp.Prompt{Name: prompt.name, Description: prompt.description, Arguments: arguments})
	}
	return prompts
}

// knowledgePromptListResult serializes prompts/list as clio does: every argument carries "required", false
// included, and a prompt without arguments carries "arguments": []. The SDK type omits both; embedding it
// keeps the value an mcp.Result while MarshalJSON writes clio's shape.
type knowledgePromptListResult struct {
	*mcp.ListPromptsResult
}

func (r knowledgePromptListResult) MarshalJSON() ([]byte, error) {
	type argument struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Required    bool   `json:"required"`
	}
	type prompt struct {
		Name        string     `json:"name"`
		Description string     `json:"description,omitempty"`
		Arguments   []argument `json:"arguments"`
	}
	prompts := make([]prompt, 0, len(r.Prompts))
	for _, p := range r.Prompts {
		arguments := []argument{}
		for _, a := range p.Arguments {
			arguments = append(arguments, argument{a.Name, a.Description, a.Required})
		}
		prompts = append(prompts, prompt{p.Name, p.Description, arguments})
	}
	return json.Marshal(struct {
		Prompts []prompt `json:"prompts"`
	}{prompts})
}

// errKnowledgePrompt is clio's answer to any argument its prompt binder cannot use.
var errKnowledgePrompt = fmt.Errorf("prompt arguments rejected")

func (p knowledgePrompt) render(values map[string]string) (string, error) {
	if p.alwaysFails {
		return "", errKnowledgePrompt
	}
	type choice struct {
		sentinel string
		rule     knowledgePromptRule
	}
	chosen := map[string]choice{}
	absent := map[string]bool{}
	for _, arg := range p.args {
		value, present := values[arg.name]
		if arg.kind == "bool" {
			// clio binds a bool only from a JSON boolean; prompt arguments are strings, so any value fails.
			if present || arg.required {
				return "", errKnowledgePrompt
			}
			continue
		}
		var rule knowledgePromptRule
		switch {
		case !present:
			if arg.required {
				return "", errKnowledgePrompt
			}
			rule = arg.absent
		case arg.kind == "int":
			number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
			if err != nil {
				return "", errKnowledgePrompt
			}
			rule = knowledgePromptRule{mode: "value", text: strconv.FormatInt(number, 10)}
		case value == "":
			rule = arg.empty
		case strings.TrimFunc(value, unicode.IsSpace) == "":
			rule = arg.space
			if rule.mode == "value" {
				rule.text = value
			}
		default:
			rule = knowledgePromptRule{mode: "value", text: value}
		}
		if rule.mode == "absent" {
			rule = arg.absent
		}
		if rule.mode == "error" {
			return "", errKnowledgePrompt
		}
		if !present || rule == arg.absent {
			absent[arg.name] = true
		}
		chosen[arg.name] = choice{arg.sentinel, rule}
	}
	for _, pair := range p.pairs {
		if absent[pair.a] && absent[pair.b] {
			chosen[pair.a] = choice{chosen[pair.a].sentinel, pair.region}
			chosen[pair.b] = choice{chosen[pair.b].sentinel, knowledgePromptRule{mode: "subst"}}
		}
	}
	var regions []knowledgePromptRule
	var replacements []string
	for _, c := range chosen {
		switch c.rule.mode {
		case "value", "subst":
			replacements = append(replacements, c.sentinel, c.rule.text)
		case "region":
			regions = append(regions, c.rule)
			replacements = append(replacements, c.sentinel, "")
		}
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].start > regions[j].start })
	text := p.template
	for i, region := range regions {
		if i > 0 && region.end > regions[i-1].start {
			return "", errKnowledgePrompt
		}
		text = text[:region.start] + region.text + text[region.end:]
	}
	// One pass, so a value that happens to contain another placeholder stays literal.
	return strings.NewReplacer(replacements...).Replace(text), nil
}

func knowledgeGetPrompt(name string, args map[string]string) (*mcp.GetPromptResult, error) {
	for _, prompt := range knowledgePrompts {
		if prompt.name != name {
			continue
		}
		text, err := prompt.render(args)
		if err != nil {
			// clio's SDK reports every binding failure as this generic internal error.
			return nil, knowledgeWireError(-32603, "An error occurred.")
		}
		return &mcp.GetPromptResult{Description: prompt.description, Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: text}},
		}}, nil
	}
	return nil, knowledgeWireError(-32602, fmt.Sprintf("Unknown prompt: '%s'", name))
}
