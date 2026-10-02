package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// pageHierarchyResponseBytes bounds GetParentSchemas, which returns every layer's full body.
const pageHierarchyResponseBytes = 32 << 20

const pageHierarchyRecoveryHint = " [hint: the page schema hierarchy could not be resolved. This is often the Creatio schema-manager " +
	"cache holding a phantom for a section whose concurrent creation was abandoned, which poisons " +
	"hierarchy reads (ENG-94418). Restart Creatio to clear the server schema-manager cache — " +
	"the confirmed recovery; flushing the Redis cache alone does NOT clear this phantom. Then verify " +
	"the schema UId via list-pages.]"

type PageGetRequest struct {
	SchemaName        string
	IncludeOperations *bool
}

// PageGetResult is clio's MCP get-page envelope. The merged bundle, the editable body and the full page
// metadata are not part of the envelope: WritePageFiles writes them to .clio-pages/{schema}/ and fills Files.
type PageGetResult struct {
	Success  bool              `json:"success"`
	Page     *PageMetadata     `json:"page,omitempty"`
	Files    *PageFiles        `json:"files,omitempty"`
	Editable *PageEditableInfo `json:"editable,omitempty"`
	Error    string            `json:"error,omitempty"`

	bundle   *jnode
	rawBody  string
	fullPage *PageMetadata
}

type PageFiles struct {
	BodyFile   string `json:"bodyFile"`
	BundleFile string `json:"bundleFile"`
	MetaFile   string `json:"metaFile"`
	FetchedAt  string `json:"fetchedAt,omitempty"`
}

type PageMetadata struct {
	SchemaName                         string            `json:"schemaName"`
	SchemaUID                          string            `json:"schemaUId"`
	PackageName                        string            `json:"packageName"`
	CurrentLeafPackageName             string            `json:"currentLeafPackageName"`
	PackageUID                         string            `json:"packageUId"`
	ParentSchemaName                   string            `json:"parentSchemaName"`
	OwnBodySummary                     *PageOwnBodyStats `json:"ownBodySummary,omitempty"`
	DesignPackageUID                   string            `json:"designPackageUId,omitempty"`
	DesignPackageName                  *string           `json:"designPackageName,omitempty"`
	WillCreateReplacingInDesignPackage bool              `json:"willCreateReplacingInDesignPackage"`
	RootSchemaUID                      string            `json:"rootSchemaUId,omitempty"`
	SchemaType                         string            `json:"schema-type"`
	SchemaTypeValue                    *int              `json:"schema-type-value,omitempty"`
}

type PageOwnBodyStats struct {
	ViewConfigDiffOperations      int             `json:"viewConfigDiffOperations"`
	ViewModelConfigDiffOperations int             `json:"viewModelConfigDiffOperations"`
	ModelConfigDiffOperations     int             `json:"modelConfigDiffOperations"`
	HandlerEntries                int             `json:"handlerEntries"`
	BodyLength                    int             `json:"bodyLength"`
	ViewConfigDiffOps             []PageOperation `json:"viewConfigDiffOps,omitempty"`
	ViewConfigDiffOpCounts        *orderedCounts  `json:"viewConfigDiffOpCounts,omitempty"`
	HandlerRequests               []string        `json:"handlerRequests"`
}

type PageOperation struct {
	Operation  *string `json:"operation,omitempty"`
	Name       *string `json:"name,omitempty"`
	Type       *string `json:"type,omitempty"`
	ParentName *string `json:"parentName,omitempty"`
}

type PageEditableInfo struct {
	EditableSchemaExists bool    `json:"editableSchemaExists"`
	EditableSchemaUID    string  `json:"editableSchemaUId,omitempty"`
	Checksum             *string `json:"checksum,omitempty"`
	ModifiedOn           *string `json:"modifiedOn,omitempty"`
}

// orderedCounts serializes operation counts in first-seen order, as clio's dictionary does.
type orderedCounts struct {
	keys   []string
	counts map[string]int
}

func (o *orderedCounts) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			buffer.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		buffer.Write(encoded)
		fmt.Fprintf(&buffer, ":%d", o.counts[key])
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

type pageLayer struct {
	UID                string
	Name               string
	PackageUID         string
	PackageName        string
	Body               *string
	SchemaType         *int
	SchemaVersion      int
	Parameters         *jnode
	LocalizableStrings *jnode
	OptionalProperties *jnode
}

// GetPage reads a Freedom UI page the way clio's get-page does: SysSchema metadata, the design package,
// the designer hierarchy from ClientUnitSchemaDesignerService, then a summary of the editable schema's own body.
func (c *Client) GetPage(ctx context.Context, input PageGetRequest) PageGetResult {
	schemaName := input.SchemaName
	if strings.TrimSpace(schemaName) == "" {
		return PageGetResult{Error: "schemaName is required"}
	}
	metadata, err := c.pageSchemaRow(ctx, schemaName)
	if err != nil {
		return PageGetResult{Error: err.Error()}
	}
	schemaUID, packageUID := rowText(metadata, "UId"), rowText(metadata, "PackageUId")
	if strings.TrimSpace(schemaUID) == "" || strings.TrimSpace(packageUID) == "" {
		return PageGetResult{Error: fmt.Sprintf("Schema '%s' metadata is missing package or schema identifiers", schemaName)}
	}
	designPackageUID := c.pageDesignPackageUID(ctx, schemaUID)
	if strings.TrimSpace(designPackageUID) == "" {
		designPackageUID = packageUID
	}
	hierarchy, err := c.pageLayers(ctx, schemaUID, designPackageUID)
	if err != nil {
		message := fmt.Sprintf("Failed to load hierarchy for '%s': %s", schemaName, err.Error())
		if strings.Contains(strings.ToLower(message), strings.ToLower("Incorrect syntax near ')'")) {
			message += pageHierarchyRecoveryHint
		}
		return PageGetResult{Error: message}
	}
	if len(hierarchy) == 0 {
		return PageGetResult{Error: fmt.Sprintf("Schema '%s' hierarchy is empty", schemaName)}
	}
	rootSchemaUID := schemaUID
	for index := len(hierarchy) - 1; index >= 0; index-- {
		if strings.EqualFold(hierarchy[index].Name, schemaName) {
			rootSchemaUID = hierarchy[index].UID
			break
		}
	}
	if !strings.EqualFold(rootSchemaUID, schemaUID) {
		full, err := c.pageLayers(ctx, rootSchemaUID, designPackageUID)
		if err != nil {
			return PageGetResult{Error: err.Error()}
		}
		if len(full) > 0 {
			hierarchy = full
		}
	}
	current := hierarchy[0]
	parts := make([]pageBundlePart, 0, len(hierarchy))
	for _, schema := range hierarchy {
		parsed := emptyParsedPageBody()
		if schema.Body != nil {
			if parsed, err = parsePageBody(*schema.Body); err != nil {
				return PageGetResult{Error: err.Error()}
			}
		}
		parts = append(parts, pageBundlePart{schema: schema, parsed: parsed})
	}
	bundle, err := safePageBundle(parts, schemaName)
	if err != nil {
		return PageGetResult{Error: err.Error()}
	}
	designPackageName := c.pagePackageName(ctx, designPackageUID)
	var editable *pageLayer
	for index := range hierarchy {
		if strings.EqualFold(hierarchy[index].PackageUID, designPackageUID) {
			editable = &hierarchy[index]
			break
		}
	}
	if editable == nil && strings.TrimSpace(designPackageUID) != "" {
		editable = c.replacingSchemaInPackage(ctx, schemaName, designPackageUID)
	}
	willCreate := editable == nil && strings.TrimSpace(designPackageUID) != "" && !strings.EqualFold(designPackageUID, current.PackageUID)
	summarySource := current
	if editable != nil {
		summarySource = *editable
	}
	summary, err := ownBodySummary(summarySource)
	if err != nil {
		return PageGetResult{Error: err.Error()}
	}
	rawBody := emptyPageBody(schemaName, current.SchemaType)
	if editable != nil && editable.Body != nil {
		rawBody = *editable.Body
	}
	fullPage := &PageMetadata{
		SchemaName: current.Name, SchemaUID: current.UID, PackageName: current.PackageName,
		CurrentLeafPackageName: current.PackageName, PackageUID: current.PackageUID,
		ParentSchemaName: rowText(metadata, "ParentSchemaName"), OwnBodySummary: summary,
		DesignPackageUID: designPackageUID, DesignPackageName: designPackageName,
		WillCreateReplacingInDesignPackage: willCreate, RootSchemaUID: rootSchemaUID,
		SchemaType: pageSchemaTypeLabel(current.SchemaType), SchemaTypeValue: current.SchemaType,
	}
	page := fullPage
	if input.IncludeOperations != nil && !*input.IncludeOperations {
		compact := *summary
		compact.ViewConfigDiffOpCounts = operationCounts(summary.ViewConfigDiffOps)
		compact.ViewConfigDiffOps = nil
		copied := *fullPage
		copied.OwnBodySummary = &compact
		page = &copied
	}
	return PageGetResult{Success: true, Page: page, Editable: c.editableSchemaInfo(ctx, editable),
		bundle: bundle, rawBody: rawBody, fullPage: fullPage}
}

// safePageBundle builds the merged bundle and turns a merge fault into clio's error text.
func safePageBundle(parts []pageBundlePart, schemaName string) (bundle *jnode, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fault, ok := recovered.(applierFault)
			if !ok {
				panic(recovered)
			}
			if fault.diff {
				err = fmt.Errorf("Failed to resolve page bundle for '%s': the schema chain contains an operation the platform itself would reject (%s).", schemaName, fault.message)
				return
			}
			err = errors.New(fault.message)
		}
	}()
	return buildPageBundle(parts), nil
}

// emptyPageBody is the body clio writes to body.js when the design package has no layer of this page yet.
func emptyPageBody(schemaName string, schemaType *int) string {
	if schemaType != nil && *schemaType == 10 {
		return "{\n\t\"viewConfigDiff\": [],\n\t\"viewModelConfigDiff\": [],\n\t\"modelConfigDiff\": []\n}"
	}
	return "define(\"" + schemaName + "\", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ {\n\treturn {\n\t\tviewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[]/**SCHEMA_VIEW_CONFIG_DIFF*/,\n\t\tviewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/,\n\t\tmodelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/,\n\t\thandlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/,\n\t\tconverters: /**SCHEMA_CONVERTERS*/{}/**SCHEMA_CONVERTERS*/,\n\t\tvalidators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/\n\t};\n});"
}

func (c *Client) pageSchemaRow(ctx context.Context, schemaName string) (map[string]json.RawMessage, error) {
	query := entityQuery("SysSchema", map[string]string{
		"Name": "Name", "UId": "UId", "PackageName": "SysPackage.Name", "PackageUId": "SysPackage.UId",
		"ParentSchemaName": "[SysSchema:Id:Parent].Name",
	}, map[string]any{"filter0": eqFilter("Name", schemaName, 1), "filter1": eqFilter("ManagerName", "ClientUnitSchemaManager", 1)}, 1)
	rows, err := c.selectRows(ctx, query)
	if err != nil {
		if isTransportError(err) || isAuthenticationError(err) {
			return nil, err
		}
		return nil, errors.New("Failed to query schema metadata")
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("Schema '%s' not found", schemaName)
	}
	return rows[0], nil
}

// designPackageUID asks ApplicationPackagesService for the package designer writes go to; any failure
// returns empty so the caller falls back to the schema's own package, as clio does.
func (c *Client) pageDesignPackageUID(ctx context.Context, schemaUID string) string {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "userLevelSchema": false})
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return ""
	}
	var decoded struct {
		Success bool            `json:"success"`
		UID     json.RawMessage `json:"uId"`
	}
	if json.Unmarshal(response, &decoded) != nil || !decoded.Success {
		return ""
	}
	return rowText(map[string]json.RawMessage{"uId": decoded.UID}, "uId")
}

func (c *Client) pageLayers(ctx context.Context, schemaUID, packageUID string) ([]pageLayer, error) {
	body, _ := json.Marshal(map[string]any{"schemaUId": schemaUID, "packageUId": packageUID, "useFullHierarchy": true, "userLevelSchema": false})
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas", body, 120*time.Second, pageHierarchyResponseBytes)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Success   *bool             `json:"success"`
		ErrorInfo json.RawMessage   `json:"errorInfo"`
		Message   json.RawMessage   `json:"message"`
		Values    []json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return nil, fmt.Errorf("page schema hierarchy response is not JSON: %w", err)
	}
	if decoded.Success == nil || !*decoded.Success {
		detail := ""
		for _, raw := range []json.RawMessage{decoded.ErrorInfo, decoded.Message} {
			if len(raw) > 0 && string(raw) != "null" {
				detail = rowText(map[string]json.RawMessage{"v": raw}, "v")
				break
			}
		}
		if detail == "" {
			return nil, errors.New("Failed to load page schema hierarchy")
		}
		return nil, fmt.Errorf("Failed to load page schema hierarchy: %s", detail)
	}
	if decoded.Values == nil {
		return nil, errors.New("Page schema hierarchy response does not contain values")
	}
	schemas := make([]pageLayer, 0, len(decoded.Values))
	for _, raw := range decoded.Values {
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		var pkg map[string]json.RawMessage
		_ = json.Unmarshal(value["package"], &pkg)
		schema := pageLayer{
			UID:         firstText(value, "uId", "id"),
			Name:        rowText(value, "name"),
			PackageUID:  firstText(pkg, "uId"),
			PackageName: firstText(pkg, "name"),
		}
		if schema.PackageUID == "" {
			schema.PackageUID = rowText(value, "packageUId")
		}
		if schema.PackageName == "" {
			schema.PackageName = rowText(value, "packageName")
		}
		if text := rowText(value, "body"); strings.TrimSpace(text) != "" {
			schema.Body = &text
		}
		var schemaType int
		if raw := value["schemaType"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &schemaType) == nil {
			schema.SchemaType = &schemaType
		}
		var schemaVersion float64
		if raw := value["schemaVersion"]; len(raw) > 0 && json.Unmarshal(raw, &schemaVersion) == nil {
			schema.SchemaVersion = int(schemaVersion)
		}
		schema.Parameters = hierarchyArray(value["parameters"])
		schema.LocalizableStrings = hierarchyArray(value["localizableStrings"])
		schema.OptionalProperties = hierarchyArray(value["optionalProperties"])
		schemas = append(schemas, schema)
	}
	return schemas, nil
}

// pagePackageName names the design package: PackageService first, because it also knows virtual packages
// that have no SysPackage row before their first save, then SysPackage. Failures leave the name absent.
func (c *Client) pagePackageName(ctx context.Context, packageUID string) *string {
	if strings.TrimSpace(packageUID) == "" {
		return nil
	}
	body, _ := json.Marshal(packageUID)
	if response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/PackageService.svc/GetPackageProperties", body, 45*time.Second, maxResponseBytes); err == nil {
		var decoded struct {
			Success bool `json:"success"`
			Package struct {
				Name json.RawMessage `json:"name"`
			} `json:"package"`
		}
		if json.Unmarshal(response, &decoded) == nil && decoded.Success {
			var name string
			if json.Unmarshal(decoded.Package.Name, &name) == nil && strings.TrimSpace(name) != "" {
				return &name
			}
		}
	}
	rows, err := c.selectRows(ctx, entityQuery("SysPackage", map[string]string{"Name": "Name"},
		map[string]any{"byUId": eqFilter("UId", packageUID, 0)}, 1))
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rowTextPointer(rows[0], "Name")
}

func (c *Client) replacingSchemaInPackage(ctx context.Context, schemaName, packageUID string) *pageLayer {
	rows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"UId": "UId"}, map[string]any{
		"byName": eqFilter("Name", schemaName, 1), "byManager": eqFilter("ManagerName", "ClientUnitSchemaManager", 1),
		"byPackage": eqFilter("SysPackage.UId", packageUID, 0),
	}, 1))
	if err != nil || len(rows) == 0 {
		return nil
	}
	replacingUID := rowText(rows[0], "UId")
	if strings.TrimSpace(replacingUID) == "" {
		return nil
	}
	hierarchy, err := c.pageLayers(ctx, replacingUID, packageUID)
	if err != nil {
		return nil
	}
	for index := range hierarchy {
		if strings.EqualFold(hierarchy[index].UID, replacingUID) {
			return &hierarchy[index]
		}
	}
	return nil
}

// editableSchemaInfo is the conflict-detection baseline. It is best effort: when the checksum query fails
// the block is left out, as clio does. ModifiedOn is kept as the raw DataService value: clio renders it
// through .NET DateTime.ToString() in the clio host's culture, which is not reproducible here.
func (c *Client) editableSchemaInfo(ctx context.Context, editable *pageLayer) *PageEditableInfo {
	if editable == nil {
		return &PageEditableInfo{}
	}
	rows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"Checksum": "Checksum", "ModifiedOn": "ModifiedOn"}, map[string]any{
		"byUId": eqFilter("UId", editable.UID, 0), "byManager": eqFilter("ManagerName", "ClientUnitSchemaManager", 1),
	}, 1))
	if err != nil || len(rows) == 0 {
		return nil
	}
	return &PageEditableInfo{EditableSchemaExists: true, EditableSchemaUID: editable.UID,
		Checksum: rowTextPointer(rows[0], "Checksum"), ModifiedOn: rowTextPointer(rows[0], "ModifiedOn")}
}

func hierarchyArray(raw json.RawMessage) *jnode {
	if len(raw) == 0 {
		return newArray()
	}
	node, err := parseJNode(raw)
	if err != nil {
		return newArray()
	}
	return arrayOrEmpty(node)
}

func ownBodySummary(schema pageLayer) (*PageOwnBodyStats, error) {
	if schema.Body == nil || strings.TrimSpace(*schema.Body) == "" {
		return &PageOwnBodyStats{HandlerRequests: []string{}}, nil
	}
	body := *schema.Body
	parsed, err := parsePageBody(body)
	if err != nil {
		return nil, err
	}
	count, requests := handlerInfo(strings.TrimSpace(parsed.handlers))
	ops := make([]PageOperation, 0, len(parsed.viewConfigDiff.items))
	for _, item := range parsed.viewConfigDiff.items {
		if !item.isObject() {
			continue
		}
		operation := PageOperation{Operation: item.get("operation").tokenString(), Name: item.get("name").tokenString(),
			ParentName: item.get("parentName").tokenString()}
		if values := item.get("values"); values.isObject() {
			operation.Type = values.get("type").tokenString()
		}
		ops = append(ops, operation)
	}
	return &PageOwnBodyStats{
		BodyLength:                    len(utf16.Encode([]rune(body))),
		ViewConfigDiffOperations:      countItems(parsed.viewConfigDiff),
		ViewModelConfigDiffOperations: countItems(parsed.viewModelConfigDiff),
		ModelConfigDiffOperations:     countItems(parsed.modelConfigDiff),
		HandlerEntries:                count,
		ViewConfigDiffOps:             ops,
		HandlerRequests:               requests,
	}, nil
}

func countItems(n *jnode) int {
	if n.isArray() {
		return len(n.items)
	}
	return 0
}

func operationCounts(ops []PageOperation) *orderedCounts {
	counts := &orderedCounts{counts: map[string]int{}}
	for _, op := range ops {
		key := derefString(op.Operation)
		if key == "" {
			key = "unknown"
		}
		if _, seen := counts.counts[key]; !seen {
			counts.keys = append(counts.keys, key)
		}
		counts.counts[key]++
	}
	return counts
}

var handlerRequestPattern = regexp.MustCompile(`request\s*:\s*["']([^"']+)["']`)

func handlerInfo(handlers string) (int, []string) {
	requests := []string{}
	if handlers == "" || handlers == "[]" {
		return 0, requests
	}
	count, depth := 0, 0
	for _, ch := range handlers {
		switch ch {
		case '{':
			if depth == 0 {
				count++
			}
			depth++
		case '}':
			depth--
		}
	}
	for _, match := range handlerRequestPattern.FindAllStringSubmatch(handlers, -1) {
		requests = append(requests, match[1])
	}
	return count, requests
}

func pageSchemaTypeLabel(value *int) string {
	if value != nil {
		switch *value {
		case 9:
			return "web"
		case 10:
			return "mobile"
		}
	}
	return "unknown"
}

func readPageSection(body string, markers ...string) (string, bool) {
	for _, marker := range markers {
		quoted := regexp.QuoteMeta(marker)
		pattern := regexp.MustCompile(`/\*\*` + quoted + `\*/([\s\S]*?)/\*\*` + quoted + `\*/`)
		if match := pattern.FindStringSubmatch(body); match != nil {
			return match[1], true
		}
	}
	return "", false
}

func firstText(values map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok && string(raw) != "null" {
			return rowText(values, key)
		}
	}
	return ""
}
