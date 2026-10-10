package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type DataWriteProcessResult struct {
	Status                string         `json:"status,omitempty"`
	ProcessID             string         `json:"processId,omitempty"`
	ResultParameterValues map[string]any `json:"resultParameterValues,omitempty"`
	Warnings              []string       `json:"warnings"`
	Error                 string         `json:"error,omitempty"`
}

func dataWriteProcessFailure(message string) DataWriteProcessResult {
	return DataWriteProcessResult{Warnings: []string{}, Error: message}
}

func (c *Client) DataWriteRunProcess(ctx context.Context, name string, parameters map[string]json.RawMessage, results []string, timeout int) DataWriteProcessResult {
	if strings.TrimSpace(name) == "" {
		return dataWriteProcessFailure("process-name is required")
	}
	signature := c.GetProcessSignature(ctx, name, "en-US")
	if !signature.Success {
		return dataWriteProcessFailure(signature.Error)
	}
	if signature.ProcessCode != name {
		return dataWriteProcessFailure(fmt.Sprintf("'%s' is not a process CODE. It resolved to '%s', so it was a display caption or the wrong casing. Pass the code: a caption is not unique and is not what the platform launches by, and this tool starts a process rather than reading one, so an ambiguous key could start the wrong one. Use '%s', or get-process-signature to confirm the code.", name, signature.ProcessCode, signature.ProcessCode))
	}
	values := []map[string]string{}
	for code, raw := range parameters {
		parameter, ok := dataWriteFindProcessParameter(signature.Parameters, code)
		if !ok {
			return dataWriteProcessFailure(dataWriteUnknownProcessParameter(code, signature.Parameters, "Output", "parameters"))
		}
		if parameter.Direction == "Output" {
			return dataWriteProcessFailure(fmt.Sprintf("'%s' is an Output parameter and cannot be assigned through 'parameters'. Read it back by listing it in 'result-parameters' instead.", code))
		}
		if string(raw) == "null" {
			continue
		}
		value, err := dataWriteCoerceProcessParameter(parameter, raw)
		if err != nil {
			return dataWriteProcessFailure(err.Error())
		}
		values = append(values, map[string]string{"name": code, "value": value})
	}
	for _, code := range results {
		parameter, ok := dataWriteFindProcessParameter(signature.Parameters, code)
		if !ok {
			return dataWriteProcessFailure(dataWriteUnknownProcessParameter(code, signature.Parameters, "Input", "result-parameters"))
		}
		if parameter.Direction == "Input" {
			return dataWriteProcessFailure(fmt.Sprintf("'%s' is an Input parameter and carries no run result, so it cannot be read through 'result-parameters'. Pass it in 'parameters' instead.", code))
		}
	}
	request := map[string]any{"schemaName": name, "parameterValues": values, "resultParameterNames": results}
	body, _ := json.Marshal(request)
	requestTimeout := time.Duration(timeout) * time.Second
	if timeout <= 0 {
		requestTimeout = 24 * time.Hour
	}
	response, err := c.dataWriteSendOnce(ctx, "POST", "ServiceModel/ProcessEngineService.svc/RunProcess", body, requestTimeout)
	if err != nil {
		return dataWriteProcessFailure(err.Error())
	}
	if strings.TrimSpace(response) == "" {
		return dataWriteProcessFailure("RunProcess returned an empty response")
	}
	var native struct {
		ProcessID             string         `json:"processId"`
		ProcessStatus         int            `json:"processStatus"`
		ResultParameterValues map[string]any `json:"resultParameterValues"`
		Success               bool           `json:"success"`
		ErrorInfo             struct {
			ErrorCode string `json:"errorCode"`
			Message   string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal([]byte(response), &native); err != nil {
		return dataWriteProcessFailure("RunProcess returned a response clio could not read: " + err.Error())
	}
	if native.ProcessID == "" {
		native.ProcessID = emptyGUID
	}
	if native.ProcessID == emptyGUID && native.ProcessStatus == 0 {
		if !native.Success {
			detail := native.ErrorInfo.Message
			if detail == "" {
				detail = "the platform returned no details"
			}
			message := fmt.Sprintf("'%s' was not started: %s.", name, detail)
			if native.ErrorInfo.ErrorCode != "" {
				message += " [" + native.ErrorInfo.ErrorCode + "]"
			}
			return DataWriteProcessResult{Status: "not-started", Warnings: []string{}, Error: message}
		}
		return DataWriteProcessResult{Status: "queued-background", Warnings: []string{fmt.Sprintf("'%s' starts in background mode, so the platform queued it and returned no process id, no status and no result parameters. This is not an error — for a fire-and-forget process the launch IS the outcome. clio cannot report whether the run succeeded; judge it by the process's own effects. Requesting result-parameters forces the same process to run synchronously instead, which is the only way to get a verdict for it.", name)}}
	}
	statuses := []string{"inactive", "running", "completed", "error", "cancelled", "cancelling"}
	status := fmt.Sprintf("unknown-status-%d", native.ProcessStatus)
	if native.ProcessStatus >= 0 && native.ProcessStatus < len(statuses) {
		status = statuses[native.ProcessStatus]
	}
	answer := DataWriteProcessResult{Status: status, ProcessID: strings.ToLower(native.ProcessID), ResultParameterValues: native.ResultParameterValues, Warnings: []string{}}
	if !native.Success || native.ProcessStatus == 3 {
		detail := native.ErrorInfo.Message
		if detail == "" {
			detail = "the platform returned no error details"
		}
		answer.Error = "The process run failed: " + detail + "."
		if native.ErrorInfo.ErrorCode != "" {
			answer.Error += " [" + native.ErrorInfo.ErrorCode + "]"
		}
	}
	return answer
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
	names := []string{}
	for _, p := range parameters {
		if strings.EqualFold(p.Name, code) {
			return fmt.Sprintf("'%s' does not match any parameter of the process. Parameter codes are case-sensitive — did you mean '%s'?", code, p.Name)
		}
		if p.Direction != excluded {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	list := strings.Join(names, ", ")
	if list == "" {
		list = "the process declares none"
	}
	return fmt.Sprintf("'%s' is not a parameter of the process. Codes accepted by '%s': %s. Use the parameter CODE from get-process-signature, not its caption — the platform silently drops a value keyed by a caption.", code, argument, list)
}

func dataWriteCoerceProcessParameter(parameter ProcessSignatureParameter, raw json.RawMessage) (string, error) {
	var scalar any
	if err := json.Unmarshal(raw, &scalar); err != nil {
		return "", err
	}
	value, ok := scalar.(string)
	if !ok {
		value = string(raw)
	}
	if parameter.ClrType == "System.Guid" || parameter.IsLookup {
		if !dataWriteIsGUID(value) {
			return "", fmt.Errorf("'%s' expects a record id (Guid) but received '%s'. A lookup parameter takes the record's Id, never its display name — resolve the id first (for example with odata-read on the referenced object).", parameter.Name, value)
		}
		return strings.ToLower(value), nil
	}
	if parameter.ClrType == "System.Boolean" {
		if strings.EqualFold(value, "true") {
			return "true", nil
		}
		if strings.EqualFold(value, "false") {
			return "false", nil
		}
		return "", fmt.Errorf("'%s' expects a boolean but received String.", parameter.Name)
	}
	return value, nil
}
