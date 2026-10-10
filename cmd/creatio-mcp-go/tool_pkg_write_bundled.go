package main

// install-gate and install-dashboards-migrator: clio's InstallGateTool and InstallDashboardsMigratorTool. Both
// install a package clio ships beside its executable; this server ships none, so the archive is read from the
// newest clio installed as a .NET tool (as docs://help/command reads clio's help). install-dashboards-migrator
// is long-running: it shares compile-creatio's configuration-build reservation and answers in-progress after
// the response deadline.
// clio: install-gate [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = true, OpenWorld = false)];
// install-dashboards-migrator [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = true, OpenWorld = false)].

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgWriteClioHome is overridden by tests; empty means the user's home directory.
var pkgWriteClioHome = ""

// pkgWriteBundledArchive finds <folder>/<file> beside the newest clio .NET tool:
// ~/.dotnet/tools/.store/clio/<version>/clio/<version>/tools/<tfm>/any/<folder>/<file>. "" when none carries it.
func pkgWriteBundledArchive(folder, file string) string {
	home := pkgWriteClioHome
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return ""
		}
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".dotnet", "tools", ".store", "clio", "*", "clio", "*", "tools", "*", "any", folder, file))
	// Every target framework folder of one clio version carries the same archives; the first one is taken.
	best, bestVersion := "", []int(nil)
	for _, match := range matches {
		parts := strings.Split(filepath.ToSlash(match), "/")
		version := knowledgeParseToolVersion(parts[len(parts)-6])
		if version != nil && (bestVersion == nil || knowledgeVersionLess(bestVersion, version)) {
			best, bestVersion = match, version
		}
	}
	return best
}

// pkgWriteEnvironmentArg reads install-*'s record argument environment-name; a blank one is refused (clio
// would resolve the active environment).
func pkgWriteEnvironmentArg(args map[string]any, tool string) (string, *mcp.CallToolResult, error) {
	name, err := optionalStringArg(args, tool, "environment-name")
	if err != nil {
		return "", nil, err
	}
	return name, pkgWriteBlankEnvironment("environment-name", name), nil
}

func init() {
	registerTool(map[string]any{"name": "install-gate"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "install-gate"
		name, refusal, err := pkgWriteEnvironmentArg(args, tool)
		if err != nil || refusal != nil {
			return refusal, err
		}
		client, failure, err := pkgWriteNamedEnvironment(envs, tool, name)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		archive := pkgWriteBundledArchive("cliogate", client.GateArchiveName())
		if archive == "" {
			return structuredToolResult(creatio.PkgWriteNoClioError("cliogate")), nil
		}
		return structuredToolResult(client.InstallGate(ctx, archive)), nil
	}, withAnnotations(toolAnnotations{Idempotent: true}))

	registerTool(map[string]any{"name": "install-dashboards-migrator"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "install-dashboards-migrator"
		name, refusal, err := pkgWriteEnvironmentArg(args, tool)
		if err != nil || refusal != nil {
			return refusal, err
		}
		tenantKey := envs.tenantKey(name)
		result, finished, err := runLongOperation(ctx, tool, pkgWriteResponseDeadline(),
			func(ctx context.Context, _ func(string)) creatio.CommandResult {
				if !pkgWriteReserveBuild(tenantKey) {
					return creatio.CommandFailure(fmt.Sprintf("A configuration build is already running on '%s' — either an earlier "+
						"%s that is still working server-side, or a compile. Wait for it to finish, then check "+
						"list-packages before calling %s again.", name, tool, tool))
				}
				defer pkgWriteReleaseBuild(tenantKey)
				client, failure, resolveErr := pkgWriteNamedEnvironment(envs, tool, name)
				switch {
				case resolveErr != nil:
					return creatio.CommandFailure(resolveErr.Error())
				case failure != nil:
					return pkgWriteResolverResult(failure)
				}
				archive := pkgWriteBundledArchive(creatio.DashboardsMigratorPackage, creatio.DashboardsMigratorPackage+".gz")
				if archive == "" {
					return creatio.PkgWriteNoClioError(creatio.DashboardsMigratorPackage)
				}
				return client.InstallDashboardsMigrator(ctx, archive)
			})
		if err != nil {
			return nil, err
		}
		if !finished {
			return structuredToolResult(creatio.CommandInfo(fmt.Sprintf("The %s install on '%s' is still "+
				"running server-side: the target is compiling the package and will restart. This is NOT a "+
				"verdict — nothing is confirmed yet, and the install may still fail. Do NOT call "+
				"%s again: while this one runs a second call is refused, and "+
				"it would only trigger another configuration build. Wait, then confirm with list-packages and "+
				"by opening the Dashboards migration page in System Designer.", creatio.DashboardsMigratorPackage, name, tool))), nil
		}
		return structuredToolResult(result), nil
	}, withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}
