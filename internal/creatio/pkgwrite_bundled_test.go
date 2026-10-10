package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pkgWriteBundledArchiveFile packs files (relative path -> content) into clio's .gz package format.
func pkgWriteBundledArchiveFile(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), strings.TrimSuffix(name, ".gz"))
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), name)
	if err := appWriteGzip(root, archive); err != nil {
		t.Fatal(err)
	}
	return archive
}

func pkgWriteFastBundled(t *testing.T) {
	t.Helper()
	savedInstall, savedBundled := appWriteInstallTimings, pkgWriteBundledTimings
	savedInitial, savedPoll := pkgWriteReadinessInitialDelay, pkgWriteReadinessPollInterval
	appWriteInstallTimings.logInterval = time.Millisecond
	pkgWriteBundledTimings.readinessTimeout, pkgWriteBundledTimings.pingDelay = 200*time.Millisecond, time.Millisecond
	pkgWriteReadinessInitialDelay, pkgWriteReadinessPollInterval = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		appWriteInstallTimings, pkgWriteBundledTimings = savedInstall, savedBundled
		pkgWriteReadinessInitialDelay, pkgWriteReadinessPollInterval = savedInitial, savedPoll
	})
}

// pkgWriteInstallServer fakes the package installer, the health ping and the bundled package's Ping.
type pkgWriteInstallServer struct {
	mu       sync.Mutex
	calls    []string
	packages string
	install  string
	ping     string
}

func (s *pkgWriteInstallServer) start(t *testing.T, netCore bool) *Client {
	t.Helper()
	logReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if route != "GetLogFile" {
			s.calls = append(s.calls, route+" "+string(body))
		}
		switch route {
		case "Login":
			_, _ = io.WriteString(w, `{"Code":0}`)
		case "UploadPackage", "CreateBackup":
			_, _ = io.WriteString(w, `{"success":true}`)
		case "GetLogFile":
			logReads++
			if logReads > 1 {
				_, _ = io.WriteString(w, "previous\ninstalled package\n")
				return
			}
			_, _ = io.WriteString(w, "previous\n")
		case "InstallPackage":
			_, _ = io.WriteString(w, s.install)
		case "SelectQuery":
			_, _ = io.WriteString(w, `{"success":true,"rows":[`+s.packages+`]}`)
		case "Ping":
			if strings.Contains(r.URL.Path, "HealthCheck") {
				return
			}
			_, _ = io.WriteString(w, s.ping)
		case "UnloadAppDomain", "RestartApp":
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me", IsNetCore: netCore})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (s *pkgWriteInstallServer) routes() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.calls))
	for _, call := range s.calls {
		names = append(names, strings.Fields(call)[0])
	}
	return strings.Join(names, ",")
}

func TestPkgWriteReadGzipEntryAndVersion(t *testing.T) {
	archive := pkgWriteBundledArchiveFile(t, "CrtDashboardsMigratorApp.gz", map[string]string{
		"descriptor.json":           `{"Descriptor":{"PackageVersion":"9.9.9.9"}}`,
		"Files/app-descriptor.json": "\uFEFF" + `{"Version":"1.2.0.5"}`,
	})
	version, diagnosis := pkgWriteBundledVersion(DashboardsMigratorPackage, archive)
	if diagnosis != "" || version.display() != "1.2.0.5" {
		t.Fatalf("version = %+v %q", version, diagnosis)
	}
	if _, found, err := pkgWriteReadGzipEntry(archive, "Files/missing.json"); found || err != nil {
		t.Fatalf("missing entry found=%v err=%v", found, err)
	}
	empty := pkgWriteBundledArchiveFile(t, "Empty.gz", map[string]string{"descriptor.json": "{}"})
	if _, diagnosis := pkgWriteBundledVersion(DashboardsMigratorPackage, empty); !strings.Contains(diagnosis, "contains no Files/app-descriptor.json") {
		t.Fatalf("no descriptor = %q", diagnosis)
	}
	if _, diagnosis := pkgWriteBundledVersion(DashboardsMigratorPackage, filepath.Join(t.TempDir(), "none.gz")); !strings.HasPrefix(diagnosis, "This clio installation does not carry the bundled CrtDashboardsMigratorApp archive") {
		t.Fatalf("absent = %q", diagnosis)
	}
	for text, want := range map[string]string{"1.2": "1.2", "1.2.3.4-rc.1": "1.2.3.4-rc.1", "1.2.3.4-IGNORE_ALL": "1.2.3.4"} {
		if version, ok := pkgWriteParsePackageVersion(text); !ok || version.display() != want {
			t.Fatalf("%s = %+v", text, version)
		}
	}
	if _, ok := pkgWriteParsePackageVersion("7"); ok {
		t.Fatal("one component is not a System.Version")
	}
	low, _ := pkgWriteParsePackageVersion("1.2")
	high, _ := pkgWriteParsePackageVersion("1.2.0")
	if low.compare(high) >= 0 {
		t.Fatal("1.2 sorts below 1.2.0 as System.Version does")
	}
}

func TestPkgWriteInstallGate(t *testing.T) {
	pkgWriteFastBundled(t)
	archive := pkgWriteBundledArchiveFile(t, "cliogate.gz", map[string]string{"descriptor.json": "{}"})
	server := &pkgWriteInstallServer{install: `{"success":true}`}
	client := server.start(t, false)
	result := client.InstallGate(context.Background(), archive)
	values := strings.Join(pkgWriteValues(result), "|")
	// The installation log arrives through the listener or the closing read, whichever sees it first.
	if result.ExitCode != 0 || !strings.HasPrefix(values, "None:Uploading...|None:Uploaded|Info:"+client.config.BaseURL+"|None:Backup process...|None:Backup completed|"+
		"None:Install cliogate.gz ...|None:Installation log:|None:installed package\n|") || !strings.HasSuffix(values, "|None:Done|None:Restart application...") {
		t.Fatalf("gate = %q", values)
	}
	if routes := server.routes(); routes != "Login,UploadPackage,CreateBackup,InstallPackage,UnloadAppDomain" {
		t.Fatalf("routes = %s", routes)
	}
	server.mu.Lock()
	install := server.calls[3]
	server.mu.Unlock()
	if install != `InstallPackage "cliogate.gz"` {
		t.Fatalf("install body = %s", install)
	}
	if client.GateArchiveName() != "cliogate.gz" {
		t.Fatal("framework archive name")
	}

	failing := &pkgWriteInstallServer{install: `{"success":false,"errorInfo":{"message":"denied"}}`}
	result = failing.start(t, true).InstallGate(context.Background(), archive)
	if result.ExitCode != 1 || result.Messages[len(result.Messages)-1].Value != "Error" ||
		!strings.Contains(failing.routes(), "RestartApp") {
		t.Fatalf("failing gate = %q (%s)", pkgWriteValues(result), failing.routes())
	}
	missing := (&pkgWriteInstallServer{}).start(t, false).InstallGate(context.Background(), filepath.Join(t.TempDir(), "cliogate.gz"))
	if missing.ExitCode != 1 || strings.Join(pkgWriteValues(missing), "|") != "None:Specified package not found by path "+filepath.Join(filepath.Dir(missing.Messages[0].Value[len("Specified package not found by path "):]), "cliogate.gz")+"|Error:Error" {
		t.Fatalf("missing = %q", pkgWriteValues(missing))
	}
}

func TestPkgWriteInstallDashboardsMigrator(t *testing.T) {
	pkgWriteFastBundled(t)
	archive := pkgWriteBundledArchiveFile(t, "CrtDashboardsMigratorApp.gz", map[string]string{"Files/app-descriptor.json": `{"Version":"1.2.0.5"}`})
	ok := &pkgWriteInstallServer{install: `{"success":true}`, packages: `{"Name":"CrtDashboardsMigratorApp","Version":"1.2.0.5"}`,
		ping: `{"PingResult":{"success":true}}`}
	result := ok.start(t, false).InstallDashboardsMigrator(context.Background(), archive)
	values := pkgWriteValues(result)
	joined := strings.Join(values, "|")
	if result.ExitCode != 0 || values[len(values)-1] != "None:Done" || !strings.Contains(joined, "Info:Waiting 0 seconds for server to start...|Info:Checking WebHost ") ||
		!strings.Contains(joined, "Info:\tWebHost - OK|Info:Server is ready after 1 attempt(s).") {
		t.Fatalf("install = %q", values)
	}
	if routes := ok.routes(); routes != "Login,SelectQuery,UploadPackage,CreateBackup,InstallPackage,Ping,Ping" {
		t.Fatalf("routes = %s", routes)
	}

	newer := &pkgWriteInstallServer{packages: `{"Name":"CrtDashboardsMigratorApp","Version":"1.3.0.0"}`}
	result = newer.start(t, false).InstallDashboardsMigrator(context.Background(), archive)
	if result.ExitCode != 1 || len(result.Messages) != 1 || !strings.HasPrefix(result.Messages[0].Value,
		"Refusing: the environment carries CrtDashboardsMigratorApp 1.3.0.0, and this clio ships 1.2.0.5 — installing would move") ||
		!strings.HasSuffix(result.Messages[0].Value, "see 'clio install-dashboards-migrator -H'.") {
		t.Fatalf("newer = %q", pkgWriteValues(result))
	}

	suffixed := pkgWriteBundledArchiveFile(t, "CrtDashboardsMigratorApp.gz", map[string]string{"Files/app-descriptor.json": `{"Version":"1.2.0.5-beta"}`})
	result = (&pkgWriteInstallServer{}).start(t, false).InstallDashboardsMigrator(context.Background(), suffixed)
	if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[0].Value, "Refusing: this clio distribution declares CrtDashboardsMigratorApp 1.2.0.5-beta, but") {
		t.Fatalf("suffixed = %q", pkgWriteValues(result))
	}

	proxy := &pkgWriteInstallServer{install: `{"success":true}`, ping: `{"other":1}`}
	result = proxy.start(t, false).InstallDashboardsMigrator(context.Background(), archive)
	if result.ExitCode != 1 || !strings.Contains(result.Messages[len(result.Messages)-1].Value, "answered with a JSON body carrying no PingResult") {
		t.Fatalf("proxy = %q", pkgWriteValues(result))
	}

	absent := (&pkgWriteInstallServer{}).start(t, false).InstallDashboardsMigrator(context.Background(), filepath.Join(t.TempDir(), "CrtDashboardsMigratorApp.gz"))
	if absent.ExitCode != 1 || !strings.HasPrefix(absent.Messages[0].Value, "The bundled CrtDashboardsMigratorApp package was not found at '") {
		t.Fatalf("absent = %q", pkgWriteValues(absent))
	}
}
