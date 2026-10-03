// Package cliocontract holds clio's MCP tool contracts as clio itself answers them: the resident
// tools/list entries (description, input schema, annotations), the get-tool-contract index and every
// tool's full get-tool-contract answer.
//
// The source of truth is docs/clio-inventory.json, written by scripts/clio-inventory.py from a live
// `clio mcp-server`. `go generate ./internal/cliocontract` copies the part this server serves into
// contracts.json, which is embedded; a test fails when the two drift apart. To follow a new clio:
// rerun the inventory script, then go generate, then go test.
package cliocontract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:generate go run ./generate ../../docs/clio-inventory.json contracts.json

//go:embed contracts.json
var embedded []byte

// Inventory is the generated subset of docs/clio-inventory.json.
type Inventory struct {
	// Clio names the clio build the data was read from.
	Clio struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clio"`
	// ToolsList is clio's tools/list, in clio's order.
	ToolsList []Tool `json:"toolsList"`
	// Index is the get-tool-contract index, entry by entry as clio wrote it.
	Index []json.RawMessage `json:"index"`
	// FullDetail names the contracts get-tool-contract answers for detail=full, in clio's order.
	FullDetail []string `json:"fullDetail"`
	// Contracts is every tool's full contract, keyed by tool name.
	Contracts map[string]json.RawMessage `json:"contracts"`
}

// Tool is one tools/list entry.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *Annotations    `json:"annotations,omitempty"`
}

// Annotations are clio's tool hints; clio always writes all four.
type Annotations struct {
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
}

// IndexEntry is the part of an index entry this server reads; the entry itself is served verbatim.
type IndexEntry struct {
	Name string `json:"name"`
}

// Extract builds the embedded file from docs/clio-inventory.json: the tool data only, without prompts and
// resources, indented so a clio upgrade shows up as a readable diff.
func Extract(inventory []byte) ([]byte, error) {
	var subset Inventory
	if err := json.Unmarshal(inventory, &subset); err != nil {
		return nil, fmt.Errorf("read clio inventory: %w", err)
	}
	if len(subset.ToolsList) == 0 || len(subset.Index) == 0 || len(subset.Contracts) == 0 {
		return nil, fmt.Errorf("clio inventory has no tools; rerun scripts/clio-inventory.py")
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", " ")
	if err := encoder.Encode(subset); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var (
	loadOnce sync.Once
	loaded   *Inventory
)

// Load returns the embedded inventory. The data is generated and pinned by tests, so a parse failure is
// a build defect and panics.
func Load() *Inventory {
	loadOnce.Do(func() {
		var inventory Inventory
		if err := json.Unmarshal(embedded, &inventory); err != nil {
			panic(fmt.Sprintf("cliocontract: embedded contracts.json: %v", err))
		}
		loaded = &inventory
	})
	return loaded
}

// Embedded returns the embedded file as it is, for the drift test.
func Embedded() []byte { return embedded }

// EntryName reads the tool name of one index entry.
func EntryName(entry json.RawMessage) string {
	var parsed IndexEntry
	_ = json.Unmarshal(entry, &parsed)
	return parsed.Name
}
