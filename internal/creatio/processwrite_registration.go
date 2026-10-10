package creatio

// register-process-element: clio's ProcessElementRegistration. Entirely local: it validates the workspace,
// the package and the user-task schema identities, then writes one after-package SQL script per database
// engine (PostgreSQL first, then SQL Server) with its descriptor, never overwriting an existing artifact.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ProcessWriteRegisterRequest is RegisterProcessElementArgs. UserTaskUID is the canonical lower-case GUID,
// or "" for Guid.Empty.
type ProcessWriteRegisterRequest struct {
	WorkspacePath string
	PackageName   string
	UserTaskUID   string
	Caption       string
}

var processWritePackagePattern = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]*\z`)

const (
	processWriteEmptyGUID       = "00000000-0000-0000-0000-000000000000"
	processWriteRegisterNote    = "Install with push-pkg or push-workspace to apply registration. No environment was changed."
	processWriteUserTaskManager = "ProcessUserTaskSchemaManager"
)

// RegisterProcessElement is RegisterProcessElementTool.RegisterProcessElement: the generated script paths as
// Info lines and clio's install note, or the failure as a validation error.
func RegisterProcessElement(request ProcessWriteRegisterRequest) ProcessWriteResult {
	paths, err := processWriteRegister(request)
	if err != nil {
		return ProcessWriteFailure(err.Error())
	}
	messages := make([]LogMessage, 0, len(paths))
	for _, path := range paths {
		messages = append(messages, LogMessage{MessageType: "Info", Value: "Registration script ready: " + path})
	}
	return ProcessWriteResult{ExitCode: 0, Messages: messages, Note: processWriteRegisterNote}
}

func processWriteRegister(request ProcessWriteRegisterRequest) ([]string, error) {
	if strings.TrimSpace(request.WorkspacePath) == "" || strings.TrimSpace(request.PackageName) == "" ||
		!processWritePackagePattern.MatchString(request.PackageName) || request.UserTaskUID == "" ||
		request.UserTaskUID == processWriteEmptyGUID || strings.TrimSpace(request.Caption) == "" ||
		strings.ContainsRune(request.Caption, 0) {
		return nil, errors.New("A workspace path, package name, nonempty user-task UId and caption are required.")
	}
	workspace, err := filepath.Abs(request.WorkspacePath)
	if err != nil {
		return nil, err
	}
	settings, err := processWriteReadArtifact(filepath.Join(workspace, ".clio", "workspaceSettings.json"))
	if err != nil {
		return nil, err
	}
	owned := false
	if packages := settings.get("Packages"); packages.isArray() {
		for _, item := range packages.items {
			if item != nil && item.kind == jkString && item.text == request.PackageName {
				owned = true
			}
		}
	}
	if !owned {
		return nil, errors.New("The package must belong to the selected clio workspace.")
	}
	packagePath := filepath.Join(workspace, "packages", request.PackageName)
	packageDocument, err := processWriteReadArtifact(filepath.Join(packagePath, "descriptor.json"))
	if err != nil {
		return nil, err
	}
	packageDescriptor := packageDocument.get("Descriptor")
	packageUID := processWriteParseGUID(processWriteArtifactString(packageDescriptor, "UId"))
	if processWriteArtifactString(packageDescriptor, "Name") != request.PackageName || packageUID == "" || packageUID == processWriteEmptyGUID {
		return nil, errors.New("The package descriptor name or UId does not match the requested package.")
	}
	if err := processWriteValidateTask(packagePath, request.UserTaskUID, packageUID); err != nil {
		return nil, err
	}
	type pending struct{ path, text string }
	var writes []pending
	var scripts []string
	for _, dialect := range []struct {
		suffix string
		engine int
	}{{"PostgreSql", 2}, {"MsSql", 0}} {
		name := "UsrRegisterTask" + strings.ReplaceAll(request.UserTaskUID, "-", "") + dialect.suffix
		directory := filepath.Join(packagePath, "SqlScripts", name)
		scriptPath := filepath.Join(directory, name+".sql")
		descriptorPath := filepath.Join(directory, "descriptor.json")
		sql := processWriteRegistrationSQL(request.UserTaskUID, packageUID, request.Caption, dialect.engine)
		if existing, err := os.ReadFile(scriptPath); err == nil {
			if strings.ReplaceAll(string(existing), "\r\n", "\n") != strings.ReplaceAll(sql, "\r\n", "\n") {
				return nil, fmt.Errorf("Existing registration SQL differs; it was preserved: %s", scriptPath)
			}
		}
		if processWriteFileExists(descriptorPath) {
			document, err := processWriteReadArtifact(descriptorPath)
			if err != nil {
				return nil, err
			}
			script := document.get("SqlScript")
			uid := processWriteParseGUID(processWriteArtifactString(script, "UId"))
			if processWriteArtifactString(script, "Name") != name || !processWriteArtifactInt(script, "DBEngineType", dialect.engine) ||
				!processWriteArtifactInt(script, "InstallType", 1) || uid == "" || uid == processWriteEmptyGUID {
				return nil, fmt.Errorf("Existing registration descriptor conflicts with the requested task: %s", descriptorPath)
			}
		} else {
			writes = append(writes, pending{descriptorPath, processWriteRegistrationDescriptor(name, dialect.engine)})
		}
		if !processWriteFileExists(scriptPath) {
			writes = append(writes, pending{scriptPath, sql})
		}
		scripts = append(scripts, scriptPath)
	}
	// Both dialects are validated before anything is written; an existing artifact is never overwritten.
	for _, write := range writes {
		if err := os.MkdirAll(filepath.Dir(write.path), 0o755); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(write.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, err
		}
		_, writeErr := file.WriteString(write.text)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return scripts, nil
}

// processWriteValidateTask is ProcessElementRegistration.ValidateTask: exactly one schema descriptor under
// Schemas carries the UId, and it is a user task whose descriptor, metadata and package identities agree.
func processWriteValidateTask(packagePath, taskUID, packageUID string) error {
	schemasPath := filepath.Join(packagePath, "Schemas")
	if info, err := os.Stat(schemasPath); err != nil || !info.IsDir() {
		return fmt.Errorf("Could not find a part of the path '%s'.", schemasPath)
	}
	matched := false
	err := filepath.WalkDir(schemasPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "descriptor.json" {
			return nil
		}
		document, err := processWriteReadArtifact(path)
		if err != nil {
			return err
		}
		descriptor := document.get("Descriptor")
		if processWriteParseGUID(processWriteArtifactString(descriptor, "UId")) != taskUID {
			return nil
		}
		if matched {
			return errors.New("The user-task UId is duplicated in the package.")
		}
		matched = true
		metadata, err := processWriteReadArtifact(filepath.Join(filepath.Dir(path), "metadata.json"))
		if err != nil {
			return err
		}
		schema := metadata.get("MetaData").get("Schema")
		if processWriteArtifactString(descriptor, "ManagerName") != processWriteUserTaskManager ||
			processWriteArtifactString(schema, "ManagerName") != processWriteUserTaskManager ||
			processWriteArtifactString(schema, "A2") != processWriteArtifactString(descriptor, "Name") ||
			processWriteParseGUID(processWriteArtifactString(schema, "UId")) != taskUID ||
			processWriteParseGUID(processWriteArtifactString(schema, "B6")) != packageUID {
			return errors.New("The schema must be a user task with matching descriptor, metadata and package identities.")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !matched {
		return errors.New("The requested user-task UId was not found in the workspace package.")
	}
	return nil
}

// processWriteReadArtifact is ProcessElementRegistration.Read: the file as JSON, with .NET's texts for a
// missing directory or file.
func processWriteReadArtifact(path string) (*jnode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if info, statErr := os.Stat(filepath.Dir(path)); statErr != nil || !info.IsDir() {
				return nil, fmt.Errorf("Could not find a part of the path '%s'.", path)
			}
			return nil, fmt.Errorf("Could not find file '%s'.", path)
		}
		return nil, err
	}
	node, err := parseJNode(data)
	if err != nil {
		return nil, fmt.Errorf("'%s' is not valid JSON: %v", path, err)
	}
	if node.kind == jkNull {
		return nil, fmt.Errorf("Invalid JSON artifact: %s", path)
	}
	return node, nil
}

func processWriteArtifactString(node *jnode, key string) string {
	value := node.get(key)
	if value == nil || value.kind != jkString {
		return ""
	}
	return value.text
}

func processWriteArtifactInt(node *jnode, key string, expected int) bool {
	value := node.get(key)
	return value != nil && value.kind == jkInteger && value.text == fmt.Sprint(expected)
}

func processWriteFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// processWriteRegistrationSQL is ProcessElementRegistration.BuildSql: engine 2 is PostgreSQL (an E-prefixed
// literal, double-quoted names), 0 is SQL Server (an N-prefixed literal, bracketed names). The lines end in
// CRLF as the released clio writes them (its raw string literal is compiled from a CRLF checkout); clio
// compares an existing script with line endings normalized, so either form is accepted on a re-run.
func processWriteRegistrationSQL(task, pkg, caption string, engine int) string {
	return strings.ReplaceAll(processWriteRegistrationSQLLines(task, pkg, caption, engine), "\n", "\r\n")
}

func processWriteRegistrationSQLLines(task, pkg, caption string, engine int) string {
	escaped := strings.ReplaceAll(caption, "'", "''")
	literal := "N'" + escaped + "'"
	quote := func(name string) string { return "[" + name + "]" }
	if engine == 2 {
		literal = "E'" + strings.ReplaceAll(escaped, `\`, `\\`) + "'"
		quote = func(name string) string { return `"` + name + `"` }
	}
	return "INSERT INTO " + quote("SysProcessUserTask") + " (" + quote("SysUserTaskSchemaUId") + ", " + quote("Caption") + ")\n" +
		"SELECT s." + quote("UId") + ", " + literal + "\n" +
		"FROM " + quote("SysSchema") + " s\n" +
		"INNER JOIN " + quote("SysPackage") + " p ON p." + quote("Id") + " = s." + quote("SysPackageId") + "\n" +
		"WHERE s." + quote("UId") + " = '" + task + "' AND p." + quote("UId") + " = '" + pkg + "'\n" +
		"AND NOT EXISTS (SELECT 1 FROM " + quote("SysProcessUserTask") + " t WHERE t." + quote("SysUserTaskSchemaUId") + " = s." + quote("UId") + ");"
}

// processWriteRegistrationDescriptor is the SqlScript descriptor clio writes (indented System.Text.Json).
func processWriteRegistrationDescriptor(name string, engine int) string {
	encodedName, _ := json.Marshal(name)
	return fmt.Sprintf("{\n  \"SqlScript\": {\n    \"UId\": \"%s\",\n    \"Name\": %s,\n    \"DBEngineType\": %d,\n    \"InstallType\": 1,\n    \"ModifiedOnUtc\": \"/Date(%d)/\"\n  }\n}",
		processWriteNewGUID(), encodedName, engine, time.Now().UnixMilli())
}

// processWriteNewGUID is Guid.NewGuid: a random version-4 GUID in lower-case D form.
func processWriteNewGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
