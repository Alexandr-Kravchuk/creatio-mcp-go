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
	config Config
	http   *http.Client

	authMu     sync.Mutex
	authFlight *authFlight
	session    *authSession
	generation uint64
}

type authSession struct {
	generation uint64
	cookies    []*http.Cookie
	csrf       string
	token      string
	expiresAt  time.Time
}

type authFlight struct {
	done chan struct{}
	err  error
}

// NewClient creates a client with isolated authentication state.
func NewClient(config Config) (*Client, error) {
	return &Client{config: config, http: &http.Client{Timeout: 45 * time.Second}}, nil
}

// ListApps authenticates, sends the DataService SelectQuery, and rejects every non-success response.
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	response, _, err := c.doAuthenticated(ctx, c.http, func() (*http.Request, error) {
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

func (c *Client) formsLogin(ctx context.Context, client *http.Client) error {
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
	response, err := client.Do(req)
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

func (c *Client) acquireToken(ctx context.Context) (*authSession, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build OAuth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.config.ClientID, c.config.Secret)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token transport failure: %w", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return nil, fmt.Errorf("OAuth token response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("OAuth token endpoint returned HTTP %d", response.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   *int64 `json:"expires_in"`
	}
	if err := json.Unmarshal(payload, &token); err != nil || token.AccessToken == "" {
		return nil, fmt.Errorf("OAuth token endpoint returned no access_token")
	}
	session := &authSession{token: token.AccessToken}
	if token.ExpiresIn != nil {
		lifetime := time.Duration(*token.ExpiresIn) * time.Second
		if lifetime <= 0 {
			session.expiresAt = time.Now()
			return session, nil
		}
		margin := 30 * time.Second
		if lifetime <= margin {
			margin = lifetime / 10
		}
		session.expiresAt = time.Now().Add(lifetime - margin)
	}
	return session, nil
}

func (c *Client) formsSession(ctx context.Context) (*authSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	loginClient := &http.Client{Transport: c.http.Transport, Jar: jar, CheckRedirect: c.http.CheckRedirect, Timeout: c.http.Timeout}
	if err := c.formsLogin(ctx, loginClient); err != nil {
		return nil, err
	}
	dataServiceURL, err := url.Parse(c.serviceURL("DataService/json/SyncReply/SelectQuery"))
	if err != nil {
		return nil, fmt.Errorf("parse DataService URL: %w", err)
	}
	session := &authSession{cookies: jar.Cookies(dataServiceURL)}
	for _, cookie := range session.cookies {
		if strings.EqualFold(cookie.Name, "BPMCSRF") {
			session.csrf = cookie.Value
			break
		}
	}
	return session, nil
}

// requestClient preserves the configured transport and redirect policy while allowing
// the request context to carry a caller-selected deadline.
func (c *Client) requestClient() *http.Client {
	return &http.Client{Transport: c.http.Transport, CheckRedirect: c.http.CheckRedirect}
}

// doAuthenticated is the single request path for Creatio endpoints that require forms or OAuth
// authentication. The caller retains ownership of endpoint-specific response handling.
func (c *Client) doAuthenticated(ctx context.Context, client *http.Client, buildRequest func() (*http.Request, error)) (*http.Response, bool, error) {
	session, err := c.currentSession(ctx)
	if err != nil {
		return nil, false, authenticationError{err: err}
	}
	request, err := buildRequest()
	if err != nil {
		return nil, false, err
	}
	response, err := c.sendWithSession(client, request, session)
	if err != nil {
		return nil, false, transportError{err: err}
	}
	if !responseNeedsAuthentication(response) {
		return response, false, nil
	}
	retryRequest, err := cloneRequestForRetry(request)
	if err != nil {
		return response, true, nil
	}
	_ = response.Body.Close()
	c.invalidateSession(session.generation)
	refreshed, err := c.currentSession(ctx)
	if err != nil {
		return nil, false, authenticationError{err: err}
	}
	response, err = c.sendWithSession(client, retryRequest, refreshed)
	if err != nil {
		return nil, false, transportError{err: err}
	}
	return response, responseNeedsAuthentication(response), nil
}

func (c *Client) currentSession(ctx context.Context) (*authSession, error) {
	for {
		c.authMu.Lock()
		if c.session != nil && c.session.valid(time.Now()) {
			session := cloneAuthSession(c.session)
			c.authMu.Unlock()
			return session, nil
		}
		if flight := c.authFlight; flight != nil {
			c.authMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flight.done:
				if flight.err != nil {
					return nil, flight.err
				}
				continue
			}
		}
		flight := &authFlight{done: make(chan struct{})}
		c.authFlight = flight
		c.authMu.Unlock()

		var session *authSession
		var err error
		if c.config.ClientID != "" {
			session, err = c.acquireToken(ctx)
		} else {
			session, err = c.formsSession(ctx)
		}
		if ctx.Err() != nil && err == nil {
			err = ctx.Err()
		}

		c.authMu.Lock()
		if err == nil {
			c.generation++
			session.generation = c.generation
			c.session = cloneAuthSession(session)
		}
		flight.err = err
		if c.authFlight == flight {
			c.authFlight = nil
		}
		close(flight.done)
		c.authMu.Unlock()
		if err != nil {
			return nil, err
		}
		return cloneAuthSession(session), nil
	}
}

func (session *authSession) valid(now time.Time) bool {
	return session != nil && (session.expiresAt.IsZero() || now.Before(session.expiresAt))
}

func cloneAuthSession(session *authSession) *authSession {
	if session == nil {
		return nil
	}
	cloned := *session
	cloned.cookies = make([]*http.Cookie, 0, len(session.cookies))
	for _, cookie := range session.cookies {
		copy := *cookie
		cloned.cookies = append(cloned.cookies, &copy)
	}
	return &cloned
}

func (c *Client) invalidateSession(generation uint64) {
	c.authMu.Lock()
	if c.session != nil && c.session.generation == generation {
		c.session = nil
	}
	c.authMu.Unlock()
}

func (c *Client) sendWithSession(client *http.Client, request *http.Request, session *authSession) (*http.Response, error) {
	sendRequest := request.Clone(request.Context())
	if request.Body != nil {
		if request.GetBody == nil {
			return nil, fmt.Errorf("request body cannot be replayed")
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		sendRequest.Body = body
	}
	sendRequest.Header.Del("Cookie")
	sendRequest.Header.Del("BPMCSRF")
	sendRequest.Header.Del("Authorization")
	for _, cookie := range session.cookies {
		sendRequest.AddCookie(cookie)
	}
	if session.csrf != "" {
		sendRequest.Header.Set("BPMCSRF", session.csrf)
	}
	if session.token != "" {
		sendRequest.Header.Set("Authorization", "Bearer "+session.token)
	}
	return client.Do(sendRequest)
}

func cloneRequestForRetry(request *http.Request) (*http.Request, error) {
	retry := request.Clone(request.Context())
	if request.Body != nil {
		if request.GetBody == nil {
			return nil, fmt.Errorf("request body cannot be replayed")
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		retry.Body = body
	}
	return retry, nil
}

func responseNeedsAuthentication(response *http.Response) bool {
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return true
	}
	body := response.Body
	prefix, _ := io.ReadAll(io.LimitReader(body, 1024))
	response.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), body), closer: body}
	isHTML := strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") || looksLikeHTML(prefix)
	if !isHTML || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false
	}
	path := ""
	if response.Request != nil && response.Request.URL != nil {
		path = response.Request.URL.Path
	}
	return looksLikeLoginHTML(prefix, path)
}

func looksLikeLoginHTML(payload []byte, requestPath string) bool {
	if strings.Contains(strings.ToLower(requestPath), "login") || strings.Contains(strings.ToLower(requestPath), "signin") {
		return true
	}
	text := strings.ToLower(string(payload))
	for _, marker := range []string{"login", "log in", "sign in", "signin", "authservice.svc/login"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (body *replayReadCloser) Close() error { return body.closer.Close() }

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
