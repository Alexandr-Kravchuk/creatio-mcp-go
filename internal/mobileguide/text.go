package mobileguide

import "encoding/json"

// AnswerText writes a value as clio's MCP server writes a tool's answer: System.Text.Json with clio's relaxed
// result encoder, members in declaration order, nulls left out (the omitzero tags), and every number kept as
// the engine produced it (System.Text.Json copies a parsed number's text through).
func AnswerText(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	node, err := parseSTJ(data)
	if err != nil {
		return "", err
	}
	return node.toJSON(styleRelaxed), nil
}

func guideText(guide *Guide) (string, error) { return AnswerText(guide) }
