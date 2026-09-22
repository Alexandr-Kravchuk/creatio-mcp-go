package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExecuteESQForwardsFiltersRelationsOrderingAndPaging(t *testing.T) {
	var dataServiceRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "csrf-token", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			if r.Header.Get("BPMCSRF") != "csrf-token" {
				t.Errorf("BPMCSRF header = %q", r.Header.Get("BPMCSRF"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read SelectQuery body: %v", err)
			}
			if err := json.Unmarshal(body, &dataServiceRequest); err != nil {
				t.Errorf("decode SelectQuery body: %v", err)
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"contact-1","AccountName":"Acme"}]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	query := json.RawMessage(`{"rootSchemaName":"Contact","operationType":0,"allColumns":false,"rowCount":25,"rowsOffset":50,"columns":{"items":{"Id":{"expression":{"expressionType":0,"columnPath":"Id"}},"AccountName":{"expression":{"expressionType":0,"columnPath":"Account.Name"}}}},"filters":{"filterType":6,"isEnabled":true,"logicalOperation":0,"items":{"NameFilter":{"filterType":1,"comparisonType":3,"leftExpression":{"expressionType":0,"columnPath":"Name"},"rightExpression":{"expressionType":2,"parameter":{"dataValueType":1,"value":"Ada"}}}}},"orders":{"items":[{"columnPath":"CreatedOn","direction":0}]}}`)
	result := client.ExecuteESQ(context.Background(), ExecuteESQRequest{Query: query})
	if !result.Success || result.Count == nil || *result.Count != 1 {
		t.Fatalf("ESQ result = %#v", result)
	}
	var rows []map[string]any
	if err := json.Unmarshal(result.Rows, &rows); err != nil || len(rows) != 1 || rows[0]["AccountName"] != "Acme" {
		t.Fatalf("ESQ rows = %s, err = %v", result.Rows, err)
	}
	if dataServiceRequest["rootSchemaName"] != "Contact" || dataServiceRequest["rowCount"] != float64(25) || dataServiceRequest["rowsOffset"] != float64(50) {
		t.Fatalf("SelectQuery envelope = %#v", dataServiceRequest)
	}
	columns := dataServiceRequest["columns"].(map[string]any)["items"].(map[string]any)
	accountExpression := columns["AccountName"].(map[string]any)["expression"].(map[string]any)
	if accountExpression["columnPath"] != "Account.Name" {
		t.Fatalf("relation column path = %#v", accountExpression)
	}
	filters := dataServiceRequest["filters"].(map[string]any)["items"].(map[string]any)
	if filters["NameFilter"] == nil || dataServiceRequest["orders"] == nil {
		t.Fatalf("filter/order fields were not forwarded: %#v", dataServiceRequest)
	}
}

func TestExecuteESQRefusesSilentlyDroppedRequestedColumn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"contact-1"}]}`))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.ExecuteESQ(context.Background(), ExecuteESQRequest{Query: json.RawMessage(`{"rootSchemaName":"Contact","columns":{"items":{"Name":{"expression":{"expressionType":0,"columnPath":"NotAColumn"}}}}}`)})
	if result.Success || !strings.Contains(result.Error, "unknown column") || result.Hint == "" {
		t.Fatalf("missing-column result = %#v", result)
	}
}

func TestExecuteESQEnforcesResponseBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = io.WriteString(w, strings.Repeat(" ", maxESQResponseBytes+1))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.ExecuteESQ(context.Background(), ExecuteESQRequest{Query: json.RawMessage(`{"rootSchemaName":"Contact"}`)})
	if result.Success || result.ErrorClass != "result-too-large" || result.Hint != "" {
		t.Fatalf("large response result = %#v", result)
	}
}

func TestGetEntitySchemaPropertiesMapsLargeMergedRuntimeSchema(t *testing.T) {
	columns := make(map[string]runtimeSchemaColumn, 2049)
	for i := 0; i < 2048; i++ {
		name := fmt.Sprintf("Column%04d", i)
		columns[fmt.Sprint(i)] = runtimeSchemaColumn{
			UID: fmt.Sprintf("column-guid-%04d", i), Name: name,
			Caption:       json.RawMessage(fmt.Sprintf(`{"en-US":%q}`, "Title "+name)),
			Description:   json.RawMessage(fmt.Sprintf(`{"en-US":%q}`, strings.Repeat("description ", 12))),
			DataValueType: 1, Required: i%7 == 0, Inherited: i%2 == 0, Indexed: i%5 == 0,
		}
	}
	columns["primary"] = runtimeSchemaColumn{
		UID: "primary-guid", Name: "Name", Caption: json.RawMessage(`{"en-US":"Full name"}`),
		Description: json.RawMessage(`{"en-US":"Display name"}`), DataValueType: 1, Required: true,
	}
	response := map[string]any{
		"success": true,
		"schema": map[string]any{
			"uId": "schema-guid", "name": "Contact", "primaryColumnUId": "primary-guid",
			"primaryDisplayColumnName": "Name", "primaryDisplayColumnUId": "primary-guid",
			"caption": map[string]string{"en-US": "Contact"}, "description": map[string]string{"en-US": "Contacts"},
			"extendParent": true, "isDBView": false, "isTrackChangesInDB": true, "isVirtual": false,
			"showInAdvancedMode": false, "administratedByOperations": true, "administratedByColumns": false,
			"administratedByRecords": false, "columns": map[string]any{"items": columns},
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) < 200_000 {
		t.Fatalf("large schema fixture is only %d bytes", len(encoded))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest":
			var request struct {
				Name string `json:"Name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Name != "Contact" {
				t.Errorf("runtime schema request = %#v, err = %v", request, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(encoded)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result, err := client.GetEntitySchemaProperties(context.Background(), EntitySchemaPropertiesRequest{SchemaName: "Contact"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "Contact" || result.PackageName != mergedSchemaPackageName || result.OwnColumnCount != 1025 || result.InheritedColumnCount != 1024 || len(result.Columns) != 2049 {
		t.Fatalf("schema summary = %#v; columns=%d", result, len(result.Columns))
	}
	if result.Title == nil || *result.Title != "Contact" || result.PrimaryColumnName == nil || *result.PrimaryColumnName != "Name" {
		t.Fatalf("schema labels/primary = %#v / %#v", result.Title, result.PrimaryColumnName)
	}
	if result.Columns[0].Name != "Column0000" || result.Columns[len(result.Columns)-1].Name != "Name" {
		t.Fatalf("schema columns not sorted: first=%q last=%q", result.Columns[0].Name, result.Columns[len(result.Columns)-1].Name)
	}
	required, err := client.GetEntitySchemaProperties(context.Background(), EntitySchemaPropertiesRequest{SchemaName: "Contact", RequiredOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(required.Columns) != 294 || required.OwnColumnCount != result.OwnColumnCount || required.InheritedColumnCount != result.InheritedColumnCount {
		t.Fatalf("required-only columns=%d counts=%d/%d", len(required.Columns), required.OwnColumnCount, required.InheritedColumnCount)
	}
}

func TestListPackagesFiltersSortsAndPagesLikeMCPContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.URL.Path != "/0/DataService/json/SyncReply/SelectQuery" {
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var query struct {
			RootSchema string `json:"rootSchemaName"`
			RowCount   int    `json:"rowCount"`
			Columns    struct {
				Items map[string]json.RawMessage `json:"items"`
			} `json:"columns"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode package query: %v", err)
		}
		if query.RootSchema != "SysPackage" || query.RowCount != 10000 || len(query.Columns.Items) != 4 {
			t.Errorf("package SelectQuery = %#v", query)
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[
			{"Name":"UsrZulu","UId":"uid-z","Maintainer":"ATF","Version":"1.2"},
			{"Name":"SysBase","UId":"uid-s","Maintainer":"Creatio","Version":"8"},
			{"Name":"UsrAlpha","UId":"uid-a","Maintainer":"ATF","Version":"2.0"}
		]}`))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	limit := 1
	result, err := client.ListPackages(context.Background(), PackageListRequest{Filter: "uSr", Limit: &limit, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Count != 1 || !result.Truncated || result.Packages[0].Name != "UsrAlpha" || result.Packages[0].UID != "uid-a" {
		t.Fatalf("package page = %#v", result)
	}
	if _, err := client.ListPackages(context.Background(), PackageListRequest{Offset: -1}); err == nil {
		t.Fatal("negative offset should be rejected before making a request")
	}
}

func TestListAppSectionsResolvesApplicationAndMapsSectionFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.URL.Path != "/0/DataService/json/SyncReply/SelectQuery" {
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var query struct {
			RootSchema string `json:"rootSchemaName"`
			Filters    struct {
				Items map[string]struct {
					Left struct {
						Column string `json:"columnPath"`
					} `json:"leftExpression"`
				} `json:"items"`
			} `json:"filters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode section query: %v", err)
		}
		switch query.RootSchema {
		case "SysInstalledApp":
			if query.Filters.Items["Code"].Left.Column != "Code" {
				t.Errorf("application lookup filter = %#v", query.Filters.Items)
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"app-1","Name":"Contacts","Code":"Contacts","Version":"1.5"}]}`))
		case "ApplicationSection":
			if query.Filters.Items["ApplicationId"].Left.Column != "ApplicationId" {
				t.Errorf("section lookup filter = %#v", query.Filters.Items)
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"section-1","Code":"Contacts","Caption":"Contacts","Description":"People","EntitySchemaName":"Contact","PackageId":"pkg-1","SectionSchemaUId":"schema-1","LogoId":"logo-1","IconBackground":"#247EE5","ClientTypeId":"client-1"}]}`))
		default:
			t.Errorf("unexpected root schema %q", query.RootSchema)
			_, _ = w.Write([]byte(`{"success":false,"rows":[]}`))
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.ListAppSections(context.Background(), " Contacts ")
	if !result.Success || result.ApplicationID != "app-1" || result.ApplicationVersion == nil || *result.ApplicationVersion != "1.5" || len(result.Sections) != 1 {
		t.Fatalf("section result = %#v", result)
	}
	section := result.Sections[0]
	if section.IconID == nil || *section.IconID != "logo-1" || section.EntitySchemaName == nil || *section.EntitySchemaName != "Contact" || section.Caption != "Contacts" {
		t.Fatalf("section fields = %#v", section)
	}
}

func TestListPagesUsesBoundedEmptyPackageCrossCheck(t *testing.T) {
	pageQueries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.URL.Path != "/0/DataService/json/SyncReply/SelectQuery" {
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var query struct {
			RootSchema string         `json:"rootSchemaName"`
			RowCount   int            `json:"rowCount"`
			Filters    map[string]any `json:"filters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode page query: %v", err)
		}
		if query.RootSchema != "SysSchema" {
			t.Errorf("page query root = %q", query.RootSchema)
		}
		pageQueries++
		filterItems := query.Filters["items"].(map[string]any)
		if filterItems["ManagerName"] == nil || filterItems["Name"] == nil {
			t.Errorf("required page filters absent: %#v", filterItems)
		}
		if pageQueries == 1 {
			if filterItems["PackageName"] == nil || query.RowCount != 1 {
				t.Errorf("primary package-filtered query = %#v", query)
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
			return
		}
		if filterItems["PackageName"] != nil || query.RowCount != pageFallbackLimit {
			t.Errorf("fallback query = %#v", query)
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[
			{"Name":"UsrOne","UId":"uid-1","PackageName":"Target","ParentSchemaName":"FormPageTemplate"},
			{"Name":"UsrOther","UId":"uid-2","PackageName":"Other","ParentSchemaName":"BlankPageTemplate"},
			{"Name":"UsrTwo","UId":"uid-3","PackageName":"Target","ParentSchemaName":"FormPageTemplate"}
		]}`))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	limit := 1
	result := client.ListPages(context.Background(), PageListRequest{PackageName: "Target", SearchPattern: " Usr* ", Limit: &limit})
	if !result.Success || result.Count != 1 || result.Total != 2 || !result.Truncated || result.Pages[0].SchemaName != "UsrOne" || pageQueries != 2 {
		t.Fatalf("page result = %#v; queries=%d", result, pageQueries)
	}
}

func TestListPagesCountsOnlyWhenPageIsFull(t *testing.T) {
	countQueries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		var query struct {
			Columns struct {
				Items map[string]json.RawMessage `json:"items"`
			} `json:"columns"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode SelectQuery: %v", err)
		}
		if _, isCount := query.Columns.Items["RecordCount"]; isCount {
			countQueries++
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"RecordCount":5}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[
			{"Name":"UsrOne","UId":"uid-1","PackageName":"Target","ParentSchemaName":"FormPageTemplate"},
			{"Name":"UsrTwo","UId":"uid-2","PackageName":"Target","ParentSchemaName":"FormPageTemplate"}
		]}`))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	limit := 2
	result := client.ListPages(context.Background(), PageListRequest{PackageName: "Target", Limit: &limit})
	if !result.Success || result.Count != 2 || result.Total != 5 || !result.Truncated || countQueries != 1 {
		t.Fatalf("page count result = %#v; count queries=%d", result, countQueries)
	}
}

func TestListPagesResolvesApplicationCodeThroughPackagesService(t *testing.T) {
	var sawApplicationPackages bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		switch r.URL.Path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			var query struct {
				RootSchema string `json:"rootSchemaName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Errorf("decode query: %v", err)
			}
			switch query.RootSchema {
			case "SysInstalledApp":
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"app-1"}]}`))
			case "SysSchema":
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"Name":"UsrPage","UId":"page-1","PackageName":"AppPackage","ParentSchemaName":"FormPageTemplate"}]}`))
			default:
				t.Errorf("unexpected query root %q", query.RootSchema)
			}
		case "/0/ServiceModel/ApplicationPackagesService.svc/GetApplicationPackages":
			var applicationID string
			if err := json.NewDecoder(r.Body).Decode(&applicationID); err != nil || applicationID != "app-1" {
				t.Errorf("application package request = %q, err=%v", applicationID, err)
			}
			sawApplicationPackages = true
			_, _ = w.Write([]byte(`{"success":true,"packages":[{"name":"AppPackage","isApplicationPrimaryPackage":true}]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.ListPages(context.Background(), PageListRequest{ApplicationCode: "App", SearchPattern: "Usr"})
	if !result.Success || result.Count != 1 || result.Pages[0].PackageName != "AppPackage" || !sawApplicationPackages {
		t.Fatalf("page result = %#v; packages service was called=%v", result, sawApplicationPackages)
	}
}

func TestClioGatePackageFileReadsAuthenticateAndNormalizePaths(t *testing.T) {
	var fileRequestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "csrf-token", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.Header.Get("BPMCSRF") != "csrf-token" {
			t.Errorf("BPMCSRF header = %q", r.Header.Get("BPMCSRF"))
		}
		switch r.URL.Path {
		case "/0/rest/CreatioApiGateway/GetPackageFilesDirectoryContent":
			if r.URL.Query().Get("packageName") != "CrtBase" {
				t.Errorf("package list query = %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`["z\\b.cs","a.cs","A.cs"]`))
		case "/0/rest/CreatioApiGateway/GetPackageFileContent":
			fileRequestCount++
			if r.URL.Query().Get("packageName") != "CrtBase" {
				t.Errorf("package content query = %v", r.URL.Query())
			}
			switch r.URL.Query().Get("filePath") {
			case "src/Main.cs":
				_, _ = w.Write([]byte(`"𐐀"`))
			case "CrtBase.csproj":
				_, _ = w.Write([]byte(`"<Project />"`))
			default:
				t.Errorf("unexpected file path %q", r.URL.Query().Get("filePath"))
			}
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	files := client.ListPackageFiles(context.Background(), " CrtBase ")
	if !files.Success || files.Count != 3 || strings.Join(files.Files, ",") != "A.cs,a.cs,z/b.cs" {
		t.Fatalf("package files = %#v", files)
	}
	content := client.GetPackageFile(context.Background(), "CrtBase", `src\Main.cs`)
	if !content.Success || content.FilePath != "src/Main.cs" || content.Content != "𐐀" || content.ContentLength != 2 || content.ProjectContent != "<Project />" || content.ProjectContentLength != 11 || fileRequestCount != 2 {
		t.Fatalf("package content = %#v; requests=%d", content, fileRequestCount)
	}
	traversal := client.GetPackageFile(context.Background(), "CrtBase", "../outside.cs")
	if traversal.Success || !strings.Contains(traversal.Error, "remain inside") || fileRequestCount != 2 {
		t.Fatalf("traversal path must be rejected before network I/O: %#v; requests=%d", traversal, fileRequestCount)
	}
}

func TestGetSQLSchemaResolvesByUniqueNameAndReadsDesignerBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		switch r.URL.Path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			var query struct {
				RootSchema string `json:"rootSchemaName"`
				RowCount   int    `json:"rowCount"`
				Filters    struct {
					Items map[string]struct {
						Left struct {
							Column string `json:"columnPath"`
						} `json:"leftExpression"`
					} `json:"items"`
				} `json:"filters"`
			}
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Errorf("decode SQL schema query: %v", err)
			}
			if query.RootSchema != "VwSysSqlScriptInPackage" || query.RowCount != 2 || query.Filters.Items["byName"].Left.Column != "Name" {
				t.Errorf("schema resolution query = %#v", query)
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"UId":"sql-guid"}]}`))
		case "/0/ServiceModel/SqlScriptSchemaDesignerService.svc/GetSchema":
			var request struct {
				SchemaUID        string `json:"schemaUId"`
				UseFullHierarchy bool   `json:"useFullHierarchy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.SchemaUID != "sql-guid" || request.UseFullHierarchy {
				t.Errorf("designer request = %#v, err = %v", request, err)
			}
			_, _ = w.Write([]byte(`{"schema":{"name":"UsrReport","body":"SELECT '𐐀';","caption":[{"cultureName":"en-US","value":"Report query"}],"package":{"name":"UsrPackage"}}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.GetSQLSchema(context.Background(), " UsrReport ")
	if !result.Success || result.SchemaName != "UsrReport" || result.SchemaUID != "sql-guid" || result.PackageName != "UsrPackage" || result.Caption != "Report query" || result.Body != "SELECT '𐐀';" || result.BodyLength != utf16Length(result.Body) {
		t.Fatalf("SQL schema result = %#v", result)
	}
}

func TestGetSQLSchemaRejectsAmbiguousNamesWithoutLoadingDesigner(t *testing.T) {
	designerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/GetSchema") {
			designerCalls++
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[{"UId":"sql-guid-1"},{"UId":"sql-guid-2"}]}`))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.GetSQLSchema(context.Background(), "DuplicateSql")
	if result.Success || !strings.Contains(result.Error, "ambiguous") || designerCalls != 0 {
		t.Fatalf("ambiguous SQL schema result = %#v; designer calls=%d", result, designerCalls)
	}
}

func newFormsTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Config{BaseURL: baseURL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
