package creatio

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

// clio caps each theme field the way its theme parameter validator does; cssFilePath uses the default display cap.
const (
	themeIDMaxLength           = 100
	themeCaptionMaxLength      = 250
	themeCSSClassNameMaxLength = 100
	themeDisplayMaxLength      = 500
)

type ThemeListItem struct {
	ID           string `json:"id"`
	Caption      string `json:"caption"`
	CSSClassName string `json:"cssClassName"`
	CSSFilePath  string `json:"cssFilePath"`
}

// ThemeListResult is clio's list-themes envelope: themes is omitted on failure, error on success.
type ThemeListResult struct {
	Success bool            `json:"success"`
	Themes  []ThemeListItem `json:"themes,omitzero"`
	Error   string          `json:"error,omitempty"`
}

// ListThemes reads the custom theme catalog through the native ThemeService. An empty catalog and a caller
// without the CanCustomizeBranding license look the same: success with no themes.
func (c *Client) ListThemes(ctx context.Context) ThemeListResult {
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ThemeService.svc/GetAvailableThemes", []byte("{}"), 45*time.Second, maxResponseBytes)
	if err != nil {
		return ThemeListResult{Error: err.Error()}
	}
	themes := []ThemeListItem{}
	// ThemeService answers with a BaseResponse; only a genuinely empty body counts as success without a payload.
	if strings.TrimSpace(string(response)) == "" {
		return ThemeListResult{Success: true, Themes: themes}
	}
	var decoded struct {
		Success   *bool `json:"success"`
		ErrorInfo *struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
		Values []*struct {
			ID           string `json:"id"`
			Caption      string `json:"caption"`
			CSSClassName string `json:"cssClassName"`
			CSSFilePath  string `json:"cssFilePath"`
		} `json:"values"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return ThemeListResult{Error: "Unexpected response from server: " + sanitizeThemeText(string(response), themeDisplayMaxLength)}
	}
	if decoded.Success != nil && !*decoded.Success {
		message := ""
		if decoded.ErrorInfo != nil {
			message = sanitizeThemeText(decoded.ErrorInfo.Message, themeDisplayMaxLength)
		}
		if strings.TrimSpace(message) == "" {
			message = "GetAvailableThemes returned success=false."
		}
		return ThemeListResult{Error: message}
	}
	for _, theme := range decoded.Values {
		if theme == nil {
			continue
		}
		themes = append(themes, ThemeListItem{
			ID:           sanitizeThemeText(theme.ID, themeIDMaxLength),
			Caption:      sanitizeThemeText(theme.Caption, themeCaptionMaxLength),
			CSSClassName: sanitizeThemeText(theme.CSSClassName, themeCSSClassNameMaxLength),
			CSSFilePath:  sanitizeThemeText(theme.CSSFilePath, themeDisplayMaxLength),
		})
	}
	return ThemeListResult{Success: true, Themes: themes}
}

// sanitizeThemeText mirrors clio's TextUtilities.SanitizeForDisplay: control, format and line/paragraph
// separator characters become spaces, and text longer than maxLength UTF-16 units is cut, without
// splitting a surrogate pair, and suffixed with "...".
func sanitizeThemeText(text string, maxLength int) string {
	if text == "" {
		return text
	}
	if maxLength <= 0 {
		return "..."
	}
	var builder strings.Builder
	units := 0
	cutAt := -1
	for _, r := range text {
		if units > maxLength {
			break
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			r = ' '
		}
		width := utf16.RuneLen(r)
		if width < 0 {
			width = 1
		}
		if cutAt < 0 && units+width > maxLength {
			cutAt = builder.Len()
		}
		builder.WriteRune(r)
		units += width
	}
	if units <= maxLength {
		return builder.String()
	}
	return builder.String()[:cutAt] + "..."
}
