package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func pkgWriteLoggingFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	constants := filepath.Join(root, "Terrasoft.Configuration", "Pkg", "UsrOrders", "Files", "src", "cs")
	if err := os.MkdirAll(constants, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "nlog.config"): "<nlog>\n  <rules>\n    <logger name=\"*\" writeTo=\"common\" />\n  </rules>\n</nlog>\n",
		filepath.Join(root, "nlog.targets.config"): "<nlog xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\">\n" +
			"  <variable name=\"DefaultLayout\" value=\"x\" />\n  <variable name=\"TodayLogPath\" value=\"y\" />\n" +
			"  <targets>\n    <target name=\"common\" xsi:type=\"File\" />\n  </targets>\n</nlog>\n",
		filepath.Join(constants, "Constants.cs"): "const string LoggerName = \"UsrOrders\";\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAddCustomLoggingThroughClioRun(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/AppInstallerService.svc/UnloadAppDomain": func(string) (int, string) { return 200, `` },
	})
	root := pkgWriteLoggingFixture(t)
	session := pkgWriteSession(t, fake, map[string]any{"EnvironmentPath": root})
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "add-custom-logging", map[string]any{"environment-name": "parity-env", "package-name": "UsrOrders", "ignored": 1}, &answer)
	if answer.ExitCode != 0 || len(answer.Messages) != 4 ||
		answer.Messages[0].Value != "Configured logger 'UsrOrders' with target 'usrOrdersAppender'." ||
		answer.Messages[1].Value != "Configured log path: ${TodayLogPath}/UsrOrders.log" ||
		answer.Messages[2].Value != "Configured environment 'parity-env'." ||
		!strings.HasPrefix(answer.Messages[3].Value, "Restart is required before the new logging route is guaranteed to be active.") {
		t.Fatalf("configure = %+v", answer)
	}
	pkgWriteCall(t, session, "add-custom-logging", map[string]any{"environment-name": "parity-env", "package-name": "UsrOrders", "restart-environment": true}, &answer)
	if answer.ExitCode != 0 || answer.Messages[0].Value != "Logger 'UsrOrders' and target 'usrOrdersAppender' are already configured; no files changed." ||
		answer.Messages[3].Value != "Restarting environment 'parity-env' to activate the logging route." || answer.Messages[4].Value != "Done restart-web-app" {
		t.Fatalf("rerun with restart = %+v", answer)
	}
	if calls := fake.recorded(); len(calls) != 1 || calls[0] != "POST /0/ServiceModel/AppInstallerService.svc/UnloadAppDomain {}" {
		t.Fatalf("calls = %q", calls)
	}
	pkgWriteAssertGated(t, session, "add-custom-logging", map[string]any{"environment-name": "parity-env", "package-name": "UsrOrders"})

	// clio resolves the environment before it validates the options: a call without one is the resolver's refusal.
	pkgWriteCall(t, session, "add-custom-logging", map[string]any{"min-level": "loud", "file-name": "../x"}, &answer)
	if answer.ExitCode != 1 || len(answer.Messages) != 1 || answer.Messages[0].Value != "[EnvironmentResolutionException] "+missingTargetMessage {
		t.Fatalf("without environment = %+v", answer)
	}
	pkgWriteCall(t, session, "add-custom-logging", map[string]any{"environment-name": "parity-env", "min-level": "loud", "file-name": "../x"}, &answer)
	want := []string{
		"ERROR (NotEmptyValidator) - Package name is required.",
		"ERROR (PredicateValidator) - Package name may contain only letters, digits, underscore, dot, and hyphen, and must start with a letter or underscore.",
		"ERROR (PredicateValidator) - Min level must be one of: Trace, Debug, Info, Warn, Error, Fatal, Off.",
		"ERROR (PredicateValidator) - File name must be a simple file name without directories or NLog layout expressions.",
	}
	if answer.ExitCode != 1 || len(answer.Messages) != len(want) {
		t.Fatalf("validation = %+v", answer)
	}
	for index, text := range want {
		if answer.Messages[index].MessageType != "None" || answer.Messages[index].Value != text {
			t.Fatalf("validation[%d] = %+v", index, answer.Messages[index])
		}
	}
	typed := pkgWriteCall(t, session, "add-custom-logging", map[string]any{"environment-name": "parity-env", "package-name": "UsrOrders", "restart-environment": "yes"}, nil)
	if !typed.IsError || !strings.Contains(pkgWriteText(typed), "argument 'restart-environment' for MCP tool 'add-custom-logging' must be a boolean") {
		t.Fatalf("typed = %s", pkgWriteText(typed))
	}
}

func TestAddCustomLoggingNeedsALocalInstallation(t *testing.T) {
	fake := newPkgWriteFake(t, nil)
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "add-custom-logging", map[string]any{"environment-name": "parity-env", "package-name": "UsrOrders"}, &answer)
	if answer.ExitCode != 1 || answer.Messages[0].Value != "Environment 'parity-env' does not have a local EnvironmentPath. add-custom-logging can only edit a registered local Creatio installation." {
		t.Fatalf("no path = %+v", answer)
	}
	pkgWriteCall(t, session, "add-custom-logging", map[string]any{"environment-name": "missing-env", "package-name": "UsrOrders"}, &answer)
	if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "[EnvironmentResolutionException] Environment with key 'missing-env' not found.") {
		t.Fatalf("unknown = %+v", answer)
	}
}
