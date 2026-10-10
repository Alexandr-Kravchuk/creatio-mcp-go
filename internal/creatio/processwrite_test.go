package creatio

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// processWriteCall is one request the process-write test server received.
type processWriteCall struct {
	Path string
	Body string
	Root string
}

// processWriteServer answers SysPackage reads with the given CrtProcessBuilder version ("" = not installed)
// and every other route through respond.
func processWriteServer(t *testing.T, version string, respond func(call processWriteCall) (int, string)) (*Client, *[]processWriteCall) {
	t.Helper()
	calls := []processWriteCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		call := processWriteCall{Path: r.URL.Path, Body: string(raw)}
		if strings.HasSuffix(r.URL.Path, "/SelectQuery") {
			var query struct {
				Root string `json:"rootSchemaName"`
			}
			_ = json.Unmarshal(raw, &query)
			call.Root = query.Root
		}
		calls = append(calls, call)
		if call.Root == "SysPackage" {
			rows := `{"Name":"Base","Version":"8.1.0"}`
			if version != "" {
				rows += `,{"Name":"crtprocessbuilder","Version":"` + version + `"}`
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[` + rows + `]}`))
			return
		}
		status, body := respond(call)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), &calls
}

// processWriteNoArchive hides the installed clio's archive so the convergence check stays out of a test.
func processWriteNoArchive(t *testing.T) {
	t.Helper()
	previous := processWriteArchiveGlob
	processWriteArchiveGlob = func() []string { return nil }
	t.Cleanup(func() { processWriteArchiveGlob = previous })
}

// processWriteArchive writes a clio package archive with the given entries and points the glob at it.
func processWriteArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	var packed bytes.Buffer
	writer := gzip.NewWriter(&packed)
	for name, content := range entries {
		units := utf16.Encode([]rune(name))
		_ = binary.Write(writer, binary.LittleEndian, int32(len(units)))
		_ = binary.Write(writer, binary.LittleEndian, units)
		_ = binary.Write(writer, binary.LittleEndian, int32(len(content)))
		_, _ = writer.Write([]byte(content))
	}
	_ = writer.Close()
	path := filepath.Join(t.TempDir(), ".store", "clio", "9.0.0.1", "clio", "9.0.0.1", "tools", "net10.0", "any", "CrtProcessBuilder", "CrtProcessBuilder.gz")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, packed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := processWriteArchiveGlob
	processWriteArchiveGlob = func() []string { return []string{path} }
	t.Cleanup(func() { processWriteArchiveGlob = previous })
	return path
}

func processWriteMessages(result ProcessWriteResult) []string {
	texts := []string{}
	for _, message := range result.Messages {
		texts = append(texts, message.MessageType+": "+message.Value)
	}
	return texts
}

func TestProcessWriteGateRefusesMissingAndOlderPackageWithVersionWording(t *testing.T) {
	processWriteNoArchive(t)
	for _, version := range []string{"", "1.6.0.9", "not-a-version"} {
		client, calls := processWriteServer(t, version, func(processWriteCall) (int, string) { return 500, "" })
		result := client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionName: "UsrP"})
		want := "To use this command, you need to install the CrtProcessBuilder package version 1.6.1.0 or higher. Install or update the package in the target environment and retry.\n" + processWriteInstallHint
		if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].Value != want || result.Note != "" {
			t.Fatalf("version %q: %+v", version, result)
		}
		for _, call := range *calls {
			if strings.Contains(call.Path, "ProcessDesignService") {
				t.Fatalf("version %q: the service was called after the gate refused", version)
			}
		}
	}
}

func TestProcessWriteGateRefusesAnEnvironmentOlderThanTheBundledArchive(t *testing.T) {
	processWriteArchive(t, map[string]string{"Files/x.cs": "x", "descriptor.json": "\xef\xbb\xbf" + `{"Descriptor":{"PackageVersion":"1.7.0.5"}}`})
	client, _ := processWriteServer(t, "1.6.9.0", func(processWriteCall) (int, string) { return 500, "" })
	result := client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionName: "UsrP"})
	want := "This clio carries CrtProcessBuilder 1.7.0.5, but the target environment has 1.6.9.0. Update the package in the target environment and retry.\n" + processWriteInstallHint
	if result.ExitCode != 1 || result.Messages[0].Value != want {
		t.Fatalf("%+v", result)
	}
	version, ok := processWriteBundledVersion()
	if !ok || version.text != "1.7.0.5" {
		t.Fatalf("bundled version = %+v, %v", version, ok)
	}
}

func TestSetActiveBusinessProcessVersionReportsTheActivatedVersion(t *testing.T) {
	processWriteNoArchive(t)
	client, calls := processWriteServer(t, "1.6.6.80", func(call processWriteCall) (int, string) {
		return 200, `{"SetActiveProcessVersionResult":{"success":true,"activeVersionName":"usrp_v2","activeVersionSchemaUId":"AAAAAAAA-0000-0000-0000-000000000002","deactivationFailureCount":1,"warnings":["w1"]}}`
	})
	result := client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionName: "UsrP_v2"})
	got := processWriteMessages(result)
	want := []string{
		"Info: Activating version 'UsrP_v2' on 'dev'...",
		"Info: Version 'usrp_v2' is now the actual one (UId: AAAAAAAA-0000-0000-0000-000000000002).",
		"Info: New process instances will start on this version. Instances already running stay on the version they started with — activation never migrates them.",
		"Warning: 1 sibling(s) are ALSO still flagged active, so which one runs is decided by package order rather than by this call. Read the family back with describe-business-process before reporting the rollback as done.",
		"Warning: w1",
	}
	if result.ExitCode != 0 || result.Note != processWriteCompileNote || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%d %q\n%s", result.ExitCode, result.Note, strings.Join(got, "\n"))
	}
	last := (*calls)[len(*calls)-1]
	if last.Path != "/0/rest/ProcessDesignService/SetActiveProcessVersion" || last.Body != `{"request":{"name":"UsrP_v2"}}` {
		t.Fatalf("request = %+v", last)
	}
}

func TestSetActiveBusinessProcessVersionWarnsWhenTheReportedVersionDiffers(t *testing.T) {
	processWriteNoArchive(t)
	client, calls := processWriteServer(t, "1.6.6.80", func(call processWriteCall) (int, string) {
		return 200, `{"setactiveprocessversionresult":{"success":true,"activeVersionName":"Other","activeVersionSchemaUId":"bbbbbbbb-0000-0000-0000-000000000002"}}`
	})
	result := client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionUID: "aaaaaaaa-0000-0000-0000-000000000002"})
	got := processWriteMessages(result)
	if result.ExitCode != 0 || len(got) != 4 || !strings.HasPrefix(got[3], "Warning: The environment reports 'Other' (UId: bbbbbbbb-0000-0000-0000-000000000002) as the actual version, which is NOT") {
		t.Fatalf("%+v", got)
	}
	if body := (*calls)[len(*calls)-1].Body; body != `{"request":{"uid":"aaaaaaaa-0000-0000-0000-000000000002"}}` {
		t.Fatalf("body = %s", body)
	}
	client, _ = processWriteServer(t, "1.6.6.80", func(call processWriteCall) (int, string) {
		return 200, `{"SetActiveProcessVersionResult":{"success":true,"activeVersionSchemaUId":"not-a-guid"}}`
	})
	result = client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionUID: "aaaaaaaa-0000-0000-0000-000000000002"})
	if got := processWriteMessages(result); result.ExitCode != 0 || !strings.HasPrefix(got[3], "Warning: The activation was accepted, but the environment did not report") {
		t.Fatalf("%+v", got)
	}
}

func TestSetActiveBusinessProcessVersionFailureTexts(t *testing.T) {
	processWriteNoArchive(t)
	cases := map[string]string{
		`{"SetActiveProcessVersionResult":{"success":false,"errorMessage":"Nope.","activeVersionName":"UsrP","activeVersionSchemaUId":"u1","deactivationFailureCount":2,"warnings":["a","b"]}}`: "Nope. The version the environment reports as actual is 'UsrP' (UId: u1). 2 sibling(s) are ALSO still flagged active, so which one runs is decided by package order rather than by this call. a b",
		`{"SetActiveProcessVersionResult":{"success":false}}`: "SetActiveProcessVersion failed.",
		`null`:        "SetActiveProcessVersion returned an empty response.",
		`{"Other":1}`: "SetActiveProcessVersion returned an unexpected response shape.",
		`{"SetActiveProcessVersionResult":{"success":"yes"}}`: processWriteActivateUnknown,
		`[1]`: processWriteActivateUnknown,
		``:    processWriteActivateUnknown,
	}
	for body, want := range cases {
		client, _ := processWriteServer(t, "1.6.1.0", func(call processWriteCall) (int, string) { return 200, body })
		result := client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionName: "UsrP"})
		if result.ExitCode != 1 || len(result.Messages) != 2 || result.Messages[1].MessageType != "Error" || result.Messages[1].Value != want || result.Note != "" {
			t.Errorf("body %s: %+v", body, result)
		}
	}
}

func TestProcessWriteVersionCompareFollowsPackageVersion(t *testing.T) {
	parse := func(text string) processWriteVersion {
		version, ok := processWriteParseVersion(text)
		if !ok {
			t.Fatalf("%q did not parse", text)
		}
		return version
	}
	if parse("1.6.6.77").compare(parse("1.6.6.77")) != 0 || parse("1.6").compare(parse("1.6.0")) >= 0 ||
		parse("1.6.6.77-rc").compare(parse("1.6.6.77")) <= 0 || parse("2.0.0.0").compare(parse("10.0.0.0")) >= 0 {
		t.Fatal("version order differs from PackageVersion")
	}
	for _, bad := range []string{"", "1", "1.2.3.4.5", "a.b", "-1.0"} {
		if _, ok := processWriteParseVersion(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
