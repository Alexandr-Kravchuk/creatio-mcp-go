package creatio

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	clioPagesDirectoryName = ".clio-pages"
	pageLockTimeout        = 30 * time.Second
)

var pageSchemaNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// WritePageFiles persists a successful get-page read the way clio's PageFileWriter does, so clio's
// update-page and sync-pages can use the baseline: .clio-pages/{schema}/body.js (the editable body),
// bundle.json (the merged hierarchy) and meta.json (page metadata plus the conflict-detection baseline). The
// tree is built under .clio-pages/.staging and swapped in under the same .locks/{schema}.lock file clio
// locks, so a reader never sees a half-written directory.
func (c *Client) WritePageFiles(result PageGetResult, schemaName, outputDirectory string) PageGetResult {
	return c.WritePageFilesFor(result, schemaName, outputDirectory, "", "")
}

// WritePageFilesFor is WritePageFiles with the baseline identity clio records: the registered environment
// name the call used, and the URI of a direct connection. A call that gave neither (this server's
// CREATIO_* default) is recorded by the configured URL.
func (c *Client) WritePageFilesFor(result PageGetResult, schemaName, outputDirectory, environmentName, uri string) PageGetResult {
	if strings.TrimSpace(schemaName) == "" || !pageSchemaNamePattern.MatchString(schemaName) {
		return PageGetResult{Error: fmt.Sprintf("Invalid schema name '%s': only letters, digits and underscore are allowed.", schemaName)}
	}
	anchor, err := pageOutputAnchor(outputDirectory)
	if err != nil {
		return PageGetResult{Error: fmt.Sprintf("Failed to prepare output directory: %s", err.Error())}
	}
	rootDir := filepath.Join(anchor, clioPagesDirectoryName)
	schemaDir := filepath.Join(rootDir, schemaName)
	lockPath := filepath.Join(rootDir, ".locks", schemaName+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return PageGetResult{Error: fmt.Sprintf("Failed to prepare output directory '%s': %s", schemaDir, err.Error())}
	}
	unlock, err := lockPageFile(lockPath, pageLockTimeout)
	if err != nil {
		return PageGetResult{Error: fmt.Sprintf("Failed to write page files: another clio operation is still using '%s' (%s).", schemaDir, err.Error())}
	}
	defer unlock()
	return c.writePageFilesLocked(result, schemaName, rootDir, schemaDir, environmentName, uri)
}

func (c *Client) writePageFilesLocked(result PageGetResult, schemaName, rootDir, schemaDir, environmentName, uri string) PageGetResult {
	stagingRoot := filepath.Join(rootDir, ".staging", schemaName)
	stagingDir := filepath.Join(stagingRoot, randomHex(4))
	ensurePagesGitIgnore(rootDir)
	if entries, err := os.ReadDir(stagingRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				_ = os.RemoveAll(filepath.Join(stagingRoot, entry.Name()))
			}
		}
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return PageGetResult{Error: fmt.Sprintf("Failed to prepare output directory '%s': %s", schemaDir, err.Error())}
	}
	fetchedAt := time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z")
	meta := orderedFields{{"fetchedAt", fetchedAt}, {"page", pageMetadataNode(result.fullPage)}}
	if baseline := c.pageBaselineNode(schemaName, result.Editable, fetchedAt, environmentName, uri); baseline != nil {
		meta = append(meta, field{"baseline", baseline})
	}
	write := func() error {
		if err := os.WriteFile(filepath.Join(stagingDir, "body.js"), []byte(result.rawBody), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stagingDir, "bundle.json"), result.bundle.stjJSON(), 0o644); err != nil {
			return err
		}
		temporary := filepath.Join(stagingDir, ".meta.json."+randomHex(16)+".tmp")
		if err := os.WriteFile(temporary, toJNode(meta).stjJSON(), 0o644); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		if err := os.Rename(temporary, filepath.Join(stagingDir, "meta.json")); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return publishStagedPageDirectory(stagingRoot, stagingDir, schemaDir)
	}
	if err := write(); err != nil {
		_ = os.RemoveAll(stagingDir)
		return PageGetResult{Error: fmt.Sprintf("Failed to write page files: %s", err.Error())}
	}
	result.Files = &PageFiles{BodyFile: filepath.Join(schemaDir, "body.js"), BundleFile: filepath.Join(schemaDir, "bundle.json"),
		MetaFile: filepath.Join(schemaDir, "meta.json"), FetchedAt: fetchedAt}
	return result
}

// publishStagedPageDirectory swaps the staged tree in: the previous generation is renamed aside first, so the
// schema directory is missing only between two renames, and it is moved back if the publish fails.
func publishStagedPageDirectory(stagingRoot, stagingDir, schemaDir string) error {
	retiredDir := filepath.Join(stagingRoot, "old-"+randomHex(4))
	retired := false
	if _, err := os.Stat(schemaDir); err == nil {
		if err := os.Rename(schemaDir, retiredDir); err != nil {
			return err
		}
		retired = true
	}
	if err := os.Rename(stagingDir, schemaDir); err != nil {
		if retired {
			_ = os.Rename(retiredDir, schemaDir)
		}
		return err
	}
	if retired {
		_ = os.RemoveAll(retiredDir)
	}
	return nil
}

func ensurePagesGitIgnore(rootDir string) {
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(rootDir, ".gitignore")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(path, []byte("*\n!.gitignore\n"), 0o644)
	}
}

// pageOutputAnchor mirrors clio's PageOutputDirectoryResolver: an explicit directory wins; otherwise the
// nearest ancestor holding .clio/workspaceSettings.json; otherwise the current directory, except that the
// user's home directory itself is replaced by clio's home so pages are not written straight into ~.
func pageOutputAnchor(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(explicit)
	}
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for directory := current; ; {
		if info, err := os.Stat(filepath.Join(directory, ".clio", "workspaceSettings.json")); err == nil && !info.IsDir() {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	home, _ := os.UserHomeDir()
	if home != "" && strings.EqualFold(strings.TrimRight(filepath.Clean(current), `/\`), strings.TrimRight(filepath.Clean(home), `/\`)) {
		return clioHomeDirectory(), nil
	}
	return current, nil
}

// clioHomeDirectory is clio's SettingsRepository.AppSettingsFolderPath: CLIO_HOME, else
// $HOME/creatio/clio (%LOCALAPPDATA%\creatio\clio on Windows).
func clioHomeDirectory() string {
	if home := os.Getenv("CLIO_HOME"); strings.TrimSpace(home) != "" {
		return home
	}
	base := os.Getenv("HOME")
	if runtime.GOOS == "windows" {
		base = os.Getenv("LOCALAPPDATA")
	}
	return filepath.Join(base, "creatio", "clio")
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

// pageMetadataNode serializes PageMetadataInfo as System.Text.Json writes it into meta.json: identity fields
// are written even when null, the optional ones are left out when null, and every operation is kept.
func pageMetadataNode(page *PageMetadata) *jnode {
	fields := orderedFields{
		{"schemaName", page.SchemaName}, {"schemaUId", page.SchemaUID}, {"packageName", page.PackageName},
		{"currentLeafPackageName", page.CurrentLeafPackageName}, {"packageUId", page.PackageUID},
		{"parentSchemaName", page.ParentSchemaName},
	}
	if summary := page.OwnBodySummary; summary != nil {
		stats := orderedFields{
			{"viewConfigDiffOperations", summary.ViewConfigDiffOperations},
			{"viewModelConfigDiffOperations", summary.ViewModelConfigDiffOperations},
			{"modelConfigDiffOperations", summary.ModelConfigDiffOperations},
			{"handlerEntries", summary.HandlerEntries}, {"bodyLength", summary.BodyLength},
		}
		ops := make([]any, 0, len(summary.ViewConfigDiffOps))
		for _, op := range summary.ViewConfigDiffOps {
			ops = append(ops, orderedFields{{"operation", op.Operation}, {"name", op.Name}, {"type", op.Type}, {"parentName", op.ParentName}})
		}
		stats = append(stats, field{"viewConfigDiffOps", ops})
		requests := make([]any, 0, len(summary.HandlerRequests))
		for _, request := range summary.HandlerRequests {
			requests = append(requests, request)
		}
		stats = append(stats, field{"handlerRequests", requests})
		fields = append(fields, field{"ownBodySummary", stats})
	}
	if page.DesignPackageUID != "" {
		fields = append(fields, field{"designPackageUId", page.DesignPackageUID})
	}
	if page.DesignPackageName != nil {
		fields = append(fields, field{"designPackageName", page.DesignPackageName})
	}
	fields = append(fields, field{"willCreateReplacingInDesignPackage", page.WillCreateReplacingInDesignPackage})
	if page.RootSchemaUID != "" {
		fields = append(fields, field{"rootSchemaUId", page.RootSchemaUID})
	}
	fields = append(fields, field{"schema-type", page.SchemaType})
	if page.SchemaTypeValue != nil {
		fields = append(fields, field{"schema-type-value", page.SchemaTypeValue})
	}
	return toJNode(fields)
}

// pageBaselineNode is clio's PageBaselineInfo: the environment name and direct URI of the call, as clio's
// PageFileWriter records them (clio's update-page matches the name, or a direct URI against its own); a call
// with neither records the configured Creatio URL as environmentUri. modifiedOn is the raw DataService value: clio renders it through .NET DateTime.ToString() in
// the clio host's culture and time zone, which is not reproducible here.
func (c *Client) pageBaselineNode(schemaName string, editable *PageEditableInfo, capturedAt, environmentName, uri string) *jnode {
	if editable == nil {
		return nil
	}
	fields := orderedFields{{"schemaName", schemaName}}
	if strings.TrimSpace(environmentName) == "" && strings.TrimSpace(uri) == "" {
		uri = strings.TrimSpace(c.config.BaseURL)
	}
	if strings.TrimSpace(environmentName) != "" {
		fields = append(fields, field{"environmentName", environmentName})
	}
	if strings.TrimSpace(uri) != "" {
		fields = append(fields, field{"environmentUri", uri})
	}
	fields = append(fields, field{"editableSchemaExists", editable.EditableSchemaExists})
	if editable.EditableSchemaUID != "" {
		fields = append(fields, field{"editableSchemaUId", editable.EditableSchemaUID})
	}
	if editable.Checksum != nil {
		fields = append(fields, field{"checksum", editable.Checksum})
	}
	if editable.ModifiedOn != nil {
		fields = append(fields, field{"modifiedOn", editable.ModifiedOn})
	}
	fields = append(fields, field{"capturedAt", capturedAt})
	return toJNode(fields)
}
