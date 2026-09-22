package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

// App is the output shape of clio list-apps --json.
type App struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// Client talks directly to documented Creatio HTTP endpoints; it has no vendor .NET dependency.
type Client struct {
	config Config
	http   *http.Client
	token  string
}

// NewClient creates a client with an isolated cookie jar for forms authentication.
func NewClient(config Config) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	return &Client{config: config, http: &http.Client{Jar: jar, Timeout: 45 * time.Second}}, nil
}

// ListApps authenticates, sends the DataService SelectQuery, and rejects every non-success response.
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	if c.config.ClientID != "" {
		if err := c.acquireToken(ctx); err != nil {
			return nil, err
		}
	} else if err := c.formsLogin(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(selectQuery())
	if err != nil {
		return nil, fmt.Errorf("encode SelectQuery: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serviceURL("DataService/json/SyncReply/SelectQuery"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build DataService request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DataService SelectQuery transport failure: %w", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return nil, fmt.Errorf("DataService SelectQuery response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("DataService SelectQuery returned HTTP %d", response.StatusCode)
	}
	if looksLikeHTML(payload) {
		return nil, fmt.Errorf("DataService SelectQuery returned HTML instead of JSON; authentication or routing failed")
	}
	var decoded selectResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("DataService SelectQuery returned invalid JSON: %w", err)
	}
	if !decoded.Success {
		return nil, fmt.Errorf("DataService SelectQuery failed: %s", decoded.ErrorInfo.Message)
	}
	apps := make([]App, 0, len(decoded.Rows))
	for _, row := range decoded.Rows {
		if row.ID == "" {
			return nil, fmt.Errorf("DataService SelectQuery returned an installed application without Id")
		}
		apps = append(apps, App{ID: row.ID, Name: row.Name, Code: row.Code, Version: versionOrNone(row.Version), Description: row.Description})
	}
	return apps, nil
}

func (c *Client) formsLogin(ctx context.Context) error {
	body, err := json.Marshal(map[string]any{"UserName": c.config.Login, "UserPassword": c.config.Password, "TimeZoneOffset": 0})
	if err != nil {
		return fmt.Errorf("encode forms login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serviceURL("ServiceModel/AuthService.svc/Login"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build forms login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("forms login transport failure: %w", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return fmt.Errorf("forms login response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("forms login returned HTTP %d", response.StatusCode)
	}
	var result struct { Code int `json:"Code"` }
	if err := json.Unmarshal(payload, &result); err != nil {
		return fmt.Errorf("forms login returned invalid JSON: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("forms login rejected credentials (code %d)", result.Code)
	}
	return nil
}

func (c *Client) acquireToken(ctx context.Context) error {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build OAuth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.config.ClientID, c.config.Secret)
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("OAuth token transport failure: %w", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return fmt.Errorf("OAuth token response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("OAuth token endpoint returned HTTP %d", response.StatusCode)
	}
	var token struct { AccessToken string `json:"access_token"` }
	if err := json.Unmarshal(payload, &token); err != nil || token.AccessToken == "" {
		return fmt.Errorf("OAuth token endpoint returned no access_token")
	}
	c.token = token.AccessToken
	return nil
}

func (c *Client) serviceURL(path string) string {
	prefix := "/0/"
	if c.config.IsNetCore { prefix = "/" }
	return c.config.BaseURL + prefix + path
}

func readResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil { return nil, err }
	if len(payload) > maxResponseBytes { return nil, fmt.Errorf("response exceeds %d-byte safety limit", maxResponseBytes) }
	return payload, nil
}

func looksLikeHTML(payload []byte) bool { return strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(payload))), "<") }
func versionOrNone(version string) string { if version == "" { return "none" }; return version }
