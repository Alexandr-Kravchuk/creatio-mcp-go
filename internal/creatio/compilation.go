package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	// compileLastResultRoute is the endpoint clio's CompilationResultReader reads. It reports the result
	// Creatio persisted for the last configuration build and carries no timestamp.
	compileLastResultRoute = "api/ConfigurationStatus/GetLastCompilationResult"
	// compileLastResultTimeout is clio's CompilationResultReader.ReadTimeoutMs.
	compileLastResultTimeout = 60 * time.Second
)

// The parser rejections are clio's CompilationLogParser texts, so a caller sees the same words.
const (
	compileEmptyPayloadMessage      = "Creatio returned an empty compilation-result payload."
	compileUnexpectedPayloadMessage = "Creatio returned an unexpected compilation-result payload. Expected errors, buildResult, and success fields."
)

// LastCompilationDiagnostic is one compiler error or warning of last-compilation-log.
type LastCompilationDiagnostic struct {
	Severity    string  `json:"severity"`
	FileName    *string `json:"file-name,omitempty"`
	Line        int     `json:"line"`
	Column      int     `json:"column"`
	Code        *string `json:"code,omitempty"`
	Description *string `json:"description,omitempty"`
}

// LastCompilationLogResult is the last-compilation-log envelope. diagnostics is [] on failure, as in clio.
type LastCompilationLogResult struct {
	Success              bool                        `json:"success"`
	CompilationSucceeded bool                        `json:"compilation-succeeded"`
	BuildResult          *int                        `json:"build-result,omitempty"`
	Diagnostics          []LastCompilationDiagnostic `json:"diagnostics"`
	Error                string                      `json:"error,omitempty"`
}

// LastCompilationLogFailure builds the failure envelope clio returns for a refused or failed read.
func LastCompilationLogFailure(message string) LastCompilationLogResult {
	return LastCompilationLogResult{Diagnostics: []LastCompilationDiagnostic{}, Error: message}
}

// GetLastCompilationLog reads the compilation result Creatio persisted for the last build. It never starts or
// tracks a compilation, so the answer may describe a build that ran long before this call.
func (c *Client) GetLastCompilationLog(ctx context.Context) LastCompilationLogResult {
	payload, err := c.compileGetLastResult(ctx)
	if err != nil {
		return LastCompilationLogFailure(err.Error())
	}
	parsed, err := compileParseLastResult(payload)
	if err != nil {
		return LastCompilationLogFailure(err.Error())
	}
	return parsed
}

// compileGetLastResult sends the one GET this tool makes. The route is a constant, never a tool argument.
func (c *Client) compileGetLastResult(ctx context.Context) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, compileLastResultTimeout)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.serviceURL(compileLastResultRoute), nil)
		if err != nil {
			return nil, fmt.Errorf("build %s request: %w", compileLastResultRoute, err)
		}
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		if isTransportError(err) {
			return nil, fmt.Errorf("%s transport failure: %w", compileLastResultRoute, err)
		}
		return nil, err
	}
	payload, err := readResponse(response)
	if err != nil {
		return nil, fmt.Errorf("%s response: %w", compileLastResultRoute, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s returned HTTP %d", compileLastResultRoute, response.StatusCode)
	}
	if looksLikeHTML(payload) {
		return nil, fmt.Errorf("%s returned HTML instead of JSON; authentication or routing failed", compileLastResultRoute)
	}
	return payload, nil
}

// compileParseLastResult mirrors clio's DeserializeCreatioCompilationLog: errors, buildResult and success
// must all be present (errors may be null, which reads as no diagnostics).
func compileParseLastResult(payload []byte) (LastCompilationLogResult, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return LastCompilationLogResult{}, fmt.Errorf("%s", compileEmptyPayloadMessage)
	}
	var decoded struct {
		Errors      json.RawMessage `json:"errors"`
		BuildResult *int            `json:"buildResult"`
		Success     *bool           `json:"success"`
	}
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return LastCompilationLogResult{}, fmt.Errorf("Creatio returned a compilation-result payload that is not valid JSON: %v", err)
	}
	if decoded.Errors == nil || decoded.BuildResult == nil || decoded.Success == nil {
		return LastCompilationLogResult{}, fmt.Errorf("%s", compileUnexpectedPayloadMessage)
	}
	var entries []struct {
		Line        int     `json:"line"`
		Column      int     `json:"column"`
		ErrorNumber *string `json:"errorNumber"`
		ErrorText   *string `json:"errorText"`
		Warning     bool    `json:"warning"`
		FileName    *string `json:"fileName"`
	}
	if err := json.Unmarshal(decoded.Errors, &entries); err != nil {
		return LastCompilationLogResult{}, fmt.Errorf("Creatio returned compilation diagnostics in an unexpected shape: %v", err)
	}
	diagnostics := make([]LastCompilationDiagnostic, 0, len(entries))
	for _, entry := range entries {
		severity := "error"
		if entry.Warning {
			severity = "warning"
		}
		diagnostics = append(diagnostics, LastCompilationDiagnostic{
			Severity: severity, FileName: entry.FileName, Line: entry.Line, Column: entry.Column,
			Code: entry.ErrorNumber, Description: entry.ErrorText,
		})
	}
	return LastCompilationLogResult{
		Success: true, CompilationSucceeded: *decoded.Success, BuildResult: decoded.BuildResult, Diagnostics: diagnostics,
	}, nil
}
