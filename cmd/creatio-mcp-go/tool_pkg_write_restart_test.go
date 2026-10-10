package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func TestRestartByEnvironmentNameWithoutWaiting(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/AppInstallerService.svc/UnloadAppDomain": func(string) (int, string) { return 200, `` },
	})
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "restart-by-environment-name", map[string]any{"environmentName": "parity-env", "waitReady": false}, &answer)
	if answer.ExitCode != 0 || len(answer.Messages) != 1 || answer.Messages[0].MessageType != "Info" || answer.Messages[0].Value != "Done restart-web-app" {
		t.Fatalf("restart = %+v", answer)
	}
	if calls := fake.recorded(); len(calls) != 1 || calls[0] != "POST /0/ServiceModel/AppInstallerService.svc/UnloadAppDomain {}" {
		t.Fatalf("calls = %q", calls)
	}
	pkgWriteAssertGated(t, session, "restart-by-environment-name", map[string]any{"environmentName": "parity-env"})

	for args, want := range map[string]string{
		"blank":   "environment-name is required and cannot be empty.",
		"unknown": "[EnvironmentResolutionException] Environment with key 'missing-env' not found.",
	} {
		input := map[string]any{"environmentName": "  ", "waitReady": false}
		if args == "unknown" {
			input = map[string]any{"environmentName": "missing-env", "waitReady": false}
		}
		pkgWriteCall(t, session, "restart-by-environment-name", input, &answer)
		if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, want) {
			t.Fatalf("%s = %+v", args, answer)
		}
	}
	for args, want := range map[string]string{
		`missing`: "missing a value for the required parameter 'environmentName'",
		`wait`:    "argument 'waitReady' for MCP tool 'restart-by-environment-name' must be a boolean",
		`timeout`: "argument 'waitTimeoutSeconds' for MCP tool 'restart-by-environment-name' must be an integer",
	} {
		input := map[string]any{}
		switch args {
		case "wait":
			input = map[string]any{"environmentName": "parity-env", "waitReady": "no"}
		case "timeout":
			input = map[string]any{"environmentName": "parity-env", "waitTimeoutSeconds": 1.5}
		}
		result := pkgWriteCall(t, session, "restart-by-environment-name", input, nil)
		if !result.IsError || !strings.Contains(pkgWriteText(result), want) {
			t.Fatalf("%s = %s", args, pkgWriteText(result))
		}
	}
	if len(fake.recorded()) != 1 {
		t.Fatalf("refusals must not reach Creatio: %q", fake.recorded())
	}
}

func TestRestartRecordsRequestFailureAndAnswersInProgress(t *testing.T) {
	t.Setenv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", "0.3")
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/AppInstallerService.svc/UnloadAppDomain": func(string) (int, string) { return 200, `` },
	})
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	// The readiness wait starts with a 10 s delay, so the call answers in-progress after the accepted request.
	pkgWriteCall(t, session, "restart-by-environment-name", map[string]any{"environmentName": "parity-env", "waitTimeoutSeconds": 5000}, &answer)
	if answer.ExitCode != 0 || answer.Messages[0].MessageType != "Info" ||
		!strings.HasPrefix(answer.Messages[0].Value, "Restart of environment 'parity-env' was accepted and the restart request itself already succeeded;") ||
		!strings.Contains(answer.Messages[0].Value, "for up to 3600s") || !strings.Contains(answer.Messages[0].Value, "Poll restart-status with environment-name 'parity-env' (or operation-id '") {
		t.Fatalf("in progress = %+v", answer)
	}
	var status struct {
		Status      string `json:"status"`
		OperationID string `json:"operation-id"`
		ExitCode    *int   `json:"exit-code"`
	}
	pkgWriteCall(t, session, "restart-status", map[string]any{"environment-name": "parity-env"}, &status)
	if status.Status != "running" || status.OperationID == "" {
		t.Fatalf("status = %+v", status)
	}

	pkgWriteCall(t, session, "restart-by-environment-name", map[string]any{"environmentName": "missing-env"}, &answer)
	if answer.ExitCode != 1 || !strings.Contains(answer.Messages[0].Value, "Environment with key 'missing-env' not found.") {
		t.Fatalf("unknown = %+v", answer)
	}
	pkgWriteCall(t, session, "restart-status", map[string]any{"environment-name": "missing-env"}, &status)
	if status.Status != "requestfailed" || status.ExitCode == nil || *status.ExitCode != 1 {
		t.Fatalf("requestfailed status = %+v", status)
	}
}

func TestRestartByCredentials(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/AppInstallerService.svc/RestartApp": func(string) (int, string) { return 200, `{}` },
	})
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "restart-by-credentials", map[string]any{"url": fake.server.URL, "userName": "example-user",
		"password": "replace-me", "isNetCore": true, "waitReady": false}, &answer)
	if answer.ExitCode != 0 || answer.Messages[0].Value != "Done restart-web-app" {
		t.Fatalf("restart = %+v", answer)
	}
	if calls := fake.recorded(); len(calls) != 1 || calls[0] != "POST /ServiceModel/AppInstallerService.svc/RestartApp {}" {
		t.Fatalf("calls = %q", calls)
	}
	for name, input := range map[string]map[string]any{
		"url is required and cannot be empty.":                        {"url": " ", "userName": "u", "password": "p"},
		"userName is required and cannot be empty.":                   {"url": "http://x", "userName": "", "password": "p"},
		"password is required and cannot be empty.":                   {"url": "http://x", "userName": "u", "password": ""},
		"Invalid URI: The format of the URI could not be determined.": {"url": "not a url", "userName": "u", "password": "p", "waitReady": false},
	} {
		pkgWriteCall(t, session, "restart-by-credentials", input, &answer)
		if answer.ExitCode != 1 || answer.Messages[0].Value != name {
			t.Fatalf("%s = %+v", name, answer)
		}
	}
	t.Setenv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", "5")
	started := time.Now()
	pkgWriteCall(t, session, "restart-by-credentials", map[string]any{"url": "not a url", "userName": "u", "password": "p"}, &answer)
	if answer.ExitCode != 1 || answer.Messages[0].Value != "Invalid URI: The format of the URI could not be determined." || time.Since(started) > 4*time.Second {
		t.Fatalf("waited bad url = %+v", answer)
	}
	missing := pkgWriteCall(t, session, "restart-by-credentials", map[string]any{"url": "http://x", "userName": "u"}, nil)
	if !missing.IsError || !strings.Contains(pkgWriteText(missing), "required parameter 'password'") {
		t.Fatalf("missing = %s", pkgWriteText(missing))
	}
	if len(fake.recorded()) != 1 {
		t.Fatalf("refusals must not reach Creatio: %q", fake.recorded())
	}
}
