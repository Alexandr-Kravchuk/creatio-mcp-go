package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGetFsmModeReadsApplicationInfo(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"off": {`{"applicationInfo":{"sysValues":{},"useStaticFileContent":true,"staticFileContent":{"schemasRuntimePath":"conf/content","ResourcesRuntimePath":"conf/content/resources/en-US"}}}`,
			`{"mode":"off","useStaticFileContent":true,"staticFileContent":{"schemasRuntimePath":"conf/content","resourcesRuntimePath":"conf/content/resources/en-US"}}`},
		"on": {`{"x":[{"UseStaticFileContent":false,"staticFileContent":null}]}`,
			`{"mode":"on","useStaticFileContent":false,"staticFileContent":null}`},
	}
	for name, c := range cases {
		server := groupCServer(t, func(path string, _ map[string]any) (int, string) {
			if path != "/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo" {
				t.Errorf("unexpected route %s", path)
			}
			return http.StatusOK, c.body
		})
		result, err := newFormsTestClient(t, server.URL).GetFsmMode(context.Background())
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if encoded, _ := json.Marshal(result); string(encoded) != c.want {
			t.Errorf("%s: result = %s, want %s", name, encoded, c.want)
		}
	}
}

func TestGetFsmModeRefusesAmbiguousOrInconsistentPayloads(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"inconsistent": {http.StatusOK, `{"useStaticFileContent":true,"staticFileContent":{"schemasRuntimePath":" "}}`, "Could not determine FSM mode"},
		"ambiguous":    {http.StatusOK, `{"a":{"useStaticFileContent":false,"staticFileContent":null},"b":{"useStaticFileContent":false,"staticFileContent":null}}`, "multiple payload candidates"},
		"missing":      {http.StatusOK, `{"applicationInfo":{}}`, "does not contain a canonical payload"},
		"not boolean":  {http.StatusOK, `{"useStaticFileContent":"true","staticFileContent":null}`, "must be a boolean"},
		"http error":   {http.StatusBadGateway, `{}`, "HTTP 502"},
	}
	for name, c := range cases {
		server := groupCServer(t, func(string, map[string]any) (int, string) { return c.status, c.body })
		_, err := newFormsTestClient(t, server.URL).GetFsmMode(context.Background())
		server.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}
