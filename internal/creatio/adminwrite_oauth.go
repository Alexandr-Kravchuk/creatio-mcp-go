package creatio

// create-server-to-server-oauth-app: clio master's CreateServerToServerOAuthAppCommand.CreateApp over
// IdentityServiceCreatioClient.CreateClioClient (OAuthConfigService/AddClient). The secret is returned in
// the answer only; nothing is logged or stored.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	adminWriteAddClientRoute       = "rest/OAuthConfigService/AddClient"
	adminWriteDefaultClientName    = "clio s2s"
	adminWriteDefaultClientURL     = "https://github.com/Advance-Technologies-Foundation/clio.git"
	adminWriteDefaultClientSummary = "server-to-server integration for clio cli"
)

// AdminWriteOAuthApp is clio's CreateServerToServerOAuthAppResult.
type AdminWriteOAuthApp struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	SystemUserID string `json:"systemUserId"`
	GrantType    string `json:"grantType"`
}

// AdminWriteOAuthAppResponse is clio's CreateServerToServerOAuthAppResponse.
type AdminWriteOAuthAppResponse struct {
	Success bool                `json:"success"`
	App     *AdminWriteOAuthApp `json:"app"`
	Error   *string             `json:"error"`
}

// AdminWriteOAuthAppFailure is the tool's catch: success false and the exception's message.
func AdminWriteOAuthAppFailure(message string) AdminWriteOAuthAppResponse {
	return AdminWriteOAuthAppResponse{Error: &message}
}

// AdminWriteOAuthAppRequest is clio's CreateServerToServerOAuthAppArgs after the tool's defaults.
type AdminWriteOAuthAppRequest struct {
	SystemUserID, SystemUser, ClientName, ClientApplicationURL, ClientDescription string
}

// CreateServerToServerOAuthApp is CreateApp: the system user (by id, else by name, Supervisor by default)
// and one AddClient call.
func (c *Client) CreateServerToServerOAuthApp(ctx context.Context, request AdminWriteOAuthAppRequest) AdminWriteOAuthAppResponse {
	systemUserID := strings.TrimSpace(request.SystemUserID)
	if dataWriteBlank(request.SystemUserID) {
		userName := oauthCheckDefaultSystemUser
		if !dataWriteBlank(request.SystemUser) {
			userName = dataWriteTrim(request.SystemUser)
		}
		resolved := c.ResolveOAuthSystemUser(ctx, userName, "")
		if !resolved.Success {
			return AdminWriteOAuthAppFailure(resolved.Error)
		}
		if resolved.User == nil || !resolved.User.Found || resolved.User.SystemUserID == nil || dataWriteBlank(*resolved.User.SystemUserID) {
			return AdminWriteOAuthAppFailure(fmt.Sprintf("System user '%s' was not found. Pass --system-user-id explicitly or "+
				"resolve an existing user with resolve-oauth-system-user.", userName))
		}
		systemUserID = *resolved.User.SystemUserID
	}
	name := adminWriteDefault(request.ClientName, adminWriteDefaultClientName)
	body := toJNode(orderedFields{{"addClientRequest", orderedFields{
		{"name", name},
		{"applicationUrl", adminWriteDefault(request.ClientApplicationURL, adminWriteDefaultClientURL)},
		{"description", adminWriteDefault(request.ClientDescription, adminWriteDefaultClientSummary)},
		{"systemUserId", systemUserID},
		{"grantType", "client_credentials"},
		{"allowedGrantTypes", []any{"client_credentials"}},
	}}})
	response, err := c.serviceRequest(ctx, serviceCall{Route: adminWriteAddClientRoute, Body: body.stjJSON(), Label: "OAuthConfigService"})
	if err != nil {
		return AdminWriteOAuthAppFailure(err.Error())
	}
	clientID, secret, err := adminWriteClientCredentials(response.payload)
	if err != nil {
		return AdminWriteOAuthAppFailure(err.Error())
	}
	if dataWriteBlank(clientID) || dataWriteBlank(secret) {
		return AdminWriteOAuthAppFailure("OAuthConfigService/AddClient did not return client credentials.")
	}
	return AdminWriteOAuthAppResponse{Success: true, App: &AdminWriteOAuthApp{ClientID: clientID, ClientSecret: secret,
		SystemUserID: systemUserID, GrantType: "client_credentials"}}
}

func adminWriteDefault(value, fallback string) string {
	if dataWriteBlank(value) {
		return fallback
	}
	return value
}

// adminWriteClientCredentials is ExtractFirstString for the id and the secret: the first string property
// (depth first, names compared ignoring case) in the order of clio's candidate names.
func adminWriteClientCredentials(payload []byte) (string, string, error) {
	if strings.TrimSpace(string(payload)) == "" {
		return "", "", nil
	}
	root, err := inspectParseJSON(payload)
	if err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || bytes.HasPrefix(bytes.TrimSpace(payload), []byte("<")) {
			return "", "", errors.New("OAuthConfigService/AddClient answered with something that is not JSON.")
		}
		return "", "", err
	}
	first := func(names ...string) string {
		for _, name := range names {
			if value, ok := adminWriteFindString(root, name); ok {
				return value
			}
		}
		return ""
	}
	return first("clientId", "ClientId", "client_id"), first("clientSecret", "ClientSecret", "secret", "Secret"), nil
}

func adminWriteFindString(node *jnode, name string) (string, bool) {
	switch {
	case node.isObject():
		for _, key := range node.keys {
			child := node.props[key]
			if strings.EqualFold(key, name) && child.kind == jkString {
				return child.text, true
			}
			if value, ok := adminWriteFindString(child, name); ok {
				return value, true
			}
		}
	case node.isArray():
		for _, item := range node.items {
			if value, ok := adminWriteFindString(item, name); ok {
				return value, true
			}
		}
	}
	return "", false
}
