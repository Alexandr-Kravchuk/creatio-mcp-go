package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type AppDeleteResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// DeleteApp ports UninstallAppCommand: resolve a name/code (or use a GUID), then pass the ID as a JSON string.
func (c *Client) DeleteApp(ctx context.Context, name string) AppDeleteResponse {
	if strings.TrimSpace(name) == "" {
		return AppDeleteResponse{Error: "app-name is required. Provide the application name or code."}
	}
	id := name
	if !isGUIDText(name) {
		apps, err := c.installedApps(ctx)
		if err != nil {
			return AppDeleteResponse{Error: err.Error()}
		}
		id = ""
		for _, app := range apps {
			if strings.EqualFold(app.Name, name) || strings.EqualFold(app.Code, name) {
				id = app.ID
				break
			}
		}
		if id == "" {
			return AppDeleteResponse{Error: fmt.Sprintf("Application with name '%s' not found.", name)}
		}
	}
	body, _ := json.Marshal(id)
	_, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AppInstallerService.svc/UninstallApp", Body: body})
	if err != nil {
		return AppDeleteResponse{Error: err.Error()}
	}
	return AppDeleteResponse{Success: true}
}
