package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type UserThemeResult struct {
	Success      bool    `json:"success"`
	Caption      *string `json:"caption,omitempty"`
	CSSClassName *string `json:"css-class-name,omitempty"`
	ID           *string `json:"id,omitempty"`
	Error        string  `json:"error,omitempty"`
}

func UserThemeFailure(message string) UserThemeResult { return UserThemeResult{Error: message} }
func (c *Client) SetUserTheme(ctx context.Context, selector string, reset bool) UserThemeResult {
	target := ThemeListItem{}
	if !reset {
		catalog := c.ListThemes(ctx)
		if !catalog.Success {
			return UserThemeFailure(catalog.Error)
		}
		selector = strings.TrimSpace(selector)
		found := false
		for _, tier := range []string{"id", "css-class-name", "caption"} {
			matches := []ThemeListItem{}
			for _, item := range catalog.Themes {
				field := item.ID
				if tier == "css-class-name" {
					field = item.CSSClassName
				} else if tier == "caption" {
					field = item.Caption
				}
				if strings.EqualFold(field, selector) {
					matches = append(matches, item)
				}
			}
			if len(matches) > 1 {
				names := []string{}
				for _, item := range matches {
					names = append(names, fmt.Sprintf("'%s' (id '%s', cssClassName '%s')", item.Caption, item.ID, item.CSSClassName))
				}
				return UserThemeFailure(fmt.Sprintf("Theme '%s' matches more than one theme by %s: %s. Specify the theme by its unique id instead.", selector, tier, strings.Join(names, "; ")))
			}
			if len(matches) == 1 {
				target = matches[0]
				found = true
				break
			}
		}
		if !found {
			names := []string{}
			for _, item := range catalog.Themes {
				if strings.TrimSpace(item.ID) != "" {
					names = append(names, fmt.Sprintf("'%s' (id '%s', cssClassName '%s')", item.Caption, item.ID, item.CSSClassName))
				}
			}
			if len(catalog.Themes) == 0 {
				return UserThemeFailure(fmt.Sprintf("Theme '%s' was not found and no custom themes are listed on this environment. %s Otherwise create a theme with create-theme, or use --reset to restore the environment default.", selector, "This can also mean the CanCustomizeBranding license is missing (list-themes returns an empty catalog in that case) — verify access with check-theming-access."))
			}
			return UserThemeFailure(fmt.Sprintf("Theme '%s' was not found. Available themes: %s. Use --reset to restore the environment default.", selector, strings.Join(names, "; ")))
		}
		if strings.TrimSpace(target.ID) == "" {
			return UserThemeFailure(fmt.Sprintf("Theme '%s' has no id and cannot be applied.", selector))
		}
		if strings.TrimSpace(target.Caption) == "" {
			target.Caption = target.ID
		}
	}
	rows, err := c.selectRows(ctx, buildSelectQuery("SysUserProfile", map[string]string{"Id": "Id"}, nil, 1))
	if err != nil {
		return UserThemeFailure("Failed to read the current user's profile: " + err.Error())
	}
	if len(rows) == 0 || rowString(rows[0], "Id") == "" {
		return UserThemeFailure("The current user's profile could not be resolved (SysUserProfile returned no row).")
	}
	expression := func(value string) map[string]any {
		return map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": 1, "value": value}}
	}
	body, _ := json.Marshal(map[string]any{"__type": "Terrasoft.Nui.ServiceModel.DataContract.UpdateQuery", "operationType": 2, "rootSchemaName": "SysUserProfile", "isForceUpdate": false, "queryKind": 0, "columnValues": map[string]any{"items": map[string]any{"Theme": expression(target.ID)}}, "filters": map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false, "logicalOperation": 0, "items": map[string]any{"primaryFilter": map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true, "trimDateTimeParameterToDate": false, "leftExpression": map[string]any{"expressionType": 0, "columnPath": "Id"}, "rightExpression": expression(rowString(rows[0], "Id"))}}}})
	response, err := c.postDataServiceJSON(ctx, "UpdateQuery", body, 100*time.Second, maxResponseBytes)
	if err != nil {
		return UserThemeFailure("Failed to apply the theme: " + err.Error())
	}
	var status struct {
		Success   bool `json:"success"`
		ErrorInfo struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal(response, &status); err != nil {
		return UserThemeFailure("Could not parse the UpdateQuery response: " + err.Error())
	}
	if !status.Success {
		message := status.ErrorInfo.Message
		if message == "" {
			message = "UpdateQuery returned success=false."
		}
		if strings.Contains(strings.ToLower(message), "cancustomizebranding") {
			message += " — the CanCustomizeBranding license is required to change the theme."
		} else if strings.Contains(strings.ToLower(message), "canchangeowntheme") {
			message += " — the CanChangeOwnTheme system operation is required to change the theme."
		}
		return UserThemeFailure(message)
	}
	rows, err = c.selectRows(ctx, buildSelectQuery("SysUserProfile", map[string]string{"Theme": "Theme"}, nil, 1))
	if err != nil {
		return UserThemeFailure("Failed to verify the applied theme: " + err.Error())
	}
	actual := ""
	if len(rows) > 0 {
		actual = rowString(rows[0], "Theme")
	}
	if !strings.EqualFold(actual, target.ID) {
		if reset {
			return UserThemeFailure("The profile theme was not cleared. Ensure the 'ChangeTheme' feature is enabled on the environment.")
		}
		return UserThemeFailure(fmt.Sprintf("The profile theme was not applied (still '%s'). Ensure the 'ChangeTheme' feature is enabled on the environment and that the CanCustomizeBranding license and CanChangeOwnTheme operation are granted.", actual))
	}
	return UserThemeResult{Success: true, Caption: &target.Caption, CSSClassName: &target.CSSClassName, ID: &target.ID}
}
