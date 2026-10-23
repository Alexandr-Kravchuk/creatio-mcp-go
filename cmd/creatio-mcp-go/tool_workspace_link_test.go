package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func linkToolFixture(t *testing.T) (root, repo, pkg string) {
	t.Helper()
	root = t.TempDir()
	// Windows without developer mode or elevation cannot create the links these tests check.
	if err := os.Symlink(root, filepath.Join(root, "probe")); err != nil {
		t.Skipf("symbolic links cannot be created here: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "probe")); err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "repo")
	pkg = filepath.Join(root, "env", "Terrasoft.WebApp", "Terrasoft.Configuration", "Pkg")
	for path, content := range map[string]string{
		filepath.Join(repo, "PkgA", "descriptor.json"): `{"Descriptor":{"Name":"PkgA","Maintainer":"Acme"}}`,
		filepath.Join(pkg, "Other", "descriptor.json"): `{}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, repo, pkg
}

func linkAnswerValues(answer creatio.CommandResult) string {
	values := make([]string, 0, len(answer.Messages))
	for _, message := range answer.Messages {
		values = append(values, message.MessageType+": "+message.Value)
	}
	return strings.Join(values, "\n")
}

func TestLinkToolsBindTheirArgumentsAsClio(t *testing.T) {
	fake := newPkgWriteFake(t, nil)
	session := pkgWriteSession(t, fake, nil)
	for _, item := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{linkByEnvPackagePathTool, map[string]any{"repoPath": "r"},
			"Error: tool 'link-from-repository-by-env-package-path' failed: The arguments dictionary is missing a value for the required parameter 'envPkgPath'. (Parameter 'arguments')"},
		{linkByEnvPackagePathTool, map[string]any{"packages": 7, "repoPath": 5},
			"invalid-parameter-type: argument 'repoPath' for MCP tool 'link-from-repository-by-env-package-path' must be a string. Received an incompatible JSON value."},
		{linkByEnvPackagePathTool, map[string]any{"repoPath": nil},
			"invalid-parameter-type: argument 'repoPath' for MCP tool 'link-from-repository-by-env-package-path' must be a string. Received a JSON null, and the argument is required."},
		{linkByEnvPackagePathTool, map[string]any{"dryRun": "x"},
			"invalid-parameter-type: argument 'dryRun' for MCP tool 'link-from-repository-by-env-package-path' must be a boolean. Received an incompatible JSON value."},
		{linkByEnvironmentTool, map[string]any{"environmentName": "parity-env", "repoPath": "r"},
			"Error: tool 'link-from-repository-by-environment' failed: The arguments dictionary is missing a value for the required parameter 'packages'. (Parameter 'arguments')"},
		{linkUnlockedTool, map[string]any{"environmentName": 5, "repoPath": "r"},
			"invalid-parameter-type: argument 'environmentName' for MCP tool 'link-from-repository-unlocked' must be a string. Received an incompatible JSON value."},
	} {
		result := pkgWriteCall(t, session, item.tool, item.args, nil)
		if !result.IsError || pkgWriteText(result) != item.want {
			t.Errorf("%s %v = %s", item.tool, item.args, pkgWriteText(result))
		}
	}
	for _, tool := range []string{linkByEnvironmentTool, linkUnlockedTool} {
		var answer creatio.CommandResult
		pkgWriteCall(t, session, tool, map[string]any{"environment-name": "parity-env", "environmentName": " ", "repoPath": "r", "packages": "A"}, &answer)
		if answer.ExitCode != 1 || linkAnswerValues(answer) != "Error: environment-name is required for "+tool+" outside credential passthrough." {
			t.Errorf("%s blank environment = %+v", tool, answer)
		}
	}
	pkgWriteAssertGated(t, session, linkByEnvPackagePathTool, map[string]any{"envPkgPath": "p", "repoPath": "r", "packages": "*"})
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("calls = %q", calls)
	}
}

func TestLinkByEnvironmentPreparesTheNamedEnvironment(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/QuerySysSettings": func(string) (int, string) {
			return 200, `{"success":true,"values":{"Maintainer":{"value":"Customer"}}}`
		},
		"/SelectQuery":           func(string) (int, string) { return 200, `{"success":true,"rows":[]}` },
		"/PostSysSettingsValues": func(string) (int, string) { return 200, `{"saveResult":{"Maintainer":true},"success":true}` },
		"/UnlockPackages":        func(string) (int, string) { return 200, `true` },
		"/GetIsFileDesignMode":   func(string) (int, string) { return 200, `{"success":true,"value":true}` },
		"/LoadPackagesToFileSystem": func(string) (int, string) {
			return 200, `{"success":true}`
		},
	})
	root, repo, pkg := linkToolFixture(t)
	session := pkgWriteSession(t, fake, map[string]any{"EnvironmentPath": filepath.Join(root, "env")})
	var answer creatio.CommandResult
	pkgWriteCall(t, session, linkByEnvironmentTool, map[string]any{"environmentName": "PARITY-ENV", "repoPath": repo, "packages": "PkgA"}, &answer)
	want := strings.Join([]string{
		"Info: Some packages are missing or incomplete in Pkg folder — running preparation...",
		"Warning: Preparation required:",
		"Warning:   - SysSetting 'Maintainer' will change: 'Customer' -> 'Acme'",
		"Warning:   - 1 package(s) will be unlocked: PkgA",
		"Warning:   - Packages will be synced to file system (2fs)",
		"Info: Proceeding...",
		"Info: Updating SysSetting 'Maintainer' from 'Customer' to 'Acme'",
		"Info: Unlocking packages: PkgA",
		"Info: Loading packages to file system (2fs)...",
		"None: Start load packages to file system on a web application",
		"Info: Load packages to file system on a web application completed",
		"Info: Packages synced to file system successfully.",
		"Info: Linking repository package(s) to environment " + pkg + " from " + repo,
	}, "\n")
	if answer.ExitCode != 0 || linkAnswerValues(answer) != want {
		t.Fatalf("answer:\n%s", linkAnswerValues(answer))
	}
	if target, err := os.Readlink(filepath.Join(pkg, "PkgA")); err != nil || target != filepath.Join(repo, "PkgA") {
		t.Fatalf("link = %q, %v", target, err)
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), `/rest/CreatioApiGateway/UnlockPackages {"unlockPackages":["PkgA"]}`) {
		t.Fatalf("calls = %q", fake.recorded())
	}
}

func TestLinkUnlockedQueriesTheNamedEnvironment(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/SelectQuery": func(string) (int, string) {
			return 200, `{"success":true,"rows":[{"Name":"PkgA","UId":"u","Maintainer":"Acme","Version":"1.0.0"}]}`
		},
	})
	root, repo, pkg := linkToolFixture(t)
	session := pkgWriteSession(t, fake, map[string]any{"EnvironmentPath": filepath.Join(root, "env")})
	var answer creatio.CommandResult
	pkgWriteCall(t, session, linkUnlockedTool, map[string]any{"environmentName": "parity-env", "repoPath": repo}, &answer)
	if answer.ExitCode != 0 || linkAnswerValues(answer) != "Info: Querying Creatio site for unlocked packages...\n"+
		"Info: Found 1 unlocked package(s): PkgA\nInfo: Linking repository package(s) to environment "+pkg+" from "+repo {
		t.Fatalf("answer:\n%s", linkAnswerValues(answer))
	}
	if calls := fake.recorded(); len(calls) != 1 || !strings.Contains(calls[0], `"columnPath":"InstallType"`) {
		t.Fatalf("calls = %q", calls)
	}
}

func TestLinkWithoutANamedEnvironmentDoesNotPrepare(t *testing.T) {
	fake := newPkgWriteFake(t, nil)
	_, repo, pkg := linkToolFixture(t)
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	pkgWriteCall(t, session, linkByEnvPackagePathTool, map[string]any{"envPkgPath": pkg, "repoPath": repo, "packages": "PkgA"}, &answer)
	if answer.ExitCode != 1 || linkAnswerValues(answer) != "Info: Some packages are missing or incomplete in Pkg folder — running preparation...\n"+
		"Error: Error during linking: "+linkNoSiteText(linkByEnvPackagePathTool, "") {
		t.Fatalf("answer:\n%s", linkAnswerValues(answer))
	}
	pkgWriteCall(t, session, linkUnlockedTool, map[string]any{"environmentName": pkg, "repoPath": repo}, &answer)
	if answer.ExitCode != 1 || !strings.HasSuffix(linkAnswerValues(answer), "is a folder path, not a registered environment, so there is no "+
		"Creatio site to ask for unlocked packages. Pass a registered environment name.") {
		t.Fatalf("unlocked answer:\n%s", linkAnswerValues(answer))
	}
	pkgWriteCall(t, session, linkByEnvPackagePathTool, map[string]any{"envPkgPath": pkg, "repoPath": repo, "packages": "PkgA", "skipPreparation": true}, &answer)
	if answer.ExitCode != 0 {
		t.Fatalf("skip preparation = %+v", answer)
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("calls = %q", calls)
	}
}
