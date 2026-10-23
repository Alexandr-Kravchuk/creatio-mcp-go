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
	"sort"
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

// ListApps returns clio's CLI list-apps shape, where an empty version prints as "none".
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	apps, err := c.installedApps(ctx)
	for i := range apps {
		apps[i].Version = versionOrNone(apps[i].Version)
	}
	return apps, err
}

// installedApps authenticates, sends the DataService SelectQuery, and rejects every non-success response.
func (c *Client) installedApps(ctx context.Context) ([]App, error) {
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
		if isRequestTransportError(err) {
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
		apps = append(apps, App{ID: row.ID, Name: row.Name, Code: row.Code, Version: row.Version, Description: row.Description})
	}
	return apps, nil
}

// AppListItem is one application in the clio MCP list-apps response; clio omits the description.
type AppListItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Version string `json:"version"`
}

// AppListResponse is the clio MCP list-apps envelope: success plus applications, or success=false plus error.
type AppListResponse struct {
	Success      bool          `json:"success"`
	Applications []AppListItem `json:"applications,omitzero"` // [] on an empty success, absent on failure, as in clio
	Error        string        `json:"error,omitempty"`
}

// ListAppsResponse returns applications in clio's MCP order (name, then code, case-insensitive), keeps an
// empty version empty as clio's MCP tool does, and reports failures inside the envelope.
func (c *Client) ListAppsResponse(ctx context.Context) AppListResponse {
	apps, err := c.installedApps(ctx)
	if err != nil {
		return AppListResponse{Error: err.Error()}
	}
	items := make([]AppListItem, 0, len(apps))
	for _, app := range apps {
		items = append(items, AppListItem{ID: app.ID, Name: app.Name, Code: app.Code, Version: app.Version})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if order := compareOrdinalIgnoreCase(items[i].Name, items[j].Name); order != 0 {
			return order < 0
		}
		return compareOrdinalIgnoreCase(items[i].Code, items[j].Code) < 0
	})
	return AppListResponse{Success: true, Applications: items}
}

func (c *Client) formsLogin(ctx context.Context, client *http.Client) error {
	// Creatio keeps the session time zone from TimeZoneOffset and returns date values in it. clio sends the
	// browser convention, minus the host's UTC offset in minutes; 0 would put the session on UTC instead.
	_, offsetSeconds := time.Now().Zone()
	body, err := json.Marshal(map[string]any{"UserName": c.config.Login, "UserPassword": c.config.Password, "TimeZoneOffset": -offsetSeconds / 60})
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
		return loginTransportFailure("forms login transport failure", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return loginReadFailure("forms login response", readErr)
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
		return nil, loginTransportFailure("OAuth token transport failure", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return nil, loginReadFailure("OAuth token response", readErr)
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
	// No client timeout: the login context carries Creatio.Client's login timeout (currentSession).
	loginClient := &http.Client{Transport: c.http.Transport, Jar: jar, CheckRedirect: c.http.CheckRedirect}
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
		return nil, false, classifyLoginFailure(err)
	}
	request, err := buildRequest()
	if err != nil {
		return nil, false, err
	}
	response, err := c.sendWithSession(client, request, session)
	if err != nil {
		return nil, false, requestTransportFailure(ctx, err)
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
		return nil, false, classifyLoginFailure(err)
	}
	response, err = c.sendWithSession(client, retryRequest, refreshed)
	if err != nil {
		return nil, false, requestTransportFailure(ctx, err)
	}
	return response, responseNeedsAuthentication(response), nil
}

// loginTimeout is how long Creatio.Client lets the login before a call take (EnsureAuthenticatedAsync(100000)),
// waiting for another call's login included. It runs on its own clock: the timeout a call gives its request
// does not apply to the login before it. A variable so that tests can shorten it.
var loginTimeout = 100 * time.Second

// loginContext is the context the login before a call runs on: the call's values and its cancellation, but
// loginTimeout instead of the call's deadline, which is the request's own timeout.
func loginContext(ctx context.Context) (context.Context, context.CancelFunc) {
	loginCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginTimeout)
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel()
		}
	})
	return loginCtx, func() {
		stop()
		cancel()
	}
}

// loginFailure is how a login that ran into the login context's end is reported: past loginTimeout it got no
// answer in time, as clio's login does; cancelled by the caller, it keeps the cancellation.
func loginFailure(loginCtx context.Context, err error) error {
	if errors.Is(loginCtx.Err(), context.DeadlineExceeded) {
		return loginTimeoutFault(err)
	}
	if isTransportError(err) {
		return err
	}
	return transportError{err: err}
}

func (c *Client) currentSession(ctx context.Context) (*authSession, error) {
	// The login context is made only when the call has to log in or wait for a login.
	var loginCtx context.Context
	stop := func() {}
	defer func() { stop() }()
	for {
		c.authMu.Lock()
		if c.session != nil && c.session.valid(time.Now()) {
			session := cloneAuthSession(c.session)
			c.authMu.Unlock()
			return session, nil
		}
		if loginCtx == nil {
			loginCtx, stop = loginContext(ctx)
		}
		if flight := c.authFlight; flight != nil {
			c.authMu.Unlock()
			select {
			case <-loginCtx.Done():
				// The login timeout ran out while another call was logging in: no answer came in time.
				return nil, loginFailure(loginCtx, loginCtx.Err())
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
		if c.config.AccessToken != "" {
			// A bearer token stored in clio's settings is used as is; nothing can refresh it.
			session = &authSession{token: c.config.AccessToken}
		} else if c.config.ClientID != "" {
			session, err = c.acquireToken(loginCtx)
		} else {
			session, err = c.formsSession(loginCtx)
		}
		switch {
		case loginCtx.Err() != nil && err == nil:
			err = loginFailure(loginCtx, loginCtx.Err())
		case loginCtx.Err() != nil && isTransportError(err):
			err = loginFailure(loginCtx, err)
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

// transportError marks a request that got no usable answer: it could not be sent, or its answer could not
// be read. A login request that failed this way carries it too, so the failure keeps its category.
type transportError struct{ err error }

func (e transportError) Error() string { return e.err.Error() }
func (e transportError) Unwrap() error { return e.err }

func isTransportError(err error) bool {
	var target transportError
	return errors.As(err, &target)
}

// loginError marks a failure to establish the session (forms login, OAuth token, or the wait for either one).
// It wraps a failure already classified as transportError or authenticationError.
type loginError struct{ err error }

func (e loginError) Error() string { return e.err.Error() }
func (e loginError) Unwrap() error { return e.err }

func isLoginError(err error) bool {
	var target loginError
	return errors.As(err, &target)
}

// isRequestTransportError is a transport failure of the endpoint request itself, after the session was
// established, that carries Go's text. Helpers that prefix such a failure with their route leave a login
// failure's text alone, as they leave a connect fault's: clio's login step raises before any route is named,
// and a call's connect fault reaches clio's tools as Creatio.Client's exception, with no route in it.
func isRequestTransportError(err error) bool {
	_, connect := asConnectFault(err)
	return isTransportError(err) && !isLoginError(err) && !connect
}

// requestTransportFailure classifies an endpoint request that got no usable answer as a transport failure,
// worded as Creatio.Client words it when it is a connect or reset fault.
func requestTransportFailure(ctx context.Context, err error) error {
	if classified, ok := requestConnectFailure(ctx, err); ok {
		return classified
	}
	return transportError{err: err}
}

// classifyLoginFailure keeps the category a session failure was given where it happened (a login request
// that got no answer is a transport failure) and reports everything else - a rejected login, an unexpected
// login answer - as an authentication failure.
func classifyLoginFailure(err error) error {
	if !isTransportError(err) && !isAuthenticationError(err) {
		err = authenticationError{err: err}
	}
	return loginError{err: err}
}

// loginTransportFailure classifies a login request that got no answer as a transport failure, worded as .NET
// words it when the fault has a known .NET sentence.
func loginTransportFailure(label string, err error) error {
	if fault := newConnectFault(err); fault != nil {
		return transportError{err: fault}
	}
	return transportError{err: fmt.Errorf("%s: %w", label, err)}
}

// loginReadFailure classifies a login answer whose body could not be read: cut off in transit, it is a
// transport failure; too large, it is an unexpected answer.
func loginReadFailure(label string, err error) error {
	err = fmt.Errorf("%s: %w", label, err)
	if errors.Is(err, errResponseTooLarge) {
		return err
	}
	return transportError{err: err}
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

// compareOrdinalIgnoreCase orders strings like .NET StringComparer.OrdinalIgnoreCase, which clio uses:
// it compares upper-cased text, so '_' (0x5F) sorts after letters. Lower-casing would put it before them.
func compareOrdinalIgnoreCase(left, right string) int {
	return strings.Compare(strings.ToUpper(left), strings.ToUpper(right))
}

// TargetKey identifies the environment this client acts on (address and identity, never a secret), for
// keying per-environment state such as tracked operations.
func (c *Client) TargetKey() string {
	return strings.ToLower(strings.TrimRight(c.config.BaseURL, "/")) + "|" + c.config.Login + "|" + c.config.ClientID
}
