package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pkgWriteFastCompile(t *testing.T) {
	t.Helper()
	saved := pkgWriteCompileTimings
	pkgWriteCompileTimings.startupGrace = 150 * time.Millisecond
	pkgWriteCompileTimings.decisionInterval = 5 * time.Millisecond
	pkgWriteCompileTimings.quietFallback = 200 * time.Millisecond
	pkgWriteCompileTimings.fullTimeout = 5 * time.Second
	pkgWriteCompileTimings.historyPoll = 10 * time.Millisecond
	pkgWriteCompileTimings.reloadProbeInterval = 10 * time.Millisecond
	pkgWriteCompileTimings.reloadProbeTimeout = 30 * time.Millisecond
	pkgWriteCompileTimings.packagePause = 5 * time.Millisecond
	pkgWriteCompileTimings.packageBudget = 2 * time.Second
	pkgWriteCompileTimings.packageSettleWindow = 60 * time.Millisecond
	pkgWriteCompileTimings.packageQuietFallback = 300 * time.Millisecond
	pkgWriteCompileTimings.pollGiveUp = 100 * time.Millisecond
	pkgWriteCompileTimings.pollBackoff = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { pkgWriteCompileTimings = saved })
}

// pkgWriteCompileServer is a fake Creatio for the compile paths. history rows are released one per call of
// the history query once release() is called; build answers the build request.
type pkgWriteCompileServer struct {
	mu        sync.Mutex
	calls     []string
	history   []string // JSON rows the history query returns (after the baseline query)
	build     func(w http.ResponseWriter, r *http.Request, body string)
	packages  string
	process   func(body string) (int, string)
	verdict   func() (int, string)
	reachable func() bool
}

func (s *pkgWriteCompileServer) start(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path+" "+string(body))
		history, packages, build, process, verdict, reachable := s.history, s.packages, s.build, s.process, s.verdict, s.reachable
		s.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			_, _ = io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery") && strings.Contains(string(body), `"CompilationHistory"`):
			if strings.Contains(string(body), `"rowCount":1`) {
				_, _ = io.WriteString(w, `{"success":true,"rows":[{"Id":"base","CreatedOn":"2026-10-10T10:00:00.000","ProjectName":"Old","DurationInSeconds":1,"ErrorsWarnings":"[]","Result":true}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"rows":[`+strings.Join(history, ",")+`]}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery") && strings.Contains(string(body), `"SysPackage"`):
			_, _ = io.WriteString(w, `{"success":true,"rows":[`+packages+`]}`)
		case strings.HasSuffix(r.URL.Path, "/GetLastCompilationResult"):
			if r.Header.Get("Cookie") == "" && reachable != nil && !reachable() {
				time.Sleep(100 * time.Millisecond)
			}
			if verdict == nil {
				w.WriteHeader(204)
				return
			}
			status, text := verdict()
			w.WriteHeader(status)
			_, _ = io.WriteString(w, text)
		case strings.HasSuffix(r.URL.Path, "/Rebuild"), strings.HasSuffix(r.URL.Path, "/RebuildPackage"):
			build(w, r, string(body))
		case strings.HasSuffix(r.URL.Path, "/CompileProcess"):
			status, text := process(string(body))
			w.WriteHeader(status)
			_, _ = io.WriteString(w, text)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL)
}

func (s *pkgWriteCompileServer) set(update func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update()
}

func (s *pkgWriteCompileServer) recorded(substring string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []string
	for _, call := range s.calls {
		if strings.Contains(call, substring) {
			matched = append(matched, call)
		}
	}
	return matched
}

func answerWith(text string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = io.WriteString(w, text) }
}

func TestPkgWriteFullCompileReadsTheResponse(t *testing.T) {
	pkgWriteFastCompile(t)
	server := &pkgWriteCompileServer{build: answerWith(`{"success":true,"buildResult":0}`)}
	result := server.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity"})
	values := pkgWriteValues(result)
	if result.ExitCode != 0 || len(values) != 6 || values[0] != "None:"+pkgWriteCompileSeparator ||
		!strings.HasPrefix(values[1], "Info:At: ") || !strings.HasSuffix(values[1], " Starting compilation...") || values[2] != "None:" ||
		values[3] != "None:" || values[4] != "Info:Compilation finished in 00:00:00" || values[5] != "None:"+pkgWriteCompileSeparator {
		t.Fatalf("full = %q", values)
	}
	if calls := server.recorded("/Rebuild"); len(calls) != 1 || calls[0] != "POST /0/ServiceModel/WorkspaceExplorerService.svc/Rebuild {}" {
		t.Fatalf("rebuild calls = %q", calls)
	}

	failing := &pkgWriteCompileServer{build: answerWith(`{"success":false,"buildResult":2,"errorInfo":{"errorCode":"E","message":"broken"},` +
		`"errors":[{"errorNumber":"CS1002","errorText":"; expected","fileName":"A.cs","line":3,"column":7},{"errorNumber":"CS0168","errorText":"unused","warning":true}]}`)}
	result = failing.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity"})
	values = pkgWriteValues(result)
	if result.ExitCode != 1 || strings.Join(values[3:], "|") != "Error:Compilation failed on the environment. Build result: 2.|Error:E: broken|Error:(CS1002) in A.cs at (3,7): ; expected" {
		t.Fatalf("failed = %q", values)
	}

	for body, want := range map[string]string{
		"":            "Error:Empty response received from server during compilation.",
		"<html>":      "Error:Server returned non-JSON response during compilation.",
		`{"success":`: "Error:The compilation response is not valid JSON; it may have been cut off.",
		`{"other":1}`: "Error:The compilation response carried no build result.",
	} {
		server := &pkgWriteCompileServer{build: answerWith(body)}
		result := server.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity"})
		values := pkgWriteValues(result)
		if result.ExitCode != 1 || values[3] != want || !strings.HasPrefix(values[4], "Error:Endpoint: http://") {
			t.Fatalf("%q = %q", body, values)
		}
	}
}

func TestPkgWriteFullCompileObservesTheReload(t *testing.T) {
	pkgWriteFastCompile(t)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	server := &pkgWriteCompileServer{
		build: func(w http.ResponseWriter, r *http.Request, _ string) {
			select {
			case <-hold:
			case <-r.Context().Done():
			}
		},
		verdict: func() (int, string) {
			return 200, `{"success":false,"buildResult":1,"errors":[{"errorNumber":"CS0103","errorText":"name missing","fileName":"B.cs","line":1,"column":2,"warning":false}]}`
		},
	}
	client := server.start(t)
	var probes atomic.Int32
	server.set(func() {
		server.history = []string{`{"Id":"r1","CreatedOn":"2026-10-10T10:00:05.000","ProjectName":"Terrasoft.Configuration.Dev.csproj","DurationInSeconds":12,"ErrorsWarnings":"[]","Result":true}`}
		server.reachable = func() bool { count := probes.Add(1); return count < 3 || count > 7 }
	})
	result := client.Compile(context.Background(), CompileRequest{EnvironmentName: "parity"})
	values := strings.Join(pkgWriteValues(result), "|")
	if result.ExitCode != 1 ||
		!strings.Contains(values, "Info:At: 10:00:05 after: \x1b[31m12\x1b[0m sec. \x1b[34mTerrasoft.Configuration.Dev.csproj\x1b[0m\x1b[32m <============\x1b[0m") ||
		!strings.Contains(values, "The environment reloaded and is answering again - a configuration build ends by reloading the application. Reading the compilation result.") ||
		!strings.HasSuffix(values, "Error:Compilation failed on the environment. Build result: 1.|Error:(CS0103) in B.cs at (1,2): name missing") {
		t.Fatalf("reload = %q", values)
	}
}

func TestPkgWriteFullCompileTransportFailure(t *testing.T) {
	pkgWriteFastCompile(t)
	server := &pkgWriteCompileServer{build: func(w http.ResponseWriter, _ *http.Request, _ string) {
		hijacker := w.(http.Hijacker)
		connection, _, _ := hijacker.Hijack()
		_ = connection.Close()
	}}
	result := server.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity"})
	values := pkgWriteValues(result)
	if result.ExitCode != 1 || values[3] != "Error:The compilation request failed and the environment never started building: no compilation history was written after the request was sent." ||
		!strings.HasPrefix(values[4], "Error:Request error: ") || !strings.HasPrefix(values[5], "Error:Endpoint: ") ||
		values[6] != "Error:Check the environment URI, the IsNetCore flag, and the credentials." {
		t.Fatalf("transport = %q", values)
	}
}

func TestPkgWritePackageCompile(t *testing.T) {
	pkgWriteFastCompile(t)
	server := &pkgWriteCompileServer{build: answerWith(`{"success":true}`), history: []string{
		`{"Id":"p1","CreatedOn":"2026-10-10T10:00:05.000","ProjectName":"UsrPkg","DurationInSeconds":0,"ErrorsWarnings":"[]","Result":true}`}}
	result := server.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", PackageName: "UsrPkg"})
	if result.ExitCode != 0 || strings.Join(pkgWriteValues(result), "|") !=
		"None:Start rebuild packages (UsrPkg).|Info:Compilation history: UsrPkg built in 0 s, succeeded|None:End rebuild packages (UsrPkg).|Info:Done" {
		t.Fatalf("package = %q", pkgWriteValues(result))
	}
	if calls := server.recorded("/RebuildPackage"); len(calls) != 1 || calls[0] != `POST /0/ServiceModel/WorkspaceExplorerService.svc/RebuildPackage { "packageName":"UsrPkg" }` {
		t.Fatalf("rebuild calls = %q", calls)
	}

	failing := &pkgWriteCompileServer{build: answerWith(`{"success":false,"buildResult":1,"errorInfo":{"message":"Build failed."},` +
		`"errors":[{"errorNumber":"CS1002","errorText":"; expected","fileName":"X.cs","line":3,"column":5}]}`)}
	result = failing.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", PackageName: "UsrPkg"})
	if result.ExitCode != 1 || strings.Join(pkgWriteValues(result), "|") != "None:Start rebuild packages (UsrPkg).|Error:(CS1002) in X.cs at (3,5): ; expected|"+
		"Error:Package compilation failed for 'UsrPkg' (build result 1). Creatio reported: Build failed. The new code was not loaded: "+
		"the environment keeps running the previous build until the errors are fixed and the package is compiled again." {
		t.Fatalf("failing package = %q", pkgWriteValues(result))
	}

	silent := &pkgWriteCompileServer{build: answerWith(`{"success":true}`)}
	pkgWriteCompileTimings.packageBudget = 150 * time.Millisecond
	result = silent.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", PackageName: "UsrPkg"})
	values := pkgWriteValues(result)
	if result.ExitCode != 1 || values[1] != "Error:The environment accepted the build of 'UsrPkg' but wrote no compilation history within 0 s, "+
		"so there is no evidence it finished. The build may still be running on the environment; check `clio last-compilation-log` before compiling again." {
		t.Fatalf("silent package = %q", values)
	}

	login := &pkgWriteCompileServer{build: answerWith(`<html>login</html>`)}
	result = login.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", PackageName: "UsrPkg"})
	if values := pkgWriteValues(result); result.ExitCode != 1 || !strings.HasPrefix(values[1], "Error:The environment answered the build request for 'UsrPkg' with a response that is not a build result") {
		t.Fatalf("login package = %q", values)
	}
}

func TestPkgWriteProcessCompile(t *testing.T) {
	pkgWriteFastCompile(t)
	missing := &pkgWriteCompileServer{packages: `{"Name":"CrtProcessBuilder","UId":"x","Version":"1.6.6.32"}`}
	result := missing.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", ProcessName: "UsrProcess"})
	if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].Value != "To use this command, you need to install the CrtProcessBuilder package version 1.6.6.33 or higher. "+
		"Install or update the package in the target environment and retry.\nRun 'clio install-process-builder -e <environment>' (or call the install-process-builder MCP tool) to install or update CrtProcessBuilder." {
		t.Fatalf("gate = %+v", result)
	}
	if calls := missing.recorded("/CompileProcess"); len(calls) != 0 {
		t.Fatalf("the gate must stop the compile: %q", calls)
	}

	installed := `{"Name":"CrtProcessBuilder","UId":"x","Version":"1.6.6.33"}`
	compiled := &pkgWriteCompileServer{packages: installed, process: func(string) (int, string) {
		return 200, `{"CompileProcessResult":{"success":true,"processName":"UsrProcess","packageName":"UsrPkg","packageType":"assembly","compileRequired":true,"compiled":true,"durationMs":201000,"errors":[],"errorCount":0}}`
	}}
	result = compiled.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", ProcessName: "UsrProcess"})
	if result.ExitCode != 0 || strings.Join(pkgWriteValues(result), "|") !=
		"Info:Compiling the package of process 'UsrProcess' on 'parity'. A compile reloads the runtime for every user of the environment and usually takes a few minutes...|"+
			"Info:Compiled package 'UsrPkg' (its own assembly) in 3:21. Verify on a run that process 'UsrProcess' executes the saved code; on a .NET (Core) host restart the application first, as after any compile." {
		t.Fatalf("compiled = %q", pkgWriteValues(result))
	}
	if calls := compiled.recorded("/CompileProcess"); len(calls) != 1 || calls[0] != `POST /0/rest/ProcessDesignService/CompileProcess {"request":{"name":"UsrProcess"}}` {
		t.Fatalf("compile calls = %q", calls)
	}

	interpreted := &pkgWriteCompileServer{packages: installed, process: func(string) (int, string) {
		return 200, `{"CompileProcessResult":{"success":true,"processName":"UsrProcess","compileRequired":false}}`
	}}
	result = interpreted.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", ProcessName: "UsrProcess"})
	if result.ExitCode != 0 || result.Messages[1].Value != "Process 'UsrProcess' carries no C# (no script task and no process methods), so nothing was compiled and nothing needs to be." {
		t.Fatalf("interpreted = %q", pkgWriteValues(result))
	}

	broken := &pkgWriteCompileServer{packages: installed, process: func(string) (int, string) {
		return 200, `{"CompileProcessResult":{"success":false,"errorMessage":"Compilation failed","compiled":true,"errorCount":3,"errors":[` +
			`{"fileName":"UsrProcess.cs","line":4,"column":9,"code":"CS0103","message":"x missing","inThisProcess":true},` +
			`{"fileName":"Other.cs","line":1,"column":1,"code":"CS1002","message":"; expected","inThisProcess":false}]}}`
	}}
	result = broken.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", ProcessName: "UsrProcess"})
	values := pkgWriteValues(result)
	fence := func(text string) string {
		return "[untrusted-source-text begin] " + text + " [untrusted-source-text end]"
	}
	if result.ExitCode != 1 || strings.Join(values[1:], "|") != "Error:CompileProcess failed: "+fence("Compilation failed")+"|"+
		"Error:"+fence("UsrProcess.cs(4,9): CS0103")+" "+fence("x missing")+"|"+
		"Error:"+fence("Other.cs(1,1): CS1002")+" "+fence("; expected")+" [another schema of the package]|"+
		"Error:... and 1 more error(s) not listed.|"+
		"Error:Fix what the errors above name, then ask the user again before the next compile: their answer covered this compile only, and every compile reloads the runtime for every user of the environment." {
		t.Fatalf("broken = %q", values)
	}

	for body, want := range map[string]string{
		``: "CompileProcess returned an empty body, so whether the package was compiled is UNKNOWN.",
		`<html><title>404 - Not Found</title></html>`: "CompileProcess was answered with an HTTP 404 error page",
		`{"other":true}`: "CompileProcess returned a response without its result, so whether the package was compiled is UNKNOWN.",
		`[1]`:            "CompileProcess returned a response clio could not read, so whether the package was compiled is UNKNOWN.",
		`{"Code":7,"Message":"denied","success":false,"errorInfo":{"message":"denied"}}`: "CompileProcess answered with a Creatio error",
	} {
		server := &pkgWriteCompileServer{packages: installed, process: func(string) (int, string) { return 200, body }}
		result := server.start(t).Compile(context.Background(), CompileRequest{EnvironmentName: "parity", ProcessName: "UsrProcess"})
		if result.ExitCode != 1 || len(result.Messages) != 2 || !strings.HasPrefix(result.Messages[1].Value, want) {
			t.Fatalf("%q = %q", body, pkgWriteValues(result))
		}
	}
}
