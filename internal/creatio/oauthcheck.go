package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	oauthCheckDefaultSystemUser = "Supervisor"
	// oauthCheckFailureMessage is clio's VerifyOAuthAppCommand.VerificationFailureMessage. clio reports every
	// verification exception with this one text, so the cause never leaks a URL or a credential.
	oauthCheckFailureMessage  = "OAuth verification failed. Check the IdentityService URL and CRM connectivity; configure OAuth credentials or supply both --client-id and --client-secret."
	oauthCheckIdentitySetting = "OAuth20IdentityServerUrl"
	oauthCheckTokenPath       = "/connect/token"
	// oauthCheckSmokeQuery is clio's IdentityServerProbe.ContactTop1SelectQuery, byte for byte.
	oauthCheckSmokeQuery = `{"rootSchemaName":"Contact","operationType":0,"allColumns":false,"rowCount":1,"columns":{"items":{"Id":{"expression":{"expressionType":0,"columnPath":"Id"}}}}}`
	// oauthCheckHTTPTimeout is .NET HttpClient's default timeout, which clio's probe keeps.
	oauthCheckHTTPTimeout = 100 * time.Second
	// oauthCheckSelectAttempts and oauthCheckRetryDelay are clio's SelectQueryHelper transient-failure budget.
	oauthCheckSelectAttempts = 3
	oauthCheckRetryDelay     = 500 * time.Millisecond
)

// oauthCheckTransientMarkers are the server-reported SelectQuery failures clio re-sends.
var oauthCheckTransientMarkers = []string{"collection was modified", "deadlock", "timeout", "timed out"}

// OAuthSystemUser is the user object of resolve-oauth-system-user. A miss carries only found:false.
type OAuthSystemUser struct {
	SystemUserID *string `json:"systemUserId,omitempty"`
	Name         *string `json:"name,omitempty"`
	Found        bool    `json:"found"`
}

// OAuthSystemUserResult is the resolve-oauth-system-user envelope.
type OAuthSystemUserResult struct {
	Success bool             `json:"success"`
	User    *OAuthSystemUser `json:"user,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// ResolveOAuthSystemUser finds a SysAdminUnit by id when one is given, otherwise by name (Supervisor by
// default), over DataService SelectQuery. Both values are trimmed; a blank value counts as not given.
func (c *Client) ResolveOAuthSystemUser(ctx context.Context, name, id string) OAuthSystemUserResult {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	filter := comparisonFilter("Name", name, 1, 3)
	if name == "" {
		filter = comparisonFilter("Name", oauthCheckDefaultSystemUser, 1, 3)
	}
	if id != "" {
		filter = comparisonFilter("Id", id, 0, 3)
	}
	query := buildSelectQuery("SysAdminUnit", map[string]string{"Id": "Id", "Name": "Name"}, map[string]any{"filter0": filter}, 1)
	rows, err := c.oauthCheckSelectRows(ctx, query)
	if err != nil {
		return OAuthSystemUserResult{Error: err.Error()}
	}
	if len(rows) == 0 {
		return OAuthSystemUserResult{Success: true, User: &OAuthSystemUser{}}
	}
	userID, userName := rowString(rows[0], "Id"), rowString(rows[0], "Name")
	return OAuthSystemUserResult{Success: true, User: &OAuthSystemUser{SystemUserID: &userID, Name: &userName, Found: true}}
}

// oauthCheckSelectRows follows clio's SelectQueryHelper.ExecuteSelectQuery rather than selectRows: a
// rejection reports errorInfo.message, or the whole response body when there is none (a malformed GUID
// arrives only as responseStatus), and a transient server failure is re-sent up to three times.
func (c *Client) oauthCheckSelectRows(ctx context.Context, query any) ([]map[string]json.RawMessage, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encode SelectQuery: %w", err)
	}
	for attempt := 1; ; attempt++ {
		payload, err := c.oauthCheckPostSelect(ctx, body)
		if err != nil {
			return nil, err
		}
		var decoded struct {
			Success   bool                         `json:"success"`
			Rows      []map[string]json.RawMessage `json:"rows"`
			ErrorInfo *struct {
				Message *string `json:"message"`
			} `json:"errorInfo"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return nil, fmt.Errorf("SelectQuery returned invalid JSON: %w", err)
		}
		if decoded.Success {
			return decoded.Rows, nil
		}
		detail := string(payload)
		if decoded.ErrorInfo != nil && decoded.ErrorInfo.Message != nil {
			detail = *decoded.ErrorInfo.Message
		}
		if attempt >= oauthCheckSelectAttempts || !oauthCheckIsTransient(detail) {
			return nil, fmt.Errorf("SelectQuery failed: %s", detail)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(oauthCheckRetryDelay):
		}
	}
}

// oauthCheckPostSelect posts one SelectQuery. Unlike postDataServiceJSON it keeps a JSON body that arrives
// with HTTP 500: Creatio rejects a malformed GUID that way, and clio reports that body as the cause.
func (c *Client) oauthCheckPostSelect(ctx context.Context, body []byte) ([]byte, error) {
	const route = "DataService/json/SyncReply/SelectQuery"
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.serviceURL(route), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build SelectQuery request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		if isTransportError(err) {
			return nil, fmt.Errorf("Creatio service %s transport failure: %w", route, err)
		}
		return nil, err
	}
	payload, err := readResponse(response)
	if err != nil {
		return nil, fmt.Errorf("Creatio service %s response: %w", route, err)
	}
	if looksLikeHTML(payload) || !json.Valid(payload) {
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("Creatio service %s returned HTTP %d", route, response.StatusCode)
		}
		return nil, fmt.Errorf("Creatio service %s returned a body that is not JSON; authentication or routing failed", route)
	}
	return payload, nil
}

func oauthCheckIsTransient(detail string) bool {
	lower := strings.ToLower(detail)
	for _, marker := range oauthCheckTransientMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// VerifyOAuthAppRequest carries the optional overrides. A nil credential means the key was absent; clio
// switches to explicit credentials as soon as either key is present, even when its value is blank.
type VerifyOAuthAppRequest struct {
	ClientID          *string
	ClientSecret      *string
	IdentityServerURL string
}

// VerifyOAuthAppOutcome is the result object of verify-oauth-app. The access token is never part of it.
type VerifyOAuthAppOutcome struct {
	TokenAcquired     bool   `json:"tokenAcquired"`
	DataServiceStatus int    `json:"dataServiceStatus"`
	OK                bool   `json:"ok"`
	IdentityServerURL string `json:"identityServerUrl"`
}

// VerifyOAuthAppResult is the verify-oauth-app envelope.
type VerifyOAuthAppResult struct {
	Success bool                   `json:"success"`
	Result  *VerifyOAuthAppOutcome `json:"result,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// VerifyOAuthAppFailure builds the failure envelope; clio uses one fixed text for every failure.
func VerifyOAuthAppFailure() VerifyOAuthAppResult {
	return VerifyOAuthAppResult{Error: oauthCheckFailureMessage}
}

// VerifyOAuthApp acquires a client_credentials token from the IdentityService and runs one bearer
// DataService SelectQuery (top 1 Contact Id) with it. Credentials default to the configured CREATIO_CLIENT_*
// pair. Nothing is written: the token request and a read are the only calls.
func (c *Client) VerifyOAuthApp(ctx context.Context, request VerifyOAuthAppRequest) VerifyOAuthAppResult {
	outcome, err := c.oauthCheckVerify(ctx, request)
	if err != nil {
		return VerifyOAuthAppFailure()
	}
	return VerifyOAuthAppResult{Success: true, Result: &outcome}
}

func (c *Client) oauthCheckVerify(ctx context.Context, request VerifyOAuthAppRequest) (VerifyOAuthAppOutcome, error) {
	clientID, clientKey := c.config.ClientID, c.config.Secret
	explicitCredentials := request.ClientID != nil || request.ClientSecret != nil
	if explicitCredentials {
		clientID, clientKey = "", ""
		if request.ClientID != nil {
			clientID = *request.ClientID
		}
		if request.ClientSecret != nil {
			clientKey = *request.ClientSecret
		}
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientKey) == "" {
		return VerifyOAuthAppOutcome{}, errors.New("OAuth credentials are missing")
	}
	identityURL := c.oauthCheckIdentityServerURL(ctx, request.IdentityServerURL)
	if !oauthCheckValidBaseURL(identityURL) {
		return VerifyOAuthAppOutcome{}, errors.New("a valid IdentityService base URL is required")
	}
	// The configured secret goes only to the configured IdentityService. clio trusts its caller here; an MCP
	// caller that names any other host must bring its own credentials, or the secret would be sent there.
	if !explicitCredentials && strings.TrimSpace(request.IdentityServerURL) != "" &&
		!strings.EqualFold(identityURL, c.oauthCheckIdentityServerURL(ctx, "")) {
		return VerifyOAuthAppOutcome{}, errors.New("configured OAuth credentials are sent only to the configured IdentityService")
	}
	token, err := c.oauthCheckAcquireToken(ctx, identityURL, clientID, clientKey)
	if err != nil {
		return VerifyOAuthAppOutcome{}, err
	}
	status := 0
	if token != "" {
		if status, err = c.oauthCheckSmoke(ctx, token); err != nil {
			return VerifyOAuthAppOutcome{}, err
		}
	}
	return VerifyOAuthAppOutcome{
		TokenAcquired: token != "", DataServiceStatus: status, OK: token != "" && status == http.StatusOK, IdentityServerURL: identityURL,
	}, nil
}

// oauthCheckIdentityServerURL follows clio's order: the argument, then the configured token endpoint without
// /connect/token, then the OAuth20IdentityServerUrl setting, then the Creatio host with -is appended to its
// first label.
func (c *Client) oauthCheckIdentityServerURL(ctx context.Context, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimRight(explicit, "/")
	}
	if strings.TrimSpace(c.config.TokenURL) != "" {
		saved := strings.TrimRight(c.config.TokenURL, "/")
		if len(saved) >= len(oauthCheckTokenPath) && strings.EqualFold(saved[len(saved)-len(oauthCheckTokenPath):], oauthCheckTokenPath) {
			return saved[:len(saved)-len(oauthCheckTokenPath)]
		}
		return saved
	}
	// clio reads the setting through cliogate first and falls back to the All-Users value; this server reads
	// the All-Users value only. A failed read counts as an empty setting, as in clio.
	if setting := c.GetSysSetting(ctx, oauthCheckIdentitySetting); setting.Success && strings.TrimSpace(setting.Value) != "" {
		return strings.TrimRight(setting.Value, "/")
	}
	return oauthCheckDeriveIdentityURL(c.config.BaseURL)
}

// oauthCheckDeriveIdentityURL mirrors clio's IdentityServerUrlResolver: scheme and port are kept, the path
// is dropped, and -is is inserted before the first dot of the host.
func oauthCheckDeriveIdentityURL(base string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		return ""
	}
	host := parsed.Hostname()
	if dot := strings.IndexByte(host, '.'); dot >= 0 {
		host = host[:dot] + "-is" + host[dot:]
	} else {
		host += "-is"
	}
	if port := parsed.Port(); port != "" && !oauthCheckDefaultPort(parsed.Scheme, port) {
		host += ":" + port
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(host)
}

func oauthCheckDefaultPort(scheme, port string) bool {
	return (strings.EqualFold(scheme, "http") && port == "80") || (strings.EqualFold(scheme, "https") && port == "443")
}

// oauthCheckValidBaseURL accepts an absolute http(s) URL without user info, query or fragment.
func oauthCheckValidBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "http" || scheme == "https") && parsed.User == nil && parsed.RawQuery == "" &&
		!parsed.ForceQuery && parsed.Fragment == "" && !strings.Contains(raw, "#")
}

// oauthCheckAcquireToken posts the client credentials in the form body, as clio's probe does (not as HTTP
// Basic). A non-success answer or an unusable token reads as "not acquired"; only a transport failure is
// an error.
func (c *Client) oauthCheckAcquireToken(ctx context.Context, identityURL, clientID, clientSecret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {clientSecret}}
	requestCtx, cancel := context.WithTimeout(ctx, oauthCheckHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(identityURL, "/")+oauthCheckTokenPath,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build OAuth token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// A redirect would carry the client secret to wherever the IdentityService points it, so none is followed.
	client := &http.Client{Transport: c.http.Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("OAuth token request failed")
	}
	payload, err := readResponse(response)
	if err != nil {
		return "", errors.New("OAuth token response could not be read")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", nil
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(payload, &decoded) != nil {
		return "", nil
	}
	var tokenType, token string
	if json.Unmarshal(decoded["token_type"], &tokenType) != nil || !strings.EqualFold(tokenType, "Bearer") ||
		json.Unmarshal(decoded["access_token"], &token) != nil {
		return "", nil
	}
	if strings.TrimSpace(token) == "" || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", nil
	}
	return token, nil
}

// oauthCheckSmoke sends the bearer SelectQuery outside this client's own session, so the configured
// credentials play no part in the answer. Any status is reported; a 200 must carry success:true.
func (c *Client) oauthCheckSmoke(ctx context.Context, token string) (int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, oauthCheckHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.serviceURL("DataService/json/SyncReply/SelectQuery"),
		bytes.NewReader([]byte(oauthCheckSmokeQuery)))
	if err != nil {
		return 0, fmt.Errorf("build OAuth smoke request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.requestClient().Do(request)
	if err != nil {
		return 0, errors.New("CRM OAuth smoke request failed")
	}
	payload, err := readResponse(response)
	if err != nil {
		return 0, errors.New("CRM OAuth smoke response could not be read")
	}
	if response.StatusCode == http.StatusOK {
		var decoded struct {
			Success *bool `json:"success"`
		}
		if json.Unmarshal(payload, &decoded) != nil || decoded.Success == nil || !*decoded.Success {
			return 0, errors.New("CRM OAuth smoke request did not return a successful DataService response")
		}
	}
	return response.StatusCode, nil
}
