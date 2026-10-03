package main

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// list-environments is clio's ShowWebAppListTool: the registered environments of clio's appsettings.json,
// re-read at call time, with passwords and client secrets masked. It takes no arguments and ignores any.
const listEnvironmentsDescription = "Shows the list of registered web applications and their settings as structured JSON, " +
	"read from appsettings.json at call time. Sensitive values such as passwords are masked. " +
	"Returns {environments, settingsFilePath, warnings}."

// sensitiveMask is what clio's ShowAppListCommand prints instead of a password or a client secret.
const sensitiveMask = "****"

// listEnvironmentsResult is ShowWebAppListToolResult; warnings is absent when there are none, as clio's
// serializer drops nulls.
type listEnvironmentsResult struct {
	Environments     []environmentListItem `json:"environments"`
	SettingsFilePath string                `json:"settingsFilePath"`
	Warnings         []string              `json:"warnings,omitempty"`
}

// environmentListItem is ShowWebAppSettingsResult in clio's member order. clio's serializer drops nulls but
// keeps empty strings, so a member is a pointer: nil when the settings file has no value for it. Booleans
// clio declares as non-nullable are always written.
type environmentListItem struct {
	Name                 string        `json:"name"`
	IsActive             bool          `json:"isActive"`
	URI                  *string       `json:"uri,omitempty"`
	DBName               *string       `json:"dbName,omitempty"`
	BackupFilePath       *string       `json:"backupFilePath,omitempty"`
	Login                *string       `json:"login,omitempty"`
	Password             *string       `json:"password,omitempty"`
	Maintainer           *string       `json:"maintainer,omitempty"`
	IsNetCore            bool          `json:"isNetCore"`
	ClientID             *string       `json:"clientId,omitempty"`
	ClientSecret         *string       `json:"clientSecret,omitempty"`
	AuthAppURI           *string       `json:"authAppUri,omitempty"`
	SimpleLoginURI       string        `json:"simpleLoginUri"`
	Safe                 *bool         `json:"safe,omitempty"`
	DeveloperModeEnabled *bool         `json:"developerModeEnabled,omitempty"`
	IsDevMode            bool          `json:"isDevMode"`
	WorkspacePathes      *string       `json:"workspacePathes,omitempty"`
	EnvironmentPath      *string       `json:"environmentPath,omitempty"`
	DBServerKey          *string       `json:"dbServerKey,omitempty"`
	DBServer             *dbServerItem `json:"dbServer,omitempty"`
}

type dbServerItem struct {
	URI           string `json:"uri,omitempty"`
	WorkingFolder string `json:"workingFolder,omitempty"`
	Login         string `json:"login,omitempty"`
	Password      string `json:"password,omitempty"`
}

// mask is clio's MaskSensitiveData: a non-empty secret becomes ****, an empty one stays as it is.
func mask(value string) string {
	if value == "" {
		return ""
	}
	return sensitiveMask
}

// listEnvironments answers with a text block only, as clio does for this tool.
func (e *environments) listEnvironments() *mcp.CallToolResult {
	settings, warning := e.snapshot()
	items := make([]environmentListItem, 0, len(settings.Environments))
	for _, environment := range settings.Environments {
		member := func(name, value string) *string {
			if !environment.Present(name) {
				return nil
			}
			return &value
		}
		authAppURI := member("AuthAppUri", environment.AuthAppURI())
		if computed := environment.AuthAppURI(); computed != "" {
			authAppURI = &computed
		}
		pw, cs := member("Password", mask(environment.Password)), member("ClientSecret", mask(environment.ClientSecret))
		item := environmentListItem{
			Name: environment.Name, IsActive: strings.EqualFold(environment.Name, settings.ActiveEnvironmentKey),
			URI: member("Uri", environment.URI), DBName: member("DbName", environment.DBName),
			BackupFilePath: member("BackupFilePath", environment.BackupFilePath), Login: member("Login", environment.Login),
			Password: pw, Maintainer: member("Maintainer", environment.Maintainer),
			IsNetCore: environment.IsNetCore, ClientID: member("ClientId", environment.ClientID),
			ClientSecret: cs, AuthAppURI: authAppURI,
			SimpleLoginURI: environment.SimpleLoginURI(), Safe: environment.Safe,
			DeveloperModeEnabled: environment.DeveloperModeEnabled,
			IsDevMode:            environment.DeveloperModeEnabled != nil && *environment.DeveloperModeEnabled,
			WorkspacePathes:      member("WorkspacePathes", environment.WorkspacePathes),
			EnvironmentPath:      member("EnvironmentPath", environment.EnvironmentPath),
			DBServerKey:          member("DbServerKey", environment.DBServerKey),
		}
		if server, ok := settings.DBServers[environment.DBServerKey]; ok && environment.DBServerKey != "" {
			dpw := mask(server.Password)
			item.DBServer = &dbServerItem{URI: dotNetURIString(server.URI), WorkingFolder: server.WorkingFolder,
				Login: server.Login, Password: dpw}
		}
		items = append(items, item)
	}
	sortEnvironmentItems(items)
	result := listEnvironmentsResult{Environments: items, SettingsFilePath: e.path}
	if warning != "" {
		result.Warnings = []string{warning}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return toolError(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}

// sortEnvironmentItems orders by name as clio does (StringComparer.OrdinalIgnoreCase).
func sortEnvironmentItems(items []environmentListItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToUpper(items[i].Name) < strings.ToUpper(items[j].Name)
	})
}

// dotNetURIString prints a URI the way System.Uri.ToString does for the common case: an address without
// a path gains a trailing slash.
func dotNetURIString(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return value
	}
	return value + "/"
}
