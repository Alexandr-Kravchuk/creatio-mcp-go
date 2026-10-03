package creatio

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var processWritePackagePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func processWriteGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func processWriteJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("Invalid JSON artifact: %s", path)
	}
	return value, nil
}

func processWriteObject(parent map[string]any, key string) map[string]any {
	child, _ := parent[key].(map[string]any)
	return child
}
func processWriteString(parent map[string]any, key string) string {
	value, _ := parent[key].(string)
	return value
}

func processWriteSQL(task, pkg, caption string, engine int) string {
	escaped := strings.ReplaceAll(caption, "'", "''")
	literal := "N'" + escaped + "'"
	quote := func(name string) string { return "[" + name + "]" }
	if engine == 2 {
		literal = "E'" + strings.ReplaceAll(escaped, `\`, `\\`) + "'"
		quote = func(name string) string { return `"` + name + `"` }
	}
	return fmt.Sprintf("INSERT INTO %s (%s, %s)\nSELECT s.%s, %s\nFROM %s s\nINNER JOIN %s p ON p.%s = s.%s\nWHERE s.%s = '%s' AND p.%s = '%s'\nAND NOT EXISTS (SELECT 1 FROM %s t WHERE t.%s = s.%s);", quote("SysProcessUserTask"), quote("SysUserTaskSchemaUId"), quote("Caption"), quote("UId"), literal, quote("SysSchema"), quote("SysPackage"), quote("Id"), quote("SysPackageId"), quote("UId"), task, quote("UId"), pkg, quote("SysProcessUserTask"), quote("SysUserTaskSchemaUId"), quote("UId"))
}

// RegisterProcessElement writes only package-owned scripts after validating the workspace and schema identities.
func RegisterProcessElement(workspace, packageName, taskUID, caption string) CommandResult {
	paths, err := processWriteRegister(workspace, packageName, taskUID, caption)
	if err != nil {
		return CommandFailure(err.Error())
	}
	messages := []LogMessage{}
	for _, path := range paths {
		messages = append(messages, LogMessage{MessageType: "Info", Value: "Registration script ready: " + path})
	}
	return CommandResult{ExitCode: 0, Messages: messages, Note: "Install with push-pkg or push-workspace to apply registration. No environment was changed."}
}

func processWriteRegister(workspace, packageName, taskUID, caption string) ([]string, error) {
	uid := normalizeGUID(taskUID)
	if strings.TrimSpace(workspace) == "" || !processWritePackagePattern.MatchString(packageName) || uid == "" || uid == "00000000-0000-0000-0000-000000000000" || strings.TrimSpace(caption) == "" || strings.ContainsRune(caption, 0) {
		return nil, errors.New("A workspace path, package name, nonempty user-task UId and caption are required.")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	settings, err := processWriteJSON(filepath.Join(root, ".clio", "workspaceSettings.json"))
	if err != nil {
		return nil, err
	}
	owned := false
	packages, _ := settings["Packages"].([]any)
	for _, item := range packages {
		if item == packageName {
			owned = true
		}
	}
	if !owned {
		return nil, errors.New("The package must belong to the selected clio workspace.")
	}
	packagePath := filepath.Join(root, "packages", packageName)
	descriptorDoc, err := processWriteJSON(filepath.Join(packagePath, "descriptor.json"))
	if err != nil {
		return nil, err
	}
	descriptor := processWriteObject(descriptorDoc, "Descriptor")
	packageUID := normalizeGUID(processWriteString(descriptor, "UId"))
	if processWriteString(descriptor, "Name") != packageName || packageUID == "" {
		return nil, errors.New("The package descriptor name or UId does not match the requested package.")
	}
	matches := 0
	err = filepath.WalkDir(filepath.Join(packagePath, "Schemas"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "descriptor.json" {
			return nil
		}
		artifact, err := processWriteJSON(path)
		if err != nil {
			return err
		}
		schemaDescriptor := processWriteObject(artifact, "Descriptor")
		if normalizeGUID(processWriteString(schemaDescriptor, "UId")) != uid {
			return nil
		}
		matches++
		if matches > 1 {
			return errors.New("The user-task UId is duplicated in the package.")
		}
		metaDoc, err := processWriteJSON(filepath.Join(filepath.Dir(path), "metadata.json"))
		if err != nil {
			return err
		}
		meta := processWriteObject(processWriteObject(metaDoc, "MetaData"), "Schema")
		if processWriteString(schemaDescriptor, "ManagerName") != "ProcessUserTaskSchemaManager" || processWriteString(meta, "ManagerName") != "ProcessUserTaskSchemaManager" || processWriteString(meta, "A2") != processWriteString(schemaDescriptor, "Name") || normalizeGUID(processWriteString(meta, "UId")) != uid || normalizeGUID(processWriteString(meta, "B6")) != packageUID {
			return errors.New("The schema must be a user task with matching descriptor, metadata and package identities.")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if matches == 0 {
		return nil, errors.New("The requested user-task UId was not found in the workspace package.")
	}
	type pending struct{ path, content string }
	var writes []pending
	var paths []string
	for _, dialect := range []struct {
		suffix string
		engine int
	}{{"PostgreSql", 2}, {"MsSql", 0}} {
		name := "UsrRegisterTask" + strings.ReplaceAll(uid, "-", "") + dialect.suffix
		dir := filepath.Join(packagePath, "SqlScripts", name)
		scriptPath, descriptorPath := filepath.Join(dir, name+".sql"), filepath.Join(dir, "descriptor.json")
		sql := processWriteSQL(uid, packageUID, caption, dialect.engine)
		if prior, err := os.ReadFile(scriptPath); err == nil {
			if strings.ReplaceAll(string(prior), "\r\n", "\n") != sql {
				return nil, fmt.Errorf("Existing registration SQL differs; it was preserved: %s", scriptPath)
			}
		} else if os.IsNotExist(err) {
			writes = append(writes, pending{scriptPath, sql})
		} else {
			return nil, err
		}
		if _, err := os.Stat(descriptorPath); err == nil {
			doc, err := processWriteJSON(descriptorPath)
			if err != nil {
				return nil, err
			}
			existing := processWriteObject(doc, "SqlScript")
			if processWriteString(existing, "Name") != name || existing["DBEngineType"] != float64(dialect.engine) || existing["InstallType"] != float64(1) || normalizeGUID(processWriteString(existing, "UId")) == "" {
				return nil, fmt.Errorf("Existing registration descriptor conflicts with the requested task: %s", descriptorPath)
			}
		} else if os.IsNotExist(err) {
			doc := map[string]any{"SqlScript": map[string]any{"UId": processWriteGUID(), "Name": name, "DBEngineType": dialect.engine, "InstallType": 1, "ModifiedOnUtc": fmt.Sprintf("/Date(%d)/", time.Now().UnixMilli())}}
			data, _ := json.MarshalIndent(doc, "", "  ")
			writes = append(writes, pending{descriptorPath, string(data)})
		} else {
			return nil, err
		}
		paths = append(paths, scriptPath)
	}
	for _, write := range writes {
		if err := os.MkdirAll(filepath.Dir(write.path), 0755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(write.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString(write.content)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return paths, nil
}
