package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dataWriteProcessFake answers the process library, the process schema, RunProcess (with runAnswer) and the
// SysProcessLog read; launches counts RunProcess calls and lastLaunch keeps the last body.
type dataWriteProcessFake struct {
	runAnswer  string
	logAnswer  string
	launches   int
	lastLaunch map[string]any
}

func newDataWriteProcessFake(t *testing.T) (*dataWriteProcessFake, *Client) {
	fake := &dataWriteProcessFake{runAnswer: `{"success":true,"processId":"bbbbbbbb-0000-0000-0000-000000000001","processStatus":2,"resultParameterValues":{"Output":"done"}}`,
		logAnswer: `{"success":true,"rows":[]}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			io.WriteString(w, `{"Code":0}`)
		case "/0/DataService/json/SyncReply/SelectQuery":
			if strings.Contains(string(body), "SysProcessLog") {
				io.WriteString(w, fake.logAnswer)
				return
			}
			io.WriteString(w, `{"success":true,"rows":[{"Id":"aaaaaaaa-0000-0000-0000-000000000001","Name":"UsrParityProcess","Caption":"Own process","VersionParentUId":"aaaaaaaa-0000-0000-0000-000000000001","IsActiveVersion":true}]}`)
		case "/0/DataService/json/SyncReply/ProcessSchemaRequest":
			metadata, _ := json.Marshal(`{"metaData":{"schema":{"parameters":[` +
				`{"name":"Input","dataValueType":"325a73b8-0f47-44a0-8412-7606f78003ac","direction":0},` +
				`{"name":"Flag","dataValueType":"90b65bf8-0ffc-4141-8779-2420877af907","direction":0},` +
				`{"name":"When","dataValueType":"d21e9ef4-c064-4012-b286-fa1a8171da44","direction":0},` +
				`{"name":"Ref","dataValueType":"b295071f-7ea9-4e62-8d1a-919bf3732ff2","referenceSchemaUId":"cccccccc-0000-0000-0000-000000000001","direction":0},` +
				`{"name":"Output","dataValueType":"325a73b8-0f47-44a0-8412-7606f78003ac","direction":1}]}}}`)
			io.WriteString(w, `{"success":true,"schema":{"metaData":`+string(metadata)+`}}`)
		case "/0/ServiceModel/ProcessEngineService.svc/RunProcess":
			fake.launches++
			_ = json.Unmarshal(body, &fake.lastLaunch)
			io.WriteString(w, fake.runAnswer)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	return fake, client
}

func TestDataWriteRunProcessCoercesAndReportsTheVerdict(t *testing.T) {
	fake, client := newDataWriteProcessFake(t)
	result := client.RunProcess(context.Background(), DataWriteProcessRequest{ProcessName: "UsrParityProcess", ResultParameters: []string{"Output"},
		Parameters: []DataWriteProcessParameter{{"Flag", "TRUE"}, {"Input", 5.0}, {"Ref", "{CCCCCCCC-0000-0000-0000-000000000002}"}, {"When", "2026-08-26T13:45:00Z"}}})
	if result.Status != "completed" || result.Error != "" || result.ProcessID != "bbbbbbbb-0000-0000-0000-000000000001" || string(result.ResultParameterValues) != `{"Output":"done"}` {
		t.Fatalf("result=%#v", result)
	}
	values := fake.lastLaunch["parameterValues"].([]any)
	want := []string{"Flag=true", "Input=5", "Ref=cccccccc-0000-0000-0000-000000000002", "When=2026-08-26T13:45:00.0000000Z"}
	for i, item := range values {
		pair := item.(map[string]any)
		if got := pair["name"].(string) + "=" + pair["value"].(string); got != want[i] {
			t.Fatalf("value %d = %s, want %s", i, got, want[i])
		}
	}
	if fake.lastLaunch["resultParameterNames"].([]any)[0] != "Output" {
		t.Fatalf("launch %#v", fake.lastLaunch)
	}
}

func TestDataWriteRunProcessRefusesBeforeLaunch(t *testing.T) {
	fake, client := newDataWriteProcessFake(t)
	for _, tc := range []struct {
		request DataWriteProcessRequest
		want    string
	}{
		{DataWriteProcessRequest{}, "process-name is required"},
		{DataWriteProcessRequest{ProcessName: "usrparityprocess"}, "'usrparityprocess' is not a process CODE. It resolved to 'UsrParityProcess'"},
		{DataWriteProcessRequest{ProcessName: "UsrParityProcess", Parameters: []DataWriteProcessParameter{{"input", "x"}}},
			"'input' does not match any parameter of the process. Parameter codes are case-sensitive — did you mean 'Input'?"},
		{DataWriteProcessRequest{ProcessName: "UsrParityProcess", Parameters: []DataWriteProcessParameter{{"Nope", "x"}}},
			"'Nope' is not a parameter of the process. Codes accepted by 'parameters': Flag, Input, Ref, When."},
		{DataWriteProcessRequest{ProcessName: "UsrParityProcess", Parameters: []DataWriteProcessParameter{{"Output", "x"}}},
			"'Output' is an Output parameter and cannot be assigned through 'parameters'."},
		{DataWriteProcessRequest{ProcessName: "UsrParityProcess", Parameters: []DataWriteProcessParameter{{"Flag", 1.0}}},
			"'Flag' expects a boolean but received Number."},
		{DataWriteProcessRequest{ProcessName: "UsrParityProcess", Parameters: []DataWriteProcessParameter{{"Ref", "Main"}}},
			"'Ref' expects a record id (Guid) but received 'Main'."},
		{DataWriteProcessRequest{ProcessName: "UsrParityProcess", ResultParameters: []string{"Input"}},
			"'Input' is an Input parameter and carries no run result"},
	} {
		result := client.RunProcess(context.Background(), tc.request)
		if !strings.HasPrefix(result.Error, tc.want) {
			t.Errorf("%#v: %q", tc.request, result.Error)
		}
	}
	if fake.launches != 0 {
		t.Fatalf("a refused call launched: %d", fake.launches)
	}
	gate := &DataWriteLaunchGate{}
	gate.TryWithdraw()
	result := client.RunProcess(context.Background(), DataWriteProcessRequest{ProcessName: "UsrParityProcess", Gate: gate})
	if result.Status != DataWriteNotStartedStatus || fake.launches != 0 {
		t.Fatalf("withdrawn gate %#v", result)
	}
}

func TestDataWriteRunProcessMapsPlatformAnswers(t *testing.T) {
	fake, client := newDataWriteProcessFake(t)
	run := func() DataWriteProcessResult {
		return client.RunProcess(context.Background(), DataWriteProcessRequest{ProcessName: "UsrParityProcess", Deadline: time.Now().Add(30 * time.Second)})
	}
	fake.runAnswer = `{"success":true,"processId":"00000000-0000-0000-0000-000000000000","processStatus":0}`
	if result := run(); result.Status != "queued-background" || len(result.Warnings) != 1 || result.Error != "" {
		t.Fatalf("background %#v", result)
	}
	fake.runAnswer = `{"success":false,"processId":"00000000-0000-0000-0000-000000000000","processStatus":0,"errorInfo":{"errorCode":"ProcessCannotBeManuallyStartedException","message":"No manual start"}}`
	if result := run(); result.Status != "not-started" || !strings.HasPrefix(result.Error, "'UsrParityProcess' cannot be launched: [untrusted-source-text begin] No manual start [untrusted-source-text end]. Nothing was started.") {
		t.Fatalf("refusal %#v", result)
	}
	fake.runAnswer = `{"success":true,"processId":"bbbbbbbb-0000-0000-0000-000000000001","processStatus":3,"errorInfo":{"message":"check the process log"}}`
	fake.logAnswer = `{"success":true,"rows":[{"ErrorDescription":"\r\nSystem.Exception: boom\n   at Stack"}]}`
	result := run()
	if result.Status != "error" || result.Error != "The process run failed: [untrusted-source-text begin] check the process log [untrusted-source-text end]. "+
		"The process log of this run reports: [untrusted-source-text begin] System.Exception: boom [untrusted-source-text end]" {
		t.Fatalf("failed run %#v", result)
	}
	fake.runAnswer = `{"success":false,"processId":"00000000-0000-0000-0000-000000000000","processStatus":0,"errorInfo":{"errorCode":"KeyNotFoundException","message":"missing"}}`
	if result := run(); result.Status != "error" || !strings.Contains(result.Error, "[KeyNotFoundException] This is most often a script task") {
		t.Fatalf("missing compiled method %#v", result)
	}
}
