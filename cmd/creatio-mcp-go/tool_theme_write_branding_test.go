package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// themeWriteBrandingStand serves one registered environment ("brand-env") from handler and returns an MCP
// session connected to a server that reads it.
func themeWriteBrandingStand(t *testing.T, handler http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	clearCreatioVariables(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "brand-env", map[string]map[string]any{
		"brand-env": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": false},
	})
	return connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "branding", Version: "test"}, nil))
}

func themeWriteBrandingCall(t *testing.T, session *mcp.ClientSession, call *mcp.CallToolParams) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded)
	return decoded, result
}

func TestUploadImageThroughExecutorsAndGate(t *testing.T) {
	var mu sync.Mutex
	stored := map[string][]byte{}
	session := themeWriteBrandingStand(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/0/ImageAPIService/upload":
			body, _ := io.ReadAll(r.Body)
			stored[r.URL.Query().Get("fileId")] = body
			_, _ = w.Write([]byte(`{"success":true}`))
		case strings.HasPrefix(r.URL.Path, "/0/img/entity/hash/SysImage/Data/"):
			_, _ = w.Write(stored[strings.TrimPrefix(r.URL.Path, "/0/img/entity/hash/SysImage/Data/")])
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	image := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(image, []byte("png bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		decoded, result := themeWriteBrandingCall(t, session, &mcp.CallToolParams{Name: executor,
			Arguments: map[string]any{"command": "upload-image", "args": map[string]any{"environment-name": "brand-env", "file": image}}})
		if result.IsError || decoded["success"] != true || decoded["image-id"] == nil || decoded["error"] != nil {
			t.Fatalf("%s: %#v", executor, decoded)
		}
	}
	if len(stored) != 2 {
		t.Fatalf("uploads = %d", len(stored))
	}
	// Not read-only: a direct call is refused before anything is sent.
	_, result := themeWriteBrandingCall(t, session, &mcp.CallToolParams{Name: "upload-image",
		Arguments: map[string]any{"environment-name": "brand-env", "file": image}})
	if !result.IsError || len(stored) != 2 {
		t.Fatalf("direct call was not gated: %#v", result.Content[0])
	}
}

func TestUploadImageArgumentRefusals(t *testing.T) {
	session := themeWriteBrandingStand(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"environmentName": "brand-env", "file": "x.png"}, "Rename: 'environmentName' -> 'environment-name'."},
		{map[string]any{"environment-name": "brand-env", "file": "x.png", "image": "y"}, "Unknown args: 'image'. Valid: environment-name, file."},
		{map[string]any{"file": "x.png"}, "environment-name is required and cannot be empty."},
		{map[string]any{"environment-name": "brand-env", "file": " "}, "file is required and cannot be empty."},
		{map[string]any{"environment-name": "brand-env", "file": "/no/such/logo.png"}, "File not found: '/no/such/logo.png'."},
	}
	for _, c := range cases {
		decoded, _ := themeWriteBrandingCall(t, session, &mcp.CallToolParams{Name: "clio-run",
			Arguments: map[string]any{"command": "upload-image", "args": c.args}})
		if decoded["success"] != false || decoded["error"] != c.want {
			t.Errorf("%v: %#v, want %q", c.args, decoded, c.want)
		}
	}
}
