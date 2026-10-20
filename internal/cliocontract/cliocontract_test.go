package cliocontract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedContractsMatchTheInventory pins contracts.json to docs/clio-inventory.json: a hand edit of
// either side, or a new inventory without `go generate`, fails here.
func TestEmbeddedContractsMatchTheInventory(t *testing.T) {
	inventory, err := os.ReadFile(filepath.Join("..", "..", "docs", "clio-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := Extract(inventory)
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may turn the generated file's line ends into CRLF; that is not drift.
	if !bytes.Equal(bytes.ReplaceAll(Embedded(), []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("internal/cliocontract/contracts.json is stale; run `go generate ./internal/cliocontract`")
	}
}

func TestInventoryIsComplete(t *testing.T) {
	inventory := Load()
	if inventory.Clio.Version == "" || len(inventory.ToolsList) == 0 {
		t.Fatalf("inventory has no clio version or no tools/list: %+v", inventory.Clio)
	}
	if inventory.Legend == "" || len(inventory.ToolIndex) != len(inventory.Index) {
		t.Fatalf("inventory has %d tool-index lines for %d index entries, legend %q", len(inventory.ToolIndex), len(inventory.Index), inventory.Legend)
	}
	for position, entry := range inventory.Index {
		name := EntryName(entry)
		if name == "" {
			t.Fatalf("index entry without a name: %s", entry)
		}
		if line := inventory.ToolIndex[position]; LineName(line) != name {
			t.Errorf("tool-index line %d is %q, but index entry %d is %q", position, line, position, name)
		}
		if _, ok := inventory.Contracts[name]; !ok {
			t.Errorf("index names %q, but the inventory has no contract for it", name)
		}
		var short struct {
			Detail            string `json:"detail"`
			FullContractBytes int    `json:"full-contract-bytes"`
		}
		if err := json.Unmarshal(inventory.ShortContracts[name], &short); err != nil || short.Detail != "short" || short.FullContractBytes <= 0 {
			t.Errorf("%s: no short contract with detail and full-contract-bytes: %s", name, inventory.ShortContracts[name])
		}
	}
	for _, name := range inventory.DefaultShort {
		if _, ok := inventory.ShortContracts[name]; !ok {
			t.Errorf("defaultShort names %q, but the inventory has no short contract for it", name)
		}
	}
	for _, tool := range inventory.ToolsList {
		if _, ok := inventory.Contracts[tool.Name]; !ok {
			t.Errorf("tools/list names %q, but the inventory has no contract for it", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Errorf("%s: input schema is not an object schema: %s", tool.Name, tool.InputSchema)
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint == nil || tool.Annotations.DestructiveHint == nil {
			t.Errorf("%s: clio always annotates its tools", tool.Name)
		}
	}
	for _, name := range inventory.FullDetail {
		if _, ok := inventory.Contracts[name]; !ok {
			t.Errorf("detail=full names %q, but the inventory has no contract for it", name)
		}
	}
}
