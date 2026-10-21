package creatio

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const adminWriteFsmOff = `{"applicationInfo":{"sysValues":{},"useStaticFileContent":true,"staticFileContent":{"schemasRuntimePath":"conf/content"}}}`

func TestSetFsmModeRejectsAnUnknownMode(t *testing.T) {
	stand, client := newAdminWriteManageStand(t, map[string][]string{})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, "maybe")
	if result.ExitCode != 1 || !reflect.DeepEqual(result.Messages, []LogMessage{{"Error", "Invalid value for IsFsm. Expected: 'on' or 'off'."}}) {
		t.Fatalf("result = %#v", result)
	}
	if stand.routes() != "" {
		t.Errorf("requests = %s", stand.routes())
	}
}

func TestSetFsmModeOnTogglesRemotelyThenExports(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote toggle is the macOS/Linux path")
	}
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		applicationInfoRoute: {adminWriteFsmOff},
		"rest/CreatioApiGateway/SetFileDesignMode": {
			`{"success":true,"previousFileDesignMode":"false","newFileDesignMode":"true","webConfigPath":"C:\\site\\Web.config"}`},
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode": {`{"success":true,"value":true}`},
		"ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem": {`{"success":true,"errors":[]}`},
	})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, " ON ")
	want := []LogMessage{
		{"None", "FSM toggled remotely via cliogate on environment 'dev': false → true. IIS will auto-recycle the AppPool to pick up the new Web.config."},
		{"None", "Start load packages to file system on a web application"},
		{"Info", "Load packages to file system on a web application completed"},
		{"None", ""}, // LoadPackagesToFileSystemCommand.Load's WriteLine (seen live in T23)
	}
	if result.ExitCode != 0 || !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("result = %#v", result)
	}
	if got := stand.body("rest/CreatioApiGateway/SetFileDesignMode"); got != `{"isFileDesignMode":true}` {
		t.Errorf("toggle body = %s", got)
	}
	if got := stand.routes(); got != applicationInfoRoute+",rest/CreatioApiGateway/SetFileDesignMode,"+
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode,ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem" {
		t.Errorf("routes = %s", got)
	}
}

func TestSetFsmModeOffImportsFirstAndStopsWhenTheImportFails(t *testing.T) {
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode": {`{"success":true,"value":true}`},
		"ServiceModel/AppInstallerService.svc/LoadPackagesToDB": {
			`{"success":false,"errors":[{"workspaceItem":{"name":"UsrPkg"},"errorInfo":{"message":"locked","errorCode":"E1"}}]}`},
	})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, "off")
	if result.ExitCode != 1 || len(result.Messages) != 4 ||
		result.Messages[1].Value != "Load packages to database on a web application ended with error: Synchronization rejected item 'UsrPkg': locked (error code: E1)" ||
		result.Messages[2] != (LogMessage{"None", ""}) ||
		!strings.HasPrefix(result.Messages[3].Value, "Packages were not imported into the database, so the file system mode configuration was left unchanged.") {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(stand.routes(), "SetFileDesignMode") || strings.Contains(stand.routes(), applicationInfoRoute) {
		t.Errorf("the configuration must not change: %s", stand.routes())
	}
}

func TestSetFsmModeOffEditsTheLocalConfigOfANetCoreSite(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "Terrasoft.WebHost.dll.config")
	original := "<configuration>\n  <terrasoft>\n    <fileDesignMode enabled=\"true\" />\n  </terrasoft>\n" +
		"  <appSettings>\n    <add key=\"UseStaticFileContent\" value=\"false\" />\n  </appSettings>\n</configuration>\n"
	if err := os.WriteFile(config, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, client := newAdminWriteManageStand(t, map[string][]string{
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode": {`{"success":true,"value":false}`},
	})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev", IsNetCore: true, EnvironmentPath: directory}, "off")
	if result.ExitCode != 0 || len(result.Messages) != 3 || result.Messages[0] != (LogMessage{"None", ""}) ||
		result.Messages[1].MessageType != "Warning" ||
		!strings.Contains(result.Messages[2].Value, "fileDesignMode           true          false") ||
		!strings.Contains(result.Messages[2].Value, "UseStaticFileContent     false         true") {
		t.Fatalf("result = %#v", result)
	}
	data, _ := os.ReadFile(config)
	if !strings.Contains(string(data), `<fileDesignMode enabled="false" />`) || !strings.Contains(string(data), `value="true"`) {
		t.Errorf("config = %s", data)
	}
	missing := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev", IsNetCore: true}, "off")
	if missing.ExitCode != -1 || missing.Messages[len(missing.Messages)-1].Value != "[Exception] Could not find path to environment: 'dev'" {
		t.Errorf("no path = %#v", missing)
	}
}
