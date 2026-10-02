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

const dataForgeAppInfo = `{"success":true,"applicationInfo":{"sysValues":{"coreVersion":"10.2.363.0"}}}`

// dataForgeTestServer answers the login, the version probe and the given Data Forge routes, and records
// every request body by path.
func dataForgeTestServer(t *testing.T, routes map[string]string) (*Client, map[string][]string) {
	t.Helper()
	var mu sync.Mutex
	bodies := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = append(bodies[r.URL.Path], string(body))
		mu.Unlock()
		if r.URL.Path == "/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo" && routes[r.URL.Path] == "" {
			_, _ = w.Write([]byte(dataForgeAppInfo))
			return
		}
		answer, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(answer, "<") {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return client, bodies
}

func dataForgeJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestDataForgeStatusReadsReadyServiceThroughCreatioProxy(t *testing.T) {
	client, bodies := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeMaintenanceService/GetServiceStatus": `{"GetServiceStatusResult":{"IsOnline":true,"Liveness":{"HttpStatusCode":200},` +
			`"Readiness":{"HttpStatusCode":200,"Message":"ok"},"DataStructureReadiness":"Ready","LookupsReadinessInfo":"lookup index error"}}`,
	})
	got := dataForgeJSON(t, client.DataForgeStatus(context.Background()))
	want := `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":[],` +
		`"health":{"liveness":true,"readiness":true,"data-structure-readiness":true,"lookups-readiness":false,"correlation-id":""},` +
		`"status":{"success":true,"status":"Ready"}}`
	if got != want {
		t.Fatalf("status = %s\nwant %s", got, want)
	}
	if body := bodies["/0/rest/DataForgeMaintenanceService/GetServiceStatus"]; len(body) != 1 || body[0] != "{}" {
		t.Errorf("GetServiceStatus bodies = %q, want one {}", body)
	}
	if body := bodies["/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo"]; len(body) != 1 || body[0] != "" {
		t.Errorf("version probe bodies = %q, want one empty body", body)
	}
}

func TestDataForgeStatusReportsUnconfiguredServiceAsUnavailable(t *testing.T) {
	// Creatio answers HTTP 400 with an HTML page when Data Forge is not configured; clio reads that as no answer.
	client, _ := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeMaintenanceService/GetServiceStatus": "\xef\xbb\xbf<html><body>Request Error</body></html>",
	})
	got := dataForgeJSON(t, client.DataForgeStatus(context.Background()))
	want := `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":[],` +
		`"health":{"liveness":false,"readiness":false,"data-structure-readiness":false,"lookups-readiness":false,"correlation-id":""},` +
		`"status":{"success":false,"status":"Unavailable","error":"Empty maintenance status response."}}`
	if got != want {
		t.Fatalf("status = %s\nwant %s", got, want)
	}
}

func TestDataForgeStatusOfflineAndUnwrappedPayload(t *testing.T) {
	client, _ := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeMaintenanceService/GetServiceStatus": `{"isOnline":false,"liveness":{"httpStatusCode":503,"message":"down"}}`,
	})
	status := client.DataForgeStatus(context.Background())
	if !status.Success || status.Status.Status != "Offline" || status.Status.Error == nil || *status.Status.Error != "down" {
		t.Fatalf("status = %s", dataForgeJSON(t, status))
	}
}

func TestDataForgeRefusesPlatformBefore10(t *testing.T) {
	client, bodies := dataForgeTestServer(t, map[string]string{
		"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo": `{"applicationInfo":{"sysValues":{"coreVersion":"8.1.2.3"}}}`,
	})
	got := dataForgeJSON(t, client.DataForgeFindTables(context.Background(), "contact", nil))
	want := `{"success":false,"source":"clio+dataforge-service","correlation-id":"","warnings":[],"error":{"code":"find_tables_error",` +
		`"message":"DataForge MCP tools require Creatio platform version 10.0.0 or later. Current Creatio platform version: 8.1.2.3. CrtDataForge is included in supported platform versions."},"similar-tables":[]}`
	if got != want {
		t.Fatalf("find-tables = %s\nwant %s", got, want)
	}
	if len(bodies["/0/rest/DataForgeSchemaReadService/GetSimilarTableNames"]) != 0 {
		t.Error("the Data Forge read ran although the platform is too old")
	}
}

func TestDataForgeVersionParsing(t *testing.T) {
	for text, supported := range map[string]bool{"10.0.0": true, "10.0": false, "0.0.0.0": true, "9.9.9.9": false, "11.1": true} {
		version, ok := dataForgeParseVersion(text)
		if !ok {
			t.Fatalf("parse %q failed", text)
		}
		got := version == [4]int{0, 0, 0, 0} || dataForgeCompareVersions(version, [4]int{10, 0, 0, -1}) >= 0
		if got != supported {
			t.Errorf("%q supported = %v, want %v", text, got, supported)
		}
	}
	if _, err := dataForgeExtractVersion([]byte(`{"a":{"productVersion":"n/a"},"Version":"10.1.0"}`)); err != nil {
		t.Errorf("a non-version ProductVersion must fall through to Version: %v", err)
	}
	if _, err := dataForgeExtractVersion([]byte(`{}`)); err == nil || !strings.HasPrefix(err.Error(), "Unable to verify Creatio platform version.") {
		t.Errorf("missing version error = %v", err)
	}
}

func TestDataForgeFindTablesSendsRequestAndMapsRows(t *testing.T) {
	client, bodies := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeSchemaReadService/GetSimilarTableNames": `{"GetSimilarTableNamesResult":{"success":true,"Data":[` +
			`{"Name":"Contact","Caption":"Contact","Description":"People"},{"Name":"Lead"}]}}`,
	})
	limit := 5
	got := dataForgeJSON(t, client.DataForgeFindTables(context.Background(), "person", &limit))
	want := `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":[],"similar-tables":[` +
		`{"name":"Contact","caption":"Contact","description":"People"},{"name":"Lead","caption":""}]}`
	if got != want {
		t.Fatalf("find-tables = %s\nwant %s", got, want)
	}
	var request struct {
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal([]byte(bodies["/0/rest/DataForgeSchemaReadService/GetSimilarTableNames"][0]), &request); err != nil {
		t.Fatal(err)
	}
	if request.Request["query"] != "person" || request.Request["limit"] != float64(5) {
		t.Errorf("request = %#v", request.Request)
	}
}

func TestDataForgeReadFailuresUseServiceMessageOrClioFallback(t *testing.T) {
	client, _ := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeSchemaReadService/GetSimilarTableNames":  `{"GetSimilarTableNamesResult":{"errorInfo":{"message":"Value cannot be null.\r\nParameter name: baseUri"},"success":false,"Data":null}}`,
		"/0/rest/DataForgeSchemaReadService/GetTableRelationships": `not json`,
	})
	tables := client.DataForgeFindTables(context.Background(), "x", nil)
	if tables.Success || tables.Error.Code != "find_tables_error" || tables.Error.Message != "Value cannot be null.\r\nParameter name: baseUri" {
		t.Errorf("find-tables = %s", dataForgeJSON(t, tables))
	}
	relations := client.DataForgeGetRelations(context.Background(), "Contact", "Account", nil)
	if relations.Success || relations.Error.Code != "relations_error" || relations.Error.Message != "Empty response from DataForgeSchemaReadService/GetTableRelationships" {
		t.Errorf("relations = %s", dataForgeJSON(t, relations))
	}
	if missing := client.DataForgeGetRelations(context.Background(), "Contact", " ", nil); missing.Error.Message != "target-table is required." {
		t.Errorf("missing target = %s", dataForgeJSON(t, missing))
	}
}

func TestDataForgeFindLookupsMapsIDsSchemasAndScores(t *testing.T) {
	client, bodies := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeSchemaReadService/GetLookupValues": `{"GetLookupValuesResult":{"success":true,"data":[` +
			`{"id":"AAAAAAAA-0000-0000-0000-000000000001","name":"ContactType","valueName":"Customer","vectorSimilarityScore":0.12345678901234567},` +
			`{"valueId":"v2","referenceSchemaName":"AccountType","name":"ignored","valueName":"Partner"}]}}`,
	})
	got := dataForgeJSON(t, client.DataForgeFindLookups(context.Background(), "customer", nil, nil))
	want := `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":[],"similar-lookups":[` +
		`{"lookup-id":"aaaaaaaa-0000-0000-0000-000000000001","schema-name":"ContactType","value":"Customer","score":0.123456789012346},` +
		`{"lookup-id":"v2","schema-name":"AccountType","value":"Partner"}]}`
	if got != want {
		t.Fatalf("find-lookups = %s\nwant %s", got, want)
	}
	if body := bodies["/0/rest/DataForgeSchemaReadService/GetLookupValues"][0]; body != `{"request":{"limit":null,"query":"customer","schemaName":null}}` {
		t.Errorf("request body = %s", body)
	}
}

func TestDataForgeFindLookupsTreatsMalformedGUIDAsEmptyAnswer(t *testing.T) {
	client, _ := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeSchemaReadService/GetLookupValues": `{"GetLookupValuesResult":{"success":true,"data":[{"id":"not-a-guid"}]}}`,
	})
	if got := client.DataForgeFindLookups(context.Background(), "x", nil, nil); got.Error == nil || got.Error.Message != "Empty response from DataForgeSchemaReadService/GetLookupValues" {
		t.Fatalf("find-lookups = %s", dataForgeJSON(t, got))
	}
}

const dataForgeRuntimeSchema = `{"success":true,"schema":{"name":"Contact","columns":{"items":{` +
	`"1":{"name":"Owner","caption":{"en-US":"Owner"},"dataValueType":10,"isRequired":true,"referenceSchemaName":"Contact"},` +
	`"2":{"name":"Id","caption":"Id","dataValueType":0,"isInherited":true},` +
	`"3":{"name":"_Note","caption":null,"description":{"uk-UA":"Нотатка"},"dataValueType":99},` +
	`"4":{"name":"age","caption":"Age","dataValueType":4}}}}}`

func TestDataForgeGetTableColumnsMapsOwnColumnsInClioOrder(t *testing.T) {
	client, bodies := dataForgeTestServer(t, map[string]string{
		"/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest": dataForgeRuntimeSchema,
	})
	got := dataForgeJSON(t, client.DataForgeGetTableColumns(context.Background(), " Contact "))
	want := `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":[],"columns":[` +
		`{"name":"age","caption":"Age","data-type":"Integer","required":false},` +
		`{"name":"Owner","caption":"Owner","data-type":"Lookup","required":true,"reference-schema-name":"Contact"},` +
		`{"name":"_Note","description":"Нотатка","data-type":"99","required":false}]}`
	if got != want {
		t.Fatalf("columns = %s\nwant %s", got, want)
	}
	if body := bodies["/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest"][0]; body != `{"Name":" Contact "}` {
		t.Errorf("runtime schema request = %s, want the name untrimmed", body)
	}
	if len(bodies["/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo"]) != 0 {
		t.Error("table columns must not run the Data Forge version guard")
	}
}

func TestDataForgeContextAggregatesAndCollapsesRepeatedFailures(t *testing.T) {
	client, _ := dataForgeTestServer(t, map[string]string{
		"/0/rest/DataForgeMaintenanceService/GetServiceStatus":     `{"GetServiceStatusResult":{"IsOnline":true,"Readiness":{"HttpStatusCode":503,"Message":"indexing"}}}`,
		"/0/rest/DataForgeSchemaReadService/GetSimilarTableNames":  `{"GetSimilarTableNamesResult":{"success":true,"Data":[{"Name":"Contact"},{"Name":"contact"}]}}`,
		"/0/rest/DataForgeSchemaReadService/GetLookupValues":       `{"GetLookupValuesResult":{"success":false,"errorInfo":{"message":"down"}}}`,
		"/0/rest/DataForgeSchemaReadService/GetTableRelationships": `{"GetTableRelationshipsResult":{"success":true,"paths":["Contact.Account"]}}`,
		"/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest": dataForgeRuntimeSchema,
	})
	source, target, lowerSource := "Contact", "Account", "contact"
	hint1, hint2, hint3 := "Mr", "Ms", " mr "
	got := dataForgeJSON(t, client.DataForgeContext(context.Background(), DataForgeContextRequest{
		RequirementSummary: "ignored when terms exist",
		CandidateTerms:     []*string{&source},
		LookupHints:        []*string{&hint1, &hint2, &hint3},
		RelationPairs:      []*DataForgeRelationPair{{SourceTable: &source, TargetTable: &target}, {SourceTable: &lowerSource, TargetTable: &target}, {SourceTable: &source}},
	}))
	want := `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":["lookups:Mr:down (also: Ms)"],` +
		`"health":{"liveness":true,"readiness":false,"data-structure-readiness":false,"lookups-readiness":false,"correlation-id":""},` +
		`"status":{"success":false,"status":"NotReady","error":"indexing"},` +
		`"similar-tables":[{"name":"Contact","caption":""}],"similar-lookups":[],` +
		// '>' is written escaped as a backslash-u003e sequence, by json.Marshal as by clio's serializer.
		`"relations":{"Contact-` + string(rune(0x5c)) + `u003eAccount":["Contact.Account"]},` +
		`"columns":{"Contact":[{"name":"age","caption":"Age","data-type":"Integer","required":false},` +
		`{"name":"Owner","caption":"Owner","data-type":"Lookup","required":true,"reference-schema-name":"Contact"},` +
		`{"name":"_Note","description":"Нотатка","data-type":"99","required":false}]},` +
		`"coverage":{"health":true,"tables":true,"lookups":false,"relations":true,"table-columns":true}}`
	if got != want {
		t.Fatalf("context = %s\nwant %s", got, want)
	}
}

func TestDataForgeContextFailsWholeWhenStatusCannotBeRead(t *testing.T) {
	client, _ := dataForgeTestServer(t, map[string]string{
		"/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo": `{"version":"none"}`,
	})
	got := dataForgeJSON(t, client.DataForgeContext(context.Background(), DataForgeContextRequest{}))
	want := `{"success":false,"source":"clio+dataforge-service","correlation-id":"","warnings":[],"error":{"code":"context_error",` +
		`"message":"Unable to verify Creatio platform version. DataForge MCP tools require Creatio platform version 10.0.0 or later."},` +
		`"similar-tables":[],"similar-lookups":[],"relations":{},"columns":{},` +
		`"coverage":{"health":false,"tables":false,"lookups":false,"relations":false,"table-columns":false}}`
	if got != want {
		t.Fatalf("context = %s\nwant %s", got, want)
	}
}
