package creatio

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// EntitySchemaSearchRequest selects entity schemas by exact name, name substring and/or UId. Supplied
// criteria are combined with AND, as in clio.
type EntitySchemaSearchRequest struct {
	SchemaName    string
	SearchPattern string
	UID           string
}

// EntitySchemaSearchResult is one SysSchema row of an entity schema: one per package that declares or
// replaces the schema.
type EntitySchemaSearchResult struct {
	SchemaName        string  `json:"schema-name"`
	PackageName       string  `json:"package-name"`
	PackageMaintainer string  `json:"package-maintainer"`
	ParentSchemaName  *string `json:"parent-schema-name,omitempty"`
}

const entitySchemaSearchRowCount = 10000

// .NET Guid.TryParse accepts the D, N, B and P forms, with surrounding whitespace.
var guidTextPattern = regexp.MustCompile(`^\s*(?:[0-9a-fA-F]{32}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}|\([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\))\s*$`)

func isGUIDText(value string) bool { return guidTextPattern.MatchString(value) }

// FindEntitySchemas mirrors clio's FindEntitySchemaCommand. Rows keep the server order: clio does not sort
// them. An empty substring result is re-checked once without the server-side contains filter, because a
// just-created schema can be visible by identity before the contains filter finds it; a re-check that hits
// the row bound fails instead of claiming absence.
func (c *Client) FindEntitySchemas(ctx context.Context, input EntitySchemaSearchRequest) ([]EntitySchemaSearchResult, error) {
	if strings.TrimSpace(input.SchemaName) == "" && strings.TrimSpace(input.SearchPattern) == "" && strings.TrimSpace(input.UID) == "" {
		return nil, fmt.Errorf("At least one of 'schema-name', 'search-pattern', or 'uid' is required.")
	}
	if strings.TrimSpace(input.UID) != "" && !isGUIDText(input.UID) {
		return nil, fmt.Errorf("'--uid' value '%s' is not a valid Guid.", input.UID)
	}
	results, err := c.queryEntitySchemas(ctx, input, true)
	if err != nil {
		return nil, err
	}
	pattern := strings.TrimSpace(input.SearchPattern)
	if len(results) > 0 || pattern == "" {
		return results, nil
	}
	broader, err := c.queryEntitySchemas(ctx, input, false)
	if err != nil {
		return nil, err
	}
	matches := make([]EntitySchemaSearchResult, 0)
	for _, result := range broader {
		if containsOrdinalIgnoreCase(result.SchemaName, pattern) {
			matches = append(matches, result)
		}
	}
	if len(broader) >= entitySchemaSearchRowCount {
		return nil, fmt.Errorf("Complete results for search pattern '%s' could not be confirmed because the broader verification query reached its %d-row safety bound. Use --schema-name or --uid for an exact lookup.", pattern, entitySchemaSearchRowCount)
	}
	return matches, nil
}

func (c *Client) queryEntitySchemas(ctx context.Context, input EntitySchemaSearchRequest, includePattern bool) ([]EntitySchemaSearchResult, error) {
	filters := map[string]any{"filter0": comparisonFilter("ManagerName", "EntitySchemaManager", 1, 3)}
	add := func(filter map[string]any) { filters[fmt.Sprintf("filter%d", len(filters))] = filter }
	if name := strings.TrimSpace(input.SchemaName); name != "" {
		add(comparisonFilter("Name", name, 1, 3))
	}
	if pattern := strings.TrimSpace(input.SearchPattern); includePattern && pattern != "" {
		add(comparisonFilter("Name", pattern, 1, 11))
	}
	if uid := strings.TrimSpace(input.UID); uid != "" {
		add(comparisonFilter("UId", uid, 0, 3))
	}
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{
		"Name": "Name", "UId": "UId", "PackageName": "SysPackage.Name",
		"PackageMaintainer": "SysPackage.Maintainer", "ParentSchemaName": "[SysSchema:Id:Parent].Name",
	}, filters, entitySchemaSearchRowCount))
	if err != nil {
		return nil, err
	}
	results := make([]EntitySchemaSearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, EntitySchemaSearchResult{
			SchemaName: rowString(row, "Name"), PackageName: rowString(row, "PackageName"),
			PackageMaintainer: rowString(row, "PackageMaintainer"),
			ParentSchemaName:  nonBlankOrNil(rowString(row, "ParentSchemaName")),
		})
	}
	return results, nil
}
