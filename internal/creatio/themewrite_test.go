package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildThemeDeterministicTemplates(t *testing.T) {
	options := ThemeBuildOptions{Primary: "#0055aa", CSSClassName: "parity-theme", Caption: "Parity", ID: "11111111-1111-4111-8111-111111111111", Version: "10.0"}
	a := BuildTheme(context.Background(), options)
	b := BuildTheme(context.Background(), options)
	if !a.Success || a.CSS != b.CSS || a.Descriptor != b.Descriptor {
		t.Fatalf("build = %#v", a)
	}
	if strings.Contains(a.CSS, "<%") || !strings.Contains(a.CSS, ".parity-theme") || !strings.Contains(a.CSS, "--crt-palette-primary-500: #0055aa") {
		t.Fatalf("unfilled CSS: %.300s", a.CSS)
	}
	var descriptor map[string]string
	if err := json.Unmarshal([]byte(a.Descriptor), &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor["id"] != options.ID || descriptor["caption"] != "Parity" || descriptor["cssClassName"] != options.CSSClassName {
		t.Fatalf("descriptor=%v", descriptor)
	}
	for _, bad := range []ThemeBuildOptions{{Primary: "not-a-color", CSSClassName: "valid"}, {Primary: "#123456", CSSClassName: "../escape"}, {Primary: "#123456", CSSClassName: "valid", Version: "8.0"}, {Primary: "#123456", CSSClassName: "valid", Version: "10.0", EnvironmentName: "test"}} {
		if got := BuildTheme(context.Background(), bad); got.Success || got.Error == "" {
			t.Fatalf("invalid input accepted: %#v", got)
		}
	}
}

func TestThemeWritesFixedServiceBodies(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.URL.Path+":"+string(body))
		io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	created := client.CreateTheme(context.Background(), ThemeWriteCreateOptions{ID: "11111111-1111-4111-8111-111111111111", Caption: "Parity", CSSClassName: "parity", CSSContent: ".parity{}"}, nil)
	if !created.Success {
		t.Fatalf("create=%#v", created)
	}
	for _, result := range []CommandResult{client.UpdateTheme(context.Background(), "11111111-1111-4111-8111-111111111111", "Caption", "parity", ".parity{}"), client.DeleteTheme(context.Background(), "11111111-1111-4111-8111-111111111111"), client.ClearThemesCache(context.Background())} {
		if result.ExitCode != 0 {
			t.Fatalf("result=%#v", result)
		}
	}
	if len(requests) != 4 {
		t.Fatalf("requests=%v", requests)
	}
	for i, route := range []string{"CreateTheme", "UpdateTheme", "DeleteTheme", "ClearThemesCache"} {
		if !strings.Contains(requests[i], "/"+route+":") {
			t.Errorf("route %d=%s", i, requests[i])
		}
	}
}
