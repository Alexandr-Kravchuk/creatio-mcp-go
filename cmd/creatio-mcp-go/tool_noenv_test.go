package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A call without environment-name is answered as clio 8.1.0.142 answers it (measured with the tool's own
// arguments otherwise valid, and with the name absent, "" and "  "): some tools check the name themselves, with
// their own text and envelope; some check other arguments first; the rest leave it to the resolver.
func TestCallsWithoutEnvironmentNameAreAnsweredLikeClio(t *testing.T) {
	clearCreatioVariables(t)
	envs := newEnvironments(filepath.Join(t.TempDir(), "appsettings.json"))
	session := connectTestClient(t, newMCPServerWithHiddenTools(envs, defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "noenv-probe", Version: "test"}, nil))

	const required = "environment-name is required and cannot be empty."
	resolver := "[EnvironmentResolutionException] " + missingTargetMessage
	command := func(exitCode int, message string) map[string]any {
		return map[string]any{"exit-code": exitCode, "execution-log-messages": []any{map[string]any{"message-type": "Error", "value": message}}}
	}
	forge := func(code, message string) map[string]any {
		return map[string]any{"success": false, "error": map[string]any{"code": code, "message": message}}
	}
	guid := "5b1f0c2e-1111-4b6a-9c1e-000000000001"
	// blank: the name is sent as "" and "  " as well as left out; the answer is the same for all three except
	// where binds says an absent name fails the binding of an argument record that requires it.
	cases := []struct {
		tool   string
		args   map[string]any
		want   map[string]any // top-level fields of the answer
		raised string         // a failure clio raises: the text it carries
		binds  bool
	}{
		{tool: "list-themes", args: map[string]any{}, want: map[string]any{"success": false, "error": required}},
		{tool: "check-theming-access", args: map[string]any{}, want: map[string]any{"success": false, "error": required}},
		{tool: "get-target-package", args: map[string]any{"package": "Custom"}, want: map[string]any{"success": false, "resolutionFailed": false, "error": required}},
		{tool: "dataforge-status", args: map[string]any{}, want: forge("status_error", "environment-name is required.")},
		{tool: "dataforge-find-tables", args: map[string]any{"query": "contact"}, want: forge("find_tables_error", "environment-name is required.")},
		{tool: "dataforge-find-tables", args: map[string]any{}, want: forge("find_tables_error", "query is required.")},
		{tool: "dataforge-find-lookups", args: map[string]any{"query": ""}, want: forge("find_lookups_error", "query is required.")},
		{tool: "dataforge-get-relations", args: map[string]any{"source-table": "Contact"}, want: forge("relations_error", "target-table is required.")},
		{tool: "dataforge-get-relations", args: map[string]any{}, want: forge("relations_error", "source-table is required.")},
		{tool: "dataforge-get-table-columns", args: map[string]any{"table-name": "Contact"}, want: forge("columns_error", "environment-name is required.")},
		{tool: "dataforge-context", args: map[string]any{}, want: forge("context_error", "environment-name is required.")},
		{tool: "describe-business-process", args: map[string]any{"process-name": "UsrP"}, want: command(1, required)},
		{tool: "create-business-process", args: map[string]any{"descriptor": map[string]any{"name": "UsrP"}},
			want: command(-1, required+" "+creatio.MiniContractCreateBusinessProcess([]string{"environment-name"}))},
		{tool: "modify-business-process", args: map[string]any{"process-name": "UsrP", "operations": []any{}},
			want: command(-1, required+" "+creatio.MiniContractModifyBusinessProcess([]string{"environment-name"}))},
		{tool: "modify-business-process-as-new-version", args: map[string]any{"process-name": "UsrP"}, want: command(-1, required)},
		{tool: "set-active-business-process-version", args: map[string]any{"version-name": "v"}, want: command(-1, required)},
		{tool: "validate-process-graph", args: map[string]any{"nodes": []any{map[string]any{"name": "s", "type": "startEvent"}}},
			want: map[string]any{"success": false, "error": required}},
		{tool: "read-page-business-rules", args: map[string]any{"package-name": "UsrPkg", "page-schema-name": "UsrPage"},
			want: map[string]any{"count": 0, "rules": []any{}, "error": "environment-name is required."}},
		{tool: "read-page-business-rules", args: map[string]any{},
			want: map[string]any{"count": 0, "rules": []any{}, "error": "environment-name, package-name, page-schema-name are required."}},
		{tool: "compile-status", args: map[string]any{}, want: map[string]any{"success": false, "status": "invalid-request", "note": required}},
		{tool: "restart-status", args: map[string]any{}, want: map[string]any{"success": false, "status": "invalid-request", "note": required}},
		{tool: "list-app-sections", args: map[string]any{}, want: map[string]any{"success": false, "error": "application-code is required."}},
		{tool: "get-package-file", args: map[string]any{"file-path": "a"}, want: map[string]any{"success": false, "error": "package-name is required and cannot be empty."}},
		{tool: "get-package-file", args: map[string]any{"package-name": "P"}, want: map[string]any{"success": false, "error": "file-path is required and cannot be empty."}},
		{tool: "list-package-files", args: map[string]any{}, want: map[string]any{"success": false, "error": "package-name is required and cannot be empty."}},
		{tool: "generate-process-model", args: map[string]any{"code": "UsrP"}, want: command(1, resolver)},
		{tool: "get-sequence-context", args: map[string]any{}, raised: "environment-name is required."},
		{tool: "enroll-sequence-participants", args: map[string]any{"sequence-id": guid, "contact-ids": []any{guid}}, raised: "environment-name is required."},
		{tool: "get-email-template", args: map[string]any{"email-id": guid}, want: map[string]any{"success": false, "error": "environment-name is required.", "variants": []any{}}, binds: true},
		{tool: "get-email-template", args: map[string]any{"email-id": "x"}, want: map[string]any{"success": false, "error": "email-id must be a GUID.", "variants": []any{}}, binds: true},
		{tool: "execute-esq", args: map[string]any{"query": map[string]any{"rootSchemaName": "Contact"}}, binds: true,
			want: map[string]any{"success": false, "error": "environment-name is required."}},
		{tool: "odata-create", args: map[string]any{"entity": "Contact", "rows": []any{map[string]any{"Name": "a"}}}, binds: true},
		{tool: "odata-update", args: map[string]any{"entity": "Contact", "id": guid, "data": map[string]any{"Name": "a"}}, binds: true},
		{tool: "odata-delete", args: map[string]any{"entity": "Contact", "id": guid}, binds: true},
		// The resolver's own refusal, whether the name is absent or blank.
		{tool: "install-gate", args: map[string]any{}, want: command(1, resolver)},
		{tool: "install-dashboards-migrator", args: map[string]any{}, want: command(1, resolver)},
		{tool: "compile-creatio", args: map[string]any{}, want: command(1, resolver)},
		{tool: "add-custom-logging", args: map[string]any{}, want: command(1, resolver)},
		{tool: "inspect-access", args: map[string]any{"action": "ip-list"}, want: command(1, resolver)},
		// Arguments clio checks before it resolves the environment.
		{tool: "get-app-info", args: map[string]any{}, want: map[string]any{"success": false, "error": "Provide exactly one identifier: id or code."}},
		{tool: "list-page-templates", args: map[string]any{"schema-type": "desktop"}, want: map[string]any{"success": false, "count": 0, "error": "Unknown schema-type 'desktop'. Use 'web' or 'mobile'."}},
		{tool: "get-process-page-facts", args: map[string]any{}, want: map[string]any{"success": false, "error": "schema-name is required."}},
		{tool: "get-related-page-addon", args: map[string]any{"entity-schema-name": "Contact"}, want: map[string]any{"success": false, "pageCount": 0, "error": "package-name is required."}},
		{tool: "inspect-user", args: map[string]any{"action": "create"}, want: command(1, "This inspection tool accepts only: list, lock-status.")},
		{tool: "create-entity-schema", args: map[string]any{"package-name": "UsrPkg", "schema-name": "UsrA"},
			want: command(1, creatio.MiniContractCreateEntitySchema([]string{"title-localizations"}))},
		{tool: "create-app", args: map[string]any{"code": "UsrApp"},
			want: map[string]any{"success": false, "error": "name is required. " + creatio.MiniContractCreateApp([]string{"name"})}},
	}
	for _, item := range cases {
		for _, name := range []any{nil, "", "  "} {
			args := map[string]any{}
			for key, value := range item.args {
				args[key] = value
			}
			if name != nil {
				args["environment-name"] = name
			}
			for _, call := range callPaths(item.tool, args) {
				result, err := session.CallTool(context.Background(), call)
				if err != nil {
					t.Fatalf("%s %v via %s: %v", item.tool, name, call.Name, err)
				}
				text := result.Content[0].(*mcp.TextContent).Text
				switch {
				case item.binds && name == nil && item.tool != "get-email-template":
					if !result.IsError || !strings.Contains(text, "invalid-parameter-type: argument 'args' for MCP tool '"+item.tool+"' must be an object. Received an incompatible JSON value.") {
						t.Errorf("%s without a name via %s: %s", item.tool, call.Name, text)
					}
				case item.binds && name == nil:
					if !result.IsError || !strings.Contains(text, "invalid-parameter-type: argument 'args' for MCP tool 'get-email-template' must be an object.") {
						t.Errorf("%s without a name via %s: %s", item.tool, call.Name, text)
					}
				case item.raised != "":
					if !result.IsError || !strings.Contains(text, item.raised) {
						t.Errorf("%s %v via %s: %s", item.tool, name, call.Name, text)
					}
				case item.want == nil:
					// the blank-name answer of a call that binds is the tool's own concern, covered by its parity cases
				default:
					var answer map[string]any
					if err := json.Unmarshal([]byte(text), &answer); err != nil {
						t.Errorf("%s %v via %s: %s", item.tool, name, call.Name, text)
						continue
					}
					for field, want := range item.want {
						if canonicalJSON(t, answer[field]) != canonicalJSON(t, want) {
							t.Errorf("%s %v via %s: %s = %s, want %s", item.tool, name, call.Name, field, canonicalJSON(t, answer[field]), canonicalJSON(t, want))
						}
					}
				}
			}
		}
	}
}
