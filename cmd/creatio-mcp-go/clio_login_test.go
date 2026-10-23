package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A stand that goes away after the session was established: clio's get-app-info logs in again and fails there,
// clio's find-app reuses its session and gets the call's AggregateException (measured against clio 8.1.0.142).
func TestStandThatWentAwayIsWordedPerClioLoginOfTheTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No keep-alive, so the next request dials the closed port instead of reusing a connection.
		w.Header().Set("Connection", "close")
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: "x", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
	}))
	client, err := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	call := func(name string, args map[string]any) string {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"args": args}})
		if err != nil || len(result.Content) == 0 {
			t.Fatalf("%s: result = %#v, err = %v", name, result, err)
		}
		return result.Content[0].(*mcp.TextContent).Text
	}
	call("find-app", map[string]any{"search-pattern": "customer"})
	server.Close()

	if text := call("get-app-info", map[string]any{"code": "CrtCustomer360App"}); strings.Contains(text, "One or more errors occurred.") ||
		!strings.Contains(text, " ([redacted-uri])") {
		t.Fatalf("get-app-info, whose clio counterpart logs in on every call = %s", text)
	}
	if text := call("find-app", map[string]any{"search-pattern": "customer"}); !strings.Contains(text, `"One or more errors occurred. (`) {
		t.Fatalf("find-app, whose clio counterpart reuses its session = %s", text)
	}
}
