package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	processWriteSequenceID = "5b1f0c2e-1111-4b6a-9c1e-000000000001"
	processWriteContactA   = "5b1f0c2e-2222-4b6a-9c1e-000000000002"
	processWriteContactB   = "5b1f0c2e-2222-4b6a-9c1e-000000000003"
)

func processWriteSequenceServer(t *testing.T, native string, nativeStatus int, readback string) (*Client, *[]processWriteCall) {
	t.Helper()
	return processWriteServer(t, "", func(call processWriteCall) (int, string) {
		if strings.HasSuffix(call.Path, "/AddByFilter") {
			return nativeStatus, native
		}
		return 200, readback
	})
}

func TestEnrollSequenceParticipantsSubmitsOnceAndReadsBack(t *testing.T) {
	client, calls := processWriteSequenceServer(t, `{"success":true,"addedCount":2,"failedCount":0,"errorMessages":[]}`, 200,
		`{"success":true,"rows":[{"id":"p1","contact-id":"`+processWriteContactA+`","status-id":"s","status-name":"Added","stage-name":null},{"id":"p2","contact-id":"`+processWriteContactB+`","status-id":"s","status-name":"Added"}]}`)
	result, err := client.EnrollSequenceParticipants(context.Background(), processWriteSequenceID, []string{processWriteContactA, processWriteContactB})
	if err != nil || !result.Success || result.Completion != "completed" || *result.AddedCount != 2 || result.Readback.State != "complete" ||
		len(result.Readback.Participants) != 2 || result.Diagnostic.SideEffect != "acknowledged" || result.NextStep != processWriteSequenceAdvice {
		t.Fatalf("%+v %v", result, err)
	}
	var submit, read processWriteCall
	for _, call := range *calls {
		switch {
		case strings.HasSuffix(call.Path, "/AddByFilter"):
			submit = call
		case strings.HasSuffix(call.Path, "/SelectQuery"):
			read = call
		}
	}
	if submit.Path != "/0/rest/SequenceParticipantBulkAddService/AddByFilter" {
		t.Fatalf("submit %+v", submit)
	}
	var body struct {
		Request struct {
			SequenceID string `json:"sequenceId"`
			Filter     string `json:"filter"`
		} `json:"request"`
	}
	_ = json.Unmarshal([]byte(submit.Body), &body)
	var filter map[string]any
	_ = json.Unmarshal([]byte(body.Request.Filter), &filter)
	items := filter["items"].(map[string]any)
	if body.Request.SequenceID != processWriteSequenceID || filter["rootSchemaName"] != "Contact" || filter["logicalOperation"] != float64(1) || len(items) != 2 ||
		items["filter1"].(map[string]any)["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"] != processWriteContactB {
		t.Fatalf("filter %s", submit.Body)
	}
	var query struct {
		Root     string `json:"rootSchemaName"`
		RowCount int    `json:"rowCount"`
		Filters  struct {
			Items map[string]map[string]any `json:"items"`
		} `json:"filters"`
	}
	_ = json.Unmarshal([]byte(read.Body), &query)
	audience := query.Filters.Items["audience"]
	if query.Root != "SequenceParticipant" || query.RowCount != 101 || audience == nil || audience["rootSchemaName"] != nil || audience["logicalOperation"] != float64(1) ||
		query.Filters.Items["filter0"]["leftExpression"].(map[string]any)["columnPath"] != "Sequence" {
		t.Fatalf("readback %s", read.Body)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"participants":[{"id":"p1","contact-id":"`+processWriteContactA+`","status-id":"s","status-name":"Added","stage-name":null},`) {
		t.Fatalf("participants are not passed through in order: %s", encoded)
	}
}

func TestEnrollSequenceParticipantsReportsTheNativeOutcome(t *testing.T) {
	client, _ := processWriteSequenceServer(t, `{"success":false,"addedCount":1,"failedCount":1,"errorMessages":["Contact is already enrolled."],"errorInfo":{"message":"password=replace-me-please"}}`, 200,
		`{"success":true,"rows":[]}`)
	result, err := client.EnrollSequenceParticipants(context.Background(), processWriteSequenceID, []string{processWriteContactA, processWriteContactB})
	if err != nil || result.Success || *result.PlatformSuccess || *result.FailedCount != 1 || len(result.Errors) != 2 ||
		result.Errors[0] != "[untrusted-source-text begin] Contact is already enrolled. [untrusted-source-text end]" || strings.Contains(result.Errors[1], "replace-me-please") ||
		result.Readback.State != "incomplete" || *result.Readback.Error != "Fewer visible participants than the native added count; verify permissions and current records before resubmission." ||
		result.Diagnostic.SideEffect != "unknown" || *result.Diagnostic.Message != result.Errors[0] {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestEnrollSequenceParticipantsIsUncertainWhenTheAnswerCannotBeTrusted(t *testing.T) {
	cases := []struct {
		native   string
		status   int
		received bool
		message  string
	}{
		{`{"success":true,"addedCount":3,"failedCount":0}`, 200, true, "Native enrollment returned incompatible counts."},
		{`{"success":true,"failedCount":0}`, 200, true, "The given key was not present in the dictionary."},
		{`{"success":"yes","addedCount":0,"failedCount":0}`, 200, true, "The requested operation requires an element of type 'Boolean'"},
		{`{}`, 500, true, "The given key was not present in the dictionary."},
		{`<html>Server Error</html>`, 500, true, "'<' is an invalid start of a value. LineNumber: 0 | BytePositionInLine: 0."},
	}
	for _, item := range cases {
		client, _ := processWriteSequenceServer(t, item.native, item.status, `{"success":false}`)
		result, err := client.EnrollSequenceParticipants(context.Background(), processWriteSequenceID, []string{processWriteContactA})
		if err != nil || result.Success || result.Completion != "uncertain" || result.PlatformSuccess != nil || result.AddedCount != nil ||
			!strings.HasPrefix(result.Errors[0], "[untrusted-source-text begin] "+item.message) || result.NextStep != processWriteSequenceUncertain ||
			result.Readback.State != "failed" || *result.Readback.Error != "Participant readback failed; check DataService read permissions." {
			t.Errorf("%s: %+v %v", item.native, result, err)
			continue
		}
		wantTransport := "unknown"
		if item.received {
			wantTransport = "response-received"
		}
		if result.Diagnostic.TransportOutcome != wantTransport || result.Diagnostic.SideEffect != "unknown" {
			t.Errorf("%s: diagnostic %+v", item.native, result.Diagnostic)
		}
	}
}

func TestEnrollSequenceParticipantsReadbackStates(t *testing.T) {
	rows := make([]string, 101)
	for index := range rows {
		rows[index] = fmt.Sprintf(`{"id":"p%d","contact-id":"c","status-id":"s","status-name":"n"}`, index)
	}
	cases := map[string][2]string{
		`{"success":true,"rows":[` + strings.Join(rows, ",") + `]}`: {"truncated", ""},
		`{"success":true,"rows":[{"id":"p1","contact-id":"c"}]}`:    {"failed", "Participant readback omitted required fields."},
		`{"rows":[]}`: {"failed", "[untrusted-source-text begin] The given key was not present in the dictionary. [untrusted-source-text end]"},
	}
	for readback, want := range cases {
		client, _ := processWriteSequenceServer(t, `{"success":true,"addedCount":0,"failedCount":0}`, 200, readback)
		result, err := client.EnrollSequenceParticipants(context.Background(), processWriteSequenceID, []string{processWriteContactA})
		got := ""
		if result.Readback.Error != nil {
			got = *result.Readback.Error
		}
		if err != nil || result.Readback.State != want[0] || got != want[1] {
			t.Errorf("%.60s: %s %q %v", readback, result.Readback.State, got, err)
		}
		if want[0] == "truncated" && (len(result.Readback.Participants) != 100 || result.Success) {
			t.Errorf("truncated: %d participants, success %v", len(result.Readback.Participants), result.Success)
		}
	}
}

func TestEnrollSequenceParticipantsRefusesAnUnusableSelection(t *testing.T) {
	client, calls := processWriteSequenceServer(t, `{}`, 200, `{}`)
	many := make([]string, 101)
	for index := range many {
		many[index] = fmt.Sprintf("5b1f0c2e-2222-4b6a-9c1e-%012d", index+1)
	}
	for _, contacts := range [][]string{nil, {}, many, {processWriteContactA, processWriteContactA}, {processWriteEmptyGUID}} {
		if _, err := client.EnrollSequenceParticipants(context.Background(), processWriteSequenceID, contacts); err == nil || err.Error() != ProcessWriteSequenceInvalid {
			t.Errorf("%d contacts: %v", len(contacts), err)
		}
	}
	if _, err := client.EnrollSequenceParticipants(context.Background(), "", []string{processWriteContactA}); err == nil {
		t.Error("an empty sequence was accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused selection reached Creatio: %+v", *calls)
	}
}
