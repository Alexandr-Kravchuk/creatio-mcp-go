package main

import (
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

// knowledgeProbeUpdate answers info-knowledge checkUpdates=true. Contacting a publisher is not implemented
// in this server yet, so availability stays "unknown" with a diagnostic saying why; clio would download and
// verify the latest release here.
func knowledgeProbeUpdate(_ *knowledge.Runtime, _ string, _ knowledge.SourceConfig, _ *knowledge.CurrentState) (string, string) {
	return "unknown", "Update checks are not implemented by creatio-mcp-go; run `clio info-knowledge --check-updates`."
}
