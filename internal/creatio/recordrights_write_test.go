package creatio

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSetRecordRightsGrantAndRevokePayload(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		calls := 0
		server := groupCServer(t, func(path string, body map[string]any) (int, string) {
			if path != "/0/rest/RightsService/ApplyChanges" {
				t.Errorf("unexpected %s", path)
				return http.StatusNotFound, ""
			}
			calls++
			record := body["record"].(map[string]any)
			row := body["recordRights"].([]any)[0].(map[string]any)
			if record["entitySchemaName"] != "UsrParityMock" || record["primaryColumnValue"] != "record" || row["Operation"] != float64(1) || row["isDeleted"] != revoke || row["isNew"] == revoke {
				t.Errorf("payload %#v", body)
			}
			if revoke && row["RightLevel"] != float64(-1) || !revoke && row["RightLevel"] != float64(2) {
				t.Errorf("right level %#v", row)
			}
			return http.StatusOK, `{}`
		})
		client := newFormsTestClient(t, server.URL)
		result := client.SetRecordRights(context.Background(), RecordRightsWriteRequest{Entity: "UsrParityMock", RecordID: "record", Grantee: "00000000-0000-0000-0000-000000000001", Operation: "edit", Level: "delegated", Revoke: revoke})
		server.Close()
		if calls != 1 || !result.Success {
			t.Fatalf("result %#v calls=%d", result, calls)
		}
	}
}
func TestSetRecordRightsRefusesMalformedGrantBeforeSending(t *testing.T) {
	client := &Client{}
	result := client.SetRecordRights(context.Background(), RecordRightsWriteRequest{Operation: "read", Grantee: "bad"})
	if result.Success || !strings.Contains(result.Error, "GUID") {
		t.Fatalf("result %#v", result)
	}
}

func TestSetRecordRightsRejectsNonJSONAnswer(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) { return http.StatusOK, "<html>gateway</html>" })
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.SetRecordRights(context.Background(), RecordRightsWriteRequest{Entity: "UsrParityMock", RecordID: "record", Grantee: "00000000-0000-0000-0000-000000000001", Operation: "read"})
	if result.Success || !strings.Contains(result.Error, "Unexpected response") {
		t.Fatalf("result %#v", result)
	}
}
