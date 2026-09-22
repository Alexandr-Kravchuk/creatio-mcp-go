package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
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
	server := mcp.NewServer(&mcp.Implementation{Name: "creatio-mcp-go", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list-apps", Description: "List installed Creatio applications through DataService."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			apps, err := client.ListApps(ctx)
			if err != nil {
				return nil, nil, err
			}
			return nil, apps, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "creatio-mcp-go:", err); os.Exit(1) }
