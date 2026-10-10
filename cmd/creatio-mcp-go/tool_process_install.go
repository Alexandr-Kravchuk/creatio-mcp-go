package main

// install-process-builder: clio's InstallProcessBuilderTool. Long-running: progress notifications while it
// works, clio's in-progress note when the response deadline passes first (the install goes on), and one
// configuration build per target at a time. The reservation is this tool's own; clio shares it with
// compile-creatio, which this server does not serve yet.

import (
	"context"
	"fmt"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const processInstallTool = "install-process-builder"

var processInstallBuilds = struct {
	sync.Mutex
	running map[string]bool
}{running: map[string]bool{}}

func processInstallReserve(key string) bool {
	processInstallBuilds.Lock()
	defer processInstallBuilds.Unlock()
	if processInstallBuilds.running[key] {
		return false
	}
	processInstallBuilds.running[key] = true
	return true
}

func processInstallRelease(key string) {
	processInstallBuilds.Lock()
	defer processInstallBuilds.Unlock()
	delete(processInstallBuilds.running, key)
}

func init() {
	registerTool(map[string]any{"name": processInstallTool},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			name, err := optionalStringArg(args, processInstallTool, "environment-name")
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(processInstallTool, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			key := envs.tenantKey(name)
			if !processInstallReserve(key) {
				return structuredToolResult(creatio.CommandFailure(fmt.Sprintf("A configuration build is already running on '%s' — either an earlier "+
					"%s that is still working server-side, or a compile. Wait for "+
					"it to finish, then retry the process-designer tool you were using; it refuses again if the "+
					"package is still missing, and only then call %s.", name, processInstallTool, processInstallTool))), nil
			}
			result, finished, err := runLongOperation(ctx, processInstallTool, 0,
				func(ctx context.Context, stage func(string)) creatio.CommandResult {
					defer processInstallRelease(key)
					stage("Installing " + "CrtProcessBuilder")
					return client.InstallProcessBuilder(ctx)
				})
			if err != nil {
				return nil, err
			}
			if !finished {
				return structuredToolResult(creatio.CommandInfo(fmt.Sprintf("The CrtProcessBuilder install on '%s' is still "+
					"running server-side: the target is compiling the package and will restart. This is NOT a "+
					"verdict — nothing is confirmed yet, and the install may still fail. Do NOT call "+
					"%s again: while this one runs a second call is refused, and it "+
					"would only trigger another configuration build. Wait, then retry the process-designer tool "+
					"that sent you here — its package gate IS the confirmation, and it refuses again if the "+
					"install did not take.", name, processInstallTool))), nil
			}
			return structuredToolResult(result), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
}
