package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	listJSON := flag.Bool("list-apps-json", false, "write list-apps-compatible JSON to stdout and exit")
	flag.Parse()
	config, err := creatio.LoadConfig()
	if err != nil { fatal(err) }
	client, err := creatio.NewClient(config)
	if err != nil { fatal(err) }
	if *listJSON {
		apps, err := client.ListApps(context.Background())
		if err != nil { fatal(err) }
		if err := json.NewEncoder(os.Stdout).Encode(apps); err != nil { fatal(fmt.Errorf("write JSON: %w", err)) }
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "creatio-mcp-go", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list-apps", Description: "List installed Creatio applications through DataService."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			apps, err := client.ListApps(ctx)
			if err != nil { return nil, nil, err }
			return nil, apps, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil { fatal(err) }
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "creatio-mcp-go:", err); os.Exit(1) }
