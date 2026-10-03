package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaTransferExportPreservesPayloadAndNeverOverwrites(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "Success", true: "Failure"}[reject], func(t *testing.T) {
			data := `{"Name":"UsrParityProbe","UId":"schema","ManagerName":"SourceCodeSchemaManager","Properties":[1],"MetaData":"{\"a\":true}","LocalizableValues":[{"Culture":"en-US","Value":"caption"}]}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/Login") {
					io.WriteString(w, `{"Code":0}`)
					return
				}
				if r.URL.Path != "/0/rest/CreatioApiGateway/ExportSchema" {
					t.Errorf("unexpected route %s", r.URL.Path)
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["schemaName"] != "UsrParityProbe" || body["packageName"] != "Custom" || body["managerName"] != nil {
					t.Errorf("request %v", body)
				}
				if reject {
					io.WriteString(w, `{"success":false,"errorInfo":{"message":"ambiguous"}}`)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"success": true, "schemaData": data, "schema": map[string]any{"schemaName": "UsrParityProbe", "schemaUId": "schema", "managerName": "SourceCodeSchemaManager", "packageName": "Custom"}})
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
			destination := t.TempDir()
			input := SchemaTransferRequest{SchemaName: "UsrParityProbe", PackageName: "Custom", Destination: destination}
			result := client.ExportSchema(context.Background(), input)
			if (result.ExitCode == 0) == reject {
				t.Fatalf("%#v", result)
			}
			if reject {
				return
			}
			dir := filepath.Join(destination, "UsrParityProbe")
			raw, err := os.ReadFile(filepath.Join(dir, "schema-data.json"))
			if err != nil || string(raw) != data {
				t.Fatalf("payload changed: %s %v", raw, err)
			}
			for _, file := range []string{"descriptor.json", "metadata.json", "properties.json", "resources/resource.en-US.json"} {
				if _, err = os.Stat(filepath.Join(dir, file)); err != nil {
					t.Error(file, err)
				}
			}
			result = client.ExportSchema(context.Background(), input)
			if result.ExitCode == 0 {
				t.Fatal("overwrote bundle")
			}
		})
	}
}
func TestSchemaTransferImportEnforcesIdentityAndDryRun(t *testing.T) {
	for _, mode := range []string{"create", "replace", "newlayer", "dryrun", "identity", "missinguid", "denied"} {
		t.Run(mode, func(t *testing.T) {
			imported := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/Login") {
					io.WriteString(w, `{"Code":0}`)
					return
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				switch r.URL.Path {
				case "/0/rest/CreatioApiGateway/FindSchemaLayers":
					if body["schemaName"] != "UsrParityProbe" || body["managerName"] != "SourceCodeSchemaManager" {
						t.Errorf("find %v", body)
					}
					if mode == "newlayer" {
						io.WriteString(w, `{"success":true,"layers":[{"packageName":"Other"}]}`)
					} else if mode == "replace" || mode == "identity" || mode == "missinguid" {
						uid := "schema"
						if mode == "identity" {
							uid = "different"
						}
						json.NewEncoder(w).Encode(map[string]any{"success": true, "layers": []map[string]any{{"packageName": "Custom", "schemaUId": uid, "managerName": "SourceCodeSchemaManager"}}})
					} else {
						io.WriteString(w, `{"success":true,"layers":[]}`)
					}
				case "/0/rest/CreatioApiGateway/ImportSchema":
					imported = true
					if body["packageName"] != "Custom" || !strings.Contains(body["schemaData"].(string), "UsrParityProbe") {
						t.Errorf("import %v", body)
					}
					if mode == "denied" {
						io.WriteString(w, `{"success":false,"errorInfo":{"message":"locked"}}`)
					} else {
						io.WriteString(w, `{"success":true,"importResult":"Done"}`)
					}
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
			dir := t.TempDir()
			payload := map[string]any{"Name": "UsrParityProbe", "ManagerName": "SourceCodeSchemaManager"}
			if mode != "missinguid" {
				payload["UId"] = "schema"
			}
			raw, _ := json.Marshal(payload)
			os.WriteFile(filepath.Join(dir, "schema-data.json"), raw, 0600)
			result := client.ImportSchema(context.Background(), SchemaTransferRequest{Path: dir, PackageName: "Custom", DryRun: mode == "dryrun"})
			refused := mode == "newlayer" || mode == "identity" || mode == "missinguid" || mode == "denied"
			if (result.ExitCode == 0) == refused {
				t.Fatalf("%#v", result)
			}
			shouldCall := mode == "create" || mode == "replace" || mode == "denied"
			if imported != shouldCall {
				t.Fatalf("imported %v mode %s", imported, mode)
			}
		})
	}
}
func TestAppDeleteUsesScalarGUIDAndTransportFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "Success", true: "Failure"}[fail], func(t *testing.T) {
			const id = "00000000-0000-0000-0000-000000000001"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/Login") {
					io.WriteString(w, `{"Code":0}`)
					return
				}
				if r.URL.Path != "/0/ServiceModel/AppInstallerService.svc/UninstallApp" {
					t.Errorf("route %s", r.URL.Path)
				}
				var body string
				json.NewDecoder(r.Body).Decode(&body)
				if body != id {
					t.Errorf("id %s", body)
				}
				if fail {
					http.Error(w, "failed", 500)
				} else {
					io.WriteString(w, `{}`)
				}
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
			result := client.DeleteApp(context.Background(), id)
			if result.Success == fail {
				t.Fatalf("%#v", result)
			}
		})
	}
}

func TestAppDeleteResolvesCodeBeforeUninstall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			io.WriteString(w, `{"success":true,"rows":[{"Id":"00000000-0000-0000-0000-000000000001","Name":"Parity app","Code":"UsrParityApp"}]}`)
		case strings.HasSuffix(r.URL.Path, "/UninstallApp"):
			var id string
			json.NewDecoder(r.Body).Decode(&id)
			if id != "00000000-0000-0000-0000-000000000001" {
				t.Errorf("resolved ID %s", id)
			}
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	result := client.DeleteApp(context.Background(), "usrparityapp")
	if !result.Success {
		t.Fatalf("%#v", result)
	}
	result = client.DeleteApp(context.Background(), "UsrParityMissingApp")
	if result.Success || !strings.Contains(result.Error, "not found") {
		t.Fatalf("%#v", result)
	}
}
