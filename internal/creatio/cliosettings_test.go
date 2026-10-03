package creatio

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The fixture uses invented names and example.com hosts only.
const clioSettingsFixture = "\xef\xbb\xbf" + `{
  "ActiveEnvironmentKey": "beta",
  "dbConnectionStringKeys": {"local": {"Uri": "mssql://db.example.com", "WorkingFolder": "/tmp", "Login": "sa", "Password": "db-pass"}},
  "Environments": {
    "beta": {"Uri": "http://beta.example.com:88/site", "Login": "user-b", "Password": "pass-b", "IsNetCore": false, "Safe": false},
    "Alpha": {"uri": "https://alpha.example.com/", "login": "user-a", "password": "pass-a", "isNetCore": "true", "DeveloperModeEnabled": true},
    "cloud": {"Uri": "https://tenant.creatio.com", "ClientId": "client-1", "ClientSecret": "secret-1", "DbServerKey": "local"},
    "token": {"Uri": "https://token.example.com", "AccessToken": "bearer-1", "AccessTokenType": "Bearer"}
  }
}`

func TestParseClioSettingsReadsFormsOAuthAndNetCore(t *testing.T) {
	settings, err := ParseClioSettings([]byte(clioSettingsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Environments) != 4 || settings.Environments[0].Name != "beta" || settings.Environments[1].Name != "Alpha" {
		t.Fatalf("environments out of file order: %+v", settings.Environments)
	}
	alpha, ok := settings.Find("ALPHA")
	if !ok || !alpha.IsNetCore || alpha.Login != "user-a" || alpha.DeveloperModeEnabled == nil || !*alpha.DeveloperModeEnabled {
		t.Fatalf("Alpha = %+v, %v", alpha, ok)
	}
	config, err := alpha.Config()
	if err != nil || config.BaseURL != "https://alpha.example.com" || !config.IsNetCore || config.Login != "user-a" || config.ClientID != "" {
		t.Fatalf("Alpha config = %+v, %v", config, err)
	}
	cloud, _ := settings.Find("cloud")
	config, err = cloud.Config()
	if err != nil || config.ClientID != "client-1" || config.Secret != "secret-1" ||
		config.TokenURL != "https://tenant-is.creatio.com/connect/token" || config.Login != "" {
		t.Fatalf("cloud config = %+v, %v", config, err)
	}
	token, _ := settings.Find("token")
	if config, err = token.Config(); err != nil || config.AccessToken != "bearer-1" {
		t.Fatalf("token config = %+v, %v", config, err)
	}
	if active, ok := settings.Active(); !ok || active.Name != "beta" {
		t.Fatalf("active = %+v, %v", active, ok)
	}
	if settings.DBServers["local"].WorkingFolder != "/tmp" {
		t.Fatalf("db servers = %+v", settings.DBServers)
	}
	if _, ok := settings.Find("missing"); ok {
		t.Fatal("an unknown name was found")
	}
	if got := alpha.SimpleLoginURI(); got != "https://alpha.example.com/Shell/?simplelogin=true" {
		t.Fatalf("simple login = %q", got)
	}
}

func TestParseClioSettingsToleratesCommentsAndRejectsGarbage(t *testing.T) {
	settings, err := ParseClioSettings([]byte("{\n// note\n\"Environments\": {\"a\": {\"Uri\": \"http://a.example.com\",},},\n}"))
	if err != nil || len(settings.Environments) != 1 {
		t.Fatalf("commented file = %+v, %v", settings, err)
	}
	if _, err := ParseClioSettings([]byte("[1,2]")); err == nil {
		t.Fatal("an array was accepted as settings")
	}
}

func TestFillOverridesLikeClio(t *testing.T) {
	settings, _ := ParseClioSettings([]byte(clioSettingsFixture))
	beta, _ := settings.Find("beta")
	filled := beta.Fill(ConnectionOverrides{Login: "other"})
	if filled.Login != "other" || filled.Password != "pass-b" || filled.URI != beta.URI {
		t.Fatalf("filled = %+v", filled)
	}
	direct := ClioEnvironment{}.Fill(ConnectionOverrides{URI: "https://x.creatio.com", ClientID: "c", ClientSecret: "s"})
	config, err := direct.Config()
	if err != nil || config.TokenURL != "https://x-is.creatio.com/connect/token" || config.ClientID != "c" {
		t.Fatalf("direct config = %+v, %v", config, err)
	}
	token, _ := settings.Find("token")
	if moved := token.Fill(ConnectionOverrides{URI: "https://elsewhere.example.com"}); moved.AccessToken != "" {
		t.Fatal("a stored bearer token followed the call to another host")
	}
	if _, err := (ClioEnvironment{Name: "x", URI: "not a url"}).Config(); err == nil {
		t.Fatal("an invalid Uri was accepted")
	}
}

func TestLoadClioSettingsMissingFile(t *testing.T) {
	if _, err := LoadClioSettings(filepath.Join(t.TempDir(), "appsettings.json")); !os.IsNotExist(err) {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestClioSettingsPathFollowsClio(t *testing.T) {
	t.Setenv("CLIO_HOME", filepath.Join("custom", "home"))
	if got := ClioSettingsPath(); got != filepath.Join("custom", "home", "appsettings.json") {
		t.Fatalf("CLIO_HOME path = %q", got)
	}
	t.Setenv("CLIO_HOME", "")
	variable := "HOME"
	if runtime.GOOS == "windows" {
		variable = "LOCALAPPDATA"
	}
	t.Setenv(variable, filepath.Join("user", "dir"))
	if got := ClioSettingsPath(); got != filepath.Join("user", "dir", "creatio", "clio", "appsettings.json") {
		t.Fatalf("default path = %q", got)
	}
}
