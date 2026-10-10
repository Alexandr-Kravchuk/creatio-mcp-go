package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func TestCompileCreatioRefusesAScopeItCannotHonour(t *testing.T) {
	fake := newPkgWriteFake(t, nil)
	session := pkgWriteSession(t, fake, nil)
	for want, args := range map[string]map[string]any{
		"Rename: 'packageName' -> 'package-name'. Nothing was compiled.":                                                              {"environment-name": "parity-env", "packageName": "UsrPkg"},
		"Rename: 'ProcessName' -> 'process-name'. Nothing was compiled.":                                                              {"environment-name": "parity-env", "ProcessName": "UsrProc"},
		"Unknown args: 'scope'. " + pkgWriteCompileHint + " Nothing was compiled.":                                                    {"environment-name": "parity-env", "scope": "all"},
		"`package-name` must contain exactly one package name. Comma-separated package lists are not supported by `compile-creatio`.": {"environment-name": "parity-env", "package-name": "A,B"},
		"`package-name` is empty. Pass the package name, or omit the argument for a full compilation.":                                {"environment-name": "parity-env", "package-name": " "},
		"`process-name` is empty. Pass the business process code, or omit the argument for a full compilation.":                       {"environment-name": "parity-env", "process-name": ""},
		"`process-name` is null. Pass a value, or omit both scope arguments for a full compilation. Nothing was compiled.":            {"environment-name": "parity-env", "process-name": nil},
		"Provide only one of `package-name` or `process-name`: `process-name` already compiles the package the process is in.":        {"environment-name": "parity-env", "package-name": "A", "process-name": "B"},
		"environment-name is required and cannot be empty.":                                                                           {"package-name": "UsrPkg"},
	} {
		var answer creatio.CommandResult
		pkgWriteCall(t, session, "compile-creatio", args, &answer)
		if answer.ExitCode != 1 || len(answer.Messages) != 1 || answer.Messages[0].Value != want {
			t.Fatalf("%v = %+v", args, answer)
		}
	}
	typed := pkgWriteCall(t, session, "compile-creatio", map[string]any{"environment-name": "parity-env", "package-name": 3}, nil)
	if !typed.IsError || !strings.Contains(pkgWriteText(typed), "argument 'package-name' for MCP tool 'compile-creatio' must be a string") {
		t.Fatalf("typed = %s", pkgWriteText(typed))
	}
	pkgWriteAssertGated(t, session, "compile-creatio", map[string]any{"environment-name": "parity-env"})
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("refusals must not reach Creatio: %q", calls)
	}
	var status struct {
		Status string `json:"status"`
	}
	pkgWriteCall(t, session, "compile-status", map[string]any{"environment-name": "parity-env"}, &status)
	if status.Status != "not-found" {
		t.Fatalf("a refusal must not be tracked: %+v", status)
	}
}

func TestCompileCreatioRecordsTheCompileAndAnswersInProgress(t *testing.T) {
	t.Setenv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", "0.3")
	release := make(chan struct{})
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/SelectQuery": func(string) (int, string) { return 200, `{"success":true,"rows":[]}` },
		"/RebuildPackage": func(string) (int, string) {
			<-release
			return 200, `{"success":false,"buildResult":1,"errors":[{"errorNumber":"CS1","errorText":"bad","fileName":"A.cs","line":1,"column":1}]}`
		},
	})
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "compile-creatio", map[string]any{"environment-name": "parity-env", "package-name": "UsrPkg"}, &answer)
	if answer.ExitCode != 0 || answer.Messages[0].MessageType != "Info" ||
		!strings.HasPrefix(answer.Messages[0].Value, "Compilation for 'parity-env' (operation-id '") ||
		!strings.Contains(answer.Messages[0].Value, "was started and has not finished (MCP response deadline reached)") {
		t.Fatalf("in progress = %+v", answer)
	}
	pkgWriteCall(t, session, "compile-creatio", map[string]any{"environment-name": "parity-env"}, &answer)
	if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "THIS clio process is already running a compilation for 'parity-env'") {
		t.Fatalf("second compile = %+v", answer)
	}
	var status struct {
		Status      string   `json:"status"`
		PackageName string   `json:"package-name"`
		ExitCode    *int     `json:"exit-code"`
		Tail        []string `json:"message-tail"`
	}
	pkgWriteCall(t, session, "compile-status", map[string]any{"environment-name": "parity-env"}, &status)
	if status.Status != "running" || status.PackageName != "UsrPkg" {
		t.Fatalf("running status = %+v", status)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for status.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		pkgWriteCall(t, session, "compile-status", map[string]any{"environment-name": "parity-env"}, &status)
	}
	if status.Status != "failed" || status.ExitCode == nil || *status.ExitCode != 1 || len(status.Tail) == 0 ||
		!strings.HasPrefix(status.Tail[len(status.Tail)-1], "Package compilation failed for 'UsrPkg' (build result 1).") {
		t.Fatalf("finished status = %+v", status)
	}
	if calls := fake.recorded(); !pkgWriteHasCall(calls, `POST /0/ServiceModel/WorkspaceExplorerService.svc/RebuildPackage { "packageName":"UsrPkg" }`) {
		t.Fatalf("calls = %q", calls)
	}
	pkgWriteCall(t, session, "compile-creatio", map[string]any{"environment-name": "missing-env"}, &answer)
	if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "[EnvironmentResolutionException] Environment with key 'missing-env' not found.") {
		t.Fatalf("unknown = %+v", answer)
	}
}

func pkgWriteHasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
