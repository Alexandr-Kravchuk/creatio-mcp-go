package main

import (
	"net/http"
	"strings"
	"testing"
)

// The refusals clio 8.1.0.142 gives for run-process's wait, wait-timeout, watch and cleanup, measured with
// its MCP server on a registered environment that cannot be reached (T20): they come before any request.
func TestRunProcessVerificationArgumentsOfClio142(t *testing.T) {
	requests := 0
	session := dataWriteTestSession(t, func(w http.ResponseWriter, r *http.Request) { requests++ })
	base := func(extra map[string]any) map[string]any {
		args := map[string]any{"environment-name": "parity-stand", "process-name": "UsrP"}
		for key, value := range extra {
			args[key] = value
		}
		return args
	}
	for _, c := range []struct {
		args   map[string]any
		want   string
		raised bool
	}{
		{base(map[string]any{"wait-timeout": 301.0}), "wait-timeout must be between 0 and 300 seconds.", false},
		{base(map[string]any{"wait-timeout": 1.5}), "invalid-parameter-type: argument 'wait-timeout' for MCP tool 'run-process' must be a number.", true},
		{base(map[string]any{"watch": []any{"A", "B", "C", "D", "E", "F"}}), "watch takes at most 5 entities.", false},
		{base(map[string]any{"watch": []any{nil}}), "watch must list entity schema names (letters, digits, underscore); '' is not one.", false},
		{base(map[string]any{"watch": []any{1.0}}), "invalid-parameter-type: argument 'watch' for MCP tool 'run-process' contains a value that does not match the documented shape.", true},
		{base(map[string]any{"watch": "Activity"}), "invalid-parameter-type: argument 'watch' for MCP tool 'run-process' must be an array.", true},
		{base(map[string]any{"cleanup": true, "wait": nil}), "cleanup deletes only what the run created in the watched entities, so it needs watch", false},
		{base(map[string]any{"wait": "yes"}), "invalid-parameter-type: argument 'wait' for MCP tool 'run-process' must be a boolean.", true},
		{base(map[string]any{"bogus": 1.0}), "Unknown args: 'bogus'. Valid: environment-name, process-name, parameters, result-parameters, timeout, wait, wait-timeout, watch, cleanup, uri, login, password.", false},
	} {
		result, answer := dataWriteTestCall(t, session, "clio-run", "run-process", c.args)
		if c.raised {
			if !result.IsError || !strings.Contains(dataWriteTestText(result), c.want) {
				t.Fatalf("%v: %s", c.args, dataWriteTestText(result))
			}
			continue
		}
		if message, _ := answer["error"].(string); result.IsError || !strings.HasPrefix(message, c.want) {
			t.Fatalf("%v: %s", c.args, dataWriteTestText(result))
		}
	}
	if requests != 0 {
		t.Fatalf("%d requests reached Creatio", requests)
	}
}
