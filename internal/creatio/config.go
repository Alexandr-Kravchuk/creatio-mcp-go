// Package creatio contains the small, explicit wire-level client used by the pilot.
package creatio

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config is deliberately environment-only: no connection information is read from files.
type Config struct {
	BaseURL   string
	Login     string
	Password  string
	ClientID  string
	Secret    string
	TokenURL  string
	IsNetCore bool
}

// LoadConfig reads the pilot's connection configuration without exposing its values.
func LoadConfig() (Config, error) {
	c := Config{
		BaseURL:   firstEnv("CREATIO_URL", "CREATIO_MCP_BASE_URL"),
		Login:     firstEnv("CREATIO_LOGIN", "CREATIO_MCP_LOGIN"),
		Password:  firstEnv("CREATIO_PASSWORD", "CREATIO_MCP_PASSWORD"),
		ClientID:  os.Getenv("CREATIO_CLIENT_ID"),
		Secret:    os.Getenv("CREATIO_CLIENT_SECRET"),
		TokenURL:  firstEnv("CREATIO_AUTH_APP_URI", "CREATIO_TOKEN_URL"),
		IsNetCore: strings.EqualFold(os.Getenv("CREATIO_IS_NET_CORE"), "true"),
	}
	if c.BaseURL == "" {
		return Config{}, fmt.Errorf("CREATIO_URL is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("CREATIO_URL must be an absolute http(s) URL without credentials, query, or fragment")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Config{}, fmt.Errorf("CREATIO_URL must use http or https")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	forms := c.Login != "" || c.Password != ""
	oauth := c.ClientID != "" || c.Secret != "" || c.TokenURL != ""
	if forms && oauth {
		return Config{}, fmt.Errorf("configure either forms credentials or OAuth client credentials, not both")
	}
	if forms && (c.Login == "" || c.Password == "") {
		return Config{}, fmt.Errorf("forms authentication requires both CREATIO_LOGIN and CREATIO_PASSWORD")
	}
	if oauth && (c.ClientID == "" || c.Secret == "" || c.TokenURL == "") {
		return Config{}, fmt.Errorf("OAuth requires CREATIO_CLIENT_ID, CREATIO_CLIENT_SECRET, and CREATIO_AUTH_APP_URI")
	}
	if !forms && !oauth {
		return Config{}, fmt.Errorf("configure forms credentials or OAuth client credentials")
	}
	return c, nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
