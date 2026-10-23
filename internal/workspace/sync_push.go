package workspace

// push-workspace: clio's PushWorkspaceCommand as PushWorkspaceTool runs it (UseApplicationInstaller, no
// callback process, no TIDE repository, no unlock): the page-text warnings, then WorkspaceInstaller.Install —
// every package packed before the first request, ResetSchemaChangeState per package, the CreatioPackages.zip
// archive installed through the application installer.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// PushRequest is PushWorkspaceArgs plus what the command reads from the resolved environment.
type PushRequest struct {
	// Root is the workspace-path argument as given.
	Root           string
	SkipBackup     *bool
	EnvironmentURI string
	DeveloperMode  bool
	IsNetCore      bool
}

// syncPaths are WorkspacePathBuilder's paths for a root, combined as Path.Combine does (no clean-up), because
// clio names them in its messages.
type syncPaths struct{ root string }

func (p syncPaths) settings() string {
	return combinePath(combinePath(p.root, ".clio"), "workspaceSettings.json")
}
func (p syncPaths) packages() string       { return combinePath(p.root, "packages") }
func (p syncPaths) nuget() string          { return combinePath(p.root, ".nuget") }
func (p syncPaths) solution() string       { return combinePath(p.root, ".solution") }
func (p syncPaths) pkg(name string) string { return combinePath(p.packages(), name) }
func (p syncPaths) buildProps() string     { return combinePath(p.root, ".build-props") }
func (p syncPaths) layout() Layout         { return Layout{Root: p.root} }
func (p syncPaths) standaloneProject(name string) string {
	return combinePath(combinePath(p.pkg(name), "Files"), name+".csproj")
}

// readSyncSettings is Workspace.WorkspaceSettings: JsonConverter.DeserializeObjectFromFile.
func readSyncSettings(paths syncPaths) (Settings, error) {
	if !fileExists(paths.settings()) {
		return Settings{}, fmt.Errorf("Json file not found by path: '%s'", paths.settings())
	}
	return paths.layout().ReadSettings()
}

// filteredPackages is Workspace.GetFilteredPackages: IgnorePackages applied (and logged), ExternalPackages left out.
func filteredPackages(log *Log, settings Settings) []string {
	return withoutExternal(filterPackages(log, settings.Packages, settings.IgnorePackages), settings.ExternalPackages)
}

// PushWorkspace is PushWorkspaceCommand.Execute.
func PushWorkspace(ctx context.Context, client *creatio.Client, request PushRequest) creatio.CommandResult {
	log := &Log{}
	log.Info("Push workspace...")
	if err := pushWorkspace(ctx, log, client, request); err != nil {
		log.Error(err.Error())
		return log.Result(1)
	}
	log.Info("Done")
	return log.Result(0)
}

func pushWorkspace(ctx context.Context, log *Log, client *creatio.Client, request PushRequest) error {
	paths := syncPaths{root: request.Root}
	settings, err := readSyncSettings(paths)
	if err != nil {
		return err
	}
	pageTextWarnings(log, inspectPageText(log, paths.packages(), filteredPackages(log, settings)))
	packages := filteredPackages(log, settings)
	return withTempDirectory(log, func(temp string) error {
		packed := filepath.Join(temp, "CreatioPackages")
		if err := os.MkdirAll(packed, 0o777); err != nil {
			return err
		}
		var missing []folderWithoutDescriptor
		for _, name := range packages {
			err := packPackage(log, paths.pkg(name), filepath.Join(packed, name+".gz"))
			var descriptorMissing *descriptorMissingError
			switch {
			case errors.As(err, &descriptorMissing):
				missing = append(missing, descriptorMissing.folders...)
			case err != nil:
				return err
			}
		}
		if len(missing) > 0 {
			return &descriptorMissingError{folders: missing}
		}
		for _, name := range packages {
			if err := client.ResetSchemaChangeState(ctx, name); err != nil {
				return err
			}
		}
		archive := filepath.Join(temp, "CreatioPackages.zip")
		if err := zipDirectory(packed, archive); err != nil {
			return err
		}
		log.Info("Installing workspace packages using ApplicationInstaller...")
		if _, err := client.InstallWorkspaceArchive(ctx, creatio.WorkspaceInstallRequest{
			ArchivePath: archive, CreateBackup: request.SkipBackup == nil || !*request.SkipBackup,
			EnvironmentURI: request.EnvironmentURI, DeveloperMode: request.DeveloperMode,
		}, log); err != nil {
			return err
		}
		log.Info("Installation completed successfully.")
		warnUnbuiltStandalonePackages(log, paths, request.IsNetCore)
		return nil
	})
}

// warnUnbuiltStandalonePackages stands for WorkspaceInstaller.BuildStandalonePackagesIfNeeded: on a .NET
// Framework environment pushed from macOS or Linux clio builds every standalone package (Files/<name>.csproj)
// on the environment afterwards. This server does not run that build, and says so.
func warnUnbuiltStandalonePackages(log *Log, paths syncPaths, isNetCore bool) {
	if runtime.GOOS == "windows" || isNetCore {
		return
	}
	_, folders, err := directoryFiles(paths.packages())
	if err != nil {
		return
	}
	var names []string
	for _, folder := range folders {
		name := lastSegment(folder)
		if fileExists(paths.standaloneProject(name)) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	log.Warning("Standalone packages were not built on the environment: " + strings.Join(names, ", ") + ". clio builds " +
		"them after pushing to a .NET Framework environment from macOS or Linux; this server does not. Build them with " +
		"compile-creatio.")
}
