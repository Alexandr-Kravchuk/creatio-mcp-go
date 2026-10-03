package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serviceTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "csrf", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCallServiceReturnsJSONAndRefusesFailures(t *testing.T) {
	var gotMethod, gotBody, gotCSRF, gotQuery string
	client := serviceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotBody, gotCSRF, gotQuery = r.Method, string(body), r.Header.Get("BPMCSRF"), r.URL.RawQuery
		switch r.URL.Path {
		case "/0/rest/Svc/Ok":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/0/rest/Svc/Fail":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false}`))
		case "/0/rest/Svc/Html":
			_, _ = w.Write([]byte(`<html>error</html>`))
		case "/0/rest/Svc/Big":
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		}
	})
	ctx := context.Background()
	payload, err := client.callService(ctx, serviceCall{Route: "rest/Svc/Ok", Body: []byte(`{"a":1}`)})
	if err != nil || string(payload) != `{"ok":true}` || gotMethod != http.MethodPost || gotBody != `{"a":1}` || gotCSRF != "csrf" {
		t.Fatalf("ok call = %q, %v (method %s body %s csrf %q)", payload, err, gotMethod, gotBody, gotCSRF)
	}
	if _, err := client.callService(ctx, serviceCall{Method: http.MethodGet, Route: "rest/Svc/Ok", Query: map[string][]string{"q": {"1"}}}); err != nil || gotMethod != http.MethodGet || gotQuery != "q=1" {
		t.Fatalf("GET = %v method %s query %s", err, gotMethod, gotQuery)
	}
	for route, want := range map[string]string{
		"rest/Svc/Fail": "Creatio service rest/Svc/Fail returned HTTP 500",
		"rest/Svc/Html": "Creatio service rest/Svc/Html returned HTML instead of JSON; authentication or routing failed",
		"rest/Svc/Big":  "Creatio service rest/Svc/Big response: response exceeds safety byte limit: 30 bytes",
	} {
		if _, err := client.callService(ctx, serviceCall{Route: route, Limit: 30}); err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", route, err, want)
		}
	}
	response, err := client.serviceRequest(ctx, serviceCall{Route: "rest/Svc/Fail"})
	if err != nil || response.status != http.StatusInternalServerError || string(response.payload) != `{"ok":false}` {
		t.Fatalf("serviceRequest keeps any status: %#v, %v", response, err)
	}
}

func TestCallServiceRefusesRoutesOutsideTheServiceRoots(t *testing.T) {
	client := serviceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may be sent, got %s", r.URL.Path)
	})
	for _, route := range []string{"", "rest/", "odata/Contact", "rest/../x", "rest/x?y=1", `ServiceModel\x`, "rest/x#y", "/rest/x"} {
		if _, err := client.callService(context.Background(), serviceCall{Route: route}); err == nil || err.Error() != "invalid Creatio service route" {
			t.Errorf("route %q: err = %v", route, err)
		}
	}
}
