package main

import (
	"context"
	"errors"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// emailTemplateGUIDText parses the GUID spellings .NET Guid.TryParse accepts and returns the lower-case form.
func emailTemplateGUIDText(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '{' && value[len(value)-1] == '}') || (value[0] == '(' && value[len(value)-1] == ')')) {
		value = value[1 : len(value)-1]
	}
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", false
		}
		value = strings.ReplaceAll(value, "-", "")
	}
	if len(value) != 32 || strings.Trim(strings.ToLower(value), "0123456789abcdef") != "" {
		return "", false
	}
	value = strings.ToLower(value)
	return value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], true
}

func init() {
	registerTool(map[string]any{
		"name": "get-email-template",
		"description": "Reads the content of a Creatio marketing email (BulkEmail) or message template (EmailTemplate) on the target Creatio environment. " +
			"Returns every current Beefree variant (BfEmailTemplate PageJson/PageHtml/AmpHtml) and every legacy variant (Body/Subject/TemplateConfig), " +
			"each with a checksum for guarded updates. Omitting language returns the Beefree variant the email sends by default (is-default), which " +
			"is not necessarily the one with an empty language. Use the returned email-id and checksum with update-email-template; do not author " +
			"legacy TemplateConfig when Beefree content is available.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"email-id"}, "properties": map[string]any{
			"email-id":    map[string]string{"type": "string", "description": "GUID of the BulkEmail marketing email or EmailTemplate message-template host record."},
			"language":    map[string]string{"type": "string", "description": "Optional Beefree language code. Omit or pass an empty string for the default variant. When that variant is absent, returns an exists=false variant with a creation checksum."},
			"language-id": map[string]string{"type": "string", "description": "Optional SysLanguage GUID for EmailTemplateLang. When absent, returns an exists=false legacy variant with a creation checksum."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		// email-id is a required member of clio's argument record, so its binder refuses the whole object.
		if _, present := args["email-id"]; !present {
			return nil, errors.New("invalid-parameter-type: argument 'args' for MCP tool 'get-email-template' must be an object. Received an incompatible JSON value.")
		}
		values := map[string]string{}
		for _, name := range []string{"email-id", "language", "language-id"} {
			value, err := optionalStringArg(args, "get-email-template", name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		emailID, ok := emailTemplateGUIDText(values["email-id"])
		if !ok {
			return structuredToolResult(creatio.EmailTemplateFailure("email-id must be a GUID.")), nil
		}
		if strings.TrimSpace(values["language-id"]) != "" {
			if _, ok := emailTemplateGUIDText(values["language-id"]); !ok {
				return structuredToolResult(creatio.EmailTemplateFailure("language-id must be a GUID.")), nil
			}
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("get-email-template", args, scopeName)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.EmailTemplateFailure(refusal)), nil
		}
		return structuredToolResult(client.GetEmailTemplate(ctx, emailID, values["language"], values["language-id"])), nil
	})
}
