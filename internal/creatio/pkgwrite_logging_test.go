package creatio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pkgWriteRulesFixture = "\xEF\xBB\xBF<?xml version=\"1.0\" encoding=\"utf-8\"?>\r\n" +
	"<nlog xmlns=\"http://www.nlog-project.org/schemas/NLog.xsd\">\r\n" +
	"  <rules>\r\n" +
	"    <logger name=\"Audit\" writeTo=\"auditAppender\" minlevel=\"Info\" final=\"true\" />\r\n" +
	"    <logger name=\"*\" writeTo=\"commonAppender\" minlevel=\"Info\" />\r\n" +
	"  </rules>\r\n" +
	"</nlog>\r\n"

const pkgWriteTargetsFixture = "<nlog xmlns=\"http://www.nlog-project.org/schemas/NLog.xsd\" xmlns:x=\"http://www.w3.org/2001/XMLSchema-instance\">\n" +
	"  <variable name=\"DefaultLayout\" value=\"${date}\" />\n" +
	"  <variable name=\"TodayLogPath\" value=\"C:/Logs\" />\n" +
	"  <targets>\n" +
	"    <target name=\"commonAppender\" x:type=\"File\" layout=\"${DefaultLayout}\" fileName=\"${TodayLogPath}/Common.log\" />\n" +
	"  </targets>\n" +
	"</nlog>\n"

func pkgWriteLoggingInstall(t *testing.T, framework bool) string {
	t.Helper()
	root := t.TempDir()
	app := root
	if framework {
		app = filepath.Join(root, "Terrasoft.WebApp")
	}
	constants := filepath.Join(app, "Terrasoft.Configuration", "Pkg", "UsrOrders", "Files", "src", "cs")
	if err := os.MkdirAll(constants, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(app, "nlog.config"):         pkgWriteRulesFixture,
		filepath.Join(app, "nlog.targets.config"): pkgWriteTargetsFixture,
		filepath.Join(constants, "Constants.cs"):  "namespace UsrOrders {\n\tinternal static class Constants {\n\t\tinternal const string LoggerName = \"UsrOrdersApp\";\n\t}\n}\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPkgWriteConfigureCustomLogging(t *testing.T) {
	root := pkgWriteLoggingInstall(t, true)
	result := ConfigureCustomLogging(root, "UsrOrders", "debug", "")
	if !result.Success || !result.Changed || result.LoggerName != "UsrOrdersApp" || result.TargetName != "usrOrdersAppender" ||
		result.LogPath != "${TodayLogPath}/UsrOrders.log" {
		t.Fatalf("configure = %+v", result)
	}
	app := filepath.Join(root, "Terrasoft.WebApp")
	rules, _ := os.ReadFile(filepath.Join(app, "nlog.config"))
	if !strings.HasPrefix(string(rules), "\xEF\xBB\xBF") || !strings.Contains(string(rules),
		"  <rules>\r\n    <logger name=\"UsrOrdersApp\" writeTo=\"usrOrdersAppender\" minlevel=\"Debug\" final=\"true\" />\r\n    <logger name=\"Audit\"") {
		t.Fatalf("rules = %q", rules)
	}
	targets, _ := os.ReadFile(filepath.Join(app, "nlog.targets.config"))
	if strings.HasPrefix(string(targets), "\xEF\xBB\xBF") || !strings.Contains(string(targets),
		"  <targets>\n    <target name=\"usrOrdersAppender\" x:type=\"File\" layout=\"${DefaultLayout}\" fileName=\"${TodayLogPath}/UsrOrders.log\" />\n    <target name=\"commonAppender\"") {
		t.Fatalf("targets = %q", targets)
	}
	again := ConfigureCustomLogging(root, "UsrOrders", "Debug", "")
	if !again.Success || again.Changed {
		t.Fatalf("rerun = %+v", again)
	}
	conflict := ConfigureCustomLogging(root, "UsrOrders", "Error", "")
	if conflict.Success || conflict.ErrorMessage != "Logger 'UsrOrdersApp' already exists in '"+filepath.Join(app, "nlog.config")+"' with conflicting or duplicate entries; no files were changed." {
		t.Fatalf("conflict = %+v", conflict)
	}
	leftovers, _ := filepath.Glob(filepath.Join(app, "*.bak"))
	if len(leftovers) != 0 {
		t.Fatalf("backups left: %v", leftovers)
	}
}

func TestPkgWriteCustomLoggingRefusals(t *testing.T) {
	root := pkgWriteLoggingInstall(t, false)
	if result := ConfigureCustomLogging(root, "UsrMissing", "Info", ""); result.Success ||
		!strings.HasPrefix(result.ErrorMessage, "Package 'UsrMissing' does not contain generated logger constants at '") {
		t.Fatalf("missing package = %+v", result)
	}
	if result := ConfigureCustomLogging(t.TempDir(), "UsrOrders", "Info", ""); result.Success ||
		!strings.HasPrefix(result.ErrorMessage, "Could not find both 'nlog.config' and 'nlog.targets.config' under EnvironmentPath '") {
		t.Fatalf("no nlog = %+v", result)
	}
	if result := ConfigureCustomLogging(root, "UsrOrders", "Info", "CON"); result.Success || result.ErrorMessage != "File name must be a simple file name without directories or NLog layout expressions." {
		t.Fatalf("reserved = %+v", result)
	}
	if result := ConfigureCustomLogging(root, "UsrOrders", "Info", "orders"); !result.Success || result.LogPath != "${TodayLogPath}/orders.log" {
		t.Fatalf("file name = %+v", result)
	}
	for value, ok := range map[string]bool{"": true, "a.log": true, "../a": false, "${x}": false, "nul.txt": false, ".": false} {
		if CustomLoggingSafeFile(value) != ok {
			t.Fatalf("safe file %q", value)
		}
	}
}
