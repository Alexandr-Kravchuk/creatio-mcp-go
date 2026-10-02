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

func TestGetProcessSignatureResolvesByCaptionAndReadsParameters(t *testing.T) {
	var schemaRequest map[string]any
	selects := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			selects++
			if !strings.Contains(string(body), `"leftExpression":{"columnPath":"Caption"`) {
				_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[` +
				`{"Id":"aaaaaaaa-0000-0000-0000-000000000001","Name":"UsrProc_v1","Caption":"My process","VersionParentUId":"aaaaaaaa-0000-0000-0000-000000000001","IsActiveVersion":false},` +
				`{"Id":"aaaaaaaa-0000-0000-0000-000000000002","Name":"UsrProc_v2","Caption":"My process","VersionParentUId":"aaaaaaaa-0000-0000-0000-000000000001","IsActiveVersion":true}]}`))
		case "/0/DataService/json/SyncReply/ProcessSchemaRequest":
			_ = json.Unmarshal(body, &schemaRequest)
			metadata, _ := json.Marshal(`{"metaData":{"schema":{"parameters":[` +
				`{"name":"RecordId","dataValueType":"{B295071F-7EA9-4e62-8D1A-919BF3732FF2}","referenceSchemaUId":"16BE3651-8FE2-4159-8DD0-A803D4683DD3","direction":0},` +
				`{"name":"Result","dataValueType":"651ec16f-d140-46db-b9e2-825c985a8ac2","direction":1},` +
				`{"name":"Other","dataValueType":"00000000-0000-0000-0000-000000000000"}]}}}`)
			_, _ = w.Write([]byte(`{"success":true,"schema":{"metaData":` + string(metadata) +
				`,"resources":{"Parameters.RecordId.Caption":{"en-US":"Record"}}}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetProcessSignature(context.Background(), "My process", "")
	if !result.Success || result.ProcessCode != "UsrProc_v2" || result.ProcessID != "aaaaaaaa-0000-0000-0000-000000000002" || selects != 2 {
		t.Fatalf("result = %#v, selects = %d", result, selects)
	}
	if schemaRequest["uId"] != "aaaaaaaa-0000-0000-0000-000000000002" || schemaRequest["convertLocalizableStringToParameter"] != true {
		t.Fatalf("schema request = %#v", schemaRequest)
	}
	encoded, _ := json.Marshal(result.Parameters)
	want := `[{"name":"RecordId","caption":"Record","clrType":"System.Guid","dataValueTypeId":"b295071f-7ea9-4e62-8d1a-919bf3732ff2","direction":"Input","isLookup":true,"referenceSchemaUId":"16be3651-8fe2-4159-8dd0-a803d4683dd3"},` +
		`{"name":"Result","clrType":"System.Collections.Generic.List` + "`" + `1[[System.Object, System.Private.CoreLib, Version=10.0.0.0, Culture=neutral, PublicKeyToken=7cec85d7bea7798e]]","dataValueTypeId":"651ec16f-d140-46db-b9e2-825c985a8ac2","direction":"Output","isLookup":false},` +
		`{"name":"Other","clrType":"System.Object","direction":"Input","isLookup":false}]`
	if string(encoded) != want {
		t.Fatalf("parameters =\n%s\nwant\n%s", encoded, want)
	}
}

func TestGetProcessSignatureReportsUnknownProcessAsResolutionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetProcessSignature(context.Background(), "Nope", "en-US")
	encoded, _ := json.Marshal(result)
	if string(encoded) != `{"success":false,"processResolutionFailed":true,"parameters":[],"error":"ResolveProcessByNameOrCaption - Could not find process with name or caption:Nope"}` {
		t.Fatalf("envelope = %s", encoded)
	}
}

func TestResolveProcessByCaptionRefusesSeveralProcesses(t *testing.T) {
	active := true
	caption := "Shared"
	_, err := resolveProcessByCaption("Shared", []processLibRow{
		{ID: "a", Name: "P1", Caption: &caption, VersionParentID: "f1", IsActiveVersion: &active},
		{ID: "b", Name: "P2", Caption: &caption, VersionParentID: "f2", IsActiveVersion: &active},
	})
	want := "ResolveProcessByNameOrCaption - Multiple processes match caption 'Shared': 'Shared' (code: P1); 'Shared' (code: P2). Re-run with the exact process code."
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v", err)
	}
}
