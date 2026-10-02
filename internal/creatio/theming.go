package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// themingServiceMinVersion is clio's ThemeServiceRequirement.MinVersion: the Creatio core that ships the
// native ThemeService the theme write commands need. check-theming-access reports it on every success.
const themingServiceMinVersion = "10.0.0"

const (
	themingCanExecuteRoute     = "rest/RightsService/GetCanExecuteOperation"
	themingLicenseStatusRoute  = "ServiceModel/LicenseService.svc/GetLicOperationStatuses"
	themingManageOperation     = "CanManageThemes"
	themingBrandingLicenseCode = "CanCustomizeBranding"
)

// ThemingAccessResult is clio's check-theming-access envelope: the two verdicts and the ThemeService floor
// on success, only error on failure.
type ThemingAccessResult struct {
	Success                bool   `json:"success"`
	CanManageThemes        *bool  `json:"canManageThemes,omitempty"`
	CanCustomizeBranding   *bool  `json:"canCustomizeBranding,omitempty"`
	ThemeServiceMinVersion string `json:"themeServiceMinVersion,omitempty"`
	Error                  string `json:"error,omitempty"`
}

// ThemingAccessFailure is clio's ThemingAccessResult.Failure: a blank message reads "unknown".
func ThemingAccessFailure(message string) ThemingAccessResult {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	return ThemingAccessResult{Error: message}
}

// CheckThemingAccess asks the two generic services clio composes: RightsService for the CanManageThemes
// system operation and LicenseService for the CanCustomizeBranding license. Neither is ThemeService, so the
// answer is available below the 10.0.0 floor it reports.
func (c *Client) CheckThemingAccess(ctx context.Context) ThemingAccessResult {
	canManage, err := c.themingCanExecuteOperation(ctx, themingManageOperation)
	if err != nil {
		return ThemingAccessFailure(themingRedact(err.Error()))
	}
	canBrand, err := c.themingLicenseGranted(ctx, themingBrandingLicenseCode)
	if err != nil {
		return ThemingAccessFailure(themingRedact(err.Error()))
	}
	return ThemingAccessResult{Success: true, CanManageThemes: &canManage, CanCustomizeBranding: &canBrand,
		ThemeServiceMinVersion: themingServiceMinVersion}
}

// themingCanExecuteOperation is clio's CreatioRightsClient.GetCanExecuteOperation: a missing or null
// result reads as false.
func (c *Client) themingCanExecuteOperation(ctx context.Context, operation string) (bool, error) {
	body, _ := json.Marshal(map[string]string{"operation": operation})
	var response struct {
		Result *bool `json:"GetCanExecuteOperationResult"`
	}
	if err := c.themingPostAndDecode(ctx, themingCanExecuteRoute, body, &response); err != nil {
		return false, err
	}
	return response.Result != nil && *response.Result, nil
}

// themingLicenseGranted is clio's CreatioLicenseClient.GetLicenseOperationStatuses for one code: a
// response without success:true, or one that does not list the code, reads as not granted. Codes match
// case-insensitively, as clio's status map does.
func (c *Client) themingLicenseGranted(ctx context.Context, code string) (bool, error) {
	body, _ := json.Marshal(map[string][]string{"licOperationCodes": {code}})
	var response struct {
		Result *struct {
			Success  *bool `json:"success"`
			Statuses []*struct {
				Key   string `json:"Key"`
				Value bool   `json:"Value"`
			} `json:"licOperationStatuses"`
		} `json:"GetLicOperationStatusesResult"`
	}
	if err := c.themingPostAndDecode(ctx, themingLicenseStatusRoute, body, &response); err != nil {
		return false, err
	}
	if response.Result == nil || response.Result.Success == nil || !*response.Result.Success {
		return false, nil
	}
	granted, found := false, false
	for _, status := range response.Result.Statuses {
		// clio fills a dictionary in order, so a repeated code keeps its last value.
		if status != nil && status.Key != "" && strings.EqualFold(status.Key, code) {
			granted, found = status.Value, true
		}
	}
	return found && granted, nil
}

// themingPostAndDecode is clio's CreatioServiceClient.PostAndDeserialize. clio names the full URL in its
// messages; this server names the route so the environment address stays out of the transcript.
func (c *Client) themingPostAndDecode(ctx context.Context, route string, body []byte, target any) error {
	response, err := c.postCreatioJSON(ctx, route, body, 100*time.Second, maxResponseBytes)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(response)) == "" {
		return fmt.Errorf("Empty response from %s.", route)
	}
	if err := json.Unmarshal(response, target); err != nil {
		return fmt.Errorf("Unexpected response from %s: %s", route, sanitizeThemeText(string(response), themeDisplayMaxLength))
	}
	return nil
}

// The patterns below are the address and path rules of clio's SensitiveErrorTextRedactor, which clio runs
// over every theme-tool failure before it reaches the MCP client. clio's credential-pair, e-mail and
// host:port rules are not reproduced; the messages built here never carry those.
var (
	themingURIPattern     = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s"'<>)\\]+`)
	themingJWTPattern     = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	themingBearerPattern  = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;"']+`)
	themingWindowsPattern = regexp.MustCompile(`(?:[A-Za-z]:\\|\\\\)[^\s"'<>|]*`)
	themingPosixPattern   = regexp.MustCompile(`/(?:Users|home|root|var|etc|opt|usr|tmp|private|mnt|srv|Library|Applications|System|app|data|config)(?:/[^\s"'<>:]*)+`)
)

func themingRedact(text string) string {
	text = themingURIPattern.ReplaceAllString(text, "[redacted-uri]")
	text = themingJWTPattern.ReplaceAllString(text, "[redacted]")
	text = themingBearerPattern.ReplaceAllString(text, "[redacted]")
	text = themingWindowsPattern.ReplaceAllString(text, "[redacted-path]")
	return themingPosixPattern.ReplaceAllString(text, "[redacted-path]")
}
