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

func newFormsTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Config{BaseURL: baseURL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
