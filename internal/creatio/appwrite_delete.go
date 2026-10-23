package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// appWriteUninstallTimeout is RemoteCommandOptions' default TimeOut (100 000 ms), which uninstall-app-remote keeps.
// An uninstall can take a minute on a loaded site (live run T24: 47.8-63.9 s).
const appWriteUninstallTimeout = 100 * time.Second

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
	// RemoteCommand posts with MaxAttempts and RetryDelay, as the restart request does.
	var err error
	for attempt := 1; attempt <= pkgWriteRestartAttempts; attempt++ {
		call := serviceCall{Route: "ServiceModel/AppInstallerService.svc/UninstallApp", Body: body, Timeout: appWriteUninstallTimeout}
		if _, err = c.callService(ctx, call); err == nil {
			return AppDeleteResponse{Success: true}
		}
		if attempt == pkgWriteRestartAttempts || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(pkgWriteRestartRetryDelay):
		}
	}
	return AppDeleteResponse{Error: err.Error()}
}
