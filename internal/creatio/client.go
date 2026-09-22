package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
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
	config  Config
	http    *http.Client
	tokenMu sync.RWMutex
	token   string
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
	response, err := c.doAuthenticated(ctx, c.http, func() (*http.Request, error) {
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
		return req, nil
	})
	if err != nil {
		if isAuthenticationError(err) {
			return nil, err
		}
		if isTransportError(err) {
			return nil, fmt.Errorf("DataService SelectQuery transport failure: %w", err)
		}
		return nil, err
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build forms login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Forms-authenticated DataService calls also require the CSRF header. The vendor Creatio.Client
	// reads the BPMCSRF cookie set by the login response and echoes it as a header; without it the
	// server answers HTTP 403 with no indication of what is missing.
	if csrf := c.csrfToken(); csrf != "" {
		req.Header.Set("BPMCSRF", csrf)
	}
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
	var result struct {
		Code int `json:"Code"`
	}
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
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(payload, &token); err != nil || token.AccessToken == "" {
		return fmt.Errorf("OAuth token endpoint returned no access_token")
	}
	c.tokenMu.Lock()
	c.token = token.AccessToken
	c.tokenMu.Unlock()
	return nil
}

func (c *Client) bearerToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

// requestClient preserves the configured transport, cookie jar, and redirect policy while allowing
// the request context to carry a caller-selected deadline.
func (c *Client) requestClient() *http.Client {
	return &http.Client{Transport: c.http.Transport, Jar: c.http.Jar, CheckRedirect: c.http.CheckRedirect}
}

// doAuthenticated is the single request path for Creatio endpoints that require forms or OAuth
// authentication. The caller retains ownership of endpoint-specific response handling.
func (c *Client) doAuthenticated(ctx context.Context, client *http.Client, buildRequest func() (*http.Request, error)) (*http.Response, error) {
	if c.config.ClientID != "" {
		if err := c.acquireToken(ctx); err != nil {
			return nil, authenticationError{err: err}
		}
	} else if err := c.formsLogin(ctx); err != nil {
		return nil, authenticationError{err: err}
	}
	request, err := buildRequest()
	if err != nil {
		return nil, err
	}
	if csrf := c.csrfToken(); csrf != "" {
		request.Header.Set("BPMCSRF", csrf)
	}
	if token := c.bearerToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, transportError{err: err}
	}
	return response, nil
}

type authenticationError struct{ err error }

func (e authenticationError) Error() string { return e.err.Error() }
func (e authenticationError) Unwrap() error { return e.err }

func isAuthenticationError(err error) bool {
	var target authenticationError
	return errors.As(err, &target)
}

type transportError struct{ err error }

func (e transportError) Error() string { return e.err.Error() }
func (e transportError) Unwrap() error { return e.err }

func isTransportError(err error) bool {
	var target transportError
	return errors.As(err, &target)
}

// authURL is deliberately NOT built through serviceURL. The authentication route is the documented
// site-root exception: the vendor Creatio.Client posts to "/ServiceModel/AuthService.svc/Login" with no
// "/0/" prefix on .NET Framework, while every DataService route does take that prefix. Applying the
// prefix uniformly is what a naive reimplementation does, and it answers HTTP 401 with no explanation.
// csrfToken returns the BPMCSRF value the login response placed in the cookie jar, or "" when the
// request is bearer-authenticated and no such cookie exists.
func (c *Client) csrfToken() string {
	u, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return ""
	}
	for _, cookie := range c.http.Jar.Cookies(u) {
		if strings.EqualFold(cookie.Name, "BPMCSRF") {
			return cookie.Value
		}
	}
	return ""
}

func (c *Client) authURL() string {
	return c.config.BaseURL + "/ServiceModel/AuthService.svc/Login"
}

func (c *Client) serviceURL(path string) string {
	prefix := "/0/"
	if c.config.IsNetCore {
		prefix = "/"
	}
	return c.config.BaseURL + prefix + path
}

func readResponse(response *http.Response) ([]byte, error) {
	return readResponseLimit(response, maxResponseBytes)
}

var errResponseTooLarge = fmt.Errorf("response exceeds safety byte limit")

func readResponseLimit(response *http.Response, limit int) ([]byte, error) {
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > limit {
		return nil, fmt.Errorf("%w: %d bytes", errResponseTooLarge, limit)
	}
	return payload, nil
}

func looksLikeHTML(payload []byte) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(payload))), "<")
}
func versionOrNone(version string) string {
	if version == "" {
		return "none"
	}
	return version
}
