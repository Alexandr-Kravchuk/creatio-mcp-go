package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

// knowledgeProbeUpdate answers info-knowledge checkUpdates=true the way clio's ProbeArtifactUpdate does: one
// call to the source's GitHub-release or NuGet transport, the candidate verified but never published.
func knowledgeProbeUpdate(_ *knowledge.Runtime, alias string, source knowledge.SourceConfig, current *knowledge.CurrentState) (string, string) {
	return knowledgeManager().ProbeUpdate(context.Background(), alias, source, current)
}
