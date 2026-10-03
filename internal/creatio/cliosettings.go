package creatio

// Reading clio's own settings file, so one process can serve every environment clio has registered.
// The file is only read here; it belongs to clio and is never written.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ClioEnvironment is one entry of clio's appsettings.json "Environments" object. Only the members this
// server uses or shows through list-environments are read; clio keeps others it does not need here.
type ClioEnvironment struct {
	Name                 string
	URI                  string
	Login                string
	Password             string
	ClientID             string
	ClientSecret         string
	authAppURI           string
	IsNetCore            bool
	Safe                 *bool
	DeveloperModeEnabled *bool
	Maintainer           string
	DBName               string
	BackupFilePath       string
	DBServerKey          string
	WorkspacePathes      string
	EnvironmentPath      string
	AccessToken          string
	AccessTokenType      string
	// present names the members the file holds with a non-null value; clio shows an empty string but omits null.
	present map[string]bool
}

// Present reports whether the settings file holds the member (by its appsettings.json name) with a
// non-null value.
func (e ClioEnvironment) Present(member string) bool { return e.present[member] }

// AuthAppURI mirrors clio's EnvironmentSettings.AuthAppUri getter: an empty value on a *.creatio.com site
// becomes that site's identity-server token endpoint.
func (e ClioEnvironment) AuthAppURI() string {
	if e.authAppURI == "" && strings.Contains(strings.ToLower(e.URI), ".creatio.com") {
		return strings.ReplaceAll(strings.ToLower(e.URI), ".creatio.com", "-is.creatio.com/connect/token")
	}
	return e.authAppURI
}

// SimpleLoginURI mirrors clio's EnvironmentSettings.SimpleloginUri.
func (e ClioEnvironment) SimpleLoginURI() string {
	if e.URI == "" && !e.present["Uri"] {
		return ""
	}
	clean := e.URI
	if index := strings.Index(clean, ".creatio.com"); index != -1 {
		clean = clean[:index+len(".creatio.com")]
	}
	if e.IsNetCore {
		return strings.TrimRight(clean, "/") + "/Shell/?simplelogin=true"
	}
	return strings.TrimRight(clean, "/") + "/0/Shell/?simplelogin=true"
}

// ConnectionOverrides are clio's per-call connection arguments (uri, login, password, client-id,
// client-secret, auth-app-uri). A non-empty value replaces the registered one, as EnvironmentSettings.Fill does.
type ConnectionOverrides struct {
	URI, Login, Password, ClientID, ClientSecret, AuthAppURI string
}

// Empty reports whether no override was supplied.
func (o ConnectionOverrides) Empty() bool { return o == ConnectionOverrides{} }

// Fill applies per-call overrides the way clio's EnvironmentSettings.Fill does: each non-empty argument
// replaces the stored value. A stored bearer token is carried only when it is the one credential left and
// the call still targets the stored address.
func (e ClioEnvironment) Fill(overrides ConnectionOverrides) ClioEnvironment {
	result := e
	pick := func(override, stored string) string {
		if override != "" {
			return override
		}
		return stored
	}
	result.URI = pick(overrides.URI, e.URI)
	result.Login = pick(overrides.Login, e.Login)
	pw := pick(overrides.Password, e.Password)
	result.Password = pw
	result.ClientID = pick(overrides.ClientID, e.ClientID)
	cs := pick(overrides.ClientSecret, e.ClientSecret)
	result.ClientSecret = cs
	result.authAppURI = pick(overrides.AuthAppURI, e.AuthAppURI())
	explicitCredentials := overrides.Login != "" || overrides.Password != "" || overrides.ClientID != "" || overrides.ClientSecret != ""
	storedCredentials := e.Login != "" || e.Password != "" || e.ClientID != "" || e.ClientSecret != ""
	if explicitCredentials || storedCredentials || (overrides.URI != "" && !sameURI(overrides.URI, e.URI)) {
		result.AccessToken, result.AccessTokenType = "", ""
	}
	return result
}

func sameURI(first, second string) bool {
	left, errLeft := url.Parse(strings.TrimRight(strings.TrimSpace(first), "/"))
	right, errRight := url.Parse(strings.TrimRight(strings.TrimSpace(second), "/"))
	return errLeft == nil && errRight == nil && left.IsAbs() && right.IsAbs() &&
		strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host) && left.Path == right.Path
}

// Config turns the environment into this server's connection configuration. The authentication mode is
// chosen as clio's ApplicationClientFactory chooses it: a stored bearer token first, then OAuth client
// credentials when a client id is set, otherwise forms login.
func (e ClioEnvironment) Config() (Config, error) {
	base := strings.TrimSpace(e.URI)
	if base == "" {
		return Config{}, fmt.Errorf("environment '%s' has no Uri", e.Name)
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("environment '%s' has an invalid Uri; an absolute http(s) URL is required", e.Name)
	}
	config := Config{BaseURL: strings.TrimRight(base, "/"), IsNetCore: e.IsNetCore}
	switch {
	case e.AccessToken != "":
		if e.AccessTokenType != "" && !strings.EqualFold(e.AccessTokenType, "Bearer") {
			return Config{}, fmt.Errorf("Access-token type '%s' is not supported; only 'Bearer' is supported.", e.AccessTokenType)
		}
		at := e.AccessToken
		config.AccessToken = at
	case e.ClientID != "":
		config.ClientID, config.Secret, config.TokenURL = e.ClientID, e.ClientSecret, e.AuthAppURI()
	default:
		user, pw := e.Login, e.Password
		config.Login, config.Password = user, pw
	}
	return config, nil
}

// ClioSettings is the part of clio's appsettings.json this server reads.
type ClioSettings struct {
	ActiveEnvironmentKey string
	// Environments keep the file's order; clio matches names case-insensitively and takes the first match.
	Environments []ClioEnvironment
	// DBServers is "dbConnectionStringKeys", which an environment's DbServerKey refers to.
	DBServers map[string]ClioDBServer
}

// ClioDBServer is one "dbConnectionStringKeys" entry.
type ClioDBServer struct {
	URI           string `json:"Uri"`
	WorkingFolder string `json:"WorkingFolder"`
	Login         string `json:"Login"`
	Password      string `json:"Password"`
}

// Find returns the registered environment whose name matches case-insensitively, as clio's
// SettingsRepository.FindEnvironmentKey does.
func (s ClioSettings) Find(name string) (ClioEnvironment, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ClioEnvironment{}, false
	}
	for _, environment := range s.Environments {
		if strings.EqualFold(environment.Name, name) {
			return environment, true
		}
	}
	return ClioEnvironment{}, false
}

// Active returns the environment named by ActiveEnvironmentKey. Like clio's FindEnvironment(null), the key
// must match exactly.
func (s ClioSettings) Active() (ClioEnvironment, bool) {
	if strings.TrimSpace(s.ActiveEnvironmentKey) == "" {
		return ClioEnvironment{}, false
	}
	for _, environment := range s.Environments {
		if environment.Name == s.ActiveEnvironmentKey {
			return environment, true
		}
	}
	return ClioEnvironment{}, false
}

// ClioSettingsPath returns the appsettings.json clio itself uses: CLIO_HOME when set, otherwise
// %LOCALAPPDATA%\creatio\clio on Windows and $HOME/creatio/clio elsewhere (clio's AppSettingsFolderPath,
// built from the assembly's company "creatio" and product "clio").
func ClioSettingsPath() string {
	if home := os.Getenv("CLIO_HOME"); strings.TrimSpace(home) != "" {
		return filepath.Join(home, "appsettings.json")
	}
	variable := "HOME"
	if runtime.GOOS == "windows" {
		variable = "LOCALAPPDATA"
	}
	return filepath.Join(os.Getenv(variable), "creatio", "clio", "appsettings.json")
}

// LoadClioSettings reads and parses clio's settings file.
func LoadClioSettings(path string) (ClioSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClioSettings{}, err
	}
	return ParseClioSettings(data)
}

// ParseClioSettings parses appsettings.json content. Property names match case-insensitively and
// comments or trailing commas are tolerated, as Json.NET does when clio reads the file.
func ParseClioSettings(data []byte) (ClioSettings, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !json.Valid(data) {
		strict, err := lenientJSON(string(data))
		if err != nil {
			return ClioSettings{}, fmt.Errorf("appsettings.json is not valid JSON")
		}
		data = strict
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ClioSettings{}, fmt.Errorf("appsettings.json did not contain a settings object.")
	}
	var settings ClioSettings
	var environments json.RawMessage
	for key, value := range root {
		switch strings.ToLower(key) {
		case "activeenvironmentkey":
			_ = json.Unmarshal(value, &settings.ActiveEnvironmentKey)
		case "environments":
			environments = value
		case "dbconnectionstringkeys":
			_ = json.Unmarshal(value, &settings.DBServers)
		}
	}
	if len(environments) == 0 || string(environments) == "null" {
		return settings, nil
	}
	names, err := objectKeys(environments)
	if err != nil {
		return ClioSettings{}, fmt.Errorf("appsettings.json Environments is not an object")
	}
	var raw map[string]rawClioEnvironment
	if err := json.Unmarshal(environments, &raw); err != nil {
		return ClioSettings{}, fmt.Errorf("appsettings.json Environments could not be read: %w", err)
	}
	for _, name := range names {
		entry := raw[name]
		present := map[string]bool{}
		text := func(member string, value *string) string {
			if value == nil {
				return ""
			}
			present[member] = true
			return *value
		}
		pw, cs, at := text("Password", entry.Password), text("ClientSecret", entry.ClientSecret), text("AccessToken", entry.AccessToken)
		settings.Environments = append(settings.Environments, ClioEnvironment{
			Name: name, URI: text("Uri", entry.URI), Login: text("Login", entry.Login), Password: pw,
			ClientID: text("ClientId", entry.ClientID), ClientSecret: cs,
			authAppURI: text("AuthAppUri", entry.AuthAppURI),
			IsNetCore:  bool(entry.IsNetCore), Safe: (*bool)(entry.Safe), DeveloperModeEnabled: (*bool)(entry.DeveloperModeEnabled),
			Maintainer: text("Maintainer", entry.Maintainer), DBName: text("DbName", entry.DBName),
			BackupFilePath: text("BackupFilePath", entry.BackupFilePath), DBServerKey: text("DbServerKey", entry.DBServerKey),
			WorkspacePathes: text("WorkspacePathes", entry.WorkspacePathes), EnvironmentPath: text("EnvironmentPath", entry.EnvironmentPath),
			AccessToken: at, AccessTokenType: text("AccessTokenType", entry.AccessTokenType),
			present: present,
		})
	}
	return settings, nil
}

type rawClioEnvironment struct {
	URI                  *string   `json:"Uri"`
	Login                *string   `json:"Login"`
	Password             *string   `json:"Password"`
	ClientID             *string   `json:"ClientId"`
	ClientSecret         *string   `json:"ClientSecret"`
	AuthAppURI           *string   `json:"AuthAppUri"`
	IsNetCore            flexBool  `json:"IsNetCore"`
	Safe                 *flexBool `json:"Safe"`
	DeveloperModeEnabled *flexBool `json:"DeveloperModeEnabled"`
	Maintainer           *string   `json:"Maintainer"`
	DBName               *string   `json:"DbName"`
	BackupFilePath       *string   `json:"BackupFilePath"`
	DBServerKey          *string   `json:"DbServerKey"`
	WorkspacePathes      *string   `json:"WorkspacePathes"`
	EnvironmentPath      *string   `json:"EnvironmentPath"`
	AccessToken          *string   `json:"AccessToken"`
	AccessTokenType      *string   `json:"AccessTokenType"`
}

// flexBool reads a JSON boolean, or the strings "true"/"false", as Json.NET converts both.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	switch strings.ToLower(text) {
	case "true":
		*b = true
	case "false", "null", "":
		*b = false
	default:
		return fmt.Errorf("invalid boolean %s", data)
	}
	return nil
}

// objectKeys lists a JSON object's keys in document order, keeping the first of duplicated keys.
func objectKeys(data []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	seen := map[string]bool{}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return nil, err
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys, nil
}
