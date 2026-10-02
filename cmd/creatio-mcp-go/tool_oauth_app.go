package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "resolve-oauth-system-user",
		"description": "Resolve a Creatio system user (SysAdminUnit) by name (default Supervisor) or by id over DataService REST on the " +
			"single CREATIO_URL configured at process start. Returns { success, user: { systemUserId, name, found } }; a miss is " +
			"success:true with found:false. id takes precedence over name.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]string{"type": "string", "description": "System user (SysAdminUnit) name to resolve. Defaults to Supervisor."},
			"id":   map[string]string{"type": "string", "description": "System user (SysAdminUnit) id to resolve. Takes precedence over name when supplied."},
		}},
	}, invokeResolveOAuthSystemUser)
	registerTool(map[string]any{
		"name": "verify-oauth-app",
		"description": "Verify a server-to-server OAuth app end to end: acquire a client_credentials token from the IdentityService " +
			"token endpoint, then run one bearer-authenticated DataService SelectQuery on CREATIO_URL with it. Returns tokenAcquired, " +
			"dataServiceStatus, ok and identityServerUrl; the token is never returned. Credentials default to CREATIO_CLIENT_ID/SECRET; " +
			"supply both client-id and client-secret to override. The IdentityService URL defaults to CREATIO_AUTH_APP_URI without " +
			"/connect/token, then OAuth20IdentityServerUrl, then the Creatio host with -is. Nothing is changed.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"client-id":           map[string]string{"type": "string", "description": "OAuth client id override. Omit both credentials to use the configured ones."},
			"client-secret":       map[string]string{"type": "string", "description": "OAuth client secret to verify. Never returned or logged."},
			"identity-server-url": map[string]string{"type": "string", "description": "Explicit IdentityService base URL."},
		}},
	}, invokeVerifyOAuthApp)
}

// Both OAuth tools are lenient in clio: unknown keys are ignored, so only environment selectors are refused.
func invokeResolveOAuthSystemUser(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
	name, err := optionalStringArg(args, "resolve-oauth-system-user", "name")
	if err != nil {
		return nil, err
	}
	id, err := optionalStringArg(args, "resolve-oauth-system-user", "id")
	if err != nil {
		return nil, err
	}
	if refusal := runtimeSelectorRefusal(args); refusal != "" {
		return structuredToolResult(creatio.OAuthSystemUserResult{Error: refusal}), nil
	}
	return structuredToolResult(client.ResolveOAuthSystemUser(ctx, name, id)), nil
}

func invokeVerifyOAuthApp(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
	values := map[string]string{}
	for _, key := range []string{"client-id", "client-secret", "identity-server-url"} {
		value, err := optionalStringArg(args, "verify-oauth-app", key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	// clio answers every verify-oauth-app failure with one fixed text, so a refused selector does too.
	if runtimeSelectorRefusal(args) != "" {
		return structuredToolResult(creatio.VerifyOAuthAppFailure()), nil
	}
	request := creatio.VerifyOAuthAppRequest{IdentityServerURL: values["identity-server-url"]}
	// A present credential switches clio to explicit credentials even when it is blank, so presence is kept.
	if oauthCheckPresent(args, "client-id") {
		value := values["client-id"]
		request.ClientID = &value
	}
	if oauthCheckPresent(args, "client-secret") {
		value := values["client-secret"]
		request.ClientSecret = &value
	}
	return structuredToolResult(client.VerifyOAuthApp(ctx, request)), nil
}

func oauthCheckPresent(args map[string]any, key string) bool {
	value, ok := args[key]
	return ok && value != nil
}
