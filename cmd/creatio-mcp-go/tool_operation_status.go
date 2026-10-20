package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// compile-status and restart-status read the operation registry (operations.go) that compile-creatio and
// the restart tools record into, scoped to the caller's environment as clio scopes them. A compile-status
// not-found answer then reads the environment's own compilation history (clio 8.1.0.142, ENG-102333); nothing
// else is sent to Creatio.
const (
	// clio 8.1.0.142's RestartStatusTool.NotFoundNote.
	runtimeRestartNotFoundNote = "This MCP server session holds no record of a restart operation for this environment. That does not mean " +
		"no restart ran: a record lives only in this session, and only for a while after its restart ends. Do " +
		"not restart again to find out - check that the environment answers, for example with " +
		"describe-environment (through clio-run); a restarted instance can take 1-10 minutes to warm up."

	// clio 8.1.0.142's CompileStatusTool texts.
	compileNotARunVerdict = " That does not mean no compile ran: a record lives only in this session, and only for a while after its " +
		"compile ends. Do not compile again to find out."
	compileHistoryNote = " compilation-history lists the environment's newest compilation-history rows, newest first, and " +
		"older-failures up to five failed rows from further back among the newest 100. A compile writes one row per " +
		"project as that project's build ends, so one compile's rows arrive over its run, up to about half a minute " +
		"apart (finished-utc; finished-seconds-ago counts back from checked-utc). Only rows that finished after your " +
		"call can be your compile's: compare finished-utc with the started-utc of compile-creatio's in-progress " +
		"answer, allowing a minute either way because the environment's clock and this host's can differ, or, " +
		"without one, take your call to be at least as long ago as your client's timeout error says it waited, plus " +
		"the time since. A row that finished clearly before your call is not your compile's, even one more than seven " +
		"minutes old that looks like a finished compile. Other compiles and schema publishes write rows too, so match by " +
		"time. Any of them with succeeded=false and errors means a build failed. Treat your compile as finished only " +
		"when its newest row is more than seven minutes old: until then more rows can follow, and the application " +
		"reloads about two minutes after the last one. These rows cannot prove which compile wrote them, so ask the " +
		"user before a restart that rests on them alone. If no row was written since your call, the compile has " +
		"either not finished its first project yet or never started: a request still on its way when this MCP server " +
		"restarted was lost with it. Call compile-status again in a minute or two. If a package or process-name " +
		"compile has had ten minutes, or a full one about 20, and still no row was written since your call, treat it " +
		"as not run and ask the user before compiling again. Do not read last-compilation-log" +
		" for it: with no row since your call it can only return an earlier compile's verdict."
	compileHistoryRuleSummary = "only rows that finished after your call (the started-utc of compile-creatio's in-progress answer) can be your " +
		"compile's, and " +
		"other compiles write rows too; it has finished only once its newest row is over seven minutes old, and a " +
		"restart resting on those rows alone needs the user's confirmation; none yet means it has not finished a " +
		"project yet or never started, and if none appears within ten minutes (about 20 for a full compile), treat it " +
		"as not run and ask the user before compiling again - last-compilation-log could then only " +
		"show an earlier compile's verdict."
	compileHistoryUnavailableNote = " The environment's compilation history could not be read (see compilation-history-error), so this answer " +
		"cannot say whether that compile finished. Right after a compile the application reloads and briefly stops " +
		"answering, so call compile-status again in a minute; if the next poll or two still cannot read it, stop " +
		"polling. last-compilation-log (through clio-run) reads the environment's latest FINISHED " +
		"compile and carries no time, so until a compile you started has had time to finish (a package or " +
		"process-name compile about ten minutes, a full one about 20) it can return an earlier compile's verdict: " +
		"do not rely on it, or restart on it, before then. Even after that it cannot show that your compile ran: a " +
		"compile whose request was lost when this MCP server restarted never ran, and the verdict is then an earlier " +
		"compile's. Tell the user it is unconfirmed, and ask before compiling again or restarting on it."
	compileUnresolvedEnvironmentNote = " Neither this answer nor last-compilation-log can read that environment until environment-name names one " +
		"this MCP server can reach (see compilation-history-error)."
	compileHistoryUnreadableError = "The environment's compilation history could not be read: the environment did not answer in time, or did not " +
		"answer with compilation-history rows and the session's time-zone offset."
	compileHistoryUnresolvedError = "environment-name does not name an environment this MCP server can reach, so its compilation history cannot be " +
		"read; asking again will not help. Check the name with list-environments."
)

// compileEchoableOperationID is clio's EchoableOperationId: only an id shaped like the GUIDs compile-creatio
// issues is repeated back.
var compileEchoableOperationID = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)

// compileNoRecordSentence is CompileStatusTool.NoRecordSentence.
func compileNoRecordSentence(operationID string) string {
	trimmed := strings.TrimSpace(operationID)
	switch {
	case trimmed == "":
		return "This MCP server session holds no record of a compile-creatio operation for this environment."
	case compileEchoableOperationID.MatchString(trimmed):
		return "This MCP server session holds no record of operation-id '" + trimmed + "' for this environment."
	default:
		return "This MCP server session holds no record of the operation-id you passed for this environment."
	}
}

// compileHistoryReader reads an environment's compilation history; a seam for tests.
var compileHistoryReader = func(ctx context.Context, client *creatio.Client) (*creatio.CompilationHistoryReading, error) {
	return client.ReadCompilationHistory(ctx, creatio.CompilationHistoryFetchCount, creatio.CompilationHistoryRequestTimeout)
}

// compileHistoryBudget bounds the whole history read; a seam for tests.
var compileHistoryBudget = creatio.CompilationHistoryReadBudget

// compileStatusResponse is clio's CompileStatusResponse; restart-status leaves the compile-only fields out.
type compileStatusResponse struct {
	Success         bool     `json:"success"`
	Status          string   `json:"status"`
	OperationID     string   `json:"operation-id,omitempty"`
	EnvironmentName string   `json:"environment-name,omitempty"`
	PackageName     string   `json:"package-name,omitempty"`
	StartedUTC      *utcTime `json:"started-utc,omitempty"`
	FinishedUTC     *utcTime `json:"finished-utc,omitempty"`
	ExitCode        *int     `json:"exit-code,omitempty"`
	MessageTail     []string `json:"message-tail,omitzero"`
	Note            string   `json:"note,omitempty"`
	ProcessName     string   `json:"process-name,omitempty"`
	// The not-found history of clio 8.1.0.142's compile-status.
	CheckedUTC              *utcTime                      `json:"checked-utc,omitempty"`
	CompilationHistory      []creatio.CompileHistoryEntry `json:"compilation-history,omitempty"`
	CompilationHistoryError string                        `json:"compilation-history-error,omitempty"`
	OlderFailures           []creatio.CompileHistoryEntry `json:"older-failures,omitempty"`
}

func init() {
	registerTool(map[string]any{
		"name":        "compile-status",
		"description": "Returns the status of the most recent compile-creatio operation tracked for an environment, or of a specific operation-id from a compile-creatio in-progress response. Use this after compile-creatio returns an in-progress note, AND after your MCP client stopped waiting for compile-creatio (for example 'Request timed out'): the compile may still be running, and while this MCP server has not restarted it is tracked here. Do not re-run compile-creatio just to check. A not-found answer means this MCP server session holds no record, not that nothing ran: it then lists the environment's newest compilation-history rows with the time each finished (UTC, and seconds before checked-utc): " + compileHistoryRuleSummary,
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"operation-id": map[string]any{"type": "string", "description": "Optional operation id from a compile-creatio in-progress response. When omitted, returns the most recently started operation for this environment."}}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return operationStatusResult(ctx, "compile-status", envs, args, compileOperations, compileNotFound)
	})
	registerTool(map[string]any{
		"name":        "restart-status",
		"description": "Returns the readiness status of the most recent restart tracked for an environment, or of a specific operation-id from a restart-by-environment-name in-progress response. Use this after that tool returns an in-progress note to check whether the instance finished warming up; do not re-run the restart just to check. Restarts started via restart-by-credentials are NOT reportable here: lookups are keyed by a registered environment name, which that path does not have.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"operation-id": map[string]any{"type": "string", "description": "Optional operation id from a restart in-progress response. When omitted, returns the most recently started restart for this environment."}}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return operationStatusResult(ctx, "restart-status", envs, args, restartOperations,
			func(context.Context, *environments, string, string) compileStatusResponse {
				return compileStatusResponse{Note: runtimeRestartNotFoundNote}
			})
	})
}

// operationStatusResult is lenient like clio (unknown keys are ignored). environment-name is echoed exactly
// as passed; an unknown name answers not-found, as in clio.
func operationStatusResult(ctx context.Context, tool string, envs *environments, args map[string]any, store *operationStore,
	notFound func(ctx context.Context, envs *environments, name, operationID string) compileStatusResponse) (*mcp.CallToolResult, error) {
	operationID, err := optionalStringArg(args, tool, "operation-id")
	if err != nil {
		return nil, err
	}
	name, err := optionalStringArg(args, tool, "environment-name")
	if err != nil {
		return nil, err
	}
	record, ok := store.lookup(envs.tenantKey(name), operationID)
	if !ok {
		response := notFound(ctx, envs, name, operationID)
		response.Success, response.Status, response.EnvironmentName = true, "not-found", name
		return structuredToolResult(response), nil
	}
	environmentName := record.EnvironmentName
	if environmentName == "" {
		environmentName = name
	}
	return structuredToolResult(compileStatusResponse{
		Success: true, Status: record.Status, OperationID: record.ID, EnvironmentName: environmentName,
		PackageName: record.Details.PackageName, ProcessName: record.Details.ProcessName,
		StartedUTC: utcTimePointer(&record.StartedUTC), FinishedUTC: utcTimePointer(record.FinishedUTC),
		ExitCode: record.ExitCode, MessageTail: record.MessageTail,
	}), nil
}

// compileNotFound is CompileStatusTool.BuildNotFound: the missing record, then the environment's newest
// compilation-history rows, or why they could not be read.
func compileNotFound(ctx context.Context, envs *environments, name, operationID string) compileStatusResponse {
	noRecord := compileNoRecordSentence(operationID) + compileNotARunVerdict
	reading, failure := readCompileHistory(ctx, envs, name)
	if reading == nil {
		note := compileHistoryUnavailableNote
		if failure == compileHistoryUnresolvedError {
			note = compileUnresolvedEnvironmentNote
		}
		return compileStatusResponse{Note: noRecord + note, CompilationHistoryError: failure}
	}
	listed, older := creatio.CompileHistoryEntries(reading)
	checked := utcTime(reading.CheckedUTC)
	return compileStatusResponse{Note: noRecord + compileHistoryNote, CheckedUTC: &checked,
		CompilationHistory: listed, OlderFailures: older}
}

// readCompileHistory is CompileStatusTool.TryReadHistory: a name that does not resolve is the caller's mistake;
// anything else - an unreachable host, a login page, a failed query, a read past its bound - is "could not be
// read".
func readCompileHistory(ctx context.Context, envs *environments, name string) (*creatio.CompilationHistoryReading, string) {
	if envs == nil {
		return nil, compileHistoryUnresolvedError
	}
	client, err := envs.client(name, creatio.ConnectionOverrides{})
	if err != nil {
		var target *environmentError
		if errors.As(err, &target) && !target.safe {
			return nil, compileHistoryUnresolvedError
		}
		return nil, compileHistoryUnreadableError
	}
	reader, budget := compileHistoryReader, compileHistoryBudget
	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type outcome struct {
		reading *creatio.CompilationHistoryReading
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- outcome{err: errors.New("history read failed")}
			}
		}()
		reading, err := reader(bounded, client)
		done <- outcome{reading, err}
	}()
	select {
	case result := <-done:
		if result.err != nil || result.reading == nil {
			return nil, compileHistoryUnreadableError
		}
		return result.reading, ""
	case <-time.After(budget):
		return nil, compileHistoryUnreadableError
	}
}
