package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/hosttools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildID is stamped at link time with -ldflags "-X main.buildID=...". It exists so a replacement
// experiment can prove WHICH build answered: a resident process and a freshly launched one can be
// asked separately and compared.
var buildID = "unstamped"

func main() {
	listJSON := flag.Bool("list-apps-json", false, "write list-apps-compatible JSON to stdout and exit")
	version := flag.Bool("version", false, "print this build's identity and exit")
	resident := flag.Bool("resident", false, "print this build's identity, then stay alive until killed")
	writeProbe := flag.Bool("write-probe", false, "run the write-truthfulness probe and print JSON outcomes")
	flag.Parse()
	if *version {
		fmt.Println(buildID)
		return
	}
	if *resident {
		// Deliberately no Creatio connection: this mode exists only to hold the executable open while
		// something tries to overwrite it on disk.
		fmt.Println(buildID)
		os.Stdout.Sync()
		// NOT select{}: the Go runtime detects that every goroutine is asleep and panics with
		// "all goroutines are asleep - deadlock!", so the process dies immediately and the experiment
		// then measures an overwrite that nothing was holding. Sleep keeps the image mapped for real.
		time.Sleep(30 * time.Minute)
		return
	}
	config, err := creatio.LoadConfig()
	if err != nil {
		fatal(err)
	}
	client, err := creatio.NewClient(config)
	if err != nil {
		fatal(err)
	}
	if *writeProbe {
		runWriteProbe(client)
		return
	}
	if *listJSON {
		apps, err := client.ListApps(context.Background())
		if err != nil {
			fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(apps); err != nil {
			fatal(fmt.Errorf("write JSON: %w", err))
		}
		return
	}
	server := newMCPServer(client)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fatal(err)
	}
}

func newMCPServer(client *creatio.Client) *mcp.Server {
	return newMCPServerWithHiddenTools(client, defaultHiddenToolServices())
}

type hiddenToolServices struct {
	findEmptyIISPort func(context.Context) hosttools.PortDiscoveryResult
	startCreatio     func(context.Context, string, func(float64, float64, string) error) (hosttools.StartResult, error)
}

func defaultHiddenToolServices() hiddenToolServices {
	return hiddenToolServices{
		findEmptyIISPort: func(ctx context.Context) hosttools.PortDiscoveryResult {
			return hosttools.FindEmptyIISPort(ctx, nil)
		},
		startCreatio: func(ctx context.Context, environment string, progress func(float64, float64, string) error) (hosttools.StartResult, error) {
			return hosttools.StartCreatio(ctx, hosttools.StartOptions{EnvironmentName: environment, Progress: progress})
		},
	}
}

func newMCPServerWithHiddenTools(client *creatio.Client, hostTools hiddenToolServices) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "creatio-mcp-go", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list-apps", Description: "List installed Creatio applications through DataService."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			apps, err := client.ListApps(ctx)
			if err != nil {
				return nil, nil, err
			}
			return nil, apps, nil
		})
	// Hidden tools are deliberately omitted from tools/list. They remain directly callable by raw
	// name and through clio-run, while get-tool-contract provides their schemas on demand.
	mcp.AddTool(server, &mcp.Tool{Name: "clio-run", Description: "Invoke a supported Creatio MCP tool by name."},
		func(ctx context.Context, req *mcp.CallToolRequest, input clioRunArgs) (*mcp.CallToolResult, any, error) {
			result, err := invokeHiddenTool(ctx, client, hostTools, strings.TrimSpace(input.Command), input.Args,
				progressReporter(ctx, req.Session, req.Params.GetProgressToken()))
			if err != nil {
				return nil, nil, err
			}
			return result, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "get-tool-contract", Description: "List supported hidden tools or retrieve one tool's input schema."},
		func(_ context.Context, _ *mcp.CallToolRequest, input getToolContractArgs) (*mcp.CallToolResult, any, error) {
			if input.Name == "" {
				return nil, map[string]any{"tools": hiddenToolNames()}, nil
			}
			contract, ok := hiddenToolContracts[input.Name]
			if !ok {
				return nil, nil, fmt.Errorf("unknown tool contract %q", input.Name)
			}
			return nil, contract, nil
		})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			call, ok := request.(*mcp.ServerRequest[*mcp.CallToolParamsRaw])
			if !ok || call.Params == nil || !isHiddenTool(call.Params.Name) {
				return next(ctx, method, request)
			}
			var args map[string]any
			if len(call.Params.Arguments) > 0 {
				if err := json.Unmarshal(call.Params.Arguments, &args); err != nil {
					return toolError(err), nil
				}
			}
			result, err := invokeHiddenTool(ctx, client, hostTools, call.Params.Name, args,
				progressReporter(ctx, call.Session, call.Params.GetProgressToken()))
			if err != nil {
				return toolError(err), nil
			}
			return result, nil
		}
	})
	return server
}

func hiddenToolNames() []string {
	return []string{"find-empty-iis-port", "odata-read", "start-creatio"}
}

func isHiddenTool(name string) bool {
	_, ok := hiddenToolContracts[name]
	return ok
}

func progressReporter(ctx context.Context, session *mcp.ServerSession, token any) func(float64, float64, string) error {
	if token == nil || session == nil {
		return nil
	}
	return func(progress, total float64, message string) error {
		return session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Progress: progress, Total: total, Message: message,
		})
	}
}

func invokeHiddenTool(ctx context.Context, client *creatio.Client, hostTools hiddenToolServices, name string, args map[string]any,
	progress func(float64, float64, string) error) (*mcp.CallToolResult, error) {
	switch name {
	case "odata-read":
		result, err := invokeODataRead(ctx, client, args)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	case "find-empty-iis-port":
		var input struct{}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode find-empty-iis-port arguments: %w", err)
		}
		return structuredToolResult(hostTools.findEmptyIISPort(ctx)), nil
	case "start-creatio":
		var input struct {
			EnvironmentName string `json:"environmentName"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode start-creatio arguments: %w", err)
		}
		if strings.TrimSpace(input.EnvironmentName) == "" {
			return nil, errors.New("environmentName is required")
		}
		result, err := hostTools.startCreatio(ctx, input.EnvironmentName, progress)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	default:
		return nil, fmt.Errorf("unknown tool %q; discover supported names with get-tool-contract", name)
	}
}

func structuredToolResult(value any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{}, StructuredContent: value}
}

func decodeStrictArgs(args map[string]any, target any) error {
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

type clioRunArgs struct {
	Command string         `json:"command"`
	Args    map[string]any `json:"args"`
}

type getToolContractArgs struct {
	Name string `json:"name,omitempty"`
}

var odataReadContract = map[string]any{
	"name":        "odata-read",
	"description": "Read an OData v4 collection. Supports entity, select, orderBy, top, skip and count.",
	"inputSchema": map[string]any{
		"type":     "object",
		"required": []string{"entity"},
		"properties": map[string]any{
			"entity":  map[string]string{"type": "string"},
			"select":  map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
			"orderBy": map[string]string{"type": "string"},
			"top":     map[string]string{"type": "integer"},
			"skip":    map[string]string{"type": "integer"},
			"count":   map[string]string{"type": "boolean"},
		},
	},
}

var hiddenToolContracts = map[string]map[string]any{
	"odata-read": odataReadContract,
	"find-empty-iis-port": {
		"name":        "find-empty-iis-port",
		"description": "Find the first free port in the default IIS deployment range. Windows only; reads IIS bindings and active TCP endpoints.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	"start-creatio": {
		"name":        "start-creatio",
		"description": "Start a registered local Creatio environment through IIS on Windows or dotnet on other platforms. This changes local process/server state.",
		"inputSchema": map[string]any{
			"type":       "object",
			"required":   []string{"environmentName"},
			"properties": map[string]any{"environmentName": map[string]string{"type": "string", "description": "Target registered Clio environment name."}},
		},
	},
}

func invokeODataRead(ctx context.Context, client *creatio.Client, args map[string]any) (creatio.ODataReadResult, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return creatio.ODataReadResult{}, fmt.Errorf("encode odata-read arguments: %w", err)
	}
	var input creatio.ODataReadRequest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return creatio.ODataReadResult{}, fmt.Errorf("decode odata-read arguments: %w", err)
	}
	return client.ODataRead(ctx, input)
}

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "creatio-mcp-go:", err); os.Exit(1) }

// runWriteProbe exercises two writes: one that should succeed and is cleaned up, and one that must be
// REFUSED. The refusal is the point — the vendor path can report a refused operation as an empty
// success, and this probe records exactly what is reported instead.
func runWriteProbe(client *creatio.Client) {
	ctx := context.Background()
	results := map[string]any{}

	name := "creatio-mcp-go probe " + time.Now().UTC().Format("20060102T150405Z")
	inserted, err := client.Insert(ctx, "Contact", map[string]any{"Name": name})
	results["insertShouldSucceed"] = inserted
	if err != nil {
		results["insertError"] = err.Error()
	}

	if inserted.Succeeded && inserted.RecordID != "" {
		deleted, err := client.Delete(ctx, "Contact", inserted.RecordID)
		results["deleteCleanup"] = deleted
		if err != nil {
			results["deleteError"] = err.Error()
		}
	}

	// Two different refusals, because they exercise different server behaviour:
	// (a) a schema-level error, which arrives as HTTP 500 with a body;
	// (b) a restricted schema, which is the case that can arrive as HTTP 200 with success:false —
	//     the shape the vendor provider turns into an empty success.
	refused, err := client.Insert(ctx, "Contact", map[string]any{"ThisColumnDoesNotExist": "x"})
	results["refusalBadColumn"] = refused
	if err != nil {
		results["refusalBadColumnError"] = err.Error()
	}
	restricted, err := client.Insert(ctx, "SysSchema", map[string]any{"Name": "probe"})
	results["refusalRestrictedSchema"] = restricted
	if err != nil {
		results["refusalRestrictedError"] = err.Error()
	}

	// A refusal is loud when the client did not claim success AND classified the failure AND kept the
	// server's own words. Requiring one specific class was too narrow: the server refuses in more than
	// one way, and every way must stay loud.
	loud := func(o creatio.WriteOutcome) bool {
		return !o.Succeeded && o.FailureClass != "" && o.FailureDetail != ""
	}
	results["bothRefusalsLoud"] = loud(refused) && loud(restricted)
	results["neitherReportedAsEmptySuccess"] = !refused.Succeeded && !restricted.Succeeded

	_ = json.NewEncoder(os.Stdout).Encode(results)
}
