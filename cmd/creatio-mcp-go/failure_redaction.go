package main

// Failure redaction, in the places clio applies it:
//
//   - a failure a tool raises (an error returned from invoke) is reported as "<prefix><redacted message>",
//     as clio's McpToolErrorFilter and ClioRunExecutor catch blocks do (clioFailure);
//   - every result called through clio-run / clio-run-destructive, or directly by raw name when clio does not
//     list the tool in tools/list, passes redactFailureContent, clio's ClioRunExecutor.RedactFailureContent
//     backstop: a result that signals failure has its text redacted, a success is never touched. A tool clio
//     lists answers a direct call unchanged, with whatever redaction the tool itself applies.
//
// What a failure is follows clio exactly: isError, a top-level "success": false, or a non-empty top-level
// string "error". clio's command envelope (exit-code + execution-log-messages) carries neither, so its
// messages leave unredacted, also through clio-run; that is measured clio behaviour, kept on purpose.

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// failureFieldNames are the fields clio redacts inside a structured failure (FailureFieldNames).
var failureFieldNames = map[string]bool{
	"error": true, "message": true, "detail": true, "details": true, "errorinfo": true, "exception": true,
	"stacktrace": true, "reason": true, "cause": true,
}

// redactFailureContent redacts a failed result in place. clio's hidden tools answer with text only, and
// clio redacts that text whole, every field included; this server also returns the same JSON as
// structuredContent, so a redacted JSON text replaces the structured value too, keeping the two equal.
func redactFailureContent(result *mcp.CallToolResult) {
	if result == nil {
		return
	}
	structured := genericJSON(result.StructuredContent)
	if !resultSignalsFailure(result, structured) {
		return
	}
	var firstText string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			text.Text = redact.Text(text.Text)
			if firstText == "" {
				firstText = text.Text
			}
		}
	}
	if structured == nil {
		return
	}
	if trimmed := strings.TrimSpace(firstText); strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed)) {
		result.StructuredContent = json.RawMessage(trimmed)
		return
	}
	if redactErrorFields(structured) {
		result.StructuredContent = structured
	}
}

// genericJSON round-trips a structured value into maps and slices; nil when there is none.
func genericJSON(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return nil
	}
	return decoded
}

func resultSignalsFailure(result *mcp.CallToolResult, structured any) bool {
	if result.IsError || payloadSignalsFailure(structured) {
		return true
	}
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok || !strings.HasPrefix(strings.TrimSpace(text.Text), "{") {
			continue
		}
		var decoded any
		if json.Unmarshal([]byte(text.Text), &decoded) == nil && payloadSignalsFailure(decoded) {
			return true
		}
	}
	return false
}

// payloadSignalsFailure is clio's PayloadSignalsFailure: success=false or a non-blank string error, on the
// top level only, keys matched case-insensitively.
func payloadSignalsFailure(payload any) bool {
	object, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range object {
		switch strings.ToLower(key) {
		case "success":
			if success, ok := value.(bool); ok && !success {
				return true
			}
		case "error":
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}

// redactErrorFields redacts every error-named string field anywhere in the value; true when one changed.
func redactErrorFields(node any) bool {
	changed := false
	switch value := node.(type) {
	case map[string]any:
		for key, item := range value {
			if text, ok := item.(string); ok && failureFieldNames[strings.ToLower(key)] && text != "" {
				if redacted := redact.Text(text); redacted != text {
					value[key] = redacted
					changed = true
				}
				continue
			}
			changed = redactErrorFields(item) || changed
		}
	case []any:
		for _, item := range value {
			changed = redactErrorFields(item) || changed
		}
	}
	return changed
}
