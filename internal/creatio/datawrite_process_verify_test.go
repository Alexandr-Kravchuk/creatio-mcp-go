package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// runFixture is a canned Creatio for run-process's wait, watch and cleanup: the shape the T20 comparison with
// clio 8.1.0.142 ran against (a parked run holding one activity the caller created, one created by another
// user and one of the caller's that nothing ties to the run).
type runFixture struct {
	mu        sync.Mutex
	logReads  int
	finishAt  int // the SysProcessLog read from which the run reports completed; 0: never
	cancelled bool
	requests  []string
}

const (
	runFixtureRun   = "bbbbbbbb-1111-0000-0000-000000000002"
	runFixtureMe    = "cccccccc-1111-0000-0000-000000000001"
	runFixtureNew1  = "dddddddd-1111-0000-0000-000000000001"
	runFixtureNew2  = "dddddddd-1111-0000-0000-000000000002"
	runFixtureNew3  = "dddddddd-1111-0000-0000-000000000003"
	runFixtureEl    = "eeeeeeee-1111-0000-0000-000000000001"
	runFixtureProcA = "aaaaaaaa-1111-0000-0000-000000000002"
)

func (f *runFixture) serve(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		has := func(parts ...string) bool {
			for _, part := range parts {
				if !strings.Contains(text, part) {
					return false
				}
			}
			return true
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/ProcessSchemaRequest"):
			metadata, _ := json.Marshal(`{"metaData":{"schema":{"parameters":[{"name":"Out","dataValueType":"325a73b8-0f47-44a0-8412-7606f78003ac","direction":1}]}}}`)
			io.WriteString(w, `{"success":true,"schema":{"metaData":`+string(metadata)+`}}`)
		case strings.HasSuffix(r.URL.Path, "/RunSpecifiedProcessVersion"):
			f.cancelled = false
			io.WriteString(w, `{"success":true,"processId":"`+runFixtureRun+`","processStatus":1,"resultParameterValues":{"Out":"x"}}`)
		case strings.HasSuffix(r.URL.Path, "/CancelExecution"):
			if text != `{"processDataIds":"`+runFixtureRun+`"}` {
				t.Errorf("cancel body %s", text)
			}
			f.cancelled = true
		case strings.HasSuffix(r.URL.Path, "/RuntimeEntitySchemaRequest"):
			io.WriteString(w, `{"success":true,"schema":{"uId":"11111111-2222-0000-0000-000000000001","name":"Activity","primaryDisplayColumnName":"Title",`+
				`"columns":{"items":{"a":{"name":"Id"},"b":{"name":"Title"},"c":{"name":"Owner","referenceSchemaName":"Contact"},`+
				`"d":{"name":"ProcessElementId"},"e":{"name":"CreatedBy","referenceSchemaName":"Contact"}}}}}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case has(`"rootSchemaName":"VwProcessLib"`):
			io.WriteString(w, `{"success":true,"rows":[{"Id":"`+runFixtureProcA+`","UId":"`+runFixtureProcA+`","Name":"UsrParked","Caption":"Parked","VersionParentUId":"`+
				runFixtureProcA+`","IsActiveVersion":true,"Version":0}]}`)
		case has(`"rootSchemaName":"SysProcessLog"`):
			f.logReads++
			status := 1
			if f.cancelled {
				status = 4
			} else if f.finishAt > 0 && f.logReads >= f.finishAt {
				status = 2
			}
			io.WriteString(w, `{"success":true,"rows":[{"StatusValue":`+string(rune('0'+status))+`,"ErrorDescription":null}]}`)
		case has(`"rootSchemaName":"SysProcessElementLog"`):
			io.WriteString(w, `{"success":true,"rows":[{"Caption":"Call the client","Type":"Terrasoft.Core.Process.ProcessSchemaUserTask"}]}`)
		case has(`"rootSchemaName":"Contact"`, `"macrosType":2`):
			io.WriteString(w, `{"success":true,"rows":[{"Id":"`+runFixtureMe+`"}]}`)
		case has(`"rootSchemaName":"SysProcessElementData"`):
			io.WriteString(w, `{"success":true,"rows":[{"Id":"`+runFixtureEl+`","SysProcess":{"value":"`+runFixtureRun+`"}}]}`)
		case has(`"rootSchemaName":"Activity"`, `"rightExpressions"`):
			io.WriteString(w, `{"success":true,"rows":[{"Id":"`+runFixtureNew1+`","Owner":{"value":"`+runFixtureMe+`"},"ProcessElementId":"`+runFixtureEl+`"},`+
				`{"Id":"`+runFixtureNew3+`","Owner":null,"ProcessElementId":null}]}`)
		case has(`"rootSchemaName":"Activity"`, `"orderDirection":2`):
			io.WriteString(w, `{"success":true,"rows":[{"CreatedOn":"2026-10-20T10:00:00.123"}]}`)
		case has(`"rootSchemaName":"Activity"`, `ClioCreatedBy`):
			if !has(`"value":"\"2026-10-20T10:00:00.123\""`) {
				t.Errorf("baseline not sent back as a quoted date: %s", text)
			}
			io.WriteString(w, `{"success":true,"rows":[{"Id":"old","CreatedOn":"2026-10-20T10:00:00.123"},`+
				`{"Id":"`+runFixtureNew1+`","CreatedOn":"2026-10-20T10:00:05","ClioCreatedBy":{"value":"`+runFixtureMe+`"},"Title":"Call"},`+
				`{"Id":"`+runFixtureNew2+`","CreatedOn":"2026-10-20T10:00:06","ClioCreatedBy":{"value":"cccccccc-1111-0000-0000-000000000002"},"Title":null},`+
				`{"Id":"`+runFixtureNew3+`","CreatedOn":"2026-10-20T10:00:07","ClioCreatedBy":{"value":"`+runFixtureMe+`"},"Title":"Mine"}]}`)
		case has(`"rootSchemaName":"Activity"`):
			io.WriteString(w, `{"success":true,"rows":[{"Id":"old","CreatedOn":"2026-10-20T10:00:00.123"}]}`)
		default:
			io.WriteString(w, `{"success":true,"rows":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	return client
}

func noSleep(t *testing.T) {
	previous := dataWriteSleep
	dataWriteSleep = func(time.Duration) {}
	t.Cleanup(func() { dataWriteSleep = previous })
}

func TestDataWriteRunProcessValidatesTheVerificationArguments(t *testing.T) {
	for _, c := range []struct {
		request DataWriteProcessRequest
		want    string
	}{
		{DataWriteProcessRequest{WaitTimeoutSeconds: 301}, "wait-timeout must be between 0 and 300 seconds."},
		{DataWriteProcessRequest{WaitTimeoutSeconds: -1}, "wait-timeout must be between 0 and 300 seconds."},
		{DataWriteProcessRequest{Watch: []string{"A", "B", "C", "D", "E", "F"}}, "watch takes at most 5 entities."},
		{DataWriteProcessRequest{Watch: []string{"Activity", "Act ivity"}}, "watch must list entity schema names (letters, digits, underscore); 'Act ivity' is not one."},
		{DataWriteProcessRequest{Watch: []string{""}}, "watch must list entity schema names (letters, digits, underscore); '' is not one."},
		{DataWriteProcessRequest{Cleanup: true, Watch: []string{}}, "cleanup deletes only what the run created in the watched entities, so it needs watch: " +
			"list the entities the process creates records in (for example [\"Activity\"])."},
		{DataWriteProcessRequest{Watch: []string{"Activity"}, Cleanup: true, WaitTimeoutSeconds: 300}, ""},
	} {
		if got := DataWriteValidateVerification(c.request); got != c.want {
			t.Errorf("%#v: %q", c.request, got)
		}
	}
}

func TestDataWriteRunProcessCleansUpOnlyWhatTheRunHolds(t *testing.T) {
	noSleep(t)
	fixture := &runFixture{}
	client := fixture.serve(t)
	result := client.RunProcess(context.Background(), DataWriteProcessRequest{ProcessName: "UsrParked", Wait: true, WaitTimeoutSeconds: 0,
		Watch: []string{"Activity"}, Cleanup: true, Deadline: time.Now().Add(45 * time.Second)})
	if result.Status != "cancelled" || result.Error != "" || result.ResultParameterValues != nil || result.WaitingOn != nil {
		t.Fatalf("result %#v", result)
	}
	if len(result.Created) != 3 || result.Cleanup == nil || len(result.Cleanup.Deleted) != 1 || result.Cleanup.Deleted[0].ID != runFixtureNew1 ||
		len(result.Cleanup.Skipped) != 2 || len(result.Cleanup.Failed) != 0 {
		t.Fatalf("created %#v cleanup %#v", result.Created, result.Cleanup)
	}
	encoded, _ := json.Marshal(result)
	for _, want := range []string{`"title":""`, `"versionName":"UsrParked"`, `"isActiveVersion":true`,
		"cleanup cancelled the still-running run before deleting what it created.", "cleanup left 2 record(s) it cannot tie to this run"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("answer lacks %s: %s", want, encoded)
		}
	}
	deletes := []string{}
	for _, request := range fixture.requests {
		if strings.HasPrefix(request, "DELETE ") {
			deletes = append(deletes, request)
		}
	}
	if len(deletes) != 1 || !strings.Contains(deletes[0], runFixtureNew1) {
		t.Fatalf("deletes %v", deletes)
	}
}

func TestDataWriteRunProcessWaitsForAParkedRun(t *testing.T) {
	noSleep(t)
	fixture := &runFixture{finishAt: 3}
	client := fixture.serve(t)
	result := client.RunProcess(context.Background(), DataWriteProcessRequest{ProcessName: "UsrParked", Wait: true, WaitTimeoutSeconds: 20,
		Deadline: time.Now().Add(45 * time.Second)})
	if result.Status != "completed" || result.ResultParameterValues != nil || fixture.logReads != 3 {
		t.Fatalf("result %#v reads %d", result, fixture.logReads)
	}
	if len(result.Warnings) != 1 || !strings.HasPrefix(result.Warnings[0], "resultParameterValues are not reported: the platform returned them at the launch") {
		t.Fatalf("warnings %#v", result.Warnings)
	}
	fixture.logReads, fixture.finishAt = 0, 0
	parked := client.RunProcess(context.Background(), DataWriteProcessRequest{ProcessName: "UsrParked", Wait: false,
		Deadline: time.Now().Add(45 * time.Second)})
	if parked.Status != "running" || fixture.logReads != 0 || string(parked.ResultParameterValues) != `{"Out":"x"}` {
		t.Fatalf("wait false %#v", parked)
	}
}
