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

func TestSetUserThemeWritesThemeIDAndVerifiesIgnoredWrites(t *testing.T) {
	stored := ""
	ignore := false
	id := "11111111-1111-4111-8111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/GetAvailableThemes"):
			io.WriteString(w, `{"success":true,"values":[{"id":"`+id+`","caption":"Parity","cssClassName":"parity"}]}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			if strings.Contains(string(body), `"columnPath":"Theme"`) {
				json.NewEncoder(w).Encode(map[string]any{"success": true, "rows": []any{map[string]any{"Theme": stored}}})
			} else {
				io.WriteString(w, `{"success":true,"rows":[{"Id":"22222222-2222-4222-8222-222222222222"}]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/UpdateQuery"):
			var payload map[string]any
			json.Unmarshal(body, &payload)
			value := payload["columnValues"].(map[string]any)["items"].(map[string]any)["Theme"].(map[string]any)["parameter"].(map[string]any)["value"].(string)
			if !ignore {
				stored = value
			}
			io.WriteString(w, `{"success":true}`)
		default:
			t.Errorf("route=%s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.SetUserTheme(context.Background(), "PARITY", false)
	if !result.Success || stored != id || result.ID == nil || *result.ID != id {
		t.Fatalf("result=%#v stored=%s", result, stored)
	}
	result = client.SetUserTheme(context.Background(), "", true)
	if !result.Success || stored != "" || *result.ID != "" {
		t.Fatalf("reset=%#v stored=%s", result, stored)
	}
	ignore = true
	result = client.SetUserTheme(context.Background(), id, false)
	if result.Success || !strings.Contains(result.Error, "ChangeTheme") {
		t.Fatalf("ignored=%#v", result)
	}
}
