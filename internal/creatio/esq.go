package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultESQTimeout   = 30 * time.Second
	minESQTimeout       = time.Second
	maxESQTimeout       = 120 * time.Second
	maxESQResponseBytes = 200_000
)

const esqGuidanceHint = "ESQ is a Creatio-specific JSON format. Verify the SelectQuery envelope, expressionType and comparisonType values, filter shape, and date parameter encoding before retrying."

type ExecuteESQRequest struct {
	Query     json.RawMessage
	TimeoutMS *int
}

type ExecuteESQResult struct {
	Success    bool            `json:"success"`
	Error      string          `json:"error,omitempty"`
	Count      *int            `json:"count,omitempty"`
	Rows       json.RawMessage `json:"rows,omitempty"`
	Hint       string          `json:"hint,omitempty"`
	ErrorClass string          `json:"error-class,omitempty"`
}

type selectQueryEnvelope struct {
	Success        *bool           `json:"success"`
	Rows           json.RawMessage `json:"rows"`
	ResponseStatus struct {
		Message string `json:"Message"`
	} `json:"responseStatus"`
	ErrorInfo struct {
		Message string `json:"message"`
	} `json:"errorInfo"`
	Message          string `json:"Message"`
	ExceptionMessage string `json:"ExceptionMessage"`
}

// ExecuteESQ forwards a caller-built SelectQuery unchanged to Creatio's DataService. Joins,
// filters, aggregation, order and paging remain part of that native JSON contract; this client
// does not try to synthesize an ESQ tree from another query language.
func (c *Client) ExecuteESQ(ctx context.Context, input ExecuteESQRequest) ExecuteESQResult {
	query, rootSchema, err := normalizeSelectQuery(input.Query)
	if err != nil {
		return esqFailure(err.Error(), true)
	}
	timeout := defaultESQTimeout
	if input.TimeoutMS != nil {
		requested := *input.TimeoutMS
		if requested < int(minESQTimeout.Milliseconds()) {
			requested = int(minESQTimeout.Milliseconds())
		} else if requested > int(maxESQTimeout.Milliseconds()) {
			requested = int(maxESQTimeout.Milliseconds())
		}
		timeout = time.Duration(requested) * time.Millisecond
	}
	responseBody, err := c.postDataServiceJSON(ctx, "SelectQuery", query, timeout, maxESQResponseBytes)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return ExecuteESQResult{
				Success: false, Error: fmt.Sprintf("SelectQuery result exceeds the %d UTF-8 byte limit. Select explicit columns, lower rowCount, or page the query.", maxESQResponseBytes),
				ErrorClass: "result-too-large",
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExecuteESQResult{Success: false, Error: fmt.Sprintf("SelectQuery request timed out or was canceled (timeout window %d-%d ms). Increase timeout or narrow the query, then retry.", minESQTimeout.Milliseconds(), maxESQTimeout.Milliseconds())}
		}
		return esqFailure("Creatio authentication or DataService request failed.", true)
	}
	return parseSelectQueryResponse(responseBody, query, rootSchema)
}

func normalizeSelectQuery(query json.RawMessage) ([]byte, string, error) {
	if len(query) == 0 {
		return nil, "", errors.New("query is required")
	}
	trimmed := strings.TrimSpace(string(query))
	if strings.HasPrefix(trimmed, "\"") {
		var encoded string
		if err := json.Unmarshal(query, &encoded); err != nil {
			return nil, "", errors.New("query string is not valid JSON")
		}
		query = json.RawMessage(encoded)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(query, &object); err != nil || object == nil {
		return nil, "", errors.New("query must be a SelectQuery JSON object")
	}
	var rootSchema string
	if err := json.Unmarshal(object["rootSchemaName"], &rootSchema); err != nil || strings.TrimSpace(rootSchema) == "" {
		return nil, "", errors.New("query must include a non-empty rootSchemaName")
	}
	return append([]byte(nil), query...), strings.TrimSpace(rootSchema), nil
}

func parseSelectQueryResponse(response, query []byte, rootSchema string) ExecuteESQResult {
	if len(bytes.TrimSpace(response)) == 0 {
		return esqFailure("SelectQuery returned an empty response.", true)
	}
	var decoded selectQueryEnvelope
	if err := json.Unmarshal(response, &decoded); err != nil {
		return esqFailure("SelectQuery returned invalid JSON.", true)
	}
	trimmedRows := bytes.TrimSpace(decoded.Rows)
	hasRows := json.Valid(trimmedRows) && len(trimmedRows) > 0 && trimmedRows[0] == '['
	if (decoded.Success != nil && !*decoded.Success) || (decoded.Success == nil && !hasRows && (decoded.ResponseStatus.Message != "" || decoded.ErrorInfo.Message != "" || decoded.ExceptionMessage != "" || decoded.Message != "")) {
		return esqFailure(selectQueryErrorMessage(decoded), true)
	}
	if hasRows {
		if missing := missingRequestedColumns(query, decoded.Rows); len(missing) > 0 {
			columnWord := "columns"
			if len(missing) == 1 {
				columnWord = "column"
			}
			return esqFailure(fmt.Sprintf("unknown %s %s on schema %q: verify columnPath against the schema metadata.", columnWord, quotedNames(missing), rootSchema), true)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(decoded.Rows, &rows); err == nil {
			count := len(rows)
			return ExecuteESQResult{Success: true, Count: &count, Rows: append(json.RawMessage(nil), decoded.Rows...)}
		}
	}
	return ExecuteESQResult{Success: true, Rows: append(json.RawMessage(nil), response...)}
}

func missingRequestedColumns(query, rowsJSON []byte) []string {
	var queryObject struct {
		Columns struct {
			Items map[string]json.RawMessage `json:"items"`
		} `json:"columns"`
	}
	if json.Unmarshal(query, &queryObject) != nil || len(queryObject.Columns.Items) == 0 {
		return nil
	}
	var rows []json.RawMessage
	if json.Unmarshal(rowsJSON, &rows) != nil || len(rows) == 0 {
		return nil
	}
	present := make(map[string]struct{})
	for _, rowJSON := range rows {
		var row map[string]json.RawMessage
		if json.Unmarshal(rowJSON, &row) != nil || row == nil {
			return nil
		}
		for name := range row {
			present[name] = struct{}{}
		}
	}
	var missing []string
	for alias := range queryObject.Columns.Items {
		if _, ok := present[alias]; !ok {
			missing = append(missing, alias)
		}
	}
	sort.Strings(missing)
	return missing
}

func selectQueryErrorMessage(response selectQueryEnvelope) string {
	for _, message := range []string{response.ResponseStatus.Message, response.ErrorInfo.Message, response.ExceptionMessage, response.Message} {
		if strings.TrimSpace(message) != "" {
			return "Creatio rejected the SelectQuery: " + message
		}
	}
	return "Creatio rejected the SelectQuery."
}

func esqFailure(message string, guidance bool) ExecuteESQResult {
	result := ExecuteESQResult{Success: false, Error: message}
	if guidance {
		result.Hint = esqGuidanceHint
	}
	return result
}

func quotedNames(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, ", ")
}
