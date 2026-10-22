package creatio

// set-fsm-mode: clio master's TurnFsmCommand over SetFsmConfigCommand, FileDesignModePackages
// (LoadPackagesToFileSystem / LoadPackagesToDB / SetFileDesignMode) and, for .NET Core, RestartCommand.
// "on" writes the configuration first and exports the packages afterwards; "off" imports the packages first
// and only then writes the configuration. On macOS and Linux a .NET Framework environment is switched
// remotely through cliogate's SetFileDesignMode; otherwise the environment's Web.config (or
// Terrasoft.WebHost.dll.config) under its EnvironmentPath is edited.

import (
	"context"
	"encoding/json"
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
	adminWriteFsmLoadTimeout    = 30 * time.Minute
	adminWriteFsmRestartTimeout = 90 * time.Second
)

// AdminWriteFsmTarget is what clio's EnvironmentSettings tell the command about the environment.
type AdminWriteFsmTarget struct {
	Name            string
	IsNetCore       bool
	EnvironmentPath string
}

type adminWriteFsmLog struct{ messages []LogMessage }

func (l *adminWriteFsmLog) add(messageType, value string) {
	l.messages = append(l.messages, LogMessage{MessageType: messageType, Value: value})
}

// adminWriteFsmException is an exception clio's command lets escape: BaseTool reports it with exit code -1.
type adminWriteFsmException string

func (e adminWriteFsmException) Error() string { return string(e) }

// SetFsmMode is TurnFsmCommand.Execute.
func (c *Client) SetFsmMode(ctx context.Context, target AdminWriteFsmTarget, mode string) CommandResult {
	log := &adminWriteFsmLog{messages: []LogMessage{}}
	exitCode, err := c.adminWriteTurnFsm(ctx, target, mode, log)
	if err != nil {
		log.add("Error", "[Exception] "+err.Error())
		return CommandResult{ExitCode: -1, Messages: log.messages}
	}
	return CommandResult{ExitCode: exitCode, Messages: log.messages}
}

func (c *Client) adminWriteTurnFsm(ctx context.Context, target AdminWriteFsmTarget, mode string, log *adminWriteFsmLog) (int, error) {
	value := dataWriteTrim(mode)
	isOn, isOff := strings.EqualFold(value, "on"), strings.EqualFold(value, "off")
	if !isOn && !isOff {
		log.add("Error", "Invalid value for IsFsm. Expected: 'on' or 'off'.")
		return 1, nil
	}
	if isOff {
		switch c.adminWriteLoadPackages(ctx, "LoadPackagesToDB", "database", log) {
		case adminWriteLoadCompleted:
			return c.adminWriteSetFsmConfig(ctx, target, false, log)
		case adminWriteLoadDisabled:
			log.add("Warning", "The environment already has file design mode disabled, so there was nothing to import. "+
				"Applying the file system mode configuration.")
			return c.adminWriteSetFsmConfig(ctx, target, false, log)
		}
		log.add("Error", "Packages were not imported into the database, so the file system mode configuration was "+
			"left unchanged. Fix the reported error and run 'clio turn-fsm off' again, or run "+
			"'clio set-fsm-config off' to write the configuration without importing.")
		return 1, nil
	}
	code, err := c.adminWriteSetFsmConfig(ctx, target, true, log)
	if err != nil || code != 0 {
		if err == nil {
			code = 1
		}
		return code, err
	}
	if target.IsNetCore {
		if !c.adminWriteRestartAndWait(ctx, log) {
			log.add("Error", "Application is not available after restart. Try again later or increase restart time.")
			return 1, nil
		}
	}
	switch c.adminWriteLoadPackages(ctx, "LoadPackagesToFileSystem", "file system", log) {
	case adminWriteLoadCompleted:
		return 0, nil
	case adminWriteLoadDisabled:
		log.add("Error", "File system mode was written to the configuration, but the environment still reports file "+
			"design mode as disabled, so no packages were exported. Wait for the web application to "+
			"restart and run 'clio pkg-to-file-system' to finish the export.")
	default:
		log.add("Error", "File system mode is already switched on in the configuration, but the packages were not "+
			"exported. Run 'clio pkg-to-file-system' to finish the export.")
	}
	return 1, nil
}

type adminWriteLoadResult int

const (
	adminWriteLoadRefused adminWriteLoadResult = iota
	adminWriteLoadUnknown
	adminWriteLoadDisabled
	adminWriteLoadCompleted
)

// adminWriteFsmErrorInfo is clio's ErrorInfo.
type adminWriteFsmErrorInfo struct {
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode"`
}

func adminWriteFsmErrorDetails(info *adminWriteFsmErrorInfo) string {
	if info == nil {
		return "unknown error"
	}
	return fmt.Sprintf("%s (error code: %s)", info.Message, info.ErrorCode)
}

func adminWriteFsmLoadMessage(storage, reason string) string {
	return fmt.Sprintf("Load packages to %s on a web application ended with error: %s", storage, reason)
}

// adminWriteLoadPackages is LoadPackagesTo*Command.Load over FileDesignModePackages.LoadPackagesToStorage.
func (c *Client) adminWriteLoadPackages(ctx context.Context, endpoint, storage string, log *adminWriteFsmLog) adminWriteLoadResult {
	result, err := c.adminWriteLoadStorage(ctx, endpoint, storage, log)
	if err != nil {
		log.add("Error", err.Error())
		return adminWriteLoadRefused
	}
	// Load writes an empty line after the loader returns, whatever it returned (live run T23: set-fsm-mode
	// on/off lacked the line clio prints, as pkg-to-file-system already has it).
	log.add("None", "")
	return result
}

func (c *Client) adminWriteLoadStorage(ctx context.Context, endpoint, storage string, log *adminWriteFsmLog) (adminWriteLoadResult, error) {
	stateBody, err := c.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode", Body: []byte{},
		Timeout: adminWriteFsmLoadTimeout, Label: "WorkspaceExplorerService"})
	if err != nil {
		return adminWriteLoadRefused, err
	}
	var state struct {
		Success   bool                    `json:"success"`
		Value     bool                    `json:"value"`
		ErrorInfo *adminWriteFsmErrorInfo `json:"errorInfo"`
	}
	if err := json.Unmarshal(stateBody, &state); err != nil {
		return adminWriteLoadRefused, err
	}
	if !state.Success {
		log.add("Error", "Get file design mode ended with error: "+adminWriteFsmErrorDetails(state.ErrorInfo))
		log.add("Error", adminWriteFsmLoadMessage(storage, "file design mode state is unknown"))
		return adminWriteLoadUnknown, nil
	}
	if !state.Value {
		return adminWriteLoadDisabled, nil
	}
	log.add("None", "Start load packages to "+storage+" on a web application")
	body, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AppInstallerService.svc/" + endpoint, Body: []byte{},
		Timeout: adminWriteFsmLoadTimeout, Label: "AppInstallerService"})
	if err != nil {
		return adminWriteLoadRefused, err
	}
	var response struct {
		Success   bool                    `json:"success"`
		ErrorInfo *adminWriteFsmErrorInfo `json:"errorInfo"`
		Errors    []*struct {
			WorkspaceItem *struct {
				Name string `json:"name"`
			} `json:"workspaceItem"`
			ErrorInfo *adminWriteFsmErrorInfo `json:"errorInfo"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return adminWriteLoadRefused, err
	}
	if len(response.Errors) > 0 {
		if !response.Success && response.ErrorInfo != nil {
			log.add("Error", adminWriteFsmLoadMessage(storage, adminWriteFsmErrorDetails(response.ErrorInfo)))
		}
		for _, item := range response.Errors {
			name, info := "unknown", (*adminWriteFsmErrorInfo)(nil)
			if item != nil {
				info = item.ErrorInfo
				if item.WorkspaceItem != nil && item.WorkspaceItem.Name != "" {
					name = item.WorkspaceItem.Name
				}
			}
			log.add("Error", adminWriteFsmLoadMessage(storage, fmt.Sprintf("Synchronization rejected item '%s': %s", name, adminWriteFsmErrorDetails(info))))
		}
		return adminWriteLoadRefused, nil
	}
	if response.Success {
		log.add("Info", "Load packages to "+storage+" on a web application completed")
		return adminWriteLoadCompleted, nil
	}
	log.add("Error", adminWriteFsmLoadMessage(storage, adminWriteFsmErrorDetails(response.ErrorInfo)))
	return adminWriteLoadRefused, nil
}

// adminWriteSetFsmConfig is SetFsmConfigCommand.Execute for a registered environment.
func (c *Client) adminWriteSetFsmConfig(ctx context.Context, target AdminWriteFsmTarget, on bool, log *adminWriteFsmLog) (int, error) {
	requested := "off"
	if on {
		requested = "on"
	}
	if runtime.GOOS != "windows" && !target.IsNetCore {
		// EnsurePlatformSupport: Web.config of a .NET Framework site cannot be edited from here. clio's
		// FsmModeStatusService posts GetApplicationInfo with Timeout.Infinite, so a probe that reaches the site
		// while it restarts after the previous toggle waits for it instead of failing after 45 s (live run T24).
		status, err := c.getFsmMode(ctx, adminWriteFsmLoadTimeout)
		if err != nil {
			log.add("Error", fmt.Sprintf("Could not determine current FSM state on environment '%s': %s. "+
				"Remote FSM toggle on .NET Framework Creatio from macOS/Linux requires probing the server first.", target.Name, err.Error()))
			return 1, nil
		}
		if strings.EqualFold(status.Mode, requested) {
			log.add("None", fmt.Sprintf("FSM already %s on environment '%s'; no config edit needed.", requested, target.Name))
			return 0, nil
		}
		remote := c.adminWriteSetFileDesignMode(ctx, on)
		if remote.available && remote.success {
			log.add("None", fmt.Sprintf("FSM toggled remotely via cliogate on environment '%s': %s → %s. "+
				"IIS will auto-recycle the AppPool to pick up the new Web.config.", target.Name, remote.previous, remote.next))
			return 0, nil
		}
		if !remote.available {
			log.add("None", fmt.Sprintf("cliogate SetFileDesignMode endpoint is not available on '%s' "+
				"(server cliogate is older than the version that ships with this clio). "+
				"Falling back to actionable error with manual instructions.", target.Name))
		} else {
			log.add("Error", fmt.Sprintf("cliogate SetFileDesignMode endpoint returned an error on '%s': %s", target.Name, remote.message))
		}
		log.add("Error", adminWriteFsmActionableError(target.Name, requested, status.Mode))
		return 1, nil
	}
	path := target.EnvironmentPath
	if dataWriteBlank(path) {
		// clio scans IIS for the site; this server does not, so only a registered EnvironmentPath is used.
		return 0, adminWriteFsmException(fmt.Sprintf("Could not find path to environment: '%s'", target.Name))
	}
	candidates := []string{"Web.config", "Terrasoft.WebHost.dll.config"}
	if target.IsNetCore {
		candidates = []string{"Terrasoft.WebHost.dll.config", "Web.config"}
	}
	for _, name := range candidates {
		configPath := filepath.Join(path, name)
		if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
			table, err := adminWriteEditFsmConfig(configPath, on)
			if err != nil {
				return 0, err
			}
			log.add("None", table)
			return 0, nil
		}
	}
	log.add("None", fmt.Sprintf("Config does not exist in %s. Checked: %s", path, strings.Join(candidates, ", ")))
	return 1, nil
}

var (
	adminWriteFileDesignModeTag = regexp.MustCompile(`(?s)<fileDesignMode\b[^>]*>`)
	adminWriteEnabledAttribute  = regexp.MustCompile(`(\benabled\s*=\s*)(["'])([^"']*)(["'])`)
	adminWriteAddTag            = regexp.MustCompile(`(?s)<add\b[^>]*>`)
	adminWriteKeyStatic         = regexp.MustCompile(`\bkey\s*=\s*["']UseStaticFileContent["']`)
	adminWriteValueAttribute    = regexp.MustCompile(`(\bvalue\s*=\s*)(["'])([^"']*)(["'])`)
)

// adminWriteEditFsmConfig is ModifyWebConfigFile: terrasoft/fileDesignMode enabled and the
// UseStaticFileContent app setting, reported as clio's three-column table. The file's other text is kept as
// it is (clio rewrites the whole document without insignificant white space).
func adminWriteEditFsmConfig(path string, on bool) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	terrasoft := strings.Index(text, "<terrasoft")
	if terrasoft < 0 {
		return "", errors.New("Object reference not set to an instance of an object.")
	}
	location := adminWriteFileDesignModeTag.FindStringIndex(text[terrasoft:])
	if location == nil {
		return "", errors.New("Object reference not set to an instance of an object.")
	}
	start, end := terrasoft+location[0], terrasoft+location[1]
	tag := text[start:end]
	enabled := adminWriteEnabledAttribute.FindStringSubmatch(tag)
	if enabled == nil {
		return "", errors.New("Object reference not set to an instance of an object.")
	}
	newEnabled := fmt.Sprintf("%t", on)
	rows := [][]string{{"Key", "Old Value", "New Value"}, {"--------------------", "---------", "---------"},
		{"fileDesignMode", enabled[3], newEnabled}}
	text = text[:start] + adminWriteEnabledAttribute.ReplaceAllString(tag, "${1}${2}"+newEnabled+"${4}") + text[end:]
	newStatic := fmt.Sprintf("%t", !on)
	text = adminWriteAddTag.ReplaceAllStringFunc(text, func(add string) string {
		if !adminWriteKeyStatic.MatchString(add) {
			return add
		}
		value := adminWriteValueAttribute.FindStringSubmatch(add)
		if value == nil {
			return add
		}
		rows = append(rows, []string{"UseStaticFileContent", value[3], newStatic})
		return adminWriteValueAttribute.ReplaceAllString(add, "${1}${2}"+newStatic+"${4}")
	})
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return adminWriteTable(rows), nil
}

// adminWriteTable is TextUtilities.ConvertTableToString with clio's defaults: columns padded to the widest
// value plus five spaces, one line per row.
func adminWriteTable(rows [][]string) string {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for index, cell := range row {
			if len(cell) > widths[index] {
				widths[index] = len(cell)
			}
		}
	}
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	var builder strings.Builder
	for _, row := range rows {
		for index, cell := range row {
			builder.WriteString(cell + strings.Repeat(" ", widths[index]+5-len(cell)))
		}
		builder.WriteString(newline)
	}
	return builder.String()
}

func adminWriteFsmActionableError(name, requested, actual string) string {
	fileDesignMode, useStatic := "false", "true"
	if requested == "on" {
		fileDesignMode, useStatic = "true", "false"
	}
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	return strings.Join([]string{
		"FSM toggle on .NET Framework Creatio is not supported from macOS/Linux clio:",
		"it requires editing Web.config on the IIS host.",
		fmt.Sprintf("Current state on '%s': %s. Requested: %s.", name, actual, requested),
		"",
		"Options:",
		fmt.Sprintf("  (a) Run `clio turn-fsm %s -e %s` from a Windows machine that has", requested, name),
		"      the Creatio IIS site visible locally.",
		fmt.Sprintf("  (b) Ask the env admin to set `terrasoft/fileDesignMode enabled=\"%s\"` and", fileDesignMode),
		fmt.Sprintf("      `appSettings/UseStaticFileContent value=\"%s\"` in Web.config on", useStatic),
		fmt.Sprintf("      '%s', then recycle the AppPool.", name),
		fmt.Sprintf("  (c) After the toggle, run `clio get-fsm-mode -e %s` to confirm, then", name),
		fmt.Sprintf("      `clio pkg-to-file-system -e %s` (or `pkg-to-db`) works from any platform.", name),
	}, newline)
}

type adminWriteRemoteFsm struct {
	available, success bool
	previous, next     string
	message            string
}

// adminWriteSetFileDesignMode is FileDesignModePackages.SetFileDesignMode (cliogate).
func (c *Client) adminWriteSetFileDesignMode(ctx context.Context, on bool) adminWriteRemoteFsm {
	payload := fmt.Sprintf(`{"isFileDesignMode":%t}`, on)
	response, err := c.serviceRequest(ctx, serviceCall{Route: "rest/CreatioApiGateway/SetFileDesignMode", Body: []byte(payload),
		Timeout: adminWriteFsmLoadTimeout, Label: "ClioGate"})
	if err != nil {
		return adminWriteRemoteFsm{available: true, message: err.Error()}
	}
	text := string(response.payload)
	if response.status == 404 || response.status == 405 {
		return adminWriteRemoteFsm{message: fmt.Sprintf("The remote server returned an error: (%d).", response.status)}
	}
	if dataWriteBlank(text) || strings.Contains(strings.ToLower(text), "endpoint not found") || strings.HasPrefix(strings.TrimSpace(text), "<") {
		return adminWriteRemoteFsm{message: "cliogate SetFileDesignMode endpoint is not available on this server (upgrade cliogate)."}
	}
	var parsed struct {
		Success                bool                    `json:"success"`
		ErrorInfo              *adminWriteFsmErrorInfo `json:"errorInfo"`
		PreviousFileDesignMode string                  `json:"previousFileDesignMode"`
		NewFileDesignMode      string                  `json:"newFileDesignMode"`
	}
	if err := json.Unmarshal(response.payload, &parsed); err != nil {
		return adminWriteRemoteFsm{available: true, message: "Could not parse cliogate SetFileDesignMode response: " + err.Error()}
	}
	result := adminWriteRemoteFsm{available: true, success: parsed.Success, previous: parsed.PreviousFileDesignMode, next: parsed.NewFileDesignMode}
	if !parsed.Success {
		result.message = "Unknown error from cliogate."
		if parsed.ErrorInfo != nil {
			result.message = parsed.ErrorInfo.Message
		}
	}
	return result
}

// adminWriteRestartAndWait is the .NET Core branch: RestartCommand (AppInstallerService.svc/RestartApp),
// then logins every 3 s for up to 90 s until the application answers again.
func (c *Client) adminWriteRestartAndWait(ctx context.Context, log *adminWriteFsmLog) bool {
	if _, err := c.serviceRequest(ctx, serviceCall{Route: "ServiceModel/AppInstallerService.svc/RestartApp", Body: []byte("{}"),
		Label: "AppInstallerService"}); err != nil {
		log.add("Error", err.Error())
	} else {
		log.add("Info", "Done restart-web-app")
	}
	deadline := time.Now().Add(adminWriteFsmRestartTimeout)
	waiting := false
	var last error
	for time.Now().Before(deadline) {
		c.adminWriteForgetSession()
		if _, err := c.currentSession(ctx); err == nil {
			return true
		} else {
			last = err
		}
		if !waiting {
			log.add("None", "Waiting for application to start after restart...")
			waiting = true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(3 * time.Second):
		}
	}
	if last != nil {
		log.add("Error", last.Error())
	}
	return false
}

// adminWriteForgetSession drops the cached session, so the next call logs in again.
func (c *Client) adminWriteForgetSession() {
	c.authMu.Lock()
	session := c.session
	c.authMu.Unlock()
	if session != nil {
		c.invalidateSession(session.generation)
	}
}

// AdminWriteIsNetCore reports whether the client addresses a .NET Core (Creatio NET 8) site.
func (c *Client) AdminWriteIsNetCore() bool { return c.config.IsNetCore }
