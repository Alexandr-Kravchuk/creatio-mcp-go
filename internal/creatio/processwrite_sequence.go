package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ProcessWriteSequenceReadback struct {
	State        string           `json:"state"`
	Participants []map[string]any `json:"participants"`
	Error        *string          `json:"error,omitempty"`
}

type ProcessWriteSequenceResult struct {
	Success         bool                         `json:"success"`
	Completion      string                       `json:"completion"`
	PlatformSuccess *bool                        `json:"platform-success"`
	AddedCount      *int                         `json:"added-count"`
	FailedCount     *int                         `json:"failed-count"`
	Errors          []string                     `json:"errors"`
	Readback        ProcessWriteSequenceReadback `json:"readback"`
	NextStep        string                       `json:"next-step"`
	Diagnostic      *DataWriteDiagnostic         `json:"diagnostic,omitempty"`
}

const processWriteSequenceAdvice = "Added means enrolled, not necessarily Active. Inspect the status snapshot; existing records may predate this call. The native engine can change statuses after this read. Do not repeat enrollment solely because readback failed."
const processWriteSequenceUncertain = "Submission may have committed. No retry was made. Inspect participants and activities before deciding whether to submit any remaining contacts."

func (c *Client) EnrollSequenceParticipants(ctx context.Context, sequence string, contacts []string) (ProcessWriteSequenceResult, error) {
	if !dataWriteIsGUID(sequence) || strings.EqualFold(sequence, "00000000-0000-0000-0000-000000000000") || len(contacts) < 1 || len(contacts) > 100 {
		return ProcessWriteSequenceResult{}, fmt.Errorf("Provide a non-empty sequence-id and 1–100 unique, non-empty contact-ids.")
	}
	seen := map[string]bool{}
	for _, id := range contacts {
		lower := strings.ToLower(id)
		if !dataWriteIsGUID(id) || lower == "00000000-0000-0000-0000-000000000000" || seen[lower] {
			return ProcessWriteSequenceResult{}, fmt.Errorf("Provide a non-empty sequence-id and 1–100 unique, non-empty contact-ids.")
		}
		seen[lower] = true
	}
	filter := selectQueryWithOrFilter("Contact", map[string]string{}, "Id", contacts, 0, 100)["filters"].(map[string]any)
	filter["rootSchemaName"] = "Contact"
	filterJSON, _ := json.Marshal(filter)
	body, _ := json.Marshal(map[string]any{"request": map[string]any{"sequenceId": sequence, "filter": string(filterJSON)}})
	response, err := c.dataWriteSendOnce(ctx, "POST", "rest/SequenceParticipantBulkAddService/AddByFilter", body, 30*time.Second)
	if err != nil {
		return c.processWriteSequenceUncertain(ctx, sequence, contacts, err.Error(), false), nil
	}
	var native struct {
		Success   bool     `json:"success"`
		Added     int      `json:"addedCount"`
		Failed    int      `json:"failedCount"`
		Errors    []string `json:"errorMessages"`
		ErrorInfo struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
	}
	if len(response) > 200000 || json.Unmarshal([]byte(response), &native) != nil || native.Added < 0 || native.Failed < 0 || native.Added+native.Failed > len(contacts) {
		return c.processWriteSequenceUncertain(ctx, sequence, contacts, "Native enrollment returned incompatible counts.", true), nil
	}
	if native.ErrorInfo.Message != "" {
		native.Errors = append(native.Errors, native.ErrorInfo.Message)
	}
	readback := c.processWriteSequenceReadback(ctx, sequence, contacts)
	if readback.State == "complete" && native.Added > len(readback.Participants) {
		readback.State = "incomplete"
		message := "Fewer visible participants than the native added count; verify permissions and current records before resubmission."
		readback.Error = &message
	}
	acknowledged := native.Success && native.Failed == 0 && len(native.Errors) == 0
	result := ProcessWriteSequenceResult{Success: acknowledged && readback.State == "complete", Completion: "completed", PlatformSuccess: &native.Success, AddedCount: &native.Added, FailedCount: &native.Failed, Errors: native.Errors, Readback: readback, NextStep: processWriteSequenceAdvice}
	var message *string
	if len(native.Errors) > 0 {
		message = &native.Errors[0]
	}
	result.Diagnostic = NewDataWriteDiagnostic("enroll", "SequenceParticipant", nil, true, true, acknowledged, message)
	return result, nil
}

func (c *Client) processWriteSequenceUncertain(ctx context.Context, sequence string, contacts []string, message string, received bool) ProcessWriteSequenceResult {
	result := ProcessWriteSequenceResult{Completion: "uncertain", Errors: []string{message}, Readback: c.processWriteSequenceReadback(ctx, sequence, contacts), NextStep: processWriteSequenceUncertain}
	result.Diagnostic = NewDataWriteDiagnostic("enroll", "SequenceParticipant", nil, true, received, false, &message)
	return result
}

func (c *Client) processWriteSequenceReadback(ctx context.Context, sequence string, contacts []string) ProcessWriteSequenceReadback {
	result := ProcessWriteSequenceReadback{State: "complete", Participants: []map[string]any{}}
	query := buildSelectQuery("SequenceParticipant", map[string]string{"Id": "id", "Participant.Id": "contact-id", "Status.Id": "status-id", "Status.Name": "status-name", "Stage.Name": "stage-name"}, map[string]any{"Sequence": comparisonFilter("Sequence", sequence, 0, 3)}, 101)
	filters := query["filters"].(map[string]any)
	items := filters["items"].(map[string]any)
	audience := selectQueryWithOrFilter("Contact", map[string]string{}, "Participant", contacts, 0, 100)["filters"].(map[string]any)
	delete(audience, "rootSchemaName")
	items["audience"] = audience
	rows, err := c.selectRows(ctx, query)
	if err != nil {
		result.State = "failed"
		message := err.Error()
		result.Error = &message
		return result
	}
	for _, row := range rows {
		if len(result.Participants) >= 100 {
			result.State = "truncated"
			break
		}
		result.Participants = append(result.Participants, row)
	}
	return result
}
