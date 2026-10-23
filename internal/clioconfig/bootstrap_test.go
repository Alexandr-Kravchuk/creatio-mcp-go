package clioconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// health142Case is one settings file and the check-settings-health answer clio 8.1.0.142 gave for it (its
// settings-file-path left out), plus the version and site-port range the file held afterwards.
type health142Case struct {
	Label   string         `json:"label"`
	Content string         `json:"content"`
	Report  map[string]any `json:"report"`
	After   *struct {
		SettingsVersion any `json:"SettingsVersion"`
		SitePortRange   any `json:"site-port-range"`
	} `json:"after"`
}

func loadHealth142(t *testing.T) []health142Case {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "health142.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []health142Case
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestCheckMatchesClio142Answers(t *testing.T) {
	for _, test := range loadHealth142(t) {
		t.Run(test.Label, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "appsettings.json")
			if err := os.WriteFile(path, []byte(test.Content), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := Check(path, true)
			if err != nil {
				t.Fatal(err)
			}
			if report.SettingsFilePath != path {
				t.Fatalf("settings-file-path = %q", report.SettingsFilePath)
			}
			encoded, _ := json.Marshal(report)
			var got map[string]any
			_ = json.Unmarshal(encoded, &got)
			delete(got, "settings-file-path")
			want := test.Report
			// Json.NET's parse errors name its own reader state; only the classification is compared.
			if issues, _ := want["issues"].([]any); len(issues) == 1 {
				if message, _ := issues[0].(map[string]any)["message"].(string); strings.HasPrefix(message, "appsettings.json could not be parsed:") {
					gotIssues, _ := got["issues"].([]any)
					if len(gotIssues) != 1 || !strings.HasPrefix(gotIssues[0].(map[string]any)["message"].(string), "appsettings.json could not be parsed:") {
						t.Fatalf("issues = %v, want a parse failure", got["issues"])
					}
					got["issues"], want["issues"] = nil, nil
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("report\n got %v\nwant %v", got, want)
			}
			if test.After == nil {
				return
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			if err := json.Unmarshal(bytes.TrimPrefix(after, []byte("\xEF\xBB\xBF")), &root); err != nil {
				t.Fatalf("file after the check: %v", err)
			}
			if version, wantVersion := root["SettingsVersion"], test.After.SettingsVersion; !sameNumber(version, wantVersion) {
				t.Fatalf("SettingsVersion after = %v, want %v", version, wantVersion)
			}
			var gotRange any
			if defaults, ok := root["deploy-creatio-defaults"].(map[string]any); ok {
				gotRange = defaults["site-port-range"]
			}
			if test.After.SitePortRange == nil {
				if gotRange != nil {
					t.Fatalf("site-port-range after = %v, want none", gotRange)
				}
			} else if !reflect.DeepEqual(gotRange, test.After.SitePortRange) {
				t.Fatalf("site-port-range after = %v, want %v", gotRange, test.After.SitePortRange)
			}
		})
	}
}

// sameNumber compares a JSON version as clio reads it: "3" and 3 are the same version.
func sameNumber(got, want any) bool {
	text := func(value any) string {
		encoded, _ := json.Marshal(value)
		return strings.Trim(string(encoded), `"`)
	}
	if want == nil {
		return got == nil
	}
	return text(got) == text(want)
}

func TestCheckCreatesMissingFileOnlyWhenRepairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clio", "appsettings.json")
	report, err := Check(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "file-missing" || len(report.Issues) != 1 || report.Issues[0].Code != "settings-file-missing" {
		t.Fatalf("report without repairs = %+v", report)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a check without repairs created the file: %v", err)
	}
	report, err = Check(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "healthy" || report.EnvironmentCount != 0 || report.CanExecuteEnvTools || len(report.Issues) != 0 {
		t.Fatalf("report with repairs = %+v", report)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root["SettingsVersion"] != float64(3) || root["Environments"] == nil {
		t.Fatalf("created file = %s", data)
	}
	again, err := Check(path, true)
	if err != nil || again.Status != "healthy" || len(again.RepairsApplied) != 0 {
		t.Fatalf("second check = %+v, %v", again, err)
	}
}
