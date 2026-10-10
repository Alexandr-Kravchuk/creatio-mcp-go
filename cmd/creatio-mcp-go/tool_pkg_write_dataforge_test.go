package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDataForgeMaintenanceThroughClioRun(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/ApplicationInfoService.svc/GetApplicationInfo": func(string) (int, string) {
			return 200, `{"success":true,"applicationInfo":{"sysValues":{"coreVersion":"10.2.363.0"}}}`
		},
		"/DataForgeMaintenanceService/InitializeDataStructuresAndLookups": func(string) (int, string) { return 200, `{}` },
		"/DataForgeMaintenanceService/UpdateDataStructuresAndLookups":     func(string) (int, string) { return 400, `<html>no</html>` },
	})
	session := pkgWriteSession(t, fake, nil)
	type answer struct {
		Success bool     `json:"success"`
		Source  string   `json:"source"`
		Warn    []string `json:"warnings"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Status struct {
			Success bool    `json:"success"`
			Status  string  `json:"status"`
			Error   *string `json:"error"`
		} `json:"status"`
	}
	for _, tool := range []string{"dataforge-initialize", "dataforge-update"} {
		var got answer
		pkgWriteCall(t, session, tool, map[string]any{"environment-name": "parity-env"}, &got)
		if !got.Success || got.Source != "clio+dataforge-service" || got.Status.Status != "Scheduled" || !got.Status.Success || got.Error != nil {
			t.Fatalf("%s = %+v", tool, got)
		}
		pkgWriteAssertGated(t, session, tool, map[string]any{"environment-name": "parity-env"})
	}
	calls := fake.recorded()
	if len(calls) != 4 || !strings.HasSuffix(calls[1], "/0/rest/DataForgeMaintenanceService/InitializeDataStructuresAndLookups {}") ||
		!strings.HasSuffix(calls[3], "/0/rest/DataForgeMaintenanceService/UpdateDataStructuresAndLookups {}") {
		t.Fatalf("calls = %q", calls)
	}
	for _, args := range []string{`{}`, `{"environmentName":"parity-env"}`, `{"environment-name":"  "}`} {
		var input map[string]any
		_ = json.Unmarshal([]byte(args), &input)
		var got answer
		pkgWriteCall(t, session, "dataforge-initialize", input, &got)
		if got.Success || got.Error == nil || got.Error.Code != "initialize_error" || got.Error.Message != "environment-name is required." ||
			got.Status.Status != "Failed" || got.Status.Error == nil || *got.Status.Error != "environment-name is required." {
			t.Fatalf("%s = %+v", args, got)
		}
	}
	var unknown answer
	pkgWriteCall(t, session, "dataforge-update", map[string]any{"environment-name": "missing-env"}, &unknown)
	if unknown.Success || unknown.Error == nil || unknown.Error.Code != "update_error" || !strings.HasPrefix(unknown.Error.Message, "Environment with key 'missing-env' not found.") {
		t.Fatalf("unknown = %+v", unknown)
	}
	typed := pkgWriteCall(t, session, "dataforge-update", map[string]any{"environment-name": 5}, nil)
	if !typed.IsError || !strings.Contains(pkgWriteText(typed), "argument 'environment-name' for MCP tool 'dataforge-update' must be a string") {
		t.Fatalf("typed = %s", pkgWriteText(typed))
	}
	if len(fake.recorded()) != 4 {
		t.Fatalf("refusals must not reach Creatio: %q", fake.recorded())
	}
}
