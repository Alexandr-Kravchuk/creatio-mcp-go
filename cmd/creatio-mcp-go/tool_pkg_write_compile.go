package main

// compile-creatio: clio master's CompileCreatioTool. The scope is refused before anything is tracked when it
// is not exactly what the caller asked for (an unbound or legacy key, a package list, a blank or explicit-null
// scope argument, or both scopes), because each of those read as "omitted" would become a FULL compile. A
// second compile for the same target while one runs in this server is refused, the compile is recorded for
// compile-status, and the answer is in-progress once the response deadline passes.
// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)].

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgWriteCompileAliases are CompileCreatioTool.LegacyAliases (OrdinalIgnoreCase): clio's environment-name
// aliases plus the package and process spellings.
var pkgWriteCompileAliases = map[string]string{
	"environmentname": "environment-name", "environment_name": "environment-name", "environment": "environment-name",
	"packagename": "package-name", "package_name": "package-name", "package": "package-name",
	"processname": "process-name", "process_name": "process-name", "process": "process-name",
}

const pkgWriteCompileHint = "Valid: environment-name, package-name, process-name; omit both scope arguments for a full compile."

// pkgWriteBuilds is McpToolExecutionLock's configuration-build reservation: one compile per target in this
// server at a time.
var pkgWriteBuilds = struct {
	sync.Mutex
	reserved map[string]bool
}{reserved: map[string]bool{}}

func pkgWriteReserveBuild(key string) bool {
	pkgWriteBuilds.Lock()
	defer pkgWriteBuilds.Unlock()
	if pkgWriteBuilds.reserved[key] {
		return false
	}
	pkgWriteBuilds.reserved[key] = true
	return true
}

func pkgWriteReleaseBuild(key string) {
	pkgWriteBuilds.Lock()
	defer pkgWriteBuilds.Unlock()
	delete(pkgWriteBuilds.reserved, key)
}

// pkgWriteCompileArgs is the bound CompileCreatioArgs record.
type pkgWriteCompileArgs struct {
	environmentName, packageName, processName string
	present, null                             map[string]bool
	extras                                    []string
}

// pkgWriteBindCompile binds the record as clio's binder does: names in any letter case (the last one wins),
// a value of the wrong type refused before the tool runs, every other key kept for the alias refusal.
func pkgWriteBindCompile(args map[string]any) (pkgWriteCompileArgs, error) {
	bound := pkgWriteCompileArgs{present: map[string]bool{}, null: map[string]bool{}}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var target *string
		name := strings.ToLower(key)
		switch name {
		case "environment-name":
			target = &bound.environmentName
		case "package-name":
			target = &bound.packageName
		case "process-name":
			target = &bound.processName
		default:
			bound.extras = append(bound.extras, key)
			continue
		}
		switch value := args[key].(type) {
		case nil:
			*target = ""
			bound.null[name] = true
			bound.present[name] = false
		case string:
			*target = value
			bound.present[name] = true
			delete(bound.null, name)
		default:
			return bound, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool 'compile-creatio' must be a string. Received an incompatible JSON value.", name)
		}
	}
	return bound, nil
}

// pkgWriteCompileAliasError is McpToolArgumentSupport.BuildLegacyAliasError over the overflow bag.
func pkgWriteCompileAliasError(extras []string) string {
	if len(extras) == 0 {
		return ""
	}
	var renamed, unknown []string
	for _, key := range extras {
		if canonical, ok := pkgWriteCompileAliases[strings.ToLower(key)]; ok {
			renamed = append(renamed, fmt.Sprintf("'%s' -> '%s'", themeWriteCallerKey(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", themeWriteCallerKey(key)))
		}
	}
	var parts []string
	if len(renamed) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renamed)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". "+pkgWriteCompileHint)
	}
	return strings.Join(parts, " ")
}

// pkgWriteRefuseScope is CompileCreatioTool.RefuseScope: the refusal, or the trimmed scope.
func pkgWriteRefuseScope(bound pkgWriteCompileArgs) (string, string, string) {
	if refusal := pkgWriteCompileAliasError(bound.extras); refusal != "" {
		return refusal + " Nothing was compiled.", "", ""
	}
	if !pkgWriteBlank(bound.packageName) && strings.Contains(bound.packageName, ",") {
		return "`package-name` must contain exactly one package name. Comma-separated package lists are not supported by `compile-creatio`.", "", ""
	}
	if bound.present["package-name"] && pkgWriteBlank(bound.packageName) {
		return "`package-name` is empty. Pass the package name, or omit the argument for a full compilation.", "", ""
	}
	packageName := strings.TrimSpace(bound.packageName)
	if bound.present["process-name"] && pkgWriteBlank(bound.processName) {
		return "`process-name` is empty. Pass the business process code, or omit the argument for a full compilation.", "", ""
	}
	processName := strings.TrimSpace(bound.processName)
	if packageName == "" && processName == "" {
		for _, name := range []string{"process-name", "package-name"} {
			if bound.null[name] {
				return fmt.Sprintf("`%s` is null. Pass a value, or omit both scope arguments for a full compilation. Nothing was compiled.", name), "", ""
			}
		}
	}
	if packageName != "" && processName != "" {
		return "Provide only one of `package-name` or `process-name`: `process-name` already compiles the package the process is in.", "", ""
	}
	return "", packageName, processName
}

// pkgWriteCompileInProgress is CompileCreatioTool.BuildInProgressMessage.
func pkgWriteCompileInProgress(environmentName string, record operationRecord) string {
	return fmt.Sprintf("Compilation for '%s' (operation-id '%s', started-utc %s) ", environmentName, record.ID,
		record.StartedUTC.UTC().Format("2006-01-02T15:04:05Z")) +
		"was started and has not finished (MCP response deadline reached); this answer does not say whether the " +
		"environment has begun the build. Poll compile-status with the " +
		"same environment-name (or this operation-id) for its current state — do NOT retry compile-creatio: " +
		"the Creatio core serializes compilation, so a second concurrent compile for the same environment is " +
		"rejected, not queued. Do NOT issue other environment-bound calls (e.g. restart-by-environment-name) " +
		"for this environment until compile-status reports completion. A full compilation typically takes 3-20 " +
		"minutes; a process-name compile a few. If compile-status later answers not-found, judge this compile only " +
		"by compilation-history rows that finished after started-utc, allowing a minute either way because the " +
		"environment's clock can differ from this host's."
}

// pkgWriteCompileBusy is CompileCreatioTool.CompileAlreadyInProgressMessage.
func pkgWriteCompileBusy(environmentName string) string {
	return fmt.Sprintf("THIS clio process is already running a compilation for '%s', so this request was "+
		"not started. That is a local guard, not a server-wide one - a different clio process is not "+
		"covered by it. What actually serializes concurrent builds is the Creatio platform, which rejects "+
		"a second one on the node. Poll compile-status for the running operation and wait for it to "+
		"finish before compiling again.", environmentName)
}

func init() {
	registerTool(map[string]any{"name": "compile-creatio"}, invokePkgWriteCompile,
		withAnnotations(toolAnnotations{Destructive: true}))
}

func invokePkgWriteCompile(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := pkgWriteBindCompile(args)
	if err != nil {
		return nil, err
	}
	refusal, packageName, processName := pkgWriteRefuseScope(bound)
	if refusal != "" {
		return structuredToolResult(creatio.CommandFailure(refusal)), nil
	}
	name := bound.environmentName
	if refusal := pkgWriteBlankEnvironment("environment-name", name); refusal != nil {
		return refusal, nil
	}
	tenantKey := envs.tenantKey(name)
	if !pkgWriteReserveBuild(tenantKey) {
		return structuredToolResult(creatio.CommandFailure(pkgWriteCompileBusy(name))), nil
	}
	operation := compileOperations.begin(tenantKey, name, operationDetails{PackageName: packageName, ProcessName: processName})
	result, finished, err := runLongOperation(ctx, "compile-creatio", pkgWriteResponseDeadline(),
		func(ctx context.Context, _ func(string)) creatio.CommandResult {
			defer pkgWriteReleaseBuild(tenantKey)
			var answer creatio.CommandResult
			client, failure, resolveErr := pkgWriteNamedEnvironment(envs, "compile-creatio", name)
			switch {
			case resolveErr != nil:
				answer = creatio.CommandFailure(resolveErr.Error())
			case failure != nil:
				answer = pkgWriteResolverResult(failure)
			default:
				answer = client.Compile(ctx, creatio.CompileRequest{EnvironmentName: name, PackageName: packageName, ProcessName: processName})
			}
			compileOperations.finish(operation.ID, answer.ExitCode, answer.Messages)
			return answer
		})
	if err != nil {
		return nil, err
	}
	if !finished {
		return structuredToolResult(creatio.CommandInfo(pkgWriteCompileInProgress(name, operation))), nil
	}
	return structuredToolResult(result), nil
}
