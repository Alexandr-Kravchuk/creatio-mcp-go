package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/hosttools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPProgressAndResultMetaReachClient(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "probe-server", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "progress-probe"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			token := req.Params.GetProgressToken()
			if token == nil {
				return nil, nil, errors.New("missing progress token")
			}
			if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token, Progress: 1, Total: 1, Message: "probe complete",
			}); err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Meta: mcp.Meta{
				"clioStageEvent": map[string]any{"stage": "probe-complete"},
			}}, map[string]any{"ok": true}, nil
		})

	notifications := make(chan *mcp.ProgressNotificationParams, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			notifications <- req.Params
		},
	})
	session := connectTestClient(t, server, client)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "progress-probe", Meta: mcp.Meta{"progressToken": "probe-token-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case notification := <-notifications:
		if notification.ProgressToken != "probe-token-1" || notification.Message != "probe complete" {
			t.Fatalf("progress notification = %#v", notification)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive correlated notifications/progress")
	}
	if result.Meta["clioStageEvent"] == nil {
		t.Fatalf("result _meta did not reach client: %#v", result.Meta)
	}
}

func TestMCPCallCancellationReachesToolContext(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "probe-server", Version: "test"}, nil)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	mcp.AddTool(server, &mcp.Tool{Name: "cancel-probe"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, nil, ctx.Err()
		})
	session := connectTestClient(t, server, mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	callCtx, cancel := context.WithCancel(context.Background())
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = session.CallTool(callCtx, &mcp.CallToolParams{Name: "cancel-probe"})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not reach the server tool context")
	}
	select {
	case <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client call did not finish after cancellation")
	}
}

func TestTwoTierMCPExposesContractAndRunsHiddenToolByRawName(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/odata/Contact":
			_, _ = w.Write([]byte(`{"value":[{"Id":"contact-1","Name":"Ada"}]}`))
		default:
			t.Errorf("unexpected Creatio route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer creatioServer.Close()
	client, err := creatio.NewClient(creatio.Config{
		BaseURL: creatioServer.URL, Login: "example-user", Password: "replace-me",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))

	listed, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "clio-run,get-tool-contract,list-apps" {
		t.Fatalf("resident tools = %v", names)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "odata-read" {
			t.Fatal("odata-read should be available by contract and dispatch, but omitted from tools/list")
		}
	}

	contract, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get-tool-contract", Arguments: map[string]any{"name": "odata-read"},
	})
	if err != nil || contract.IsError {
		t.Fatalf("get-tool-contract result = %#v, err = %v", contract, err)
	}
	contractValue, ok := contract.StructuredContent.(map[string]any)
	if !ok || contractValue["name"] != "odata-read" {
		t.Fatalf("contract result = %#v", contract.StructuredContent)
	}
	contracts, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-tool-contract"})
	if err != nil || contracts.IsError {
		t.Fatalf("get-tool-contract index = %#v, err = %v", contracts, err)
	}
	contractIndex, ok := contracts.StructuredContent.(map[string]any)
	if !ok || !reflect.DeepEqual(contractIndex["tools"], []any{"execute-esq", "find-empty-iis-port", "get-entity-schema-properties", "get-package-file", "get-sql-schema", "list-app-sections", "list-package-files", "list-packages", "list-pages", "odata-read", "start-creatio"}) {
		t.Fatalf("contract index = %#v", contracts.StructuredContent)
	}
	startContract, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get-tool-contract", Arguments: map[string]any{"name": "start-creatio"},
	})
	if err != nil || startContract.IsError {
		t.Fatalf("start-creatio contract = %#v, err = %v", startContract, err)
	}
	startContractValue, ok := startContract.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("start-creatio contract content = %#v", startContract.StructuredContent)
	}
	startSchema, ok := startContractValue["inputSchema"].(map[string]any)
	if !ok || !reflect.DeepEqual(startSchema["required"], []any{"environmentName"}) {
		t.Fatalf("start-creatio schema = %#v", startContractValue["inputSchema"])
	}

	for _, call := range []*mcp.CallToolParams{
		{Name: "odata-read", Arguments: map[string]any{"entity": "Contact"}},
		{Name: "clio-run", Arguments: map[string]any{
			"command": "odata-read", "args": map[string]any{"entity": "Contact"},
		}},
	} {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError {
			t.Fatalf("call %q result = %#v, err = %v", call.Name, result, err)
		}
		rowsResult, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("call %q structured content = %#v", call.Name, result.StructuredContent)
		}
		rows, ok := rowsResult["rows"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("call %q rows = %#v", call.Name, rowsResult["rows"])
		}
	}
	unsupported, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "clio-run", Arguments: map[string]any{
			"command": "odata-read", "args": map[string]any{"entity": "Contact", "filters": map[string]any{}},
		},
	})
	if err != nil || !unsupported.IsError {
		t.Fatalf("unsupported filter must fail before HTTP: result = %#v, err = %v", unsupported, err)
	}
}

func TestHiddenR1ToolsDispatchByRawNameAndStartProgress(t *testing.T) {
	var startedEnvironment string
	services := hiddenToolServices{
		findEmptyIISPort: func(context.Context) hosttools.PortDiscoveryResult {
			return hosttools.PortDiscoveryResult{Status: "available", FirstAvailablePort: intPointer(41000)}
		},
		startCreatio: func(_ context.Context, environment string, progress func(float64, float64, string) error) (hosttools.StartResult, error) {
			startedEnvironment = environment
			if progress != nil {
				if err := progress(1, 1, "mock start complete"); err != nil {
					return hosttools.StartResult{}, err
				}
			}
			return hosttools.StartResult{Status: "started", Environment: environment, StartedBy: "mock"}, nil
		},
	}
	server := newMCPServerWithHiddenTools(nil, services)
	notifications := make(chan *mcp.ProgressNotificationParams, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			notifications <- req.Params
		},
	})
	session := connectTestClient(t, server, client)

	portResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "find-empty-iis-port"})
	if err != nil || portResult.IsError {
		t.Fatalf("raw find-empty-iis-port result = %#v, err = %v", portResult, err)
	}
	port, ok := portResult.StructuredContent.(map[string]any)
	if !ok || port["status"] != "available" || port["firstAvailablePort"] != float64(41000) {
		t.Fatalf("raw find-empty-iis-port content = %#v", portResult.StructuredContent)
	}

	startResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start-creatio", Meta: mcp.Meta{"progressToken": "start-probe-token"},
		Arguments: map[string]any{"environmentName": "dev"},
	})
	if err != nil || startResult.IsError {
		t.Fatalf("raw start-creatio result = %#v, err = %v", startResult, err)
	}
	if startedEnvironment != "dev" {
		t.Fatalf("start environment = %q", startedEnvironment)
	}
	select {
	case notification := <-notifications:
		if notification.ProgressToken != "start-probe-token" || notification.Message != "mock start complete" {
			t.Fatalf("start progress notification = %#v", notification)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start-creatio did not emit correlated progress")
	}

	missingEnvironment, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "start-creatio"})
	if err != nil || !missingEnvironment.IsError {
		t.Fatalf("start-creatio without required environmentName = %#v, err = %v", missingEnvironment, err)
	}

	badArgs, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "clio-run", Arguments: map[string]any{
			"command": "find-empty-iis-port", "args": map[string]any{"rangeStart": 1},
		},
	})
	if err != nil || !badArgs.IsError {
		t.Fatalf("unknown hidden-tool input must fail: result = %#v, err = %v", badArgs, err)
	}
}

func TestStageTwoReadToolsDispatchByRawName(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.URL.Path == "/0/rest/CreatioApiGateway/GetPackageFilesDirectoryContent" {
			_, _ = w.Write([]byte(`["Files/a.cs"]`))
			return
		}
		if r.URL.Path == "/0/rest/CreatioApiGateway/GetPackageFileContent" {
			if r.URL.Query().Get("filePath") == "UsrPackage.csproj" {
				_, _ = w.Write([]byte(`"<Project />"`))
			} else {
				_, _ = w.Write([]byte(`"class A {}"`))
			}
			return
		}
		if r.URL.Path == "/0/ServiceModel/SqlScriptSchemaDesignerService.svc/GetSchema" {
			_, _ = w.Write([]byte(`{"schema":{"name":"UsrQuery","body":"SELECT 1;","package":{"name":"UsrPackage"}}}`))
			return
		}
		if r.URL.Path != "/0/DataService/json/SyncReply/SelectQuery" {
			t.Errorf("unexpected Creatio route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var query struct {
			RootSchema string `json:"rootSchemaName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode SelectQuery: %v", err)
			return
		}
		switch query.RootSchema {
		case "SysPackage":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Name":"UsrPackage","UId":"pkg-1","Version":"1","Maintainer":"ATF"}]}`))
		case "SysInstalledApp":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"app-1","Name":"Contacts","Code":"Contacts","Version":"1"}]}`))
		case "ApplicationSection":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"section-1","Code":"Contacts","Caption":"Contacts"}]}`))
		case "SysSchema":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Name":"UsrContacts_FormPage","UId":"page-1","PackageName":"UsrPackage","ParentSchemaName":"FormPageTemplate"}]}`))
		case "VwSysSqlScriptInPackage":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"UId":"sql-1"}]}`))
		default:
			t.Errorf("unexpected SelectQuery root %q", query.RootSchema)
			_, _ = w.Write([]byte(`{"success":false,"rows":[]}`))
		}
	}))
	defer creatioServer.Close()
	client, err := creatio.NewClient(creatio.Config{BaseURL: creatioServer.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))

	for _, call := range []*mcp.CallToolParams{
		{Name: "list-package-files", Arguments: map[string]any{"package-name": "UsrPackage"}},
		{Name: "get-package-file", Arguments: map[string]any{"package-name": "UsrPackage", "file-path": "Files/a.cs"}},
		{Name: "get-sql-schema", Arguments: map[string]any{"schema-name": "UsrQuery"}},
		{Name: "list-packages", Arguments: map[string]any{"filter": "usr"}},
		{Name: "list-app-sections", Arguments: map[string]any{"application-code": "Contacts"}},
		{Name: "list-pages", Arguments: map[string]any{"package-name": "UsrPackage"}},
	} {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError || result.StructuredContent == nil {
			t.Fatalf("raw call %q result = %#v, err = %v", call.Name, result, err)
		}
	}
}

func intPointer(value int) *int { return &value }

func TestStructuredToolResultSerializesContentArray(t *testing.T) {
	encoded, err := json.Marshal(structuredToolResult(map[string]any{"ok": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"content":[]`) {
		t.Fatalf("structured result must serialize content as an array: %s", encoded)
	}
}

func TestR2ToolsDispatchRawSelectQueryAndMergedSchemaRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"contact-1"}]}`))
		case "/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest":
			_, _ = w.Write([]byte(`{"success":true,"schema":{"uId":"schema-1","name":"Contact","primaryColumnUId":"column-1","primaryDisplayColumnName":"Name","caption":{"en-US":"Contact"},"columns":{"items":{"id":{"uId":"column-1","name":"Id","caption":{"en-US":"Id"},"dataValueType":0,"isRequired":true,"isInherited":false,"isIndexed":true},"name":{"uId":"column-2","name":"Name","caption":{"en-US":"Full name"},"dataValueType":1,"isRequired":true,"isInherited":false}}}}}`))
		default:
			t.Errorf("unexpected Creatio route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))

	esq, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute-esq", Arguments: map[string]any{
			"query": map[string]any{
				"rootSchemaName": "Contact", "columns": map[string]any{"items": map[string]any{
					"Id": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "Id"}},
				}},
			},
		},
	})
	if err != nil || esq.IsError {
		t.Fatalf("execute-esq result = %#v, err = %v", esq, err)
	}
	esqValue, ok := esq.StructuredContent.(map[string]any)
	if !ok || esqValue["success"] != true || esqValue["count"] != float64(1) {
		t.Fatalf("execute-esq structured content = %#v", esq.StructuredContent)
	}

	schema, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get-entity-schema-properties", Arguments: map[string]any{"schema-name": "Contact"},
	})
	if err != nil || schema.IsError {
		t.Fatalf("get-entity-schema-properties result = %#v, err = %v", schema, err)
	}
	schemaValue, ok := schema.StructuredContent.(map[string]any)
	if !ok || schemaValue["name"] != "Contact" || schemaValue["own-column-count"] != float64(2) {
		t.Fatalf("schema structured content = %#v", schema.StructuredContent)
	}
	if schemaValue["package-name"] != "(merged: all packages)" {
		t.Fatalf("schema package mode = %#v", schemaValue["package-name"])
	}
}

func connectTestClient(t *testing.T, server *mcp.Server, client *mcp.Client) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
		cancel()
	})
	return session
}
