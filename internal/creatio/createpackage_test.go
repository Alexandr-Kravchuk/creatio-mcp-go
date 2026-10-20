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
)

// createPackageStand is a canned Creatio for create-package: SchemaNamePrefix "Usr", CrtBase installed, the
// application UsrApp, and every package created is kept for the read-back.
type createPackageStand struct {
	mu       sync.Mutex
	packages map[string]map[string]any
	routes   []string
	refuse   bool
	failSave bool
}

func newCreatePackageStand(t *testing.T) (*createPackageStand, *Client) {
	stand := &createPackageStand{packages: map[string]map[string]any{
		"11111111-0000-0000-0000-000000000001": {"uId": "11111111-0000-0000-0000-000000000001", "name": "CrtBase", "version": "8.0"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stand.mu.Lock()
		defer stand.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		stand.routes = append(stand.routes, route)
		switch route {
		case "Login":
			io.WriteString(w, `{"Code":0}`)
		case "QuerySysSettings":
			io.WriteString(w, `{"success":true,"values":{"SchemaNamePrefix":{"value":"Usr"}}}`)
		case "SelectQuery":
			if strings.Contains(string(body), "SysInstalledApp") {
				if strings.Contains(string(body), `"UsrApp"`) {
					io.WriteString(w, `{"success":true,"rows":[{"Id":"22222222-0000-0000-0000-000000000001","Code":"UsrApp"}]}`)
				} else {
					io.WriteString(w, `{"success":true,"rows":[]}`)
				}
				return
			}
			var rows []map[string]any
			for _, item := range stand.packages {
				rows = append(rows, map[string]any{"Name": item["name"], "UId": item["uId"], "Version": item["version"]})
			}
			encoded, _ := json.Marshal(map[string]any{"success": true, "rows": rows})
			w.Write(encoded)
		case "CreatePackage", "CreatePackageInApp":
			if stand.refuse {
				io.WriteString(w, `{"success":false,"errorInfo":{"message":"Name is not valid"}}`)
				return
			}
			var dto map[string]any
			json.Unmarshal(body, &dto)
			if inner, ok := dto["package"].(map[string]any); ok {
				dto = inner
			}
			stand.packages[dto["uId"].(string)] = map[string]any{"uId": dto["uId"], "name": dto["name"], "version": "1.0",
				"description": dto["description"], "maintainer": "Customer", "installType": 0, "dependsOnPackages": []any{}}
			io.WriteString(w, `{"success":true}`)
		case "GetPackageProperties":
			var uid string
			json.Unmarshal(body, &uid)
			item, ok := stand.packages[uid]
			if !ok {
				io.WriteString(w, `{"success":false,"errorInfo":{"message":"not found"}}`)
				return
			}
			encoded, _ := json.Marshal(map[string]any{"success": true, "package": item})
			w.Write(encoded)
		case "SavePackageProperties":
			if stand.failSave {
				io.WriteString(w, `{"success":false,"errorInfo":{"message":"Dependency cycle"}}`)
				return
			}
			var dto map[string]any
			json.Unmarshal(body, &dto)
			stand.packages[dto["uId"].(string)]["dependsOnPackages"] = dto["dependsOnPackages"]
			io.WriteString(w, `{"success":true}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return stand, newFormsTestClient(t, server.URL)
}

func createPackageText(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func TestCreatePackagePrefixesTheNameAndReadsItBack(t *testing.T) {
	_, client := newCreatePackageStand(t)
	description := "Calls package"
	base, extra := "CrtBase", " crtbase "
	response := client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: " Calls ", Description: &description,
		Dependencies: []*string{&base, &extra, nil}})
	if !response.Success || response.PackageCreated == nil || !*response.PackageCreated || createPackageText(response.PackageName) != "UsrCalls" {
		t.Fatalf("response=%+v error=%s", response, createPackageText(response.Error))
	}
	if response.Editable == nil || !*response.Editable || createPackageText(response.Maintainer) != "Customer" ||
		len(*response.Dependencies) != 1 || *(*response.Dependencies)[0] != "CrtBase" {
		t.Fatalf("readback=%+v", response)
	}
	again := client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: "usrcalls"})
	if again.Success || *again.PackageCreated || createPackageText(again.Error) != `Package "UsrCalls" already exists in the environment. Nothing was created.` {
		t.Fatalf("duplicate=%+v error=%s", again, createPackageText(again.Error))
	}
}

func TestCreatePackageRefusesBeforeWriting(t *testing.T) {
	stand, client := newCreatePackageStand(t)
	missing, base := "Nope", "CrtBase"
	response := client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: "X", Dependencies: []*string{&missing, &base}})
	if createPackageText(response.Error) != "Dependency package(s) not found in the environment: Nope. Nothing was created." {
		t.Fatalf("error=%s", createPackageText(response.Error))
	}
	app := "NoSuchApp"
	response = client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: "Y", ApplicationCode: &app})
	if createPackageText(response.Error) != "Application 'NoSuchApp' not found." {
		t.Fatalf("error=%s", createPackageText(response.Error))
	}
	for _, route := range stand.routes {
		if strings.HasPrefix(route, "CreatePackage") {
			t.Fatalf("a refusal wrote: %v", stand.routes)
		}
	}
	stand.refuse = true
	response = client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: "Z"})
	if createPackageText(response.Error) != `The environment refused to create package "UsrZ": Name is not valid. Nothing was created.` {
		t.Fatalf("error=%s", createPackageText(response.Error))
	}
}

func TestCreatePackageInApplicationAndFailedDependencies(t *testing.T) {
	stand, client := newCreatePackageStand(t)
	app := "UsrApp"
	response := client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: "InApp", ApplicationCode: &app})
	if !response.Success || createPackageText(response.ApplicationCode) != "UsrApp" || stand.routes[len(stand.routes)-2] != "CreatePackageInApp" {
		t.Fatalf("response=%+v routes=%v", response, stand.routes)
	}
	stand.failSave = true
	base := "CrtBase"
	response = client.CreatePackage(context.Background(), CreatePackageRequest{PackageName: "Deps", Dependencies: []*string{&base}})
	if response.Success || !*response.PackageCreated ||
		createPackageText(response.Error) != `Package "UsrDeps" was created, but its dependencies were not applied: Dependency cycle Add them with add-package-dependency.` {
		t.Fatalf("response=%+v error=%s", response, createPackageText(response.Error))
	}
}

func TestCreatePackageResponseKeepsAnEmptyDependencyList(t *testing.T) {
	created := true
	encoded, _ := json.Marshal(CreatePackageResponse{PackageCreated: &created, Dependencies: &[]*string{}})
	if string(encoded) != `{"success":false,"package-created":true,"dependencies":[]}` {
		t.Fatalf("encoded=%s", encoded)
	}
	encoded, _ = json.Marshal(CreatePackageFailure(""))
	if string(encoded) != `{"success":false,"package-created":false,"error":"unknown"}` {
		t.Fatalf("encoded=%s", encoded)
	}
}
