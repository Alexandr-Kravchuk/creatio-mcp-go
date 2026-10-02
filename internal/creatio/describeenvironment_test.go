package creatio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const describeAppInfoBody = `{"errorInfo":null,"success":true,"applicationInfo":{"serviceUrl":"x","sysValues":{"coreVersion":"10.2.363.0","customer":null,"environmentType":"","maintainer":{"displayValue":"Customer","value":"Customer"},"userCulture":{"displayValue":"uk-ua","value":"c1"},"tags":[],"empty":{},"path":"a\/b","name":"Київ"}}}`

func describeServer(t *testing.T, routes map[string]func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if handler, ok := routes[r.URL.Path]; ok {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
}

func describeRespond(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestGetUserCultureReadsProfileCultureAndNormalizesCase(t *testing.T) {
	var method, body string
	server := describeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo": func(w http.ResponseWriter, r *http.Request) {
			method = r.Method
			raw := make([]byte, 16)
			n, _ := r.Body.Read(raw)
			body = string(raw[:n])
			_, _ = w.Write([]byte(describeAppInfoBody))
		},
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetUserCulture(context.Background())
	if !result.Success || result.Culture != "uk-UA" || result.ResolvedFrom != "environment" || result.Reason != "" {
		t.Fatalf("result = %#v", result)
	}
	if method != http.MethodPost || body != "{}" {
		t.Fatalf("request = %s %q", method, body)
	}
}

func TestGetUserCultureReportsReasonsAndNeverAFallbackCulture(t *testing.T) {
	cases := map[string]func(http.ResponseWriter, *http.Request){
		"userCulture-missing": describeRespond(http.StatusOK, `{"applicationInfo":{"sysValues":{"primaryCulture":{"displayValue":"en-US"}}}}`),
		"userCulture-invalid": describeRespond(http.StatusOK, `{"applicationInfo":{"sysValues":{"userCulture":{"displayValue":"not a culture"}}}}`),
		"unreachable":         describeRespond(http.StatusInternalServerError, `{}`),
	}
	for reason, handler := range cases {
		server := describeServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo": handler,
		})
		result := newFormsTestClient(t, server.URL).GetUserCulture(context.Background())
		server.Close()
		if result.Success || result.Culture != "" || result.Reason != reason || result.ResolvedFrom != "failed" {
			t.Errorf("%s: result = %#v", reason, result)
		}
	}
	if failure := UserCultureFailure(" "); failure.Reason != "unknown" {
		t.Fatalf("blank reason = %#v", failure)
	}
}

func TestDescribeEnvironmentKeepsCreatioOrderAndNewtonsoftLayout(t *testing.T) {
	server := describeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo":       describeRespond(http.StatusOK, describeAppInfoBody),
		"/0/ServiceModel/ApplicationInfoService.svc/GetSystemEnvironmentInfo": describeRespond(http.StatusOK, `{"success":true,"coreVersion":"10.2","dbEngineType":"MSSql","frameworkDescription":".NET Framework 4.8","frameworkKind":"NetFramework"}`),
		"/0/rest/CreatioApiGateway/GetSysInfo":                                describeRespond(http.StatusNotFound, `<html>404</html>`),
		"/0/DataService/json/SyncReply/SelectQuery":                           describeRespond(http.StatusOK, `{"success":true,"rows":[{"Name":"CrtBase","Version":"8.0"}]}`),
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).DescribeEnvironment(context.Background(), 0)
	if result.ExitCode != 0 || len(result.Messages) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Messages[0].MessageType != "Warning" || !strings.HasPrefix(result.Messages[0].Value, "cliogate 2.0.0.32+ is not installed - ProductName") {
		t.Fatalf("warning = %#v", result.Messages[0])
	}
	want := `{
  "coreVersion": "10.2.363.0",
  "customer": null,
  "environmentType": "",
  "maintainer": {
    "displayValue": "Customer",
    "value": "Customer"
  },
  "userCulture": {
    "displayValue": "uk-ua",
    "value": "c1"
  },
  "tags": [],
  "empty": {},
  "path": "a/b",
  "name": "Київ",
  "dbEngineType": "MSSql",
  "frameworkKind": "NetFramework",
  "frameworkDescription": ".NET Framework 4.8"
}`
	if result.Messages[1].MessageType != "None" || result.Messages[1].Value != want {
		t.Fatalf("report =\n%s", result.Messages[1].Value)
	}
}

func TestDescribeEnvironmentMergesClioGateSysInfo(t *testing.T) {
	server := describeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo":       describeRespond(http.StatusOK, describeAppInfoBody),
		"/0/ServiceModel/ApplicationInfoService.svc/GetSystemEnvironmentInfo": describeRespond(http.StatusForbidden, `{}`),
		"/0/rest/CreatioApiGateway/GetSysInfo": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("GetSysInfo method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"SysInfo":{"ProductName":"Studio","LicenseInfo":null,"DbEngineType":"PostgreSql","IsNetCore":true}}`))
		},
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).DescribeEnvironment(context.Background(), time.Second)
	if result.ExitCode != 0 || len(result.Messages) != 1 {
		t.Fatalf("result = %#v", result)
	}
	report := result.Messages[0].Value
	for _, line := range []string{`"productName": "Studio"`, `"dbEngineType": "PostgreSql"`, `"frameworkKind": "Net"`} {
		if !strings.Contains(report, line) {
			t.Errorf("report lacks %s:\n%s", line, report)
		}
	}
	if strings.Contains(report, "licenseInfo") {
		t.Errorf("a null licenseInfo must not be merged:\n%s", report)
	}
}

func TestDescribeEnvironmentClassifiesBaseProbeFailures(t *testing.T) {
	cases := map[string]func(http.ResponseWriter, *http.Request){
		"does not appear to be a Creatio application": describeRespond(http.StatusNotFound, `missing`),
		"Could not connect":                           describeRespond(http.StatusServiceUnavailable, `busy`),
		"returned an unexpected response":             describeRespond(http.StatusOK, `{"applicationInfo":{"sysValues":{"coreVersion":""}}}`),
	}
	for want, handler := range cases {
		server := describeServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo": handler,
		})
		result := newFormsTestClient(t, server.URL).DescribeEnvironment(context.Background(), time.Second)
		server.Close()
		if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].MessageType != "Error" || !strings.Contains(result.Messages[0].Value, want) {
			t.Errorf("%s: result = %#v", want, result)
		}
		if strings.Contains(result.Messages[0].Value, "/0/") {
			t.Errorf("message must name the authority only: %s", result.Messages[0].Value)
		}
	}
}

func TestDescribeEnvironmentWarnsAboutAnOldClioGate(t *testing.T) {
	server := describeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo": describeRespond(http.StatusOK, describeAppInfoBody),
		"/0/DataService/json/SyncReply/SelectQuery":                     describeRespond(http.StatusOK, `{"success":true,"rows":[{"Name":"cliogate","Version":"2.0.0.40"},{"Name":"cliogate_netcore","Version":"2.0.0.9"}]}`),
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).DescribeEnvironment(context.Background(), time.Second)
	if result.ExitCode != 0 || !strings.HasPrefix(result.Messages[0].Value, "GetSysInfo returned no data; lowest detected cliogate alias version 2.0.0.9 is below required 2.0.0.32") {
		t.Fatalf("result = %#v", result)
	}
}
