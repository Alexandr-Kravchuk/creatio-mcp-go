package creatio

// check-auth-code-flow and get-identity-service-config: clio 8.1.0.142's CheckAuthCodeFlowCommand (a
// RemoteCommand over identityServiceInfo/canUseAuthorizationCodeFlow, read as the identity commands read their
// answers) and GetIdentityServiceConfigCommand (the identity-server settings, or the address derived from the
// Creatio host, and whether its OpenID discovery document answers).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IdentityCheckAuthCodeFlow is check-auth-code-flow's verb and endpoint.
var IdentityCheckAuthCodeFlow = AdminWriteIdentityTool{Name: "check-auth-code-flow",
	route: "identityServiceInfo/canUseAuthorizationCodeFlow", method: http.MethodGet}

// CheckAuthCodeFlow is CheckAuthCodeFlowCommand.ProceedResponse over RemoteCommand.Execute: "true"/"false"
// (or {"canUseAuthorizationCodeFlow":…} with format json), an identity error as "<error>: <description>", and
// "Done check-auth-code-flow" after a success.
func (c *Client) CheckAuthCodeFlow(ctx context.Context, format string) CommandResult {
	jsonFormat := strings.EqualFold(format, "json")
	payload, err := c.adminWriteIdentityRequest(ctx, IdentityCheckAuthCodeFlow)
	if err != nil {
		return CommandFailure(c.remoteCommandFailureText(err))
	}
	result := CommandResult{ExitCode: 0, Messages: []LogMessage{}}
	add := func(messageType, value string) {
		result.Messages = append(result.Messages, LogMessage{MessageType: messageType, Value: value})
	}
	response := string(payload)
	switch {
	case dataWriteBlank(response):
		result.ExitCode = 1
		add("Error", "Empty response received from server.")
		add("Error", "Endpoint: "+c.serviceURL(IdentityCheckAuthCodeFlow.route))
	default:
		if errorCode, description, ok := adminWriteIdentityError(payload); ok {
			result.ExitCode = 1
			if jsonFormat {
				add("None", adminWriteIdentityPretty(payload))
			} else {
				add("Error", errorCode+": "+description)
			}
			break
		}
		canUse := strings.EqualFold(strings.TrimSpace(response), "true")
		text := "false"
		if canUse {
			text = "true"
		}
		if jsonFormat {
			add("None", `{"canUseAuthorizationCodeFlow":`+text+`}`)
		} else {
			add("None", text)
		}
	}
	if result.ExitCode == 0 {
		add("Info", "Done "+IdentityCheckAuthCodeFlow.Name)
	}
	return result
}

// remoteCommandFailureText is ExceptionReadableMessageExtension.GetReadableMessageException for what
// Creatio.Client raises when a RemoteCommand's login or request fails: a connect fault, a login answered with
// an error status (a WebException ProtocolError) or rejected (UnauthorizedAccessException, whose clio text also
// carries bracketed login diagnostics).
func (c *Client) remoteCommandFailureText(err error) string {
	if _, ok := asConnectFault(err); ok {
		return readableMessage(err)
	}
	var status, code int
	if _, scanErr := fmt.Sscanf(err.Error(), "forms login returned HTTP %d", &status); scanErr == nil && status > 0 {
		reason := http.StatusText(status)
		return fmt.Sprintf("The remote server returned an error: (%d) %s. (WebException: ProtocolError (HTTP %d %s))",
			status, reason, status, strings.ReplaceAll(reason, " ", ""))
	}
	if _, scanErr := fmt.Sscanf(err.Error(), "forms login rejected credentials (code %d)", &code); scanErr == nil {
		return "Unauthorized " + c.config.Login + " for " + c.config.BaseURL
	}
	return err.Error()
}

// IdentityServiceConfig is GetIdentityServiceConfigResult; null members are left out.
type IdentityServiceConfig struct {
	IdentityServerURL *string `json:"identityServerUrl,omitempty"`
	Source            string  `json:"source"`
	ClientID          *string `json:"clientId,omitempty"`
	TokenEndpoint     *string `json:"tokenEndpoint,omitempty"`
	DiscoveryEndpoint *string `json:"discoveryEndpoint,omitempty"`
	Reachable         bool    `json:"reachable"`
}

// IdentityServiceConfigResponse is get-identity-service-config's envelope.
type IdentityServiceConfigResponse struct {
	Success bool                   `json:"success"`
	Config  *IdentityServiceConfig `json:"config,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// identityDiscoveryClient is the plain HttpClient clio's probe asks the factory for: redirects followed,
// certificates checked, .NET's 100 s timeout. Tests replace it.
var identityDiscoveryClient = &http.Client{Timeout: 100 * time.Second}

// GetIdentityServiceConfig reads OAuth20IdentityServerUrl and OAuth20IdentityServerClientId (a failed read is
// an unset setting), derives the -is host when the URL is empty, and probes the discovery document.
func (c *Client) GetIdentityServiceConfig(ctx context.Context) IdentityServiceConfigResponse {
	read := func(code string) string {
		if setting := c.GetSysSetting(ctx, code); setting.Success {
			return setting.Value
		}
		return ""
	}
	settingURL, clientID := read("OAuth20IdentityServerUrl"), read("OAuth20IdentityServerClientId")
	config := IdentityServiceConfig{Source: "none"}
	identityURL := ""
	if strings.TrimSpace(settingURL) != "" {
		identityURL, config.Source = strings.TrimRight(settingURL, "/"), "setting"
	} else if derived := oauthCheckDeriveIdentityURL(c.config.BaseURL); derived != "" {
		identityURL, config.Source = derived, "derived"
	}
	if strings.TrimSpace(clientID) != "" {
		config.ClientID = &clientID
	}
	if strings.TrimSpace(identityURL) != "" {
		base := strings.TrimRight(identityURL, "/")
		token, discovery := base+"/connect/token", base+"/.well-known/openid-configuration"
		config.IdentityServerURL, config.TokenEndpoint, config.DiscoveryEndpoint = &identityURL, &token, &discovery
		config.Reachable = identityDiscoveryReachable(ctx, discovery)
	}
	return IdentityServiceConfigResponse{Success: true, Config: &config}
}

// identityDiscoveryReachable is IdentityServerProbe.IsDiscoveryReachable: a success status and a document with
// an issuer and absolute http(s) token, JWKS and authorization endpoints without user info.
func identityDiscoveryReachable(ctx context.Context, discovery string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery, nil)
	if err != nil {
		return false
	}
	response, err := identityDiscoveryClient.Do(request)
	if err != nil {
		return false
	}
	payload, err := readResponseLimit(response, maxResponseBytes)
	if err != nil || response.StatusCode < 200 || response.StatusCode > 299 {
		return false
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(payload, &document) != nil || document == nil {
		return false
	}
	text := func(name string) (string, bool) {
		raw, ok := document[name]
		if !ok {
			return "", false
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		return value, true
	}
	if issuer, ok := text("issuer"); !ok || strings.TrimSpace(issuer) == "" {
		return false
	}
	for _, name := range []string{"token_endpoint", "jwks_uri", "authorization_endpoint"} {
		value, ok := text(name)
		if !ok {
			return false
		}
		parsed, err := url.Parse(value)
		if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Host == "" {
			return false
		}
	}
	return true
}
