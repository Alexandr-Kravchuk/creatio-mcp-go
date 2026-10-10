package creatio

import (
	"context"
	"testing"
)

func TestCreateServerToServerOAuthAppResolvesTheUserAndAddsTheClient(t *testing.T) {
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit":          {`{"success":true,"rows":[{"Id":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647","Name":"Supervisor"}]}`},
		"rest/OAuthConfigService/AddClient": {`{"AddClientResult":{"success":true,"client":{"ClientId":"ABC","Secret":"replace-me"}}}`},
	})
	result := client.CreateServerToServerOAuthApp(context.Background(), AdminWriteOAuthAppRequest{ClientName: "UsrParityApp", ClientDescription: " "})
	if !result.Success || result.App == nil || *result.App != (AdminWriteOAuthApp{ClientID: "ABC", ClientSecret: "replace-me",
		SystemUserID: "7f3b869f-34f3-4f20-ab4d-7480a5fdf647", GrantType: "client_credentials"}) {
		t.Fatalf("create = %#v", result)
	}
	want := `{"addClientRequest":{"name":"UsrParityApp","applicationUrl":"https://github.com/Advance-Technologies-Foundation/clio.git",` +
		`"description":"server-to-server integration for clio cli","systemUserId":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647",` +
		`"grantType":"client_credentials","allowedGrantTypes":["client_credentials"]}}`
	if got := stand.body("rest/OAuthConfigService/AddClient"); got != want {
		t.Errorf("AddClient body = %s\nwant %s", got, want)
	}
}

func TestCreateServerToServerOAuthAppFailures(t *testing.T) {
	_, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit": {`{"success":true,"rows":[]}`},
	})
	result := client.CreateServerToServerOAuthApp(context.Background(), AdminWriteOAuthAppRequest{SystemUser: " Robot "})
	if result.Success || result.Error == nil || *result.Error != "System user 'Robot' was not found. Pass --system-user-id explicitly "+
		"or resolve an existing user with resolve-oauth-system-user." {
		t.Fatalf("missing user = %#v", result)
	}
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"rest/OAuthConfigService/AddClient": {`{"AddClientResult":{"success":false,"errorInfo":{"message":"Identity server is not configured"}}}`},
	})
	result = client.CreateServerToServerOAuthApp(context.Background(), AdminWriteOAuthAppRequest{SystemUserID: " 11111111-1111-4111-8111-111111111111 "})
	if result.Success || *result.Error != "OAuthConfigService/AddClient did not return client credentials." {
		t.Fatalf("refused = %#v", result)
	}
	if got := stand.routes(); got != "rest/OAuthConfigService/AddClient" {
		t.Errorf("an explicit system user id needs no lookup: %s", got)
	}
}
