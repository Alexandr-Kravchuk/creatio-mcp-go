package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// adminWriteTestStand is a fake Creatio for the T12 tools: route -> answer, and the bodies it received.
type adminWriteTestStand struct {
	mu       sync.Mutex
	answers  map[string]func(body map[string]any) string
	received map[string][]string
}

func (s *adminWriteTestStand) bodies(route string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received[route]...)
}

// newAdminWriteTestSession serves the fake stand as the named environment "t12-env" and connects a client.
func newAdminWriteTestSession(t *testing.T, answers map[string]func(body map[string]any) string) (*mcp.ClientSession, *adminWriteTestStand) {
	t.Helper()
	stand := &adminWriteTestStand{answers: answers, received: map[string][]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		route := strings.TrimPrefix(r.URL.Path, "/0/")
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if strings.HasSuffix(route, "/SelectQuery") {
			route += ":" + body["rootSchemaName"].(string)
		}
		stand.mu.Lock()
		stand.received[route] = append(stand.received[route], string(raw))
		answer := stand.answers[route]
		stand.mu.Unlock()
		if answer == nil {
			t.Errorf("unexpected Creatio route %s", route)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(answer(body)))
	}))
	t.Cleanup(server.Close)
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "t12-env", map[string]map[string]any{
		"t12-env": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": false},
	})
	session := connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	return session, stand
}

// adminWriteCall calls a tool through clio-run and returns the JSON text of its answer.
func adminWriteCall(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Content[0].(*mcp.TextContent).Text
}

// adminWriteRawNameIsGated checks that a direct call by the raw name answers clio's confirmation-required.
func adminWriteRawNameIsGated(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	structured, _ := json.Marshal(result.StructuredContent)
	if !result.IsError || !strings.Contains(string(structured), `"code":"confirmation-required"`) {
		t.Errorf("%s by raw name = %s", tool, structured)
	}
}

const adminWriteEmptyRows = `{"success":true,"rows":[]}`

func TestSysSettingWriteToolsThroughClioRun(t *testing.T) {
	settingRows := adminWriteEmptyRows
	session, stand := newAdminWriteTestSession(t, map[string]func(map[string]any) string{
		"DataService/json/SyncReply/InsertSysSettingRequest": func(map[string]any) string {
			settingRows = `{"success":true,"rows":[{"Id":"3c50b1a3-7aae-49f6-8921-92fb3f5adc40","ValueTypeName":"ShortText","ReferenceSchemaUId":""}]}`
			return `{"success":true,"id":"3c50b1a3-7aae-49f6-8921-92fb3f5adc40"}`
		},
		"DataService/json/SyncReply/SelectQuery:SysSettings":      func(map[string]any) string { return settingRows },
		"DataService/json/SyncReply/SelectQuery:SysSettingsValue": func(map[string]any) string { return `{"success":true,"rows":[{"TextValue":"v1"}]}` },
		"DataService/json/SyncReply/PostSysSettingsValues": func(map[string]any) string {
			return `{"success":true,"saveResult":{"UsrParityTool":true}}`
		},
	})
	created := adminWriteCall(t, session, "create-sys-setting", map[string]any{"environment-name": "t12-env", "code": "UsrParityTool",
		"name": "Tool", "value-type-name": "ShortText", "value": "v1", "unknown-key": 1})
	if created != `{"success":true,"code":"UsrParityTool","value-type-name":"ShortText","value":"v1"}` {
		t.Errorf("create = %s", created)
	}
	updated := adminWriteCall(t, session, "update-sys-setting", map[string]any{"environment-name": "t12-env", "code": "UsrParityTool", "value": "v1"})
	if updated != `{"success":true,"code":"UsrParityTool","value":"v1"}` {
		t.Errorf("update = %s", updated)
	}
	if len(stand.bodies("DataService/json/SyncReply/PostSysSettingsValues")) != 2 {
		t.Errorf("value writes = %v", stand.bodies("DataService/json/SyncReply/PostSysSettingsValues"))
	}
	missing := adminWriteCall(t, session, "update-sys-setting", map[string]any{"environment-name": "other", "code": "x", "value": "y"})
	if !strings.HasPrefix(missing, `{"success":false,"code":"x","value":null,"error":"Failed updating sys-setting.","error-category":"Configuration","cause":"Environment with key 'other' not found.`) {
		t.Errorf("unknown environment = %s", missing)
	}
	typed := adminWriteCall(t, session, "create-sys-setting", map[string]any{"environment-name": "t12-env", "code": "x", "is-personal": "no"})
	if !strings.Contains(typed, "invalid-parameter-type: argument 'is-personal' for MCP tool 'create-sys-setting' must be a boolean.") {
		t.Errorf("wrong type = %s", typed)
	}
	for _, tool := range []string{"create-sys-setting", "update-sys-setting", "download-sys-setting-file"} {
		adminWriteRawNameIsGated(t, session, tool, map[string]any{"environment-name": "t12-env", "code": "x"})
	}
}

func TestDownloadSysSettingFileThroughClioRun(t *testing.T) {
	session, _ := newAdminWriteTestSession(t, map[string]func(map[string]any) string{
		"DataService/json/SyncReply/SelectQuery:SysPackage": func(map[string]any) string {
			return `{"success":true,"rows":[{"Name":"cliogate","UId":"","Maintainer":"","Version":"2.0.0.53"}]}`
		},
		"DataService/json/SyncReply/SelectQuery:SysSettings": func(map[string]any) string {
			return `{"success":true,"rows":[{"ValueTypeName":"Binary","ReferenceSchemaUId":""}]}`
		},
		"DataService/json/SyncReply/QuerySysSettings": func(map[string]any) string {
			return `{"success":true,"values":{"UsrLogo":{"value":"AQID"}}}`
		},
	})
	destination := filepath.Join(t.TempDir(), "logo.bin")
	saved := adminWriteCall(t, session, "download-sys-setting-file", map[string]any{"environment-name": "t12-env", "code": "UsrLogo", "file-name": destination})
	want := `{"exit-code":0,"execution-log-messages":[{"message-type":"Info","value":"Binary system setting saved without content conversion."}],` +
		`"file-name":` + string(mustJSON(t, destination)) + `,"byte-count":3}`
	if saved != want {
		t.Errorf("download = %s\nwant %s", saved, want)
	}
	for args, message := range map[string]string{
		`{"environment-name":"t12-env","code":"UsrLogo","file-name":"relative.bin"}`:        "file-name must be an absolute path including the destination filename.",
		`{"environment-name":"t12-env","file-name":"/tmp/t12-absent/x.bin"}`:                "environment-name and code are required.",
		`{"environment-name":"t12-env","code":"UsrLogo","file-name":"` + destination + `"}`: "Destination already exists; choose a new filename.",
	} {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(args), &decoded); err != nil {
			t.Fatal(err)
		}
		answer := adminWriteCall(t, session, "download-sys-setting-file", decoded)
		if answer != `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":`+string(mustJSON(t, message))+`}],"file-name":null,"byte-count":null}` {
			t.Errorf("download %s = %s", args, answer)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestManageToolsThroughClioRun(t *testing.T) {
	session, stand := newAdminWriteTestSession(t, map[string]func(map[string]any) string{
		"DataService/json/SyncReply/SelectQuery:SysAdminUnit": func(map[string]any) string {
			return `{"success":true,"rows":[{"Id":"a29a3ba5-4b0d-de11-9a51-005056c00008","Name":"All employees","SysAdminUnitTypeValue":0,` +
				`"ParentRole":"","Active":true,"Contact":"","ConnectionType":0,"SynchronizeWithLDAP":false,"ForceChangePassword":false}]}`
		},
	})
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"manage-user", map[string]any{"action": "bogus"}, "Unknown user action. Use list, create, update, delete, lock-status, unlock or password."},
		{"manage-role", map[string]any{"action": "delete", "id": "a29a3ba5-4b0d-de11-9a51-005056c00008"},
			"Built-in root and system-administrator roles cannot be deleted by this command."},
		{"manage-license", map[string]any{"action": "user-assign"}, "Administration IDs must be nonempty GUIDs."},
		{"manage-access", map[string]any{"action": "ip-delete"}, "Administration IDs must be nonempty GUIDs."},
	} {
		args := map[string]any{"environment-name": "t12-env"}
		for key, value := range c.args {
			args[key] = value
		}
		answer := adminWriteCall(t, session, c.tool, args)
		if answer != `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":`+string(mustJSON(t, c.want))+`}]}` {
			t.Errorf("%s = %s", c.tool, answer)
		}
		adminWriteRawNameIsGated(t, session, c.tool, args)
	}
	if reads := stand.bodies("DataService/json/SyncReply/SelectQuery:SysAdminUnit"); len(reads) != 1 {
		t.Errorf("SysAdminUnit reads = %d", len(reads))
	}
	typed := adminWriteCall(t, session, "manage-user", map[string]any{"environment-name": "t12-env", "action": "delete", "id": "nope"})
	if !strings.Contains(typed, "invalid-parameter-type: argument 'id' for MCP tool 'manage-user' must be an object.") {
		t.Errorf("bad id = %s", typed)
	}
	missing := adminWriteCall(t, session, "manage-role", map[string]any{"environment-name": "other", "action": "list"})
	if !strings.HasPrefix(missing, `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"[EnvironmentResolutionException] Environment with key 'other' not found.`) {
		t.Errorf("unknown environment = %s", missing)
	}
}
