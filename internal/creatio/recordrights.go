package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// recordRightsRoute is the read-only RightsService method clio's get-record-rights calls.
const recordRightsRoute = "rest/RightsService/GetRecordRights"

// RecordRightsResponse is clio's get-record-rights envelope: the grants are rendered as text lines.
type RecordRightsResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

type recordRightRow struct {
	ID           string `json:"Id"`
	Operation    int    `json:"Operation"`
	RightLevel   int    `json:"RightLevel"`
	SysAdminUnit *struct {
		Value        *string `json:"value"`
		DisplayValue *string `json:"displayValue"`
	} `json:"SysAdminUnit"`
}

// GetRecordRights reads the record-level rights rows of one record and renders them exactly as clio's
// get-record-rights command logs them.
func (c *Client) GetRecordRights(ctx context.Context, entity, recordID string) RecordRightsResponse {
	body, err := json.Marshal(map[string]string{"tableName": recordRightsTableName(entity), "recordId": recordID})
	if err != nil {
		return RecordRightsResponse{Error: "Error: " + err.Error()}
	}
	payload, err := c.postCreatioJSON(ctx, recordRightsRoute, body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return RecordRightsResponse{Error: "Error: " + err.Error()}
	}
	if strings.TrimSpace(string(payload)) == "" {
		return RecordRightsResponse{Error: "Error: Empty response from " + recordRightsRoute + "."}
	}
	var response struct {
		Rows []recordRightRow `json:"GetRecordRightsResult"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return RecordRightsResponse{Error: fmt.Sprintf("Error: Unexpected response from %s: %v", recordRightsRoute, err)}
	}
	if len(response.Rows) == 0 {
		return RecordRightsResponse{Success: true, Output: fmt.Sprintf("No record rights found for %s '%s'.", entity, recordID)}
	}
	lines := []string{fmt.Sprintf("Record rights for %s '%s':", entity, recordID)}
	for _, row := range response.Rows {
		grantee, granteeValue := "(unknown)", "(unknown)"
		if row.SysAdminUnit != nil {
			if row.SysAdminUnit.DisplayValue != nil {
				grantee = *row.SysAdminUnit.DisplayValue
			}
			if row.SysAdminUnit.Value != nil {
				granteeValue = *row.SysAdminUnit.Value
			}
		}
		lines = append(lines, fmt.Sprintf("  %s / %s -> %s (%s) [id: %s]",
			recordRightOperationName(row.Operation), recordRightLevelName(row.RightLevel), grantee, granteeValue, row.ID))
	}
	return RecordRightsResponse{Success: true, Output: strings.Join(lines, "\n")}
}

// recordRightsTableName follows clio's verified convention: a Sys-prefixed entity becomes <Entity>Right,
// any other entity Sys<Entity>Right.
func recordRightsTableName(entity string) string {
	if strings.HasPrefix(entity, "Sys") {
		return entity + "Right"
	}
	return "Sys" + entity + "Right"
}

func recordRightOperationName(value int) string {
	switch value {
	case 0:
		return "read"
	case 1:
		return "edit"
	case 2:
		return "delete"
	}
	return fmt.Sprintf("%d", value)
}

func recordRightLevelName(value int) string {
	switch value {
	case 1:
		return "granted"
	case 2:
		return "delegated"
	}
	return fmt.Sprintf("%d", value)
}
