package main

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEnrollSequenceParticipantsMCP(t *testing.T) {
	session, seen := processWriteTestServer(t, "", func(path, body string) string {
		if strings.HasSuffix(path, "/AddByFilter") {
			return `{"success":true,"addedCount":1,"failedCount":0}`
		}
		return `{"success":true,"rows":[{"id":"p","contact-id":"c","status-id":"s","status-name":"Added"}]}`
	})
	args := map[string]any{"sequence-id": "5B1F0C2E-1111-4B6A-9C1E-000000000001", "contact-ids": []any{"5b1f0c2e-2222-4b6a-9c1e-000000000002"}, "ignored": 1}
	answer, _ := processWriteCall(t, session, processSequenceTool, args)
	if answer["success"] != true || answer["completion"] != "completed" || answer["added-count"] != float64(1) ||
		answer["readback"].(map[string]any)["state"] != "complete" {
		t.Fatalf("%v", answer)
	}
	if body := seen()["/0/rest/SequenceParticipantBulkAddService/AddByFilter"]; !strings.Contains(body, `"sequenceId":"5b1f0c2e-1111-4b6a-9c1e-000000000001"`) {
		t.Fatalf("body %s", body)
	}
	raised := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"contact-ids": []any{}}, "Error: tool 'enroll-sequence-participants' failed: Provide a non-empty sequence-id and 1–100 unique, non-empty contact-ids."},
		{map[string]any{"sequence-id": "nope"}, "invalid-parameter-type: argument 'sequence-id' for MCP tool 'enroll-sequence-participants' must be an object."},
		{map[string]any{"contact-ids": "x"}, "invalid-parameter-type: argument 'contact-ids' for MCP tool 'enroll-sequence-participants' must be an array."},
		{map[string]any{"contact-ids": []any{"x"}}, "invalid-parameter-type: argument 'contact-ids' for MCP tool 'enroll-sequence-participants' contains a value that does not match"},
	}
	for _, item := range raised {
		_, result := processWriteCall(t, session, processSequenceTool, item.args)
		if !result.IsError || !strings.HasPrefix(result.Content[0].(*mcp.TextContent).Text, item.want) {
			t.Errorf("%v: %s", item.args, result.Content[0].(*mcp.TextContent).Text)
		}
	}
	if got := annotationsOf(processSequenceTool); got.ReadOnly || !got.Destructive || got.Idempotent || got.OpenWorld {
		t.Fatalf("annotations %#v", got)
	}
}
