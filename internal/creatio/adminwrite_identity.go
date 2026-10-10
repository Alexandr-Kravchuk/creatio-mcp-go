package creatio

// get-identity-assertion, get-identity-public-jwk and regenerate-identity-signing-key: clio master's
// GetIdentityAssertionCommand, GetIdentityPublicJwkCommand and RegenerateIdentitySigningKeyCommand, which are
// RemoteCommands over Creatio's identityAssertion/* endpoints, with IdentityOutput's formatting.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AdminWriteIdentityTool is one of the three identity commands: its verb, endpoint and method.
type AdminWriteIdentityTool struct {
	Name   string
	route  string
	method string
}

var (
	AdminWriteIdentityAssertion  = AdminWriteIdentityTool{Name: "get-identity-assertion", route: "identityAssertion/currentUser", method: http.MethodPost}
	AdminWriteIdentityPublicJwk  = AdminWriteIdentityTool{Name: "get-identity-public-jwk", route: "identityAssertion/publicJwk", method: http.MethodGet}
	AdminWriteIdentityRegenerate = AdminWriteIdentityTool{Name: "regenerate-identity-signing-key",
		route: "identityAssertion/regenerateSigningKey", method: http.MethodPost}
)

// adminWriteIdentityTimeout is the tools' TimeOut of 30 000 ms.
const adminWriteIdentityTimeout = 30 * time.Second

// IdentityCommand runs one identity command as RemoteCommand.Execute does: the request, the command's own
// reading of the answer, then "Done <verb>" when it succeeded.
func (c *Client) IdentityCommand(ctx context.Context, tool AdminWriteIdentityTool, format string) CommandResult {
	jsonFormat := strings.EqualFold(format, "json")
	payload, err := c.adminWriteIdentityRequest(ctx, tool)
	if err != nil {
		return CommandFailure(err.Error())
	}
	result := CommandResult{ExitCode: 0, Messages: []LogMessage{}}
	add := func(messageType, value string) {
		result.Messages = append(result.Messages, LogMessage{MessageType: messageType, Value: value})
	}
	fail := func() { result.ExitCode = 1 }
	response := string(payload)
	switch tool.Name {
	case AdminWriteIdentityAssertion.Name, AdminWriteIdentityPublicJwk.Name:
		if dataWriteBlank(response) {
			fail()
			add("Error", "Empty response received from server.")
			add("Error", "Endpoint: "+c.serviceURL(tool.route))
			break
		}
		if errorCode, description, ok := adminWriteIdentityError(payload); ok {
			fail()
			if jsonFormat {
				add("None", adminWriteIdentityPretty(payload))
			} else {
				add("Error", errorCode+": "+description)
			}
			break
		}
		if jsonFormat {
			add("None", adminWriteIdentityPretty(payload))
			break
		}
		if tool.Name == AdminWriteIdentityPublicJwk.Name {
			root, err := inspectParseJSON(payload)
			if err != nil {
				fail()
				add("Error", "Failed to parse public JWK response: "+err.Error())
				add("None", response)
				break
			}
			add("None", string(root.stjJSON()))
			break
		}
		root, err := inspectParseJSON(payload)
		if err != nil || !root.isObject() {
			fail()
			message := "the response is not a JSON object"
			if err != nil {
				message = err.Error()
			}
			add("Error", "Failed to parse assertion response: "+message)
			add("None", response)
			break
		}
		assertion := adminWriteIdentityField(root, "assertion")
		if assertion == nil || assertion.kind != jkString || dataWriteBlank(assertion.text) {
			fail()
			add("Error", "Response did not contain an assertion token.")
			add("None", response)
			break
		}
		assertionType := ""
		if value := adminWriteIdentityField(root, "assertionType"); value != nil && value.kind == jkString {
			assertionType = value.text
		}
		expiresIn := "0"
		if value := adminWriteIdentityField(root, "expiresIn"); value != nil && value.kind == jkInteger {
			expiresIn = value.text
		}
		expiresAt := time.Time{}
		if value := adminWriteIdentityField(root, "expiresAt"); value != nil && value.kind == jkString {
			expiresAt = adminWriteSTJDateTime(value.text)
		}
		add("Info", fmt.Sprintf("Assertion type: %s; expires in %ss (at %s).", assertionType, expiresIn, expiresAt.Format("2006-01-02 15:04:05Z")))
		add("None", assertion.text)
	case AdminWriteIdentityRegenerate.Name:
		// 204 No Content (an empty body) is success; a body carries an ErrorInfo payload.
		if errorCode, description, ok := adminWriteIdentityError(payload); ok {
			fail()
			if jsonFormat {
				add("None", adminWriteIdentityPretty(payload))
			} else {
				add("Error", errorCode+": "+description)
			}
			break
		}
		if jsonFormat {
			add("None", `{"status":"regenerated"}`)
		} else {
			add("None", "OK")
		}
	}
	if result.ExitCode == 0 {
		add("Info", "Done "+tool.Name)
	}
	return result
}

// adminWriteIdentityRequest is ApplicationClient.ExecutePostRequest (an empty body) or ExecuteGetRequest: the
// body whatever the status.
func (c *Client) adminWriteIdentityRequest(ctx context.Context, tool AdminWriteIdentityTool) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, adminWriteIdentityTimeout)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		var body io.Reader
		if tool.method == http.MethodPost {
			body = bytes.NewReader(nil)
		}
		request, err := http.NewRequestWithContext(requestCtx, tool.method, c.serviceURL(tool.route), body)
		if err != nil {
			return nil, err
		}
		if tool.method == http.MethodPost {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		return nil, err
	}
	return readResponseLimit(response, maxResponseBytes)
}

// adminWriteIdentityError is IdentityOutput.TryParseError: an object with a string "error" property (the
// name compared ignoring case) and an optional string "error_description".
func adminWriteIdentityError(payload []byte) (string, string, bool) {
	if dataWriteBlank(string(payload)) {
		return "", "", false
	}
	root, err := inspectParseJSON(payload)
	if err != nil || !root.isObject() {
		return "", "", false
	}
	errorCode, ok := adminWriteIdentityString(root, "error")
	if !ok {
		return "", "", false
	}
	description, _ := adminWriteIdentityString(root, "error_description")
	return errorCode, description, true
}

// adminWriteIdentityString is TryGetStringProperty: the first property whose name matches ignoring case and
// whose value is a string.
func adminWriteIdentityString(root *jnode, name string) (string, bool) {
	for _, key := range root.keys {
		if value := root.props[key]; strings.EqualFold(key, name) && value.kind == jkString {
			return value.text, true
		}
	}
	return "", false
}

// adminWriteIdentityField is the first property whose name matches ignoring case.
func adminWriteIdentityField(root *jnode, name string) *jnode {
	for _, key := range root.keys {
		if strings.EqualFold(key, name) {
			return root.props[key]
		}
	}
	return nil
}

// adminWriteIdentityPretty is IdentityOutput.Pretty: the document re-written indented, or the text as it
// came when it is not JSON.
func adminWriteIdentityPretty(payload []byte) string {
	if dataWriteBlank(string(payload)) {
		return ""
	}
	root, err := inspectParseJSON(payload)
	if err != nil {
		return string(payload)
	}
	return stjIndented(root)
}

// adminWriteSTJDateTime reads an ISO 8601 date as System.Text.Json reads a DateTime: a Z suffix keeps the UTC
// clock time, an offset converts to local time, no zone keeps the clock time as written.
func adminWriteSTJDateTime(text string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		if strings.HasSuffix(text, "Z") || strings.HasSuffix(text, "z") {
			return parsed.UTC()
		}
		return parsed.In(time.Local)
	}
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
