package creatio

import "encoding/json"

// ClioJSONText renders a value as clio's MCP text content: System.Text.Json with its default encoder, which
// escapes ', +, &, <, >, ` and every non-ASCII character as \uXXXX. It matters where clio redacts the text:
// its path rule reads "d:\u0027..." as a drive path, so a failure carrying "d:'" is redacted only in this form.
func ClioJSONText(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	node, err := parseJNode(encoded)
	if err != nil {
		return "", err
	}
	return string(node.stjJSON()), nil
}
