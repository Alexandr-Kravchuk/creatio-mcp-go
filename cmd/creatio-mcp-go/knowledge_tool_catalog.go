package main

import "github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/cliocontract"

// knowledgeClioToolCatalog is the tool set clio offers a knowledge bundle (BindingsModule: the invoker
// registry's ToolNames, the names of clio's get-tool-contract index). This server declares it as its
// capability set because it is the catalog the migration targets (docs/migration-plan.md, T6): a bundle
// that requires a tool outside it is refused with MissingCapability, exactly as clio refuses one. It is
// read from the recorded clio inventory (internal/cliocontract), so it follows the clio baseline.
var knowledgeClioToolCatalog = clioIndexToolNames()

func clioIndexToolNames() []string {
	index := cliocontract.Load().Index
	names := make([]string, 0, len(index))
	for _, entry := range index {
		names = append(names, cliocontract.EntryName(entry))
	}
	return names
}
