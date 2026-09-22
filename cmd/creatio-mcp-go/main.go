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
	return []string{
		"execute-esq", "find-empty-iis-port", "get-entity-schema-properties", "get-package-file",
		"list-app-sections", "list-package-files", "list-packages", "list-pages", "odata-read", "start-creatio",
	}
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
	case "get-package-file":
		var input struct {
			PackageName string `json:"package-name"`
			FilePath    string `json:"file-path"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode get-package-file arguments: %w", err)
		}
		return structuredToolResult(client.GetPackageFile(ctx, input.PackageName, input.FilePath)), nil
	case "list-package-files":
		var input struct {
			PackageName string `json:"package-name"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode list-package-files arguments: %w", err)
		}
		return structuredToolResult(client.ListPackageFiles(ctx, input.PackageName)), nil
	case "list-packages":
		var input struct {
			Filter string `json:"filter,omitempty"`
			Limit  *int   `json:"limit,omitempty"`
			Offset int    `json:"offset,omitempty"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode list-packages arguments: %w", err)
		}
		result, err := client.ListPackages(ctx, creatio.PackageListRequest{
			Filter: input.Filter, Limit: input.Limit, Offset: input.Offset,
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	case "list-app-sections":
		var input struct {
			ApplicationCode string `json:"application-code"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode list-app-sections arguments: %w", err)
		}
		return structuredToolResult(client.ListAppSections(ctx, input.ApplicationCode)), nil
	case "list-pages":
		var input struct {
			PackageName   string `json:"package-name,omitempty"`
			Code          string `json:"code,omitempty"`
			SearchPattern string `json:"search-pattern,omitempty"`
			Limit         *int   `json:"limit,omitempty"`
			UID           string `json:"uid,omitempty"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode list-pages arguments: %w", err)
		}
		return structuredToolResult(client.ListPages(ctx, creatio.PageListRequest{
			PackageName: input.PackageName, ApplicationCode: input.Code,
			SearchPattern: input.SearchPattern, Limit: input.Limit, UID: input.UID,
		})), nil
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
	case "execute-esq":
		var input struct {
			Query     json.RawMessage `json:"query"`
			TimeoutMS *int            `json:"timeout,omitempty"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode execute-esq arguments: %w", err)
		}
		if len(input.Query) == 0 {
			return nil, errors.New("query is required")
		}
		return structuredToolResult(client.ExecuteESQ(ctx, creatio.ExecuteESQRequest{
			Query: input.Query, TimeoutMS: input.TimeoutMS,
		})), nil
	case "get-entity-schema-properties":
		var input struct {
			SchemaName   string `json:"schema-name"`
			PackageName  string `json:"package-name,omitempty"`
			RequiredOnly bool   `json:"required-only,omitempty"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode get-entity-schema-properties arguments: %w", err)
		}
		if strings.TrimSpace(input.PackageName) != "" {
			return nil, errors.New("package-name reads are not implemented; this probe supports only the merged runtime schema view")
		}
		result, err := client.GetEntitySchemaProperties(ctx, creatio.EntitySchemaPropertiesRequest{
			SchemaName: input.SchemaName, RequiredOnly: input.RequiredOnly,
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
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
	"get-package-file": {
		"name":        "get-package-file",
		"description": "Read one package-relative file and the generated package project file through ClioGate 2.0.0.47 or newer. Paths must be relative and stay inside the package Files directory.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"package-name", "file-path"}, "properties": map[string]any{
			"package-name": map[string]string{"type": "string"},
			"file-path":    map[string]string{"type": "string"},
		}},
	},
	"list-package-files": {
		"name":        "list-package-files",
		"description": "List package-relative files materialized by Creatio through ClioGate 2.0.0.47 or newer.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"package-name"}, "properties": map[string]any{
			"package-name": map[string]string{"type": "string"},
		}},
	},
	"list-packages": {
		"name":        "list-packages",
		"description": "List packages from the single CREATIO_URL configured at process start. Filters by case-insensitive package-name substring, then pages the sorted result.",
		"inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{
				"filter": map[string]string{"type": "string"},
				"limit":  map[string]any{"type": "integer", "minimum": 0, "default": 50},
				"offset": map[string]any{"type": "integer", "minimum": 0, "default": 0},
			},
		},
	},
	"list-app-sections": {
		"name":        "list-app-sections",
		"description": "List sections of an installed application by its code on the single configured Creatio instance.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"application-code"}, "properties": map[string]any{
			"application-code": map[string]string{"type": "string"},
		}},
	},
	"list-pages": {
		"name":        "list-pages",
		"description": "List Freedom UI pages by package, application code, schema-name substring and/or UId. Application code resolves through Creatio's ApplicationPackagesService.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"package-name":   map[string]string{"type": "string"},
			"code":           map[string]string{"type": "string"},
			"search-pattern": map[string]string{"type": "string"},
			"limit":          map[string]any{"type": "integer", "minimum": 0, "default": 50},
			"uid":            map[string]string{"type": "string"},
		}},
	},
	"execute-esq": {
		"name":        "execute-esq",
		"description": "Run a raw Creatio DataService SelectQuery against the single CREATIO_URL configured when the Go process starts. Query is forwarded without translation, including filters, relation paths, ordering and paging. Responses are capped at 200000 UTF-8 bytes.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":   map[string]string{"type": "object", "description": "Raw SelectQuery JSON; include rootSchemaName and any columns, filters, orders, rowCount and rowsOffset."},
				"timeout": map[string]any{"type": "integer", "minimum": 1000, "maximum": 120000, "default": 30000},
			},
		},
	},
	"get-entity-schema-properties": {
		"name":        "get-entity-schema-properties",
		"description": "Read the merged runtime entity-schema metadata from Creatio. This prototype does not implement package-scoped designer reads; target is the single CREATIO_URL configured at process start.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"schema-name"},
			"properties": map[string]any{
				"schema-name":   map[string]string{"type": "string"},
				"required-only": map[string]any{"type": "boolean", "default": false},
			},
		},
	},
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
