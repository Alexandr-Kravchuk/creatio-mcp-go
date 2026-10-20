package toolkit

// Cursor has no plugin CLI: clio's CursorAgent copies the plugin runtime named by the toolkit checkout's
// .release-manifest.json into ~/.cursor/plugins/local/<plugin>, registers clio in ~/.cursor/mcp.json and
// writes an orchestrator rule.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// invalidOperation is the InvalidOperationException clio's CursorAgent reports as "Cursor: <message>".
type invalidOperation struct{ message string }

func (e invalidOperation) Error() string { return e.message }

func cursorRun(env Environment, operation Operation, repository string) Outcome {
	home := env.agentHome("cursor")
	pluginDir := filepath.Join(home, "plugins", "local", PluginName)
	rulePath := filepath.Join(home, "rules", SkillName+".mdc")
	if operation == Delete {
		_ = os.RemoveAll(pluginDir)
		_ = os.Remove(rulePath)
		return Outcome{"cursor", Succeeded, fmt.Sprintf("Removed %s from Cursor.", PluginName)}
	}
	err := cursorInstall(env, repository, pluginDir, rulePath)
	var invalid invalidOperation
	switch {
	case errors.As(err, &invalid):
		return Outcome{"cursor", Failed, "Cursor: " + invalid.message}
	case err != nil:
		return Outcome{"cursor", Failed, fmt.Sprintf("Cursor %s failed: %s", operation.verb(), err.Error())}
	}
	if operation == Install {
		return Outcome{"cursor", Succeeded, fmt.Sprintf("Installed %s for Cursor.", PluginName)}
	}
	return Outcome{"cursor", Succeeded, fmt.Sprintf("Updated %s for Cursor.", PluginName)}
}

func cursorInstall(env Environment, repository, pluginDir, rulePath string) error {
	locator, ref := MarketplaceGitURL, ReleaseRef
	if strings.TrimSpace(repository) != "" {
		locator, ref = strings.TrimSpace(repository), ""
	}
	root, err := resolveRepository(env, locator, ref)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(pluginDir)
	if err := installRuntime(root, pluginDir); err != nil {
		return err
	}
	mcpPath := filepath.Join(env.agentHome("cursor"), "mcp.json")
	if err := mergeJSONClioServer(mcpPath); err != nil {
		return invalidOperation{err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(rulePath), 0o777); err != nil {
		return err
	}
	return os.WriteFile(rulePath, []byte(renderRule(pluginDir, mcpPath)), 0o666)
}

type gitResult struct {
	succeeded      bool
	stdout, stderr string
}

func (r gitResult) errorText() string {
	if text := strings.TrimSpace(r.stderr); text != "" {
		return text
	}
	return strings.TrimSpace(r.stdout)
}

func runGit(env Environment, args ...string) gitResult {
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "PATH="+env.Path, "HOME="+env.Home)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return gitResult{err == nil, stdout.String(), stderr.String()}
}

// resolveRepository is clio's SkillRepositoryResolver.Resolve: a local directory is used in place, a remote
// is cloned once into clio's settings directory (shallow, the given ref) and pulled afterwards.
func resolveRepository(env Environment, locator, ref string) (string, error) {
	if info, err := os.Stat(locator); err == nil && info.IsDir() {
		full, _ := filepath.Abs(locator)
		if head := runGit(env, "-C", full, "rev-parse", "HEAD"); !head.succeeded || strings.TrimSpace(head.stdout) == "" {
			return "", invalidOperation{fmt.Sprintf("Unable to resolve git HEAD for skills repository '%s'. %s", full, head.errorText())}
		}
		return full, nil
	}
	cached := filepath.Join(env.ClioHome, repositoryName(locator))
	if err := os.MkdirAll(env.ClioHome, 0o777); err != nil {
		return "", err
	}
	if info, err := os.Stat(cached); err == nil && info.IsDir() {
		if pull := runGit(env, "-C", cached, "pull", "--ff-only"); !pull.succeeded {
			return "", invalidOperation{fmt.Sprintf("Unable to update cached skills repository '%s' at '%s'. %s", locator, cached, pull.errorText())}
		}
	} else {
		args := []string{"-c", "protocol.ext.allow=never", "clone", "--depth", "1"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		if clone := runGit(env, append(args, "--", locator, cached)...); !clone.succeeded {
			return "", invalidOperation{fmt.Sprintf("Unable to clone skills repository '%s'. %s", locator, clone.errorText())}
		}
	}
	if head := runGit(env, "-C", cached, "rev-parse", "HEAD"); !head.succeeded || strings.TrimSpace(head.stdout) == "" {
		return "", invalidOperation{fmt.Sprintf("Unable to resolve git HEAD for skills repository '%s'. %s", cached, head.errorText())}
	}
	return cached, nil
}

// repositoryName is clio's GetRepositoryName: the last path segment without .git, invalid file-name
// characters replaced by '-'.
func repositoryName(locator string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(locator), `/\`)
	name := trimmed
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 && index < len(trimmed)-1 {
		name = trimmed[index+1:]
	}
	if strings.HasSuffix(strings.ToLower(name), ".git") {
		name = name[:len(name)-4]
	}
	invalid := "\x00/"
	if filepath.Separator == '\\' {
		invalid = "\"<>|\x00:*?\\/"
		for c := rune(1); c < 32; c++ {
			invalid += string(c)
		}
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if strings.ContainsRune(invalid, r) {
			return '-'
		}
		return r
	}, name))
}

// installRuntime is clio's CursorPluginRuntimeInstaller.Install.
func installRuntime(sourceRoot, target string) error {
	manifestPath := filepath.Join(sourceRoot, ".release-manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return invalidOperation{fmt.Sprintf("Toolkit source at '%s' is missing .release-manifest.json; cannot resolve the Cursor plugin runtime surface.", sourceRoot)}
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	var entries []any
	if raw, ok := manifest["plugin_runtime"]; !ok || json.Unmarshal(raw, &entries) != nil || entries == nil {
		return invalidOperation{fmt.Sprintf(".release-manifest.json in '%s' must contain 'plugin_runtime' as an array of strings.", sourceRoot)}
	}
	var paths []string
	for _, entry := range entries {
		text, ok := entry.(string)
		if !ok {
			return invalidOperation{".release-manifest.json 'plugin_runtime' must contain only strings."}
		}
		paths = append(paths, text)
	}
	if err := os.MkdirAll(target, 0o777); err != nil {
		return err
	}
	for _, relative := range paths {
		if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) || strings.HasPrefix(relative, "/") ||
			strings.HasPrefix(relative, `\`) || strings.Contains(relative, ":") {
			return invalidOperation{fmt.Sprintf("Unsafe plugin_runtime entry '%s': must be a relative path.", relative)}
		}
		for _, segment := range strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' }) {
			if segment == ".." {
				return invalidOperation{fmt.Sprintf("Unsafe plugin_runtime entry '%s': must not contain '..' segments.", relative)}
			}
		}
		source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		destination := filepath.Join(target, filepath.FromSlash(relative))
		for _, pair := range [][2]string{{source, sourceRoot}, {destination, target}} {
			full, _ := filepath.Abs(pair[0])
			root, _ := filepath.Abs(pair[1])
			if !strings.EqualFold(full, root) && !strings.HasPrefix(strings.ToLower(full), strings.ToLower(strings.TrimRight(root, `/\`)+string(filepath.Separator))) {
				return invalidOperation{fmt.Sprintf("Unsafe plugin_runtime entry '%s': resolves outside '%s'.", relative, root)}
			}
		}
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			_ = os.RemoveAll(destination)
			if err := copyTree(source, destination); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(source, destination); err != nil {
			return err
		}
	}
	return nil
}

func includeInCopy(path string) bool {
	return !strings.Contains(path, "__pycache__") && !strings.HasSuffix(strings.ToLower(path), ".pyc")
}

// copyTree is clio's FileSystem.CopyDirectoryWithFilter(source, destination, overwrite, IncludeInCopy) as
// clio 8.1.0.142 runs it: that method copies the top-level entries for which the filter is FALSE, so with
// IncludeInCopy it copies only __pycache__ / *.pyc entries of a runtime directory and creates the
// directory itself. This is a defect in clio (a runtime directory arrives empty in Cursor), reproduced on
// purpose so both servers leave the same files; see docs/status/t6-write-status.md.
func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o777); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(source, entry.Name())
		if includeInCopy(path) {
			continue
		}
		if entry.IsDir() {
			if err := copyAll(path, filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		} else if err := copyFile(path, filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyAll is clio's unfiltered CopyDirectory.
func copyAll(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o777)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o777); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

// renderRule is clio's CursorRuleWriter.RenderRule.
func renderRule(pluginRoot, mcpPath string) string {
	file := func(segments ...string) string { return filepath.Join(append([]string{pluginRoot}, segments...)...) }
	return "---\n" +
		"description: Use when creating Creatio app Business Plans, " +
		"technical implementation handoffs, or applying the approved plan through clio MCP.\n" +
		"alwaysApply: false\n" +
		"---\n" +
		"\n" +
		"# Creatio App Orchestrator\n" +
		"\n" +
		"Entrypoint for the Creatio AI App Development Toolkit (CAADT) workflow.\n" +
		"\n" +
		"Toolkit repository is installed at: `" + pluginRoot + "`\n" +
		"\n" +
		"## Load Order\n" +
		"\n" +
		"1. Read `" + file("AGENTS.md") + "` for the active orchestration contract.\n" +
		"2. Read `" + file("context", "INDEX.md") + "` to choose the smallest relevant reference set.\n" +
		"3. For environment setup, read `" + file("runbooks", "01-environment-setup.md") + "`.\n" +
		"4. For requirements gathering, read `" + file("runbooks", "02-requirements-gathering.md") + "`.\n" +
		"5. For executable helper behavior, use `" + file("runtime", "scripts", "mcp_client.py") + "` " +
		"and `" + file("runtime", "scripts", "workflow_validators.py") + "`.\n" +
		"\n" +
		"## Core Rules\n" +
		"\n" +
		"- Keep the visible planning artifact in the BA-style Business Plan format defined by `AGENTS.md`.\n" +
		"- Resolve executable clio MCP tool contracts through `get-tool-contract`; do not invent payload shapes.\n" +
		"- The `clio` MCP server is registered in `" + mcpPath + "`.\n"
}
