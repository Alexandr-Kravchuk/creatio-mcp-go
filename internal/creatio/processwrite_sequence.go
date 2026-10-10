package creatio

// enroll-sequence-participants: clio's SequenceEnrollmentService. One non-replayable submission to the
// native SequenceParticipantBulkAddService, then a bounded read of the participants of that sequence for
// those contacts. Submission certainty ("completed" or "uncertain") is kept apart from the status read.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	processWriteSequenceMaxContacts = 100
	processWriteSequenceMaxBytes    = 200000
	processWriteSequenceComplete    = "complete"
	processWriteSequenceAdvice      = "Added means enrolled, not necessarily Active. Inspect the status snapshot; existing records may predate this call. The native engine can change statuses after this read. Do not repeat enrollment solely because readback failed."
	processWriteSequenceUncertain   = "Submission may have committed. No retry was made. Inspect participants and activities before deciding whether to submit any remaining contacts."
	// ProcessWriteSequenceInvalid is the service's ArgumentException for an unusable selection.
	ProcessWriteSequenceInvalid = "Provide a non-empty sequence-id and 1–100 unique, non-empty contact-ids."
)

// ProcessWriteSequenceReadback is SequenceEnrollmentReadback: a bounded status snapshot, not attributed to
// this submission.
type ProcessWriteSequenceReadback struct {
	State        string            `json:"state"`
	Participants []json.RawMessage `json:"participants"`
	Error        *string           `json:"error,omitempty"`
}

// ProcessWriteSequenceResult is SequenceEnrollmentResult.
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

// EnrollSequenceParticipants is SequenceEnrollmentService.Enroll. sequence and contacts are canonical
// lower-case GUIDs ("" for Guid.Empty); contacts is nil when the caller sent none. An unusable selection is
// an error the tool raises.
func (c *Client) EnrollSequenceParticipants(ctx context.Context, sequence string, contacts []string) (ProcessWriteSequenceResult, error) {
	if sequence == "" || sequence == processWriteEmptyGUID || contacts == nil || len(contacts) < 1 || len(contacts) > processWriteSequenceMaxContacts {
		return ProcessWriteSequenceResult{}, errors.New(ProcessWriteSequenceInvalid)
	}
	seen := map[string]bool{}
	for _, contact := range contacts {
		if contact == "" || contact == processWriteEmptyGUID || seen[contact] {
			return ProcessWriteSequenceResult{}, errors.New(ProcessWriteSequenceInvalid)
		}
		seen[contact] = true
	}
	filter, _ := json.Marshal(processWriteSequenceFilter("Id", contacts))
	body, _ := json.Marshal(map[string]any{"request": map[string]any{"sequenceId": sequence, "filter": string(filter)}})
	received := false
	response, err := c.processWriteSendOnce(ctx, "rest/SequenceParticipantBulkAddService/AddByFilter", body, 30*time.Second)
	var native processWriteSequenceNative
	if err == nil {
		received = true
		native, err = processWriteSequenceParse(response, len(contacts))
	}
	if err != nil {
		message := err.Error()
		text := "Enrollment response unavailable."
		if fenced := DataWriteUntrusted(message); fenced != nil {
			text = *fenced
		}
		return ProcessWriteSequenceResult{Completion: "uncertain", Errors: []string{text},
			Readback: c.processWriteSequenceReadback(ctx, sequence, contacts), NextStep: processWriteSequenceUncertain,
			Diagnostic: NewDataWriteDiagnostic("enroll", "SequenceParticipant", nil, true, received, false, &message)}, nil
	}
	readback := c.processWriteSequenceReadback(ctx, sequence, contacts)
	if native.added > len(readback.Participants) && readback.State == processWriteSequenceComplete {
		message := "Fewer visible participants than the native added count; verify permissions and current records before resubmission."
		readback.State, readback.Error = "incomplete", &message
	}
	acknowledged := native.success && native.failed == 0 && len(native.errors) == 0
	var first *string
	if len(native.errors) > 0 {
		first = &native.errors[0]
	}
	success, added, failed := native.success, native.added, native.failed
	return ProcessWriteSequenceResult{Success: acknowledged && readback.State == processWriteSequenceComplete, Completion: "completed",
		PlatformSuccess: &success, AddedCount: &added, FailedCount: &failed, Errors: native.errors, Readback: readback,
		NextStep: processWriteSequenceAdvice, Diagnostic: NewDataWriteDiagnostic("enroll", "SequenceParticipant", nil, true, true, acknowledged, first)}, nil
}

type processWriteSequenceNative struct {
	success       bool
	added, failed int
	errors        []string
}

// processWriteSequenceParse reads the native answer the way clio does: success, addedCount and failedCount
// are required, the counts must fit the submission, and up to 20 errorMessages plus errorInfo.message are
// kept, each fenced as untrusted text.
func processWriteSequenceParse(response string, submitted int) (processWriteSequenceNative, error) {
	var native processWriteSequenceNative
	if len(response) > processWriteSequenceMaxBytes {
		return native, fmt.Errorf("Response exceeded the %d-byte limit.", processWriteSequenceMaxBytes)
	}
	root, err := parseJNode([]byte(response))
	if err != nil {
		return native, errors.New(processWriteJSONError([]byte(response), err))
	}
	if !root.isObject() {
		return native, errors.New("The requested operation requires an element of type 'Object', but the target element has a different type.")
	}
	success, ok := root.props["success"]
	if !ok {
		return native, errors.New("The given key was not present in the dictionary.")
	}
	if success.kind != jkBool {
		return native, errors.New("The requested operation requires an element of type 'Boolean', but the target element has a different type.")
	}
	native.success = success.flag
	for _, field := range []struct {
		name   string
		target *int
	}{{"addedCount", &native.added}, {"failedCount", &native.failed}} {
		value, ok := root.props[field.name]
		if !ok {
			return native, errors.New("The given key was not present in the dictionary.")
		}
		var number int
		if value.kind != jkInteger || json.Unmarshal([]byte(value.text), &number) != nil {
			return native, errors.New("The requested operation requires an element of type 'Number', but the target element has a different type.")
		}
		*field.target = number
	}
	if native.added < 0 || native.failed < 0 || native.added > submitted || native.failed > submitted || native.added+native.failed > submitted {
		return native, errors.New("Native enrollment returned incompatible counts.")
	}
	native.errors = []string{}
	if messages := root.props["errorMessages"]; messages.isArray() {
		for index, message := range messages.items {
			if index >= 20 {
				break
			}
			if message.kind == jkString {
				native.errors = append(native.errors, processWriteSequenceFence(message.text, "Native enrollment error."))
			}
		}
	}
	if info := root.props["errorInfo"]; info.isObject() {
		if detail := info.props["message"]; detail != nil && detail.kind == jkString {
			native.errors = append(native.errors, processWriteSequenceFence(detail.text, "Native enrollment error."))
		}
	}
	return native, nil
}

func processWriteSequenceFence(text, fallback string) string {
	if fenced := DataWriteUntrusted(text); fenced != nil {
		return *fenced
	}
	return fallback
}

// processWriteSequenceFilter is the contact selection: one equality filter per id combined with OR, as the
// filter part of a Contact select query, with its root schema named.
func processWriteSequenceFilter(column string, contacts []string) map[string]any {
	filter := selectQueryWithOrFilter("Contact", map[string]string{}, column, contacts, 0, processWriteSequenceMaxContacts)["filters"].(map[string]any)
	filter["rootSchemaName"] = "Contact"
	return filter
}

// processWriteSequenceReadback is ReadParticipants: the sequence's participants among the contacts, at most
// 100 (one more is asked for, to see a truncation). Every failure is a "failed" state, never an error.
func (c *Client) processWriteSequenceReadback(ctx context.Context, sequence string, contacts []string) ProcessWriteSequenceReadback {
	failed := func(message string) ProcessWriteSequenceReadback {
		text := processWriteSequenceFence(message, "")
		var pointer *string
		if text != "" {
			pointer = &text
		}
		return ProcessWriteSequenceReadback{State: "failed", Participants: []json.RawMessage{}, Error: pointer}
	}
	query := buildSelectQuery("SequenceParticipant", map[string]string{"id": "Id", "contact-id": "Participant.Id", "status-id": "Status.Id",
		"status-name": "Status.Name", "stage-name": "Stage.Name"}, map[string]any{"filter0": comparisonFilter("Sequence", sequence, 0, 3)}, processWriteSequenceMaxContacts+1)
	audience := processWriteSequenceFilter("Participant", contacts)
	delete(audience, "rootSchemaName")
	query["filters"].(map[string]any)["items"].(map[string]any)["audience"] = audience
	body, _ := json.Marshal(query)
	payload, err := c.processWritePost(ctx, serviceCall{Route: "DataService/json/SyncReply/SelectQuery", Body: body, Timeout: 10 * time.Second,
		Limit: maxResponseBytes, Label: "DataService"})
	if err != nil {
		return failed(err.Error())
	}
	if len(payload) > processWriteSequenceMaxBytes {
		return failed(fmt.Sprintf("Response exceeded the %d-byte limit.", processWriteSequenceMaxBytes))
	}
	root, err := parseJNode(payload)
	if err != nil {
		return failed(processWriteJSONError(payload, err))
	}
	if !root.isObject() {
		return failed("The requested operation requires an element of type 'Object', but the target element has a different type.")
	}
	if success := root.props["success"]; success == nil || success.kind != jkBool || !success.flag {
		if success == nil {
			return failed("The given key was not present in the dictionary.")
		}
		return ProcessWriteSequenceReadback{State: "failed", Participants: []json.RawMessage{},
			Error: processWriteSequenceText("Participant readback failed; check DataService read permissions.")}
	}
	rows := root.props["rows"]
	if rows == nil {
		return failed("The given key was not present in the dictionary.")
	}
	if !rows.isArray() {
		return failed("The requested operation requires an element of type 'Array', but the target element has a different type.")
	}
	participants := []json.RawMessage{}
	for index, row := range rows.items {
		if index >= processWriteSequenceMaxContacts {
			break
		}
		if !row.isObject() || row.props["id"] == nil || row.props["contact-id"] == nil || row.props["status-id"] == nil || row.props["status-name"] == nil {
			return ProcessWriteSequenceReadback{State: "failed", Participants: []json.RawMessage{},
				Error: processWriteSequenceText("Participant readback omitted required fields.")}
		}
		participants = append(participants, json.RawMessage(row.newtonsoftJSON()))
	}
	state := processWriteSequenceComplete
	if len(rows.items) > processWriteSequenceMaxContacts {
		state = "truncated"
	}
	return ProcessWriteSequenceReadback{State: state, Participants: participants}
}

func processWriteSequenceText(text string) *string { return &text }

// processWriteSendOnce is clio's ExecuteNonReplayablePostRequest: one authenticated POST, never replayed
// after a re-login and never redirected. The body comes back whatever the status, as Creatio.Client hands
// it back; only a transport failure fails.
func (c *Client) processWriteSendOnce(ctx context.Context, route string, body []byte, timeout time.Duration) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	session, err := c.currentSession(requestCtx)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.serviceURL(route), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	sender := c.requestClient()
	sender.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := c.sendWithSession(sender, request, session)
	if err != nil {
		return "", err
	}
	payload, err := readResponseLimit(response, maxResponseBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(string(payload), "\xef\xbb\xbf"), nil
}
