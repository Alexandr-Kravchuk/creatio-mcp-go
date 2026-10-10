package creatio

// run-process: clio master's RunProcessTool and RunProcessCommand (after ENG-102333). The process code and
// parameter codes are checked against the process signature, the values are coerced by parameter type, and
// ProcessEngineService.RunProcess is posted once. The MCP tool answers by the response deadline (45 s): a
// launch gate makes that answer exact - "not-started" when the request was never sent, "still-running" when
// it was. A failed run's own error is read back from SysProcessLog within what is left of the deadline.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	dataWriteRunProcessRoute     = "ServiceModel/ProcessEngineService.svc/RunProcess"
	DataWriteNotStartedStatus    = "not-started"
	DataWriteStillRunningStatus  = "still-running"
	dataWriteQueuedStatus        = "queued-background"
	dataWriteManualStartRefused  = "ProcessCannotBeManuallyStartedException"
	dataWriteMissingCompiledCode = "KeyNotFoundException"
	dataWriteLogReadMargin       = 3 * time.Second
	dataWriteMinimumLogRead      = time.Second
	dataWriteLogReadTimeout      = 10 * time.Second
)

var (
	dataWriteProcessStatuses = map[int]string{0: "inactive", 1: "running", 2: "completed", 3: "error", 4: "cancelled", 5: "cancelling"}
	dataWriteErrorCodeShape  = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_.]{0,127}\z`)
)

// DataWriteProcessResult is clio's RunProcessResponse; null members are omitted as clio's MCP serializer does.
type DataWriteProcessResult struct {
	Status                string          `json:"status,omitempty"`
	ProcessID             string          `json:"processId,omitempty"`
	ResultParameterValues json.RawMessage `json:"resultParameterValues,omitempty"`
	Warnings              []string        `json:"warnings"`
	Error                 string          `json:"error,omitempty"`
}

func dataWriteProcessFailure(message string) DataWriteProcessResult {
	return DataWriteProcessResult{Warnings: []string{}, Error: message}
}

// DataWriteProcessParameter is one supplied input value: its code and the JSON value as the caller sent it.
type DataWriteProcessParameter struct {
	Code  string
	Value any // decoded JSON: nil, bool, float64, string, []any or map[string]any
}

// DataWriteProcessRequest is RunProcessOptions.
type DataWriteProcessRequest struct {
	ProcessName      string
	Parameters       []DataWriteProcessParameter
	ResultParameters []string
	TimeoutSeconds   int
	// Deadline is when the MCP answer falls; the failed-run log read only gets what is left of it.
	Deadline time.Time
	Gate     *DataWriteLaunchGate
}

// DataWriteLaunchGate is RunProcessLaunchGate: decides once between sending the launch request and answering
// that nothing was launched.
type DataWriteLaunchGate struct {
	state  atomic.Int32 // 0 open, 1 sent, 2 withdrawn
	sentAt atomic.Int64
}

// TryMarkSent claims the gate for sending; false when the answer already said nothing was launched.
func (g *DataWriteLaunchGate) TryMarkSent(now time.Time) bool {
	g.sentAt.Store(now.UnixNano())
	return g.state.CompareAndSwap(0, 1)
}

// TryWithdraw claims the gate for a not-launched answer; true when the request had not been sent.
func (g *DataWriteLaunchGate) TryWithdraw() bool { return g.state.CompareAndSwap(0, 2) }

// SentAt is when the request was sent, or false when it was not.
func (g *DataWriteLaunchGate) SentAt() (time.Time, bool) {
	if g.state.Load() != 1 {
		return time.Time{}, false
	}
	return time.Unix(0, g.sentAt.Load()).UTC(), true
}

// DataWriteStillRunningNote is RunProcessTool.BuildStillRunningNote.
func DataWriteStillRunningNote(processName string, sent time.Time) string {
	return fmt.Sprintf("The launch request for '%s' was sent at %s and had not been answered when the MCP response deadline was reached. "+
		"This is NOT a failure and NOT a success — clio has no verdict, and cannot say whether the environment started the run. The platform "+
		"exposes no handle for an in-flight synchronous run: the process id only exists in the RunProcess response, and the SysProcessLog row "+
		"is buffered and written when the run ends, so there is nothing to poll yet. Do NOT re-run this process to find out — a second launch "+
		"duplicates the work. Judge the outcome from the process's own effects, or from a later SysProcessLog read (odata-read on SysProcessLog, "+
		"newest row for this process). Its times can come back in the environment user's time zone even when marked Z, so a row's time alone "+
		"cannot tie it to this launch: if neither its effects nor such a row certainly shows this run, tell the user the outcome is unconfirmed "+
		"and ask before launching it again.", processName, sent.UTC().Format("2006-01-02T15:04:05Z"))
}

// DataWriteNotLaunchedError is RunProcessTool.BuildNotLaunchedError.
func DataWriteNotLaunchedError(processName string) string {
	return fmt.Sprintf("'%s' was not launched: preparing the launch (logging in, reading the process and its parameters from the environment) "+
		"did not finish within the MCP response deadline, so the launch request was never sent and nothing ran. The environment is answering "+
		"slowly - for example while it reloads after a compile or a restart - so wait a minute before calling run-process again; if that call "+
		"is not launched either, tell the user the environment is not answering instead of calling again.", processName)
}

// RunProcess is RunProcessCommand.TryRun.
func (c *Client) RunProcess(ctx context.Context, request DataWriteProcessRequest) DataWriteProcessResult {
	if strings.TrimSpace(request.ProcessName) == "" {
		return dataWriteProcessFailure("process-name is required")
	}
	signature := c.GetProcessSignature(ctx, request.ProcessName, "en-US")
	if !signature.Success {
		return dataWriteProcessFailure(signature.Error)
	}
	if signature.ProcessCode != request.ProcessName {
		return dataWriteProcessFailure(fmt.Sprintf("'%s' is not a process CODE. It resolved to '%s', so it was a display caption or the wrong casing. "+
			"Pass the code: a caption is not unique and is not what the platform launches by, and this tool starts a process rather than reading "+
			"one, so an ambiguous key could start the wrong one. Use '%s', or get-process-signature to confirm the code.",
			request.ProcessName, signature.ProcessCode, signature.ProcessCode))
	}
	values := []any{}
	for _, supplied := range request.Parameters {
		parameter, ok := dataWriteFindProcessParameter(signature.Parameters, supplied.Code)
		if !ok {
			return dataWriteProcessFailure(dataWriteUnknownProcessParameter(supplied.Code, signature.Parameters, "Output", "parameters"))
		}
		if parameter.Direction == "Output" {
			return dataWriteProcessFailure(fmt.Sprintf("'%s' is an Output parameter and cannot be assigned through 'parameters'. Read it back by listing it in 'result-parameters' instead.", supplied.Code))
		}
		if supplied.Value == nil {
			continue
		}
		value, err := dataWriteCoerceProcessValue(parameter, supplied.Value)
		if err != nil {
			return dataWriteProcessFailure(err.Error())
		}
		values = append(values, orderedFields{{"name", parameter.Name}, {"value", value}})
	}
	for _, code := range request.ResultParameters {
		parameter, ok := dataWriteFindProcessParameter(signature.Parameters, code)
		if !ok {
			return dataWriteProcessFailure(dataWriteUnknownProcessParameter(code, signature.Parameters, "Input", "result-parameters"))
		}
		if parameter.Direction == "Input" {
			return dataWriteProcessFailure(fmt.Sprintf("'%s' is an Input parameter and carries no run result, so it cannot be read through 'result-parameters'. Pass it in 'parameters' instead.", code))
		}
	}
	var results any
	if request.ResultParameters != nil {
		names := []any{}
		for _, name := range request.ResultParameters {
			names = append(names, name)
		}
		results = names
	}
	body := toJNode(orderedFields{{"schemaName", signature.ProcessCode}, {"parameterValues", values}, {"resultParameterNames", results}}).stjJSON()
	if request.Gate != nil && !request.Gate.TryMarkSent(time.Now()) {
		return DataWriteProcessResult{Status: DataWriteNotStartedStatus, Warnings: []string{},
			Error: fmt.Sprintf("'%s' was not launched: the MCP call was answered or cancelled before the launch request was sent.", signature.ProcessCode)}
	}
	timeout := 24 * time.Hour * 365
	if request.TimeoutSeconds > 0 {
		timeout = time.Duration(math.Min(float64(request.TimeoutSeconds), float64(math.MaxInt32)/1000)) * time.Second
	}
	payload, err := c.callService(context.WithoutCancel(ctx), serviceCall{Route: dataWriteRunProcessRoute, Body: body, Timeout: timeout, Label: "ProcessEngineService"})
	if err != nil {
		return dataWriteProcessFailure(err.Error())
	}
	if strings.TrimSpace(string(payload)) == "" {
		return dataWriteProcessFailure("RunProcess returned an empty response")
	}
	native, err := dataWriteParseRunResponse(payload)
	if err != nil {
		return dataWriteProcessFailure("RunProcess returned a response clio could not read: " + err.Error())
	}
	response := dataWriteBuildRunResponse(native, signature.ProcessCode)
	c.dataWriteAddLoggedError(ctx, native, &response, request.Deadline)
	return response
}

type dataWriteRunResponse struct {
	ProcessID     string
	ProcessStatus int
	Results       json.RawMessage
	Success       bool
	ErrorCode     string
	ErrorMessage  string
	hasErrorCode  bool
}

func dataWriteParseRunResponse(payload []byte) (dataWriteRunResponse, error) {
	var raw struct {
		ProcessID     *string         `json:"processId"`
		ProcessStatus int             `json:"processStatus"`
		Results       json.RawMessage `json:"resultParameterValues"`
		Success       bool            `json:"success"`
		ErrorInfo     json.RawMessage `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return dataWriteRunResponse{}, err
	}
	native := dataWriteRunResponse{ProcessID: emptyGUID, ProcessStatus: raw.ProcessStatus, Success: raw.Success}
	if raw.ProcessID != nil {
		id, ok := parseGUID(*raw.ProcessID)
		if !ok {
			return dataWriteRunResponse{}, fmt.Errorf("The JSON value could not be converted to System.Guid. Path: $.processId")
		}
		native.ProcessID = id
	}
	if len(raw.Results) > 0 && string(raw.Results) != "null" {
		native.Results = raw.Results
	}
	var info map[string]any
	if json.Unmarshal(raw.ErrorInfo, &info) == nil && info != nil {
		if code, ok := info["errorCode"].(string); ok {
			native.ErrorCode, native.hasErrorCode = code, true
		}
		if message, ok := info["message"].(string); ok {
			native.ErrorMessage = message
		}
	}
	return native, nil
}

func dataWriteIsMissingCompiledMethod(code string) bool {
	return code == dataWriteMissingCompiledCode || strings.HasSuffix(code, "."+dataWriteMissingCompiledCode)
}

// dataWriteBuildRunResponse is RunProcessCommand.BuildResponse.
func dataWriteBuildRunResponse(native dataWriteRunResponse, processCode string) DataWriteProcessResult {
	noHandle := native.ProcessID == emptyGUID && native.ProcessStatus == 0
	if noHandle && !native.Success && dataWriteIsMissingCompiledMethod(native.ErrorCode) {
		return DataWriteProcessResult{Status: dataWriteProcessStatuses[3], Warnings: []string{},
			Error: dataWriteDescribeRunFailure(native.ErrorCode, native.ErrorMessage, processCode)}
	}
	if noHandle && !native.Success {
		return DataWriteProcessResult{Status: DataWriteNotStartedStatus, Warnings: []string{},
			Error: dataWriteRefusalMessage(processCode, native.ErrorCode, native.ErrorMessage)}
	}
	if noHandle {
		return DataWriteProcessResult{Status: dataWriteQueuedStatus, Warnings: []string{fmt.Sprintf("'%s' starts in background mode, so the "+
			"platform queued it and returned no process id, no status and no result parameters. This is not an error — for a fire-and-forget "+
			"process the launch IS the outcome. clio cannot report whether the run succeeded; judge it by the process's own effects. Requesting "+
			"result-parameters forces the same process to run synchronously instead, which is the only way to get a verdict for it.", processCode)}}
	}
	status, ok := dataWriteProcessStatuses[native.ProcessStatus]
	if !ok {
		status = fmt.Sprintf("unknown-status-%d", native.ProcessStatus)
	}
	response := DataWriteProcessResult{Status: status, ProcessID: native.ProcessID, ResultParameterValues: native.Results, Warnings: []string{}}
	if !native.Success || native.ProcessStatus == 3 {
		response.Error = dataWriteDescribeRunFailure(native.ErrorCode, native.ErrorMessage, processCode)
	}
	return response
}

func dataWriteRefusalMessage(processCode, errorCode, message string) string {
	detail := "the platform returned no details"
	if fenced := DataWriteUntrusted(message); fenced != nil {
		detail = *fenced
	}
	if errorCode == dataWriteManualStartRefused {
		return fmt.Sprintf("'%s' cannot be launched: %s. Nothing was started. A process whose only start events are automatic runs when its "+
			"own trigger fires (a record signal, a timer, a schedule) and has no manual entry point, so no run-process call can ever start it — "+
			"cause the trigger instead, or add a manual start event to the process.", processCode, detail)
	}
	return fmt.Sprintf("'%s' was not started: %s.", processCode, detail) + dataWriteDescribeErrorCode(errorCode)
}

func dataWriteDescribeRunFailure(errorCode, message, processCode string) string {
	detail := "the platform returned no error details"
	if fenced := DataWriteUntrusted(message); fenced != nil {
		detail = *fenced
	}
	text := fmt.Sprintf("The process run failed: %s.", detail) + dataWriteDescribeErrorCode(errorCode)
	if dataWriteIsMissingCompiledMethod(errorCode) {
		text += fmt.Sprintf(" This is most often a script task saved since the active version of the process run as '%s' was last compiled - "+
			"run-process runs the active version whatever code it is given - so the compiled code does not have it yet: the run stops when it "+
			"reaches that element, and the elements before it may already have run. If a process-name compile of it succeeded only minutes ago, "+
			"the runtime may still be reloading: run again in about two minutes before compiling again. Otherwise ask the user, then run "+
			"compile-creatio with process-name set to the active version (describe-business-process names it).", processCode)
	}
	return text
}

func dataWriteDescribeErrorCode(errorCode string) string {
	if strings.TrimSpace(errorCode) == "" {
		return ""
	}
	if dataWriteErrorCodeShape.MatchString(errorCode) {
		return " [" + errorCode + "]"
	}
	fenced := DataWriteUntrusted(errorCode)
	if fenced == nil {
		return " []"
	}
	return " [" + *fenced + "]"
}

// dataWriteAddLoggedError is RunProcessCommand.AddLoggedError: a failed run with no errorCode gets the first
// line of the error its SysProcessLog row carries, read within what is left of the response deadline.
func (c *Client) dataWriteAddLoggedError(ctx context.Context, native dataWriteRunResponse, response *DataWriteProcessResult, deadline time.Time) {
	if response.Error == "" || native.ProcessID == emptyGUID || strings.TrimSpace(native.ErrorCode) != "" {
		return
	}
	readTimeout := dataWriteLogReadTimeout
	if !deadline.IsZero() {
		left := time.Until(deadline) - dataWriteLogReadMargin
		if left < dataWriteMinimumLogRead {
			response.Warnings = append(response.Warnings, fmt.Sprintf("The error this run logged was not read, because too little of the "+
				"response time was left; it is in the process log of run %s.", native.ProcessID))
			return
		}
		readTimeout = min(readTimeout, left)
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	rows, err := c.selectRows(readCtx, buildSelectQuery("SysProcessLog", map[string]string{"ErrorDescription": "ErrorDescription"},
		map[string]any{"byId": comparisonFilter("Id", native.ProcessID, 0, 3)}, 1))
	if err != nil {
		response.Warnings = append(response.Warnings, fmt.Sprintf("The error this run logged could not be read; it is in the process log of run %s.", native.ProcessID))
		return
	}
	if len(rows) == 0 {
		return
	}
	for _, line := range strings.FieldsFunc(rowString(rows[0], "ErrorDescription"), func(r rune) bool { return r == '\r' || r == '\n' }) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			if fenced := DataWriteUntrusted(trimmed); fenced != nil {
				response.Error += " The process log of this run reports: " + *fenced
			}
			return
		}
	}
}

func dataWriteFindProcessParameter(parameters []ProcessSignatureParameter, code string) (ProcessSignatureParameter, bool) {
	for _, p := range parameters {
		if p.Name == code {
			return p, true
		}
	}
	return ProcessSignatureParameter{}, false
}

func dataWriteUnknownProcessParameter(code string, parameters []ProcessSignatureParameter, excluded, argument string) string {
	for _, p := range parameters {
		if strings.EqualFold(p.Name, code) {
			return fmt.Sprintf("'%s' does not match any parameter of the process. Parameter codes are case-sensitive — did you mean '%s'?", code, p.Name)
		}
	}
	names := []string{}
	for _, p := range parameters {
		if p.Direction != excluded {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	list := "the process declares none"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	return fmt.Sprintf("'%s' is not a parameter of the process. Codes accepted by '%s': %s. Use the parameter CODE from get-process-signature, not its caption — the platform silently drops a value keyed by a caption.", code, argument, list)
}

// dataWriteRawText is JsonElement.GetString() for a string and GetRawText() for anything else.
func dataWriteRawText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func dataWriteValueKind(value any) string {
	switch value.(type) {
	case string:
		return "String"
	case float64, json.Number:
		return "Number"
	case bool:
		if value.(bool) {
			return "True"
		}
		return "False"
	case []any:
		return "Array"
	case map[string]any:
		return "Object"
	}
	return "Null"
}

// dataWriteCoerceProcessValue is RunProcessCommand.TryCoerce.
func dataWriteCoerceProcessValue(parameter ProcessSignatureParameter, value any) (string, error) {
	lookup := parameter.IsLookup
	switch {
	case parameter.ClrType == "System.String" && !lookup:
		return dataWriteRawText(value), nil
	case parameter.ClrType == "System.Guid" || lookup:
		raw := dataWriteRawText(value)
		if id, ok := parseGUID(raw); ok {
			return id, nil
		}
		return "", fmt.Errorf("'%s' expects a record id (Guid) but received '%s'. A lookup parameter takes the record's Id, never its display name — resolve the id first (for example with odata-read on the referenced object).", parameter.Name, raw)
	case parameter.ClrType == "System.Boolean":
		switch typed := value.(type) {
		case bool:
			if typed {
				return "true", nil
			}
			return "false", nil
		case string:
			switch strings.ToLower(strings.TrimSpace(typed)) {
			case "true":
				return "true", nil
			case "false":
				return "false", nil
			}
		}
		return "", fmt.Errorf("'%s' expects a boolean but received %s.", parameter.Name, dataWriteValueKind(value))
	case parameter.ClrType == "System.DateTime":
		raw := dataWriteRawText(value)
		if text, ok := dataWriteRoundTripDate(raw); ok {
			return text, nil
		}
		return "", fmt.Errorf("'%s' expects a date/time but received '%s'. Pass it in an invariant format such as 2026-08-26T13:45:00Z.", parameter.Name, raw)
	}
	return dataWriteRawText(value), nil
}

// dataWriteRoundTripDate is DateTime.TryParse(raw, InvariantCulture, RoundtripKind) then ToString("o"): a UTC
// value keeps "Z", an offset becomes local time with the local offset, a value without a zone has none.
func dataWriteRoundTripDate(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		if strings.HasSuffix(strings.ToUpper(text), "Z") {
			return parsed.UTC().Format("2006-01-02T15:04:05.0000000Z"), true
		}
		return parsed.Local().Format("2006-01-02T15:04:05.0000000-07:00"), true
	}
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02", "01/02/2006 15:04:05", "01/02/2006"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Format("2006-01-02T15:04:05.0000000"), true
		}
	}
	return "", false
}
