// Command generate writes internal/cliocontract/contracts.json from docs/clio-inventory.json.
//
//	go run ./internal/cliocontract/generate <inventory.json> <contracts.json>
package main

import (
	"fmt"
	"os"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/cliocontract"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: generate <clio-inventory.json> <contracts.json>")
		os.Exit(2)
	}
	inventory, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := cliocontract.Extract(inventory)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
