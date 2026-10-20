package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// clio 8.1.0.142 (ENG-102333): a compile-status not-found answer reads the environment's compilation history.

func invokeStatus(t *testing.T, envs *environments, args map[string]any) map[string]any {
	t.Helper()
	result, err := registeredTools["compile-status"].invoke(context.Background(), envs, args)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	encoded, _ := json.Marshal(result.StructuredContent)
	_ = json.Unmarshal(encoded, &decoded)
	return decoded
}

func withHistoryReader(t *testing.T, reader func(context.Context, *creatio.Client) (*creatio.CompilationHistoryReading, error)) {
	t.Helper()
	previous := compileHistoryReader
	compileHistoryReader = reader
	t.Cleanup(func() { compileHistoryReader = previous })
}

func TestCompileStatusNotFoundListsTheCompilationHistory(t *testing.T) {
	checked := time.Date(2026, 10, 20, 19, 11, 59, 992039000, time.UTC)
	name := "Terrasoft.Configuration.Main.csproj"
	withHistoryReader(t, func(context.Context, *creatio.Client) (*creatio.CompilationHistoryReading, error) {
		return &creatio.CompilationHistoryReading{CheckedUTC: checked, Rows: []creatio.CompilationHistoryRow{
			{ProjectName: &name, FinishedUTC: checked.Add(-90 * time.Second), DurationSeconds: 4, Succeeded: true}}}, nil
	})
	got := invokeStatus(t, staticEnvironments(&creatio.Client{}), map[string]any{"operation-id": " zz-no-such-operation "})
	want := "This MCP server session holds no record of the operation-id you passed for this environment." +
		compileNotARunVerdict + compileHistoryNote
	history, _ := got["compilation-history"].([]any)
	if got["status"] != "not-found" || got["note"] != want || got["checked-utc"] != "2026-10-20T19:11:59.992039Z" || len(history) != 1 {
		t.Fatalf("answer = %#v", got)
	}
	row := history[0].(map[string]any)
	if row["project-name"] != name || row["finished-seconds-ago"] != float64(90) || row["finished-utc"] != "2026-10-20T19:10:29.992039Z" {
		t.Fatalf("row = %#v", row)
	}
	if _, ok := got["compilation-history-error"]; ok {
		t.Fatalf("a read history carries no error: %#v", got)
	}
}

func TestCompileStatusNotFoundSaysWhyTheHistoryIsMissing(t *testing.T) {
	withHistoryReader(t, func(context.Context, *creatio.Client) (*creatio.CompilationHistoryReading, error) {
		return nil, errors.New("login page")
	})
	got := invokeStatus(t, staticEnvironments(&creatio.Client{}), map[string]any{})
	if got["note"] != "This MCP server session holds no record of a compile-creatio operation for this environment."+
		compileNotARunVerdict+compileHistoryUnavailableNote || got["compilation-history-error"] != compileHistoryUnreadableError {
		t.Fatalf("unreadable answer = %#v", got)
	}
	got = invokeStatus(t, staticEnvironments(nil), map[string]any{"environment-name": "zz-no-such-environment"})
	if got["environment-name"] != "zz-no-such-environment" || got["compilation-history-error"] != compileHistoryUnresolvedError ||
		got["note"] != "This MCP server session holds no record of a compile-creatio operation for this environment."+
			compileNotARunVerdict+compileUnresolvedEnvironmentNote {
		t.Fatalf("unresolved answer = %#v", got)
	}
}

func TestCompileStatusAbandonsAHistoryReadPastItsBudget(t *testing.T) {
	previous := compileHistoryBudget
	compileHistoryBudget = 20 * time.Millisecond
	t.Cleanup(func() { compileHistoryBudget = previous })
	release := make(chan struct{})
	defer close(release)
	withHistoryReader(t, func(context.Context, *creatio.Client) (*creatio.CompilationHistoryReading, error) {
		<-release
		return nil, nil
	})
	got := invokeStatus(t, staticEnvironments(&creatio.Client{}), map[string]any{"operation-id": "0f"})
	if got["compilation-history-error"] != compileHistoryUnreadableError ||
		got["note"] != "This MCP server session holds no record of operation-id '0f' for this environment."+compileNotARunVerdict+compileHistoryUnavailableNote {
		t.Fatalf("answer = %#v", got)
	}
}

func TestRestartStatusNotFoundNoteIsClio142s(t *testing.T) {
	result, err := registeredTools["restart-status"].invoke(context.Background(), staticEnvironments(nil), map[string]any{"environment-name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	if string(encoded) != `{"success":true,"status":"not-found","environment-name":"x","note":"`+runtimeRestartNotFoundNote+`"}` {
		t.Fatalf("answer = %s", encoded)
	}
}
