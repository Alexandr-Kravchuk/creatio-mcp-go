package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetLastCompilationLogMapsDiagnostics(t *testing.T) {
	server := groupCServer(t, func(path string, _ map[string]any) (int, string) {
		if path != "/0/api/ConfigurationStatus/GetLastCompilationResult" {
			t.Errorf("unexpected route %s", path)
			return http.StatusNotFound, ""
		}
		return http.StatusOK, `{"errors":[` +
			`{"line":3,"column":7,"errorNumber":"CS0103","errorText":"The name 'x' does not exist","warning":false,"fileName":"UsrA.cs"},` +
			`{"line":9,"column":1,"errorNumber":"CS0168","errorText":"unused","warning":true,"fileName":null}` +
			`],"buildResult":1,"success":false}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetLastCompilationLog(context.Background())
	encoded, _ := json.Marshal(result)
	want := `{"success":true,"compilation-succeeded":false,"build-result":1,"diagnostics":[` +
		`{"severity":"error","file-name":"UsrA.cs","line":3,"column":7,"code":"CS0103","description":"The name 'x' does not exist"},` +
		`{"severity":"warning","line":9,"column":1,"code":"CS0168","description":"unused"}]}`
	if string(encoded) != want {
		t.Fatalf("result = %s\nwant     %s", encoded, want)
	}
}

func TestGetLastCompilationLogKeepsZeroBuildResultAndNullErrors(t *testing.T) {
	server := groupCServer(t, func(string, map[string]any) (int, string) {
		return http.StatusOK, `{"errors":null,"buildResult":0,"success":true}`
	})
	defer server.Close()
	encoded, _ := json.Marshal(newFormsTestClient(t, server.URL).GetLastCompilationLog(context.Background()))
	if want := `{"success":true,"compilation-succeeded":true,"build-result":0,"diagnostics":[]}`; string(encoded) != want {
		t.Fatalf("result = %s, want %s", encoded, want)
	}
}

func TestGetLastCompilationLogRejectsIncompletePayloadsInsideTheEnvelope(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"missing field": {http.StatusOK, `{"errors":[],"success":true}`, compileUnexpectedPayloadMessage},
		"null payload":  {http.StatusOK, `null`, compileEmptyPayloadMessage},
		"server error":  {http.StatusInternalServerError, `{}`, "api/ConfigurationStatus/GetLastCompilationResult returned HTTP 500"},
	}
	for name, c := range cases {
		server := groupCServer(t, func(string, map[string]any) (int, string) { return c.status, c.body })
		result := newFormsTestClient(t, server.URL).GetLastCompilationLog(context.Background())
		server.Close()
		if result.Success || result.Error != c.want || result.Diagnostics == nil || result.BuildResult != nil {
			t.Errorf("%s: result = %#v", name, result)
		}
	}
}
