package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultPackageLimit = 50
	defaultPageLimit    = 50
	pageFallbackLimit   = 10000
)

// PackageListRequest selects a bounded view of the installed SysPackage rows.
type PackageListRequest struct {
	Filter string
	Limit  *int
	Offset int
}

type PackageListItem struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Maintainer string `json:"maintainer"`
	UID        string `json:"uId"`
}

type PackageListResult struct {
	Packages  []PackageListItem `json:"packages"`
	Count     int               `json:"count"`
	Total     int               `json:"total"`
	Offset    int               `json:"offset"`
	Limit     int               `json:"limit"`
	Truncated bool              `json:"truncated"`
}

// ListPackages mirrors the MCP read contract: retrieve SysPackage rows, apply a
// case-insensitive name substring filter, sort by name, then page locally.
func (c *Client) ListPackages(ctx context.Context, input PackageListRequest) (PackageListResult, error) {
	if input.Offset < 0 {
		return PackageListResult{}, fmt.Errorf("offset must be zero or greater")
	}
	limit := defaultPackageLimit
	if input.Limit != nil && *input.Limit != 0 {
		if *input.Limit < 0 {
			return PackageListResult{}, fmt.Errorf("limit must be zero or greater. Omit limit or pass 0 to use the default of %d.", defaultPackageLimit)
		}
		limit = *input.Limit
	}
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{
		"Name": "Name", "UId": "UId", "Maintainer": "Maintainer", "Version": "Version",
	}, nil, 10000))
	if err != nil {
		return PackageListResult{}, err
	}
	needle := strings.ToLower(input.Filter)
	packages := make([]PackageListItem, 0, len(rows))
	for _, row := range rows {
		item := PackageListItem{
			Name: rowString(row, "Name"), Version: rowString(row, "Version"),
			Maintainer: rowString(row, "Maintainer"), UID: rowString(row, "UId"),
		}
		if needle == "" || strings.Contains(strings.ToLower(item.Name), needle) {
			packages = append(packages, item)
		}
	}
	sort.SliceStable(packages, func(i, j int) bool {
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
	total := len(packages)
	start := min(input.Offset, total)
	end := start + min(limit, total-start)
	page := append([]PackageListItem{}, packages[start:end]...)
	return PackageListResult{
		Packages: page, Count: len(page), Total: total, Offset: input.Offset,
		Limit: limit, Truncated: end < total,
	}, nil
}

type AppSectionsResult struct {
	Success            bool         `json:"success"`
	PackageUID         *string      `json:"package-u-id,omitempty"`
	PackageName        *string      `json:"package-name,omitempty"`
	ApplicationID      string       `json:"application-id,omitempty"`
	ApplicationName    string       `json:"application-name,omitempty"`
	ApplicationCode    string       `json:"application-code,omitempty"`
	ApplicationVersion *string      `json:"application-version,omitempty"`
	Sections           []AppSection `json:"sections"`
	Error              string       `json:"error,omitempty"`
}

type AppSection struct {
	ID               string  `json:"id"`
	Code             string  `json:"code"`
	Caption          string  `json:"caption"`
	Description      *string `json:"description,omitempty"`
	EntitySchemaName *string `json:"entity-schema-name,omitempty"`
	PackageID        *string `json:"package-id,omitempty"`
	SectionSchemaUID *string `json:"section-schema-u-id,omitempty"`
	IconID           *string `json:"icon-id,omitempty"`
	IconBackground   *string `json:"icon-background,omitempty"`
	ClientTypeID     *string `json:"client-type-id,omitempty"`
}

// ListAppSections resolves SysInstalledApp by code, then reads its ApplicationSection rows.
func (c *Client) ListAppSections(ctx context.Context, applicationCode string) AppSectionsResult {
	applicationCode = strings.TrimSpace(applicationCode)
	if applicationCode == "" {
		return AppSectionsResult{Error: "application-code is required."}
	}
	appRows, err := c.selectRows(ctx, buildSelectQuery("SysInstalledApp", map[string]string{
		"Id": "Id", "Name": "Name", "Code": "Code", "Version": "Version",
	}, map[string]any{"Code": comparisonFilter("Code", applicationCode, 1, 3)}, 1))
	if err != nil {
		return AppSectionsResult{Error: err.Error()}
	}
	if len(appRows) == 0 {
		return AppSectionsResult{Error: fmt.Sprintf("Application %q was not found.", applicationCode)}
	}
	app := appRows[0]
	appID := rowString(app, "Id")
	if appID == "" {
		return AppSectionsResult{Error: "Application lookup did not return an id."}
	}
	sectionRows, err := c.selectRows(ctx, buildSelectQuery("ApplicationSection", map[string]string{
		"Id": "Id", "ApplicationId": "ApplicationId", "Caption": "Caption", "Code": "Code",
		"Description": "Description", "EntitySchemaName": "EntitySchemaName", "PackageId": "PackageId",
		"SectionSchemaUId": "SectionSchemaUId", "LogoId": "LogoId", "IconBackground": "IconBackground",
		"ClientTypeId": "ClientTypeId",
	}, map[string]any{"ApplicationId": comparisonFilter("ApplicationId", appID, 0, 3)}, 10000))
	if err != nil {
		return AppSectionsResult{Error: err.Error()}
	}
	sections := make([]AppSection, 0, len(sectionRows))
	for _, row := range sectionRows {
		sections = append(sections, AppSection{
			ID: rowString(row, "Id"), Code: rowString(row, "Code"), Caption: rowString(row, "Caption"),
			Description: rowStringPointer(row, "Description"), EntitySchemaName: rowStringPointer(row, "EntitySchemaName"),
			PackageID: rowStringPointer(row, "PackageId"), SectionSchemaUID: rowStringPointer(row, "SectionSchemaUId"),
			IconID: rowStringPointer(row, "LogoId"), IconBackground: rowStringPointer(row, "IconBackground"),
			ClientTypeID: rowStringPointer(row, "ClientTypeId"),
		})
	}
	result := AppSectionsResult{
		Success: true, ApplicationID: appID, ApplicationName: rowString(app, "Name"),
		ApplicationCode: rowString(app, "Code"), Sections: sections,
	}
	result.ApplicationVersion = rowStringPointer(app, "Version")
	return result
}

type PageListRequest struct {
	PackageName     string
	ApplicationCode string
	SearchPattern   string
	Limit           *int
	UID             string
}

type PageListItem struct {
	SchemaName       string `json:"schema-name"`
	UID              string `json:"uId"`
	PackageName      string `json:"packageName"`
	ParentSchemaName string `json:"parentSchemaName"`
}

type PageListResult struct {
	Success   bool           `json:"success"`
	Count     int            `json:"count"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated"`
	Pages     []PageListItem `json:"pages"`
	Error     string         `json:"error,omitempty"`
}

// ListPages returns Freedom UI ClientUnit schemas and preserves clio's bounded
// empty-package cross-check and total-count behavior.
func (c *Client) ListPages(ctx context.Context, input PageListRequest) PageListResult {
	if strings.TrimSpace(input.PackageName) != "" && strings.TrimSpace(input.ApplicationCode) != "" {
		return PageListResult{Error: "Provide either package-name or code, not both."}
	}
	limit := defaultPageLimit
	if input.Limit != nil {
		if *input.Limit < 0 {
			return PageListResult{Error: fmt.Sprintf("limit must be zero or greater (got %d). Omit limit or pass 0 to use the default of %d.", *input.Limit, defaultPageLimit)}
		}
		if *input.Limit > 0 {
			limit = *input.Limit
		}
	}
	packageName := strings.TrimSpace(input.PackageName)
	if packageName == "" && strings.TrimSpace(input.ApplicationCode) != "" {
		resolved, err := c.resolvePrimaryPackageName(ctx, strings.TrimSpace(input.ApplicationCode))
		if err != nil {
			return PageListResult{Error: err.Error()}
		}
		packageName = resolved
	}
	nameFilter := strings.Trim(input.SearchPattern, "* ")
	pages, err := c.queryPages(ctx, packageName, nameFilter, strings.TrimSpace(input.UID), limit)
	if err != nil {
		return PageListResult{Error: err.Error()}
	}
	if len(pages) == 0 && packageName != "" {
		broader, queryErr := c.queryPages(ctx, "", nameFilter, strings.TrimSpace(input.UID), pageFallbackLimit)
		if queryErr != nil {
			return PageListResult{Error: "The package-filtered query returned no rows, and the broader verification query failed."}
		}
		matches := make([]PageListItem, 0)
		for _, page := range broader {
			if strings.EqualFold(page.PackageName, packageName) {
				matches = append(matches, page)
			}
		}
		page := firstPages(matches, limit)
		return PageListResult{Success: true, Count: len(page), Total: len(matches), Truncated: len(broader) >= pageFallbackLimit || len(matches) > len(page), Pages: page}
	}
	if len(pages) < limit {
		return PageListResult{Success: true, Count: len(pages), Total: len(pages), Pages: pages}
	}
	total, countErr := c.countPages(ctx, packageName, nameFilter, strings.TrimSpace(input.UID))
	if countErr != nil {
		return PageListResult{Success: true, Count: len(pages), Total: len(pages), Truncated: true, Pages: pages}
	}
	if total < len(pages) {
		total = len(pages)
	}
	return PageListResult{Success: true, Count: len(pages), Total: total, Truncated: total > len(pages), Pages: pages}
}

func (c *Client) resolvePrimaryPackageName(ctx context.Context, appCode string) (string, error) {
	apps, err := c.selectRows(ctx, buildSelectQuery("SysInstalledApp", map[string]string{"Id": "Id"}, map[string]any{
		"Code": comparisonFilter("Code", appCode, 1, 3),
	}, 1))
	if err != nil {
		return "", err
	}
	if len(apps) == 0 || rowString(apps[0], "Id") == "" {
		return "", fmt.Errorf("Application %q not found.", appCode)
	}
	appIDJSON, _ := json.Marshal(rowString(apps[0], "Id"))
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ApplicationPackagesService.svc/GetApplicationPackages", appIDJSON, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", err
	}
	var decoded struct {
		Success  bool `json:"success"`
		Packages []struct {
			Name    string `json:"name"`
			Primary bool   `json:"isApplicationPrimaryPackage"`
		} `json:"packages"`
		ErrorInfo struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return "", fmt.Errorf("application packages endpoint returned invalid JSON: %w", err)
	}
	if !decoded.Success {
		if decoded.ErrorInfo.Message != "" {
			return "", fmt.Errorf("failed to load application packages: %s", decoded.ErrorInfo.Message)
		}
		return "", fmt.Errorf("failed to load application packages")
	}
	for _, pkg := range decoded.Packages {
		if pkg.Primary && strings.TrimSpace(pkg.Name) != "" {
			return pkg.Name, nil
		}
	}
	return "", fmt.Errorf("primary package was not found for application %q", appCode)
}

func (c *Client) queryPages(ctx context.Context, packageName, nameFilter, uid string, rowCount int) ([]PageListItem, error) {
	filters := map[string]any{"ManagerName": pageComparisonFilter("ManagerName", "ClientUnitSchemaManager", 1, 3)}
	if packageName != "" {
		filters["PackageName"] = pageComparisonFilter("SysPackage.Name", packageName, 1, 3)
	}
	if nameFilter != "" {
		filters["Name"] = pageComparisonFilter("Name", nameFilter, 1, 11)
	}
	if uid != "" {
		filters["UId"] = pageComparisonFilter("UId", uid, 0, 3)
	}
	query := buildSelectQuery("SysSchema", map[string]string{
		"Name": "Name", "UId": "UId", "PackageName": "SysPackage.Name", "ParentSchemaName": "[SysSchema:Id:Parent].Name",
	}, filters, rowCount)
	rows, err := c.selectRows(ctx, query)
	if err != nil {
		return nil, err
	}
	pages := make([]PageListItem, 0, len(rows))
	for _, row := range rows {
		pages = append(pages, PageListItem{SchemaName: rowString(row, "Name"), UID: rowString(row, "UId"), PackageName: rowString(row, "PackageName"), ParentSchemaName: rowString(row, "ParentSchemaName")})
	}
	return pages, nil
}

func (c *Client) countPages(ctx context.Context, packageName, nameFilter, uid string) (int, error) {
	filters := map[string]any{"ManagerName": pageComparisonFilter("ManagerName", "ClientUnitSchemaManager", 1, 3)}
	if packageName != "" {
		filters["PackageName"] = pageComparisonFilter("SysPackage.Name", packageName, 1, 3)
	}
	if nameFilter != "" {
		filters["Name"] = pageComparisonFilter("Name", nameFilter, 1, 11)
	}
	if uid != "" {
		filters["UId"] = pageComparisonFilter("UId", uid, 0, 3)
	}
	query := map[string]any{"rootSchemaName": "SysSchema", "operationType": 0, "filters": pageFilters(filters), "columns": map[string]any{"items": map[string]any{
		"RecordCount": map[string]any{"expression": map[string]any{"expressionType": 1, "functionType": 2, "aggregationType": 1, "functionArgument": map[string]any{"expressionType": 0, "columnPath": "Id"}}},
	}}}
	rows, err := c.selectRows(ctx, query)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("count query returned no rows")
	}
	var count int
	if err := json.Unmarshal(rows[0]["RecordCount"], &count); err != nil {
		return 0, err
	}
	return count, nil
}

func firstPages(pages []PageListItem, limit int) []PageListItem {
	if len(pages) > limit {
		pages = pages[:limit]
	}
	return append([]PageListItem{}, pages...)
}

func buildSelectQuery(root string, columns map[string]string, filters map[string]any, rowCount int) map[string]any {
	columnItems := make(map[string]any, len(columns))
	for alias, path := range columns {
		columnItems[alias] = map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}, "orderDirection": 0, "orderPosition": -1, "isVisible": true}
	}
	return map[string]any{
		"rootSchemaName": root, "operationType": 0, "allColumns": false, "isDistinct": false,
		"ignoreDisplayValues": false, "rowCount": rowCount, "rowsOffset": -1, "isPageable": false,
		"columns": map[string]any{"items": columnItems}, "filters": standardFilters(filters),
	}
}

func standardFilters(filters map[string]any) map[string]any {
	items := make(map[string]any, len(filters))
	for name, filter := range filters {
		items[name] = filter
	}
	return map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false, "logicalOperation": 0, "items": items}
}

func pageFilters(filters map[string]any) map[string]any {
	items := make(map[string]any, len(filters))
	for name, filter := range filters {
		items[name] = filter
	}
	return map[string]any{"filterType": 6, "isEnabled": true, "logicalOperation": 0, "items": items}
}

func comparisonFilter(column string, value any, dataValueType, comparisonType int) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": comparisonType,
		"isEnabled": true, "trimDateTimeParameterToDate": false,
		"leftExpression":  map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}},
	}
}

func pageComparisonFilter(column string, value any, dataValueType, comparisonType int) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": comparisonType,
		"leftExpression":  map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}},
	}
}

type selectRowsResponse struct {
	Success   *bool                        `json:"success"`
	Rows      []map[string]json.RawMessage `json:"rows"`
	ErrorInfo struct {
		Message string `json:"message"`
	} `json:"errorInfo"`
	ResponseStatus struct {
		Message string `json:"Message"`
	} `json:"responseStatus"`
}

func (c *Client) selectRows(ctx context.Context, query any) ([]map[string]json.RawMessage, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encode SelectQuery: %w", err)
	}
	response, err := c.postDataServiceJSON(ctx, "SelectQuery", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var decoded selectRowsResponse
	if err := json.Unmarshal(response, &decoded); err != nil {
		return nil, fmt.Errorf("SelectQuery returned invalid JSON: %w", err)
	}
	if decoded.Success != nil && !*decoded.Success {
		message := decoded.ErrorInfo.Message
		if message == "" {
			message = decoded.ResponseStatus.Message
		}
		if message == "" {
			message = "Creatio rejected the SelectQuery."
		}
		return nil, fmt.Errorf("SelectQuery failed: %s", message)
	}
	if decoded.Rows == nil {
		return nil, fmt.Errorf("SelectQuery response has no rows array")
	}
	return decoded.Rows, nil
}

func rowString(row map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(row[key], &value)
	return value
}

func rowStringPointer(row map[string]json.RawMessage, key string) *string {
	if len(row[key]) == 0 || string(row[key]) == "null" {
		return nil
	}
	value := rowString(row, key)
	return &value
}
