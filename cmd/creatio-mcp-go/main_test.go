package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
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
