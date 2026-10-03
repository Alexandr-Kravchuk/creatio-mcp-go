package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// merge-creatio-artifact: clio's resident CreatioArtifactMergeTool. Its argument record disallows unknown
// members (environment-name included), which clio reports as a binding failure of 'args'.
func init() {
	registerTool(map[string]any{"name": "merge-creatio-artifact"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var request creatio.ArtifactMergeRequest
		fields := map[string]*string{"artifact-path": &request.ArtifactPath, "base-content": &request.BaseContent,
			"ours-content": &request.OursContent, "theirs-content": &request.TheirsContent}
		for key, value := range args {
			if key == "descriptor-content" {
				if value == nil {
					continue
				}
				text, ok := value.(string)
				if !ok {
					return nil, mergeArgumentError(key)
				}
				request.DescriptorContent = &text
				continue
			}
			target, known := fields[key]
			if !known {
				return nil, fmt.Errorf("invalid-parameter-type: argument 'args' for MCP tool 'merge-creatio-artifact' must be an object. Received an incompatible JSON value.")
			}
			if value == nil {
				continue
			}
			text, ok := value.(string)
			if !ok {
				return nil, mergeArgumentError(key)
			}
			*target = text
		}
		return structuredToolResult(creatio.MergeCreatioArtifact(request, "creatio-mcp-go "+serverVersion())), nil
	})
}

func mergeArgumentError(name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool 'merge-creatio-artifact' must be a string. Received an incompatible JSON value.", name)
}
