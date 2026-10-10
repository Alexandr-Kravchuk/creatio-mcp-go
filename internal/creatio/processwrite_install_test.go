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

func processWriteFastInstall(t *testing.T) {
	t.Helper()
	saved, savedApp := processWriteInstallTimings, appWriteInstallTimings
	processWriteInstallTimings.initialDelay, processWriteInstallTimings.pollInterval = time.Millisecond, time.Millisecond
	processWriteInstallTimings.readyTimeout, processWriteInstallTimings.pingDelay = 50*time.Millisecond, time.Millisecond
	appWriteInstallTimings.logInterval = time.Millisecond
	t.Cleanup(func() { processWriteInstallTimings, appWriteInstallTimings = saved, savedApp })
}

// processWriteInstallServer is a Creatio that accepts the upload and install, answers its health route with health
// and the CrtProcessBuilder Ping with ping.
func processWriteInstallServer(t *testing.T, installed string, health int, ping string) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	routes := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		routes = append(routes, r.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			_, _ = io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			var query struct {
				Root string `json:"rootSchemaName"`
			}
			_ = json.Unmarshal(body, &query)
			rows := ""
			if installed != "" {
				rows = `{"Name":"CrtProcessBuilder","Version":"` + installed + `"}`
			}
			_, _ = io.WriteString(w, `{"success":true,"rows":[`+rows+`]}`)
		case strings.HasSuffix(r.URL.Path, "/api/HealthCheck/Ping"):
			w.WriteHeader(health)
		case strings.HasSuffix(r.URL.Path, "/ProcessDesignService/Ping"):
			_, _ = io.WriteString(w, ping)
		case strings.HasSuffix(r.URL.Path, "/GetLogFile"):
			_, _ = io.WriteString(w, "")
		default:
			_, _ = io.WriteString(w, `{"success":true}`)
		}
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), &routes
}

func processWriteInstallMessages(result CommandResult) []string {
	texts := []string{}
	for _, message := range result.Messages {
		texts = append(texts, message.MessageType+": "+message.Value)
	}
	return texts
}

func TestInstallProcessBuilderInstallsWaitsAndPings(t *testing.T) {
	processWriteFastInstall(t)
	processWriteArchive(t, map[string]string{"descriptor.json": `{"Descriptor":{"PackageVersion":"1.6.6.80","Name":"CrtProcessBuilder"}}`})
	client, routes := processWriteInstallServer(t, "1.6.6.70", 200, `{"PingResult":{"success":true}}`)
	result := client.InstallProcessBuilder(context.Background())
	got := processWriteInstallMessages(result)
	if result.ExitCode != 0 || got[len(got)-1] != "None: Done" {
		t.Fatalf("%d\n%s", result.ExitCode, strings.Join(got, "\n"))
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"None: Uploading...", "Info: Waiting 0 seconds for server to start...", "Info: Checking WebHost ", "/0/api/HealthCheck/Ping ...", "Info: \tWebHost - OK", "Info: Server is ready after 1 attempt(s)."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in\n%s", want, joined)
		}
	}
	installedFile := false
	for _, route := range *routes {
		if strings.HasSuffix(route, "/InstallAppFromFile") {
			installedFile = true
		}
	}
	if !installedFile {
		t.Fatalf("routes %v", *routes)
	}
}

func TestInstallProcessBuilderRefusals(t *testing.T) {
	processWriteFastInstall(t)
	processWriteArchive(t, map[string]string{"descriptor.json": `{"Descriptor":{"PackageVersion":"1.6.6.80"}}`})
	client, routes := processWriteInstallServer(t, "1.7.0.0", 200, `{}`)
	result := client.InstallProcessBuilder(context.Background())
	if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[0].Value, "Refusing: the environment carries CrtProcessBuilder 1.7.0.0, and this clio ships 1.6.6.80") ||
		!strings.HasSuffix(result.Messages[0].Value, "Update clio instead. A command-line override exists for the case where the rollback is what you want; see 'clio install-process-builder -H'.") {
		t.Fatalf("%+v", result)
	}
	for _, route := range *routes {
		if strings.Contains(route, "Upload") || strings.Contains(route, "Install") {
			t.Fatalf("a refused install reached %s", route)
		}
	}
	processWriteArchive(t, map[string]string{"descriptor.json": `{"Descriptor":{"PackageVersion":"1.6.6.80-beta"}}`})
	result = client.InstallProcessBuilder(context.Background())
	if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[0].Value, "Refusing: this clio distribution declares CrtProcessBuilder 1.6.6.80-beta") {
		t.Fatalf("%+v", result)
	}
	processWriteNoArchive(t)
	result = client.InstallProcessBuilder(context.Background())
	if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[0].Value, "The bundled CrtProcessBuilder package was not found at ") {
		t.Fatalf("%+v", result)
	}
}

func TestInstallProcessBuilderReportsAnUncompiledPackage(t *testing.T) {
	processWriteFastInstall(t)
	processWriteArchive(t, map[string]string{"descriptor.json": `{"Descriptor":{}}`})
	client, _ := processWriteInstallServer(t, "", 200, `{"PingResult":{"success":false}}`)
	result := client.InstallProcessBuilder(context.Background())
	got := processWriteInstallMessages(result)
	if result.ExitCode != 1 || !strings.HasPrefix(got[0], "Warning: The bundled CrtProcessBuilder archive at ") ||
		!strings.Contains(got[0], "has a descriptor.json without a readable Descriptor.PackageVersion. Reinstall or update clio itself. Installing anyway") ||
		!strings.HasPrefix(got[len(got)-1], "Error: CrtProcessBuilder's Ping route answered, but not with success") {
		t.Fatalf("%s", strings.Join(got, "\n"))
	}
	client, _ = processWriteInstallServer(t, "", 503, `{}`)
	result = client.InstallProcessBuilder(context.Background())
	got = processWriteInstallMessages(result)
	if result.ExitCode != 1 || !strings.Contains(strings.Join(got, "\n"), "Error: \tError: 503 Service Unavailable") ||
		!strings.HasPrefix(got[len(got)-2], "Warning: Server did not become ready within 0 seconds.") ||
		!strings.HasPrefix(got[len(got)-1], "Error: CrtProcessBuilder was installed, but the environment did not become ready") {
		t.Fatalf("%s", strings.Join(got, "\n"))
	}
}
