package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// hierarchyGetBodyBudget is clio's DefaultBodySizeBudgetChars: when the bodies of the selected window add up
// to more UTF-16 characters than this, they are all left out and the response says so.
const hierarchyGetBodyBudget = 200_000

type PageHierarchyRequest struct {
	SchemaName   string
	MetadataOnly bool
	Offset       int
	Limit        int
}

// PageHierarchyResult is clio's get-page-hierarchy envelope.
type PageHierarchyResult struct {
	Success              bool                  `json:"success"`
	SchemaName           string                `json:"schemaName,omitempty"`
	RootSchemaName       string                `json:"rootSchemaName,omitempty"`
	TotalCount           int                   `json:"totalCount"`
	Offset               int                   `json:"offset"`
	ReturnedCount        int                   `json:"returnedCount"`
	HasMore              bool                  `json:"hasMore"`
	Schemas              *[]PageHierarchyEntry `json:"schemas,omitempty"`
	BodiesOmittedForSize bool                  `json:"bodiesOmittedForSize"`
	Warning              string                `json:"warning,omitempty"`
	Error                string                `json:"error,omitempty"`
}

// PageHierarchyEntry is one schema of the replacing chain; level 0 is the root.
type PageHierarchyEntry struct {
	HierarchyLevel int     `json:"hierarchyLevel"`
	SchemaName     string  `json:"schemaName,omitempty"`
	SchemaUID      string  `json:"schemaUId,omitempty"`
	PackageName    string  `json:"packageName,omitempty"`
	PackageUID     string  `json:"packageUId,omitempty"`
	SchemaVersion  int     `json:"schemaVersion"`
	SchemaType     string  `json:"schemaType"`
	HasBody        bool    `json:"hasBody"`
	BodyLength     int     `json:"bodyLength"`
	Body           *string `json:"body,omitempty"`
}

// GetPageHierarchy returns the whole Freedom UI replacing-schema chain, root first, with each raw body, the
// way clio's get-page-hierarchy does. It resolves the chain like get-page: SysSchema metadata, the design
// package, the designer hierarchy, then a re-read anchored on the root-most variant of the requested name.
func (c *Client) GetPageHierarchy(ctx context.Context, input PageHierarchyRequest) PageHierarchyResult {
	if strings.TrimSpace(input.SchemaName) == "" {
		return PageHierarchyResult{Error: "schema-name is required"}
	}
	if input.Offset < 0 {
		return PageHierarchyResult{Error: "offset must be zero or greater"}
	}
	if input.Limit < 0 {
		return PageHierarchyResult{Error: "limit must be zero or greater"}
	}
	effectiveFirst, err := c.hierarchyGetChain(ctx, input.SchemaName)
	if err != nil {
		return PageHierarchyResult{Error: err.Error()}
	}
	if len(effectiveFirst) == 0 {
		return PageHierarchyResult{Error: fmt.Sprintf("Schema '%s' hierarchy is empty or could not be resolved", input.SchemaName)}
	}
	return hierarchyGetBuild(input, effectiveFirst)
}

// hierarchyGetBuild orders the designer's effective-first chain root first, applies offset/limit and the size
// budget, and projects the entries. It does no I/O.
func hierarchyGetBuild(input PageHierarchyRequest, effectiveFirst []pageLayer) PageHierarchyResult {
	rootFirst := make([]pageLayer, len(effectiveFirst))
	for index, layer := range effectiveFirst {
		rootFirst[len(effectiveFirst)-1-index] = layer
	}
	total := len(rootFirst)
	offset := min(input.Offset, total)
	take := total - offset
	if input.Limit != 0 {
		take = min(input.Limit, total-offset)
	}
	omitForSize := false
	if !input.MetadataOnly {
		windowChars := 0
		for index := 0; index < take; index++ {
			windowChars += hierarchyGetBodyLength(rootFirst[offset+index])
		}
		omitForSize = windowChars > hierarchyGetBodyBudget
	}
	includeBodies := !input.MetadataOnly && !omitForSize
	schemas := make([]PageHierarchyEntry, 0, take)
	for index := 0; index < take; index++ {
		level := offset + index
		layer := rootFirst[level]
		entry := PageHierarchyEntry{
			HierarchyLevel: level, SchemaName: layer.Name, SchemaUID: layer.UID, PackageName: layer.PackageName,
			PackageUID: layer.PackageUID, SchemaVersion: layer.SchemaVersion, SchemaType: pageSchemaTypeLabel(layer.SchemaType),
			HasBody: layer.Body != nil && *layer.Body != "", BodyLength: hierarchyGetBodyLength(layer),
		}
		if includeBodies && entry.HasBody {
			entry.Body = layer.Body
		}
		schemas = append(schemas, entry)
	}
	result := PageHierarchyResult{
		Success: true, SchemaName: input.SchemaName, RootSchemaName: rootFirst[0].Name, TotalCount: total,
		Offset: offset, ReturnedCount: len(schemas), HasMore: offset+len(schemas) < total, Schemas: &schemas,
		BodiesOmittedForSize: omitForSize,
	}
	if omitForSize {
		result.Warning = fmt.Sprintf("Bodies omitted: the selected window exceeds the %d-char response budget. ", hierarchyGetBodyBudget) +
			"Re-request with --metadata-only, or page with --offset/--limit, or fetch a single schema body via get-page."
	}
	return result
}

func hierarchyGetBodyLength(layer pageLayer) int {
	if layer.Body == nil {
		return 0
	}
	return utf16Length(*layer.Body)
}

// hierarchyGetChain returns the designer chain, effective schema first. An empty chain with no error means
// the environment answered and the chain is genuinely empty.
func (c *Client) hierarchyGetChain(ctx context.Context, schemaName string) ([]pageLayer, error) {
	metadata, err := c.pageSchemaRow(ctx, schemaName)
	if err != nil {
		return nil, err
	}
	schemaUID, packageUID := rowText(metadata, "UId"), rowText(metadata, "PackageUId")
	if strings.TrimSpace(schemaUID) == "" || strings.TrimSpace(packageUID) == "" {
		return nil, fmt.Errorf("Schema '%s' metadata is missing the schema or package UId", schemaName)
	}
	designPackageUID, err := c.hierarchyGetDesignPackageUID(ctx, schemaUID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(designPackageUID) == "" {
		designPackageUID = packageUID
	}
	initial, err := c.pageLayers(ctx, schemaUID, designPackageUID)
	if err != nil {
		return nil, err
	}
	if len(initial) == 0 {
		return nil, nil
	}
	rootSchemaUID := schemaUID
	for index := len(initial) - 1; index >= 0; index-- {
		if strings.EqualFold(initial[index].Name, schemaName) {
			rootSchemaUID = initial[index].UID
			break
		}
	}
	if strings.EqualFold(rootSchemaUID, schemaUID) {
		return initial, nil
	}
	full, err := c.pageLayers(ctx, rootSchemaUID, designPackageUID)
	if err != nil {
		return nil, err
	}
	if len(full) > 0 {
		return full, nil
	}
	return initial, nil
}

// hierarchyGetDesignPackageUID asks ApplicationPackagesService for the design package. Only an answered
// rejection (success:false or no uId) licenses the fallback to the schema's own package; a timeout, a
// transport failure or a non-JSON body fails the read, as in clio's get-page-hierarchy, because the chain
// anchored on the runtime package can miss replacing layers.
func (c *Client) hierarchyGetDesignPackageUID(ctx context.Context, schemaUID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "userLevelSchema": false})
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", err
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(response, &decoded); err != nil {
		return "", fmt.Errorf("GetDesignPackageUId returned invalid JSON: %w", err)
	}
	var success bool
	if json.Unmarshal(decoded["success"], &success) != nil || !success {
		return "", nil
	}
	return rowText(decoded, "uId"), nil
}
