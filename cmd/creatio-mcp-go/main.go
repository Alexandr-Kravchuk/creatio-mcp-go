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
	server := mcp.NewServer(&mcp.Implementation{Name: "creatio-mcp-go", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list-apps", Description: "List installed Creatio applications through DataService."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			apps, err := client.ListApps(ctx)
			if err != nil {
				return nil, nil, err
			}
			return nil, apps, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "odata-read", Description: "Read a bounded OData v4 collection without a vendor .NET client."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input creatio.ODataReadRequest) (*mcp.CallToolResult, any, error) {
			result, err := client.ODataRead(ctx, input)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fatal(err)
	}
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
