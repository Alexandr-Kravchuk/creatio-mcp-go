package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteSchema uses the platform workspace item type, rather than guessing a schema manager.
func (c *Client) DeleteSchema(ctx context.Context, name, workspace string, remote bool) CommandResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return CommandFailure("Schema name cannot be empty.")
	}
	packages := map[string]bool{}
	if !remote {
		if workspace == "" {
			workspace, _ = os.Getwd()
		}
		raw, err := os.ReadFile(filepath.Join(workspace, ".clio", "workspaceSettings.json"))
		if err != nil {
			return CommandFailure("Current directory is not a workspace. Please run this command from a workspace directory.")
		}
		var settings struct {
			Packages []string `json:"Packages"`
		}
		if err = json.Unmarshal(raw, &settings); err != nil {
			return CommandFailure(err.Error())
		}
		for _, p := range settings.Packages {
			packages[strings.ToLower(p)] = true
		}
		if len(packages) == 0 {
			return CommandFailure("The current workspace does not contain any packages.")
		}
	}
	raw, err := c.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/GetWorkspaceItems", Body: []byte{}})
	if err != nil {
		return CommandFailure(err.Error())
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return CommandFailure(err.Error())
	}
	var matches []map[string]any
	for _, item := range response.Items {
		itemName, _ := item["name"].(string)
		pkg, _ := item["packageName"].(string)
		if strings.EqualFold(name, itemName) && (remote || packages[strings.ToLower(pkg)]) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		if remote {
			return CommandFailure(fmt.Sprintf("Schema '%s' not found in the target environment.", name))
		}
		return CommandFailure(fmt.Sprintf("Schema '%s' is not part of the current workspace.", name))
	}
	if !remote && len(matches) > 1 {
		var pkgs []string
		for _, m := range matches {
			p, _ := m["packageName"].(string)
			pkgs = append(pkgs, p)
		}
		return CommandFailure(fmt.Sprintf("Schema '%s' exists in multiple workspace packages: %s. Delete is ambiguous.", name, strings.Join(pkgs, ", ")))
	}
	item := matches[0]
	body, _ := json.Marshal([]map[string]any{item})
	raw, err = c.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/Delete", Body: body})
	if err != nil {
		return CommandFailure(err.Error())
	}
	var outcome struct {
		Success bool `json:"success"`
		Rows    int  `json:"rowsAffected"`
		Error   struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
	}
	if err = json.Unmarshal(raw, &outcome); err != nil {
		return CommandFailure(err.Error())
	}
	pkg, _ := item["packageName"].(string)
	uid, _ := item["uId"].(string)
	if !outcome.Success || outcome.Rows <= 0 {
		message := outcome.Error.Message
		if message == "" {
			message = fmt.Sprintf("Failed to delete schema '%s' from package '%s'.", name, pkg)
		}
		return CommandFailure(message)
	}
	return NewCommandResult(0, "Info", fmt.Sprintf("Deleted schema '%s' (uId=%s) from package '%s'.", name, uid, pkg))
}
