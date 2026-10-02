package creatio

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// FindAppRequest selects installed applications by an optional case-insensitive substring and an optional
// exact code. Both empty returns every application.
type FindAppRequest struct {
	SearchPattern string
	Code          string
}

// AppSearchResult is one installed application with all of its sections, in clio's find-app shape.
type AppSearchResult struct {
	ID          string                   `json:"id"`
	Code        string                   `json:"code"`
	Name        string                   `json:"name"`
	Version     *string                  `json:"version,omitempty"`
	Description *string                  `json:"description,omitempty"`
	Sections    []AppSectionSearchResult `json:"sections"`
}

// AppSectionSearchResult is one section of an AppSearchResult.
type AppSectionSearchResult struct {
	Code             string  `json:"code"`
	Caption          string  `json:"caption"`
	EntitySchemaName *string `json:"entity-schema-name,omitempty"`
	Description      *string `json:"description,omitempty"`
}

// FindAppResponse is clio's find-app envelope: success plus applications, or success=false plus error.
type FindAppResponse struct {
	Success      bool              `json:"success"`
	Applications []AppSearchResult `json:"applications,omitzero"` // [] on an empty success, absent on failure
	Error        string            `json:"error,omitempty"`
}

const findAppRowCount = 10000

// FindApp mirrors clio's FindAppCommand: one SysInstalledApp query, then one ApplicationSection query that
// OR-groups every candidate application id. The code filter runs before sections are loaded; the pattern is
// matched afterwards because it also covers section captions and codes. A failed section query still returns
// the applications, only without sections, as clio does.
func (c *Client) FindApp(ctx context.Context, input FindAppRequest) FindAppResponse {
	pattern := strings.TrimSpace(input.SearchPattern)
	code := strings.TrimSpace(input.Code)
	appRows, err := c.selectRows(ctx, buildSelectQuery("SysInstalledApp", map[string]string{
		"Id": "Id", "Code": "Code", "Name": "Name", "Description": "Description", "Version": "Version",
	}, nil, findAppRowCount))
	if err != nil {
		return FindAppResponse{Error: err.Error()}
	}
	candidates := appRows[:0:0]
	for _, row := range appRows {
		if code == "" || strings.EqualFold(rowString(row, "Code"), code) {
			candidates = append(candidates, row)
		}
	}
	ids := make([]string, 0, len(candidates))
	for _, row := range candidates {
		if id := rowString(row, "Id"); strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sectionsByApp := c.findAppSections(ctx, ids)
	results := make([]AppSearchResult, 0, len(candidates))
	for _, row := range candidates {
		id := rowString(row, "Id")
		sections := sectionsByApp[strings.ToLower(id)]
		if sections == nil {
			sections = []AppSectionSearchResult{}
		}
		app := AppSearchResult{
			ID: id, Code: rowString(row, "Code"), Name: rowString(row, "Name"),
			Version: nonBlankOrNil(rowString(row, "Version")), Description: nonBlankOrNil(rowString(row, "Description")),
			Sections: sections,
		}
		if appMatchesPattern(app, pattern) {
			results = append(results, app)
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if order := compareOrdinalIgnoreCase(results[i].Name, results[j].Name); order != 0 {
			return order < 0
		}
		return compareOrdinalIgnoreCase(results[i].Code, results[j].Code) < 0
	})
	return FindAppResponse{Success: true, Applications: results}
}

// findAppSections loads the sections of every candidate application in one request, keyed by lower-cased
// application id. Any failure yields no sections at all rather than an error, matching clio.
func (c *Client) findAppSections(ctx context.Context, appIDs []string) map[string][]AppSectionSearchResult {
	grouped := map[string][]AppSectionSearchResult{}
	if len(appIDs) == 0 {
		return grouped
	}
	rows, err := c.selectRows(ctx, selectQueryWithOrFilter("ApplicationSection", map[string]string{
		"Id": "Id", "ApplicationId": "ApplicationId", "Caption": "Caption", "Code": "Code",
		"Description": "Description", "EntitySchemaName": "EntitySchemaName",
	}, "ApplicationId", appIDs, 0, findAppRowCount))
	if err != nil {
		return map[string][]AppSectionSearchResult{}
	}
	for _, row := range rows {
		key := strings.ToLower(rowString(row, "ApplicationId"))
		grouped[key] = append(grouped[key], AppSectionSearchResult{
			Code: rowString(row, "Code"), Caption: rowString(row, "Caption"),
			EntitySchemaName: nonBlankOrNil(rowString(row, "EntitySchemaName")),
			Description:      nonBlankOrNil(rowString(row, "Description")),
		})
	}
	for _, sections := range grouped {
		sort.SliceStable(sections, func(i, j int) bool {
			if order := compareOrdinalIgnoreCase(sections[i].Caption, sections[j].Caption); order != 0 {
				return order < 0
			}
			return compareOrdinalIgnoreCase(sections[i].Code, sections[j].Code) < 0
		})
	}
	return grouped
}

func appMatchesPattern(app AppSearchResult, pattern string) bool {
	if pattern == "" {
		return true
	}
	contains := func(value *string) bool { return value != nil && containsOrdinalIgnoreCase(*value, pattern) }
	if containsOrdinalIgnoreCase(app.Name, pattern) || containsOrdinalIgnoreCase(app.Code, pattern) || contains(app.Description) {
		return true
	}
	for _, section := range app.Sections {
		if containsOrdinalIgnoreCase(section.Caption, pattern) || containsOrdinalIgnoreCase(section.Code, pattern) {
			return true
		}
	}
	return false
}

// containsOrdinalIgnoreCase is .NET string.Contains(value, StringComparison.OrdinalIgnoreCase): both sides
// are compared upper-cased, consistent with compareOrdinalIgnoreCase.
func containsOrdinalIgnoreCase(value, pattern string) bool {
	return value != "" && strings.Contains(strings.ToUpper(value), strings.ToUpper(pattern))
}

// nonBlankOrNil returns nil for a blank value and the value itself, untrimmed, otherwise. clio tests
// IsNullOrWhiteSpace but keeps the original text.
func nonBlankOrNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// selectQueryWithOrFilter is clio's BuildSelectQueryWithOrFilter: one equality filter per value on the same
// column, combined with OR, so a batch of ids costs one request instead of one per id.
func selectQueryWithOrFilter(root string, columns map[string]string, column string, values []string, dataValueType, rowCount int) map[string]any {
	query := buildSelectQuery(root, columns, nil, rowCount)
	items := make(map[string]any, len(values))
	for index, value := range values {
		items[fmt.Sprintf("filter%d", index)] = comparisonFilter(column, value, dataValueType, 3)
	}
	query["filters"] = map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false, "logicalOperation": 1, "items": items}
	return query
}
