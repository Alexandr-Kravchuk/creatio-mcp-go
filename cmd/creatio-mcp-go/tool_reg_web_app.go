package main

// reg-web-app: clio 8.1.0.142's RegWebAppTool over RegAppCommand. It writes clio's own appsettings.json
// (internal/clioconfig, under clio's settings lock with an atomic replace), so the environment it registers is
// what the next call resolves: environments.go re-reads the file when its size or modification time changes.

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/clioconfig"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const regWebAppMissingTarget = "Provide `environment-name` to register/update an environment, `active-environment` to switch the default " +
	"environment, or set `add-from-iis` to import environments from IIS."

// regWebAppIisUnavailable is what clio's PowerShell runspace raises where no WSMan client exists (macOS and
// Linux): add-from-iis scans IIS over WinRM, even for localhost.
const regWebAppIisUnavailable = "This parameter set requires WSMan, and no supported WSMan client library was found. " +
	"WSMan is either not installed or unavailable for this system."

// openSettingsFile is FileManager.OpenFile: the settings file in the default application.
var openSettingsFile = func(path string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "start", path).Start()
	}
	return exec.Command("/usr/bin/open", path).Start()
}

// detectRuntime and checkRegistrationLogin are the network steps; tests replace them.
var (
	detectRuntime          = creatio.DetectRuntime
	checkRegistrationLogin = creatio.CheckRegistrationLogin
)

type regWebAppInput struct {
	name, uri, login, password, maintainer, activeEnvironment, host     string
	clientID, clientSecret, authAppURI, workspacePaths, environmentPath string
	checkLogin, addFromIIS                                              bool
	developerMode, safe, isNetCore                                      *bool
}

func init() {
	registerTool(map[string]any{"name": "reg-web-app"}, invokeRegWebApp,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))
}

// settingsPath is the appsettings.json this server resolves environments from.
func (e *environments) settingsPath() string {
	if e != nil && e.path != "" {
		return e.path
	}
	return creatio.ClioSettingsPath()
}

func invokeRegWebApp(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	input, err := readRegWebAppArgs(args)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.name) == "" && strings.TrimSpace(input.activeEnvironment) == "" && !input.addFromIIS {
		return structuredToolResult(creatio.CommandFailure(regWebAppMissingTarget)), nil
	}
	log := &commandLog{}
	exitCode := runRegWebApp(ctx, envs.settingsPath(), input, log)
	return structuredToolResult(creatio.CommandResult{ExitCode: exitCode, Messages: log.messages}), nil
}

type commandLog struct{ messages []creatio.LogMessage }

func (l *commandLog) add(messageType, value string) {
	l.messages = append(l.messages, creatio.LogMessage{MessageType: messageType, Value: value})
}

// runRegWebApp is RegAppCommand.Execute for the arguments the MCP tool maps.
func runRegWebApp(ctx context.Context, path string, input regWebAppInput, log *commandLog) int {
	fail := func(message string) int {
		log.add("Error", message)
		return 1
	}
	log.messages = []creatio.LogMessage{}
	if input.addFromIIS {
		return regWebAppFromIIS(input, log)
	}
	if strings.ToLower(input.name) == "open" {
		log.add("None", "Open "+path+"...")
		if err := openSettingsFile(path); err != nil {
			return fail(err.Error())
		}
		return 0
	}
	if strings.TrimSpace(input.activeEnvironment) != "" {
		if !clioconfig.Exists(path, input.activeEnvironment) {
			return fail("Not found environment " + input.activeEnvironment + " in settings")
		}
		if err := clioconfig.SetActive(path, input.activeEnvironment); err != nil {
			return fail(err.Error())
		}
		log.add("Info", "Active environment set to "+input.activeEnvironment)
		return 0
	}
	_, existing, found := clioconfig.Find(path, input.name)
	authFlow := 0
	clientID := optionalText(input.clientID)
	var redirectPort *int
	var redirectURI *string
	if found {
		authFlow, redirectPort, redirectURI = existing.AuthFlow, existing.RedirectPort, existing.RedirectURI
		if strings.TrimSpace(input.clientID) == "" {
			clientID = existing.ClientID
		}
	}
	if authFlow == 1 && (clientID == nil || strings.TrimSpace(*clientID) == "" || strings.TrimSpace(input.clientSecret) != "") {
		// clio raises a FluentValidation ValidationException built from a message alone: it carries no errors,
		// so its handler logs nothing.
		return 1
	}
	isNetCore, err := resolveRegistrationRuntime(ctx, input, existing, found, log)
	if err != nil {
		return fail(err.Error())
	}
	update := clioconfig.NewEnvironment()
	update.Password, update.Login = optionalText(input.password), optionalText(input.login)
	if input.uri != "" {
		trimmed := strings.TrimRight(input.uri, "/")
		update.URI = &trimmed
	}
	update.Maintainer = optionalText(input.maintainer)
	safe := input.safe != nil && *input.safe
	update.Safe = &safe
	update.IsNetCore = isNetCore
	update.DevMode = input.developerMode
	update.ClientSecret, update.ClientID = optionalText(input.clientSecret), clientID
	update.SetAuthAppURI(optionalText(input.authAppURI))
	update.AuthFlow, update.RedirectPort, update.RedirectURI = authFlow, redirectPort, redirectURI
	update.WorkspacePathes, update.EnvironmentPath = optionalText(input.workspacePaths), optionalText(input.environmentPath)
	if err := clioconfig.Configure(path, input.name, update); err != nil {
		return fail(err.Error())
	}
	log.add("Info", fmt.Sprintf("Environment %s was configured...", input.name))
	_, stored, found := clioconfig.Find(path, input.name)
	if !found {
		return fail("Environment '" + input.name + "' was not found after it was configured.")
	}
	if stored.Safe != nil && *stored.Safe {
		return fail(safeEnvironmentError(text(stored.URI)).Error())
	}
	if !input.checkLogin {
		return 0
	}
	target := filledRegistration(stored, input)
	who := target.Login
	if stored.Login == nil && input.login == "" {
		who = target.ClientID
	}
	log.add("Info", fmt.Sprintf("Try login to %s with %s credentials ...", target.URI, who))
	if err := checkRegistrationLogin(ctx, target); err != nil {
		return fail(err.Error())
	}
	log.add("Info", "Login successful")
	return 0
}

// resolveRegistrationRuntime is RegAppCommand.ResolveIsNetCore: the argument, else the recorded runtime when
// no uri is given, else detection, which may fall back to the recorded runtime of the same uri.
func resolveRegistrationRuntime(ctx context.Context, input regWebAppInput, existing clioconfig.Environment, found bool, log *commandLog) (bool, error) {
	if input.isNetCore != nil {
		return *input.isNetCore, nil
	}
	if strings.TrimSpace(input.uri) == "" {
		return found && existing.IsNetCore, nil
	}
	pick := func(value string, stored *string) string {
		if value != "" || stored == nil {
			return value
		}
		return *stored
	}
	// BuildDetectionEnvironment: each argument, else the recorded value; AuthAppUri read back through its
	// getter, which derives the token endpoint of a *.creatio.com site.
	if !found {
		existing = clioconfig.Environment{}
	}
	probed := clioconfig.NewEnvironment()
	uri := strings.TrimRight(input.uri, "/")
	probed.URI = &uri
	authAppURI := pick(input.authAppURI, existing.AuthAppURI())
	probed.SetAuthAppURI(&authAppURI)
	pw, cs := pick(input.password, existing.Password), pick(input.clientSecret, existing.ClientSecret)
	target := creatio.RegistrationTarget{URI: uri, Login: pick(input.login, existing.Login), Password: pw,
		ClientID: pick(input.clientID, existing.ClientID), ClientSecret: cs, AuthAppURI: text(probed.AuthAppURI())}
	isNetCore, err := detectRuntime(ctx, target)
	if err == nil {
		runtimeName := ".NET Framework"
		if isNetCore {
			runtimeName = ".NET Core / NET8"
		}
		log.add("Info", "Auto-detected runtime: "+runtimeName)
		return isNetCore, nil
	}
	if found && existing.URI != nil && strings.TrimSpace(*existing.URI) != "" &&
		strings.EqualFold(strings.TrimRight(*existing.URI, "/"), strings.TrimRight(input.uri, "/")) {
		recorded := ".NET Framework"
		if existing.IsNetCore {
			recorded = ".NET Core / NET8"
		}
		log.add("Warning", err.Error()+" Keeping the runtime already recorded for "+input.name+": "+recorded+". Pass --IsNetCore to change it.")
		return existing.IsNetCore, nil
	}
	return false, err
}

// filledRegistration is EnvironmentSettings.Fill: a non-empty argument replaces the stored value.
func filledRegistration(stored clioconfig.Environment, input regWebAppInput) creatio.RegistrationTarget {
	pick := func(value string, stored *string) string {
		if value != "" {
			return value
		}
		return text(stored)
	}
	pw, cs := pick(input.password, stored.Password), pick(input.clientSecret, stored.ClientSecret)
	return creatio.RegistrationTarget{
		URI: pick(input.uri, stored.URI), Login: pick(input.login, stored.Login), Password: pw,
		ClientID: pick(input.clientID, stored.ClientID), ClientSecret: cs,
		AuthAppURI: pick(input.authAppURI, stored.AuthAppURI()), IsNetCore: stored.IsNetCore,
	}
}

func regWebAppFromIIS(input regWebAppInput, log *commandLog) int {
	if runtime.GOOS != "windows" {
		log.add("Error", regWebAppIisUnavailable)
		return 1
	}
	// clio scans IIS through a PowerShell remoting runspace (WSMan), which this server does not host.
	log.add("Error", "add-from-iis is not supported by creatio-mcp-go: clio scans IIS through a PowerShell remoting runspace. "+
		"Run 'clio reg-web-app --add-from-iis' instead.")
	return 1
}

func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// readRegWebAppArgs binds RegWebAppArgs: unknown keys are ignored, a known key of the wrong JSON type is
// clio's invalid-parameter-type refusal, null is absent except for the two non-nullable booleans.
func readRegWebAppArgs(args map[string]any) (regWebAppInput, error) {
	var input regWebAppInput
	textFields := []struct {
		name   string
		target *string
	}{
		{"environment-name", &input.name}, {"uri", &input.uri}, {"login", &input.login}, {"password", &input.password},
		{"maintainer", &input.maintainer}, {"active-environment", &input.activeEnvironment}, {"host", &input.host},
		{"client-id", &input.clientID}, {"client-secret", &input.clientSecret}, {"auth-app-uri", &input.authAppURI},
		{"workspace-paths", &input.workspacePaths}, {"environment-path", &input.environmentPath},
	}
	for index, field := range textFields {
		value, err := optionalStringArg(args, "reg-web-app", field.name)
		if err != nil {
			return input, err
		}
		*field.target = value
		if index == 5 {
			// check-login and add-from-iis follow active-environment in clio's argument record.
			for _, flag := range []struct {
				name   string
				target *bool
			}{{"check-login", &input.checkLogin}, {"add-from-iis", &input.addFromIIS}} {
				value, err := requiredBoolArg(args, "reg-web-app", flag.name)
				if err != nil {
					return input, err
				}
				*flag.target = value
			}
		}
	}
	for _, flag := range []struct {
		name   string
		target **bool
	}{{"developer-mode-enabled", &input.developerMode}, {"safe", &input.safe}, {"is-net-core", &input.isNetCore}} {
		value, err := nullableBoolArg(args, "reg-web-app", flag.name)
		if err != nil {
			return input, err
		}
		*flag.target = value
	}
	return input, nil
}

func booleanArgError(tool, name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
}

// requiredBoolArg reads a bool member with a default: absent is false, null or any non-boolean is refused.
func requiredBoolArg(args map[string]any, tool, name string) (bool, error) {
	value, ok := args[name]
	if !ok {
		return false, nil
	}
	flag, isBool := value.(bool)
	if !isBool {
		return false, booleanArgError(tool, name)
	}
	return flag, nil
}

// nullableBoolArg reads a bool? member: absent or null is nil.
func nullableBoolArg(args map[string]any, tool, name string) (*bool, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	flag, isBool := value.(bool)
	if !isBool {
		return nil, booleanArgError(tool, name)
	}
	return &flag, nil
}
