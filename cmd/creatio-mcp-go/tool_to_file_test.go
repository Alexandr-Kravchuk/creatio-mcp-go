package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toFileFixture is a canned Creatio for the five *-to-file twins and odata-read.
func toFileFixture(t *testing.T) *mcp.ClientSession {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("CLIO_HOME", filepath.Join(t.TempDir(), "clio-home"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		if r.Body != nil {
			raw := make([]byte, 1<<16)
			n, _ := r.Body.Read(raw)
			body = string(raw[:n])
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login"):
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "y"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case strings.HasSuffix(r.URL.Path, "/odata/Contact"):
			_, _ = w.Write([]byte(`{"@odata.context":"http://h/odata/$metadata#Contact","value":[{"Id":"1","Name":"Ada"}]}`))
		case strings.HasSuffix(r.URL.Path, "/SelectQuery") && strings.Contains(body, "EntitySchemaManager"):
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"UId":"e0000000-0000-0000-0000-000000000001","ExtendParent":false}]}`))
		case strings.HasSuffix(r.URL.Path, "/SelectQuery") && strings.Contains(body, "EsqRows"):
			_, _ = w.Write([]byte(`{"rows": [{"Id":"1"}],"success":true}`))
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	return connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil))
}

func clioRunText(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run",
		Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	// The text is escaped as System.Text.Json escapes it; undo the ASCII escapes for readable assertions.
	text := result.Content[0].(*mcp.TextContent).Text
	for _, plain := range []string{"'", ">", "<", "&", "+", "`"} {
		text = strings.ReplaceAll(text, `\`+fmt.Sprintf("u%04X", plain[0]), plain)
	}
	return text
}

func TestToFileTwinsWriteAndRefuseAsClio142(t *testing.T) {
	session := toFileFixture(t)
	// Not read-only: a direct call by raw name is refused with clio's confirmation-required answer.
	direct, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "odata-read-to-file",
		Arguments: map[string]any{"entity": "Contact", "output-file": "x.json"}})
	if err != nil || !direct.IsError || direct.StructuredContent.(map[string]any)["code"] != "confirmation-required" {
		t.Fatalf("direct call = %#v, %v", direct, err)
	}
	// Measured clio 8.1.0.142 answers (t21b validation-cases.json).
	refusals := []struct{ tool, want string }{
		{"execute-esq-to-file", `invalid-parameter-type: argument 'args' for MCP tool 'execute-esq-to-file' must be an object.`},
		{"odata-read-to-file", `invalid-parameter-type: argument 'args' for MCP tool 'odata-read-to-file' must be an object.`},
		{"get-component-info-to-file", `invalid-parameter-type: argument 'args' for MCP tool 'get-component-info-to-file' must be an object.`},
		{"get-request-info-to-file", `invalid-parameter-type: argument 'args' for MCP tool 'get-request-info-to-file' must be an object.`},
		{"list-entity-client-schemas-to-file", `"error":"output-file is required. Use list-entity-client-schemas when the list should be returned inline."`},
	}
	for _, c := range refusals {
		args := map[string]map[string]any{
			"execute-esq-to-file":                {"query": map[string]any{"rootSchemaName": "Contact"}},
			"odata-read-to-file":                 {"entity": "Contact"},
			"get-component-info-to-file":         {"version": "8.3.3"},
			"get-request-info-to-file":           {"version": "8.3.3"},
			"list-entity-client-schemas-to-file": {"entity-name": "Contact"},
		}[c.tool]
		if text := clioRunText(t, session, c.tool, args); !strings.Contains(text, c.want) {
			t.Fatalf("%s without output-file: %s", c.tool, text)
		}
		args["output-file"] = "/etc/t21b-out.json"
		if text := clioRunText(t, session, c.tool, args); !strings.Contains(text, "resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.") ||
			strings.Contains(text, "t21b-out") {
			t.Fatalf("%s outside the workspace: %s", c.tool, text)
		}
	}
	if text := clioRunText(t, session, "execute-esq-to-file", map[string]any{"query": map[string]any{}, "output-file": 5}); !strings.Contains(text,
		"invalid-parameter-type: argument 'output-file' for MCP tool 'execute-esq-to-file' must be a string.") {
		t.Fatalf("output-file kind: %s", text)
	}

	// execute-esq-to-file: the rows array as it arrived; the answer drops rows for output-file.
	text := clioRunText(t, session, "execute-esq-to-file", map[string]any{"query": map[string]any{"rootSchemaName": "EsqRows"}, "output-file": "esq/rows.json"})
	var esq map[string]any
	_ = json.Unmarshal([]byte(text), &esq)
	if esq["success"] != true || esq["count"] != float64(1) || esq["rows"] != nil || !strings.HasSuffix(esq["output-file"].(string), filepath.Join("esq", "rows.json")) {
		t.Fatalf("esq answer = %s", text)
	}
	if content, _ := os.ReadFile(filepath.Join("esq", "rows.json")); string(content) != `[{"Id":"1"}]` {
		t.Fatalf("esq file = %q", content)
	}
	if text := clioRunText(t, session, "execute-esq-to-file", map[string]any{"query": map[string]any{"rootSchemaName": "EsqRows"}, "output-file": "esq/rows.json"}); !strings.Contains(text,
		"already exists; refusing to overwrite it. Choose a different path or remove the existing file. Use execute-esq when the rows should be returned inline.") {
		t.Fatalf("esq overwrite: %s", text)
	}

	// odata-read-to-file: the response bytes unchanged, a row/column summary instead of value.
	text = clioRunText(t, session, "odata-read-to-file", map[string]any{"entity": "Contact", "outputFile": "x.json", "output-file": "od/contacts.json"})
	if !strings.Contains(text, `"error":"Rename: 'outputFile' -> 'output-file'.","error-code":"argument"`) {
		t.Fatalf("odata alias: %s", text)
	}
	text = clioRunText(t, session, "odata-read-to-file", map[string]any{"entity": "Contact", "output-file": "od/contacts.json"})
	var od map[string]any
	_ = json.Unmarshal([]byte(text), &od)
	if od["success"] != true || od["row-count"] != float64(1) || od["value"] != nil || od["correlation-id"] == nil ||
		!strings.Contains(text, `"column-sizes":{"Id":3,"Name":5}`) {
		t.Fatalf("odata answer = %s", text)
	}
	if content, _ := os.ReadFile(filepath.Join("od", "contacts.json")); !strings.HasPrefix(string(content), `{"@odata.context"`) {
		t.Fatalf("odata file = %q", content)
	}

	// list-entity-client-schemas-to-file: the inline answer in the file, counts in the answer.
	text = clioRunText(t, session, "list-entity-client-schemas-to-file", map[string]any{"entity-name": "Lecs", "output-file": "lecs.json"})
	if !strings.Contains(text, `"sections":{"total":0,"classic":0,"freedom":0,"unknown":0}`) || !strings.Contains(text, `"entityUId":"e0000000-0000-0000-0000-000000000001"`) {
		t.Fatalf("lecs answer = %s", text)
	}
	content, _ := os.ReadFile("lecs.json")
	if !strings.HasPrefix(string(content), `{"success":true,"entity":"Lecs","entityUId":"e0000000-0000-0000-0000-000000000001","sections":[],"editPages":[],"note":"No SysModule sections`) {
		t.Fatalf("lecs file = %s", content)
	}
}

func TestODataReadServesClio142Arguments(t *testing.T) {
	session := toFileFixture(t)
	for _, call := range callPaths("odata-read", map[string]any{"entity": "Contact", "order-by": "Name desc", "select": "Id,Name",
		"filters": map[string]any{"all": []any{map[string]any{"field": "Name", "value": "Ada"}}}, "top": 5, "count": false}) {
		result, err := session.CallTool(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, `"success":true,"count":1,"value":[{"Id":"1","Name":"Ada"}]`) || !strings.Contains(text, `"correlation-id"`) {
			t.Fatalf("odata-read = %s", text)
		}
	}
	text := clioRunText(t, session, "odata-read", map[string]any{"entity": "Contact", "orderBy": "Name", "limit": 3})
	if !strings.Contains(text, `"error":"Rename: 'limit' -> 'top', 'orderBy' -> 'order-by'.","error-code":"argument"`) {
		t.Fatalf("legacy names = %s", text)
	}
	text = clioRunText(t, session, "odata-read", map[string]any{"entity": "Contact", "filters": map[string]any{"all": []any{map[string]any{"column": "Name"}}}})
	if !strings.Contains(text, "invalid-parameter-type: argument 'filters' for MCP tool 'odata-read' contains a value that does not match the documented shape.") {
		t.Fatalf("condition without field = %s", text)
	}
}
