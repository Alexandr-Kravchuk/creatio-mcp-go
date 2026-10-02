package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListThemesPostsEmptyBodyAndSanitizesFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/ServiceModel/ThemeService.svc/GetAvailableThemes":
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || string(body) != "{}" {
				t.Errorf("request = %s %q", r.Method, body)
			}
			_, _ = w.Write([]byte(`{"success":true,"values":[{"id":"t1","caption":"Dark\u0007 theme","cssClassName":"dark","cssFilePath":"a.css"},null]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListThemes(context.Background())
	if !result.Success || len(result.Themes) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := result.Themes[0]; got.ID != "t1" || got.Caption != "Dark  theme" || got.CSSClassName != "dark" || got.CSSFilePath != "a.css" {
		t.Fatalf("theme = %#v", got)
	}
}

func TestListThemesReportsServiceFailureInEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"errorInfo":{"errorCode":"SecurityException","message":"Access denied"}}`))
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListThemes(context.Background())
	if result.Success || result.Error != "Access denied" || result.Themes != nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestSanitizeThemeTextCapsWithoutSplittingSurrogatePairs(t *testing.T) {
	if got := sanitizeThemeText("abcdef", 4); got != "abcd..." {
		t.Fatalf("plain cap = %q", got)
	}
	// "😀" is two UTF-16 units: a cap landing inside it drops the whole character.
	if got := sanitizeThemeText("abc😀d", 4); got != "abc..." {
		t.Fatalf("surrogate cap = %q", got)
	}
	if got := sanitizeThemeText("a b", 10); got != "a b" {
		t.Fatalf("separator = %q", got)
	}
}
