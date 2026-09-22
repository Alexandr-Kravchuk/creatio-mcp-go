package hosttools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type outputResponse struct {
	output []byte
	err    error
}

type fakeRunner struct {
	outputs map[string]outputResponse
	calls   []string
	starts  []startCall
}

type startCall struct {
	executable string
	args       []string
	workdir    string
}

func (r *fakeRunner) Output(_ context.Context, executable string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, filepath.Base(executable)+" "+key)
	response, ok := r.outputs[key]
	if !ok {
		return nil, errors.New("unexpected command: " + key)
	}
	return response.output, response.err
}

func (r *fakeRunner) Start(executable string, args []string, workingDirectory string) (int, error) {
	r.starts = append(r.starts, startCall{executable: executable, args: append([]string(nil), args...), workdir: workingDirectory})
	return 4321, nil
}

func TestFindEmptyIISPortForWindowsIncludesIISAndTCPv4AndTCPv6(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]outputResponse{
		"list sites /xml": {output: []byte(`<appcmd><SITE SITE.NAME="Default Web Site"><bindings><binding bindingInformation="*:40001:" /></bindings></SITE></appcmd>`)},
		"-ano -p tcp":     {output: []byte("Active Connections\r\n  Proto  Local Address          Foreign Address        State           PID\r\n  TCP    0.0.0.0:40000          0.0.0.0:0              LISTENING       10\r\n  TCPv6  [::]:40002             [::]:0                 LISTENING       11\r\n")},
	}}
	result := FindEmptyIISPortForWindows(context.Background(), runner, 40000, 40003)
	if result.Status != "available" || result.FirstAvailablePort == nil || *result.FirstAvailablePort != 40003 {
		t.Fatalf("port result = %#v", result)
	}
	if result.IISBoundPortCount != 1 || result.ActiveTCPPortCount != 2 {
		t.Fatalf("port census counts = IIS:%d TCP:%d", result.IISBoundPortCount, result.ActiveTCPPortCount)
	}
}

func TestFindEmptyIISPortForWindowsPreservesRangeOnFailure(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]outputResponse{
		"list sites /xml": {err: errors.New("appcmd unavailable")},
	}}
	result := FindEmptyIISPortForWindows(context.Background(), runner, 51000, 51010)
	if result.Status != "unavailable" || result.RangeStart != 51000 || result.RangeEnd != 51010 {
		t.Fatalf("port failure result = %#v", result)
	}
}

func TestStartCreatioUsesDotnetProcessForRegisteredEnvironment(t *testing.T) {
	home := t.TempDir()
	environmentPath := filepath.Join(home, "app")
	if err := os.Mkdir(environmentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(environmentPath, "Terrasoft.WebHost.dll"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			t.Errorf("ping path = %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer pingServer.Close()
	writeSettingsFixture(t, home, settingsFile{
		ActiveEnvironmentKey: "dev",
		Environments: map[string]environmentRecord{
			"dev": {URI: pingServer.URL, EnvironmentPath: environmentPath},
		},
	})
	runner := &fakeRunner{}
	var progress []string
	result, err := StartCreatio(context.Background(), StartOptions{
		Home: home, Platform: "darwin", Runner: runner, HTTPClient: pingServer.Client(),
		Sleep: func(context.Context, time.Duration) error { return nil },
		Progress: func(value, total float64, message string) error {
			progress = append(progress, message)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "started" || result.Environment != "dev" || result.StartedBy != "process" || result.ProcessID != 4321 || result.PingWarning != "" {
		t.Fatalf("start result = %#v", result)
	}
	wantStart := startCall{executable: "dotnet", args: []string{"Terrasoft.WebHost.dll"}, workdir: environmentPath}
	if !reflect.DeepEqual(runner.starts, []startCall{wantStart}) {
		t.Fatalf("process start = %#v", runner.starts)
	}
	if !reflect.DeepEqual(progress, []string{"Obtained environment to start", "Started IIS site or Creatio host process", "Start operation complete"}) {
		t.Fatalf("progress = %v", progress)
	}
}

func TestStartCreatioFindsAndStartsIISApplicationMapping(t *testing.T) {
	home := t.TempDir()
	environmentPath := filepath.Join(home, "site-app")
	if err := os.Mkdir(environmentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettingsFixture(t, home, settingsFile{
		ActiveEnvironmentKey: "dev",
		Environments: map[string]environmentRecord{
			"dev": {EnvironmentPath: environmentPath},
		},
	})
	runner := &fakeRunner{outputs: map[string]outputResponse{
		"list sites /xml": {output: []byte(`<appcmd><SITE SITE.NAME="Default Web Site" state="Stopped" /></appcmd>`)},
		"list vdir Default Web Site/ /text:physicalPath": {output: []byte(filepath.Join(home, "site-root"))},
		"list app /xml": {output: []byte(`<appcmd><APP APP.NAME="Default Web Site/Creatio" APPPOOL.NAME="CreatioPool" /></appcmd>`)},
		"list vdir Default Web Site/Creatio/ /text:physicalPath": {output: []byte(environmentPath)},
		"list apppool CreatioPool /xml":                          {output: []byte(`<appcmd><APPPOOL APPPOOL.NAME="CreatioPool" state="Stopped" /></appcmd>`)},
		"start apppool CreatioPool":                              {output: []byte("APPPOOL object " + `"CreatioPool"` + " added")},
		"start site Default Web Site":                            {output: []byte("SITE object " + `"Default Web Site"` + " added")},
	}}
	result, err := StartCreatio(context.Background(), StartOptions{
		Home: home, Platform: "windows", Runner: runner,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StartedBy != "iis" || result.IISSite != "Default Web Site" || result.IISAppPool != "CreatioPool" {
		t.Fatalf("start result = %#v", result)
	}
	wantCalls := []string{
		"appcmd.exe list sites /xml",
		"appcmd.exe list vdir Default Web Site/ /text:physicalPath",
		"appcmd.exe list app /xml",
		"appcmd.exe list vdir Default Web Site/Creatio/ /text:physicalPath",
		"appcmd.exe list apppool CreatioPool /xml",
		"appcmd.exe start apppool CreatioPool",
		"appcmd.exe start site Default Web Site",
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("appcmd calls = %v, want %v", runner.calls, wantCalls)
	}
}

func TestStartCreatioFallsBackToDotnetWhenIISIsUnavailable(t *testing.T) {
	home := t.TempDir()
	environmentPath := filepath.Join(home, "app")
	if err := os.Mkdir(environmentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(environmentPath, "Terrasoft.WebHost.dll"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSettingsFixture(t, home, settingsFile{
		Environments: map[string]environmentRecord{
			"qa": {EnvironmentPath: environmentPath},
		},
	})
	runner := &fakeRunner{}
	result, err := StartCreatio(context.Background(), StartOptions{
		Home: home, EnvironmentName: "qa", Platform: "windows", Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StartedBy != "process" || result.Environment != "qa" || len(runner.starts) != 1 {
		t.Fatalf("start result = %#v, process starts = %#v", result, runner.starts)
	}
}

func writeSettingsFixture(t *testing.T, home string, settings settingsFile) {
	t.Helper()
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "appsettings.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
