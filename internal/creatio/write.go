package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// WriteOutcome is deliberately explicit about WHY something failed, because the failure this pilot
// exists to avoid is a rejected write reported as an empty success.
//
// clio's knowledge base records the vendor behaviour being avoided here: ATF.Repository's
// RemoteDataProvider catches its exceptions and returns Success=false with an empty payload, and the
// consumer side then drops the flag — so a refused operation can surface as a successful no-op.
type WriteOutcome struct {
	Operation      string `json:"operation"`
	Succeeded      bool   `json:"succeeded"`
	RecordID       string `json:"recordId,omitempty"`
	RowsAffected   int    `json:"rowsAffected"`
	FailureClass   string `json:"failureClass,omitempty"`
	FailureDetail  string `json:"failureDetail,omitempty"`
	HTTPStatusCode int    `json:"httpStatusCode,omitempty"`
}

type writeResponse struct {
	Success      bool      `json:"success"`
	ErrorInfo    errorInfo `json:"errorInfo"`
	RowsAffected int       `json:"rowsAffected"`
	ID           string    `json:"id"`
}

// Insert posts an InsertQuery and classifies every way it can fail.
func (c *Client) Insert(ctx context.Context, schema string, values map[string]any) (WriteOutcome, error) {
	columns := map[string]any{}
	for name, value := range values {
		// No "expression" wrapper here. clio's own DataServiceBatchCommand writes the item as
		// {expressionType, parameter} directly; wrapping it makes the server answer HTTP 500 with
		// NullReferenceException and no indication of which field is wrong.
		columns[name] = map[string]any{
			"expressionType": 2,
			"parameter":      map[string]any{"dataValueType": 1, "value": value},
		}
	}
	payload := map[string]any{
		"rootSchemaName": schema, "operationType": 1,
		"columnValues": map[string]any{"items": columns},
		"__type":       "Terrasoft.Nui.ServiceModel.DataContract.InsertQuery",
	}
	return c.write(ctx, "insert", "InsertQuery", payload)
}

// Delete posts a DeleteQuery filtered to one record id.
func (c *Client) Delete(ctx context.Context, schema, id string) (WriteOutcome, error) {
	payload := map[string]any{
		"rootSchemaName": schema, "operationType": 3,
		"filters": map[string]any{
			"filterType": 6, "isEnabled": true, "logicalOperation": 0,
			"items": map[string]any{"pk": map[string]any{
				"filterType": 1, "comparisonType": 3, "isEnabled": true,
				"leftExpression":  map[string]any{"expressionType": 0, "columnPath": "Id"},
				"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": 0, "value": id}},
			}},
		},
		"__type": "Terrasoft.Nui.ServiceModel.DataContract.DeleteQuery",
	}
	return c.write(ctx, "delete", "DeleteQuery", payload)
}

func (c *Client) write(ctx context.Context, operation, route string, payload map[string]any) (WriteOutcome, error) {
	out := WriteOutcome{Operation: operation}
	if c.config.ClientID != "" {
		if err := c.acquireToken(ctx); err != nil {
			out.FailureClass = "auth"
			out.FailureDetail = err.Error()
			return out, err
		}
	} else if err := c.formsLogin(ctx); err != nil {
		out.FailureClass = "auth"
		out.FailureDetail = err.Error()
		return out, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		out.FailureClass = "encode"
		out.FailureDetail = err.Error()
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.serviceURL("DataService/json/SyncReply/"+route), bytes.NewReader(body))
	if err != nil {
		out.FailureClass = "request"
		out.FailureDetail = err.Error()
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if csrf := c.csrfToken(); csrf != "" {
		req.Header.Set("BPMCSRF", csrf)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		out.FailureClass = "transport"
		out.FailureDetail = err.Error()
		return out, fmt.Errorf("%s: transport: %w", route, err)
	}
	defer response.Body.Close()
	out.HTTPStatusCode = response.StatusCode
	raw, err := readResponse(response)
	if err != nil {
		out.FailureClass = "read"
		out.FailureDetail = err.Error()
		return out, err
	}
	if response.StatusCode != http.StatusOK {
		out.FailureClass = "http"
		// Carry a bounded slice of the body: an HTTP 500 with no detail is the same unhelpful silence
		// this pilot exists to avoid, one layer up.
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		out.FailureDetail = fmt.Sprintf("HTTP %d: %s", response.StatusCode, snippet)
		return out, fmt.Errorf("%s: HTTP %d", route, response.StatusCode)
	}
	// An HTML login page where JSON was expected means the session was not accepted; reporting that as
	// a parse error would hide the real cause.
	if trimmed := strings.TrimSpace(string(raw)); strings.HasPrefix(trimmed, "<") {
		out.FailureClass = "html-login-page"
		out.FailureDetail = "server answered HTML where JSON was expected"
		return out, fmt.Errorf("%s: %s", route, out.FailureDetail)
	}
	var parsed writeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		out.FailureClass = "malformed-json"
		out.FailureDetail = err.Error()
		return out, fmt.Errorf("%s: malformed JSON: %w", route, err)
	}
	// THE case this pilot exists for. success:false is a refusal, never an empty success.
	if !parsed.Success {
		out.FailureClass = "server-refused"
		out.FailureDetail = parsed.ErrorInfo.Message
		if out.FailureDetail == "" {
			out.FailureDetail = "server returned success:false with no message"
		}
		return out, fmt.Errorf("%s: server refused: %s", route, out.FailureDetail)
	}
	out.Succeeded = true
	out.RowsAffected = parsed.RowsAffected
	out.RecordID = parsed.ID
	return out, nil
}
