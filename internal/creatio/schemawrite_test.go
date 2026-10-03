package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSchemaWriteBodyPreservesDesignerMetadata(t *testing.T) {
	for _, kind := range []schemaWriteKind{schemaWriteSourceCode, schemaWriteSQLScript} {
		for _, reject := range []bool{false, true} {
			t.Run(kind.managerName+map[bool]string{false: "Success", true: "Refused"}[reject], func(t *testing.T) {
				saved := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/ServiceModel/AuthService.svc/Login":
						io.WriteString(w, `{"Code":0}`)
					case "/0/" + schemaWriteSelectRoute:
						var q map[string]any
						json.NewDecoder(r.Body).Decode(&q)
						root := q["rootSchemaName"]
						if kind == schemaWriteSQLScript && root != "VwSysSqlScriptInPackage" {
							t.Errorf("SQL root %v", root)
						}
						io.WriteString(w, `{"success":true,"rows":[{"UId":"00000000-0000-0000-0000-000000000001"}]}`)
					case "/0/" + kind.getRoute:
						var q map[string]any
						json.NewDecoder(r.Body).Decode(&q)
						if q["schemaUId"] != "00000000-0000-0000-0000-000000000001" || q["useFullHierarchy"] != false {
							t.Errorf("get args %v", q)
						}
						io.WriteString(w, `{"schema":{"name":"UsrParityProbe","body":"old","customProperty":{"retain":true}}}`)
					case "/0/" + kind.saveRoute:
						saved = true
						var q map[string]any
						json.NewDecoder(r.Body).Decode(&q)
						if q["body"] != "a😀" || q["customProperty"].(map[string]any)["retain"] != true {
							t.Errorf("save args %v", q)
						}
						if reject {
							io.WriteString(w, `{"success":false,"validationErrors":[{"message":"locked"}]}`)
						} else {
							io.WriteString(w, `{"success":true}`)
						}
					default:
						t.Errorf("unexpected %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				client, err := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
				if err != nil {
					t.Fatal(err)
				}
				body := "a😀"
				result := client.schemaWriteUpdateBody(context.Background(), SchemaBodyUpdateRequest{SchemaName: "UsrParityProbe", Body: &body}, kind)
				if !saved || result.Success == reject {
					t.Fatalf("result %#v saved %v", result, saved)
				}
				if reject && result.Error != "locked" {
					t.Fatal(result.Error)
				}
				if !reject && result.BodyLength != 3 {
					t.Fatal(result.BodyLength)
				}
			})
		}
	}
}
func TestSchemaWriteDryRunNeverLoadsOrSaves(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		if r.URL.Path != "/0/"+schemaWriteSelectRoute {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		io.WriteString(w, `{"success":true,"rows":[{"UId":"probe"}]}`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	body := "new"
	result := client.UpdateSourceCodeSchema(context.Background(), SchemaBodyUpdateRequest{SchemaName: "UsrParityProbe", Body: &body, DryRun: true})
	if !result.Success || !result.DryRun {
		t.Fatalf("%#v", result)
	}
}
func TestSchemaWriteDoesNotCreateWhenAbsenceIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		if r.URL.Path != "/0/"+schemaWriteSelectRoute {
			t.Errorf("unexpected mutation %s", r.URL.Path)
		}
		io.WriteString(w, `{"success":false,"errorInfo":{"message":"denied"}}`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	problem := client.schemaWriteRequireAbsent(context.Background(), "UsrParityProbe", schemaWriteSourceCode)
	if !strings.Contains(problem, "Could not check") || !strings.Contains(problem, "denied") {
		t.Fatal(problem)
	}
}

func TestSchemaWriteCreateAndInstallRequests(t *testing.T) {
	for _, tool := range []string{"source", "sql", "install"} {
		for _, reject := range []bool{false, true} {
			t.Run(tool+map[bool]string{false: "Success", true: "Failure"}[reject], func(t *testing.T) {
				saved := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/Login"):
						io.WriteString(w, `{"Code":0}`)
					case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
						var q map[string]any
						json.NewDecoder(r.Body).Decode(&q)
						if q["rootSchemaName"] == "SysPackage" {
							io.WriteString(w, `{"success":true,"rows":[{"UId":"pkg"}]}`)
						} else if tool == "install" {
							io.WriteString(w, `{"success":true,"rows":[{"UId":"script"}]}`)
						} else {
							io.WriteString(w, `{"success":true,"rows":[]}`)
						}
					case strings.HasSuffix(r.URL.Path, "/CreateNewSchema"):
						var q map[string]any
						json.NewDecoder(r.Body).Decode(&q)
						if q["packageUId"] != "pkg" {
							t.Errorf("create args %v", q)
						}
						io.WriteString(w, `{"success":true,"schema":{"uId":"source","retained":true}}`)
					case strings.HasSuffix(r.URL.Path, "/SaveSchema"):
						saved = true
						var q map[string]any
						json.NewDecoder(r.Body).Decode(&q)
						if q["name"] != "UsrParityProbe" {
							t.Errorf("name %v", q)
						}
						if tool == "sql" && (q["dbEngineType"] != float64(2) || q["body"] != " ") {
							t.Errorf("SQL args %v", q)
						}
						if tool == "source" && (q["retained"] != true || q["caption"].([]any)[0].(map[string]any)["value"] != "UsrParityProbe") {
							t.Errorf("source args %v", q)
						}
						if reject {
							io.WriteString(w, `{"success":false,"errorInfo":{"message":"locked"}}`)
						} else {
							io.WriteString(w, `{"success":true}`)
						}
					case strings.HasSuffix(r.URL.Path, "/InstallSqlScripts"):
						saved = true
						raw, _ := io.ReadAll(r.Body)
						if string(raw) != `["script"]` {
							t.Errorf("install body %s", raw)
						}
						if reject {
							io.WriteString(w, `{"success":false,"errorInfo":{"message":"locked"}}`)
						} else {
							io.WriteString(w, `{"success":true}`)
						}
					default:
						io.WriteString(w, `{"success":false}`)
					}
				}))
				defer server.Close()
				client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
				var success bool
				var problem string
				switch tool {
				case "source":
					r := client.CreateSourceCodeSchema(context.Background(), SourceCodeSchemaCreateRequest{SchemaName: "UsrParityProbe", PackageName: "Custom"})
					success, problem = r.Success, r.Error
				case "sql":
					engine := 2
					r := client.CreateSQLSchema(context.Background(), SQLSchemaCreateRequest{SchemaName: "UsrParityProbe", PackageName: "Custom", DBEngineType: &engine})
					success, problem = r.Success, r.Error
				default:
					r := client.InstallSQLSchema(context.Background(), "UsrParityProbe")
					success, problem = r.Success, r.Error
				}
				if !saved || success == reject {
					t.Fatalf("success %v saved %v error %s", success, saved, problem)
				}
				if reject && problem != "locked" {
					t.Fatal(problem)
				}
			})
		}
	}
}

func TestSchemaDeleteRequiresAffectedRowAndCarriesPlatformType(t *testing.T) {
	for _, rows := range []int{0, 1} {
		t.Run(string(rune('0'+rows)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/Login"):
					io.WriteString(w, `{"Code":0}`)
				case strings.HasSuffix(r.URL.Path, "/GetWorkspaceItems"):
					io.WriteString(w, `{"items":[{"name":"UsrParityProbe","uId":"schema","packageName":"Custom","type":42}]}`)
				case strings.HasSuffix(r.URL.Path, "/Delete"):
					var body []map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if len(body) != 1 || body[0]["type"] != float64(42) {
						t.Errorf("delete payload %v", body)
					}
					if rows == 0 {
						io.WriteString(w, `{"success":true,"rowsAffected":0}`)
					} else {
						io.WriteString(w, `{"success":true,"rowsAffected":1}`)
					}
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
			result := client.DeleteSchema(context.Background(), "UsrParityProbe", "", true)
			if (result.ExitCode == 0) != (rows == 1) {
				t.Fatalf("%#v", result)
			}
		})
	}
}
