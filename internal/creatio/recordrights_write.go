package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type RecordRightsWriteRequest struct {
	Entity, RecordID, Grantee, Operation, Level string
	Revoke                                      bool
}

func (c *Client) SetRecordRights(ctx context.Context, input RecordRightsWriteRequest) RecordRightsResponse {
	fail := func(text string) RecordRightsResponse { return RecordRightsResponse{Error: "Error: " + text} }
	operation := strings.ToLower(strings.TrimSpace(input.Operation))
	op := 0
	switch operation {
	case "read":
	case "edit":
		op = 1
	case "delete":
		op = 2
	default:
		return fail(fmt.Sprintf("Unknown operation '%s'. Expected one of: read, edit, delete. (Parameter 'value')", input.Operation))
	}
	levelName := strings.ToLower(strings.TrimSpace(input.Level))
	if levelName == "" {
		levelName = "granted"
	}
	level := 1
	if !input.Revoke {
		switch levelName {
		case "granted":
		case "delegated":
			level = 2
		default:
			return fail(fmt.Sprintf("Unknown level '%s'. Expected one of: granted, delegated. (Parameter 'value')", input.Level))
		}
	} else {
		level = -1
	}
	if _, ok := DataWriteParseGUID(input.Grantee); !ok {
		return fail("--grantee must be a SysAdminUnit GUID.")
	}
	row := map[string]any{"Id": schemaWriteNewGUID(), "Operation": op, "RightLevel": level, "SysAdminUnit": map[string]any{"value": input.Grantee, "displayValue": nil}, "SysAdminUnitType": emptyGUID, "Position": -1, "isNew": !input.Revoke, "isDeleted": input.Revoke}
	body, _ := json.Marshal(map[string]any{"record": map[string]any{"entitySchemaName": input.Entity, "primaryColumnValue": input.RecordID}, "recordRights": []any{row}})
	payload, err := c.dataWriteSendOnce(ctx, "POST", "rest/RightsService/ApplyChanges", body, 100*time.Second)
	if err != nil {
		return fail(err.Error())
	}
	if strings.TrimSpace(payload) == "" {
		return fail("Empty response from " + c.serviceURL("rest/RightsService/ApplyChanges") + ".")
	}
	if !json.Valid([]byte(payload)) {
		return fail("Unexpected response from " + c.serviceURL("rest/RightsService/ApplyChanges") + ": " + dataWriteSanitizeForDisplay(payload, 500))
	}
	output := fmt.Sprintf("Granted '%s' at '%s' for %s on %s '%s'.", operation, levelName, input.Grantee, input.Entity, input.RecordID)
	if input.Revoke {
		output = fmt.Sprintf("Revoked '%s' for %s on %s '%s'.", operation, input.Grantee, input.Entity, input.RecordID)
	}
	return RecordRightsResponse{Success: true, Output: output}
}
