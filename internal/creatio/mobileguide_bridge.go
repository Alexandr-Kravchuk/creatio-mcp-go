package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/mobileguide"
)

// What get-mobile-page-conversion-guide reads from an environment and the registries: get-page's merged bundle
// and schema chain, get-component-info's platform version probe and catalogs, the mobile request catalog, the
// conversion rules, and the probes' read-only requests (OData GETs, DataService SelectQuery,
// AddonSchemaDesignerService.GetSchema, the page business-rule schema context).

// mobileGuideStand is the mobileguide.Stand of one call. failure is the error clio's ReadPageUnderTenantLock
// raises when the environment cannot be resolved; every page read then reports it.
type mobileGuideStand struct {
	client  *Client
	failure error
}

// NewMobileGuideStand returns the stand reads of one call; failure (when not nil) is the environment
// resolution failure each page read reports.
func NewMobileGuideStand(client *Client, failure error) mobileguide.Stand {
	return &mobileGuideStand{client: client, failure: failure}
}

// MobileGuideThreePartVersion is PlatformVersionResolver.TryNormaliseToThreePartSemver.
func MobileGuideThreePartVersion(value string) bool {
	_, ok := componentInfoThreePartVersion(value)
	return ok
}

func (s *mobileGuideStand) ReadPage(ctx context.Context, schemaName string) (mobileguide.PageRead, error) {
	if s.failure != nil {
		return mobileguide.PageRead{}, s.failure
	}
	if s.client == nil {
		return mobileguide.PageRead{}, errors.New("Either a configured environment name or an explicit URI is required for MCP command execution.")
	}
	return mobileGuidePageRead(s.client.GetPage(ctx, PageGetRequest{SchemaName: schemaName})), nil
}

// mobileGuidePageRead keeps what the tool reads of a get-page answer: the metadata, the merged bundle sections
// as compact Newtonsoft JSON and the schema chain names.
func mobileGuidePageRead(result PageGetResult) mobileguide.PageRead {
	read := mobileguide.PageRead{Success: result.Success, Error: result.Error}
	if !result.Success {
		return read
	}
	if page := result.fullPage; page != nil {
		read.SchemaName, read.SchemaUID, read.PackageUID = page.SchemaName, page.SchemaUID, page.PackageUID
		read.ParentSchemaName, read.SchemaType = page.ParentSchemaName, page.SchemaType
	}
	bundle := result.bundle
	if bundle == nil {
		return read
	}
	section := func(node *jnode) []byte {
		if node == nil || node.kind == jkNull {
			return nil
		}
		return node.newtonsoftJSON()
	}
	text := func(node *jnode) *string {
		if node == nil || node.kind != jkString {
			return nil
		}
		value := node.text
		return &value
	}
	read.ViewConfig = section(bundle.get("viewConfig"))
	read.ViewModelConfig = section(bundle.get("viewModelConfig"))
	read.ModelConfig = section(bundle.get("modelConfig"))
	if resources := bundle.get("resources"); resources.isObject() {
		read.ResourcesStrings = section(resources.get("strings"))
	}
	read.Handlers, read.Converters, read.Validators = text(bundle.get("handlers")), text(bundle.get("converters")), text(bundle.get("validators"))
	if schemas := bundle.get("schemas"); schemas.isArray() {
		for _, item := range schemas.items {
			name := ""
			if item.isObject() {
				if value := text(item.get("schemaName")); value != nil {
					name = *value
				}
			}
			read.Chain = append(read.Chain, name)
		}
	}
	return read
}

func (s *mobileGuideStand) ResolveVersion(ctx context.Context) (mobileguide.VersionResolution, error) {
	resolution, err := s.client.componentInfoProbeVersion(ctx)
	if err != nil {
		return mobileguide.VersionResolution{}, err
	}
	return mobileguide.VersionResolution{Version: resolution.version, Environment: resolution.environment, Reason: resolution.reason}, nil
}

// LoadCatalog is ComponentInfoCatalog.LoadAsync for the mobile or web registry: clio's validation (the same
// errors), then the entries as raw JSON in the catalog's order (blank types dropped, stably sorted by type).
func (s *mobileGuideStand) LoadCatalog(ctx context.Context, mobile bool, version string) (*mobileguide.Catalog, error) {
	flavor := componentInfoWebFlavor
	if mobile {
		flavor = componentInfoMobileFlavor
	}
	fetched, err := componentInfoFetchRegistry(ctx, flavor, strings.TrimSpace(version))
	if err != nil {
		return nil, err
	}
	catalog, err := componentInfoParseCatalog(fetched.content, fetched.resolvedVersion, fetched.source)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(fetched.content, []byte("\xef\xbb\xbf")))
	var raw []json.RawMessage
	var baseInputs json.RawMessage
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, err
		}
	} else {
		var envelope struct {
			Components []json.RawMessage `json:"components"`
			References *struct {
				BaseInputs json.RawMessage `json:"baseInputs"`
			} `json:"references"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, err
		}
		raw = envelope.Components
		if envelope.References != nil {
			baseInputs = envelope.References.BaseInputs
		}
	}
	type keyed struct {
		componentType string
		raw           json.RawMessage
	}
	entries := make([]keyed, 0, len(raw))
	for _, item := range raw {
		var probe struct {
			ComponentType string `json:"componentType"`
		}
		if string(bytes.TrimSpace(item)) == "null" || json.Unmarshal(item, &probe) != nil || componentInfoBlank(probe.ComponentType) {
			continue
		}
		entries = append(entries, keyed{probe.ComponentType, item})
	}
	componentInfoSortStable(entries, func(entry keyed) string { return entry.componentType })
	result := &mobileguide.Catalog{BaseInputs: baseInputs, ResolvedVersion: catalog.resolvedVersion}
	for _, entry := range entries {
		result.Entries = append(result.Entries, entry.raw)
	}
	return result, nil
}

// LoadRequestCatalog is MobileRequestInfoCatalog.LoadAsync: the request types in the catalog's order.
func (s *mobileGuideStand) LoadRequestCatalog(ctx context.Context, version string) ([]string, string, error) {
	fetched, err := componentInfoFetchRegistry(ctx, dataWriteMobileRequestsFlavor, strings.TrimSpace(version))
	if err != nil {
		return nil, "", err
	}
	catalog, err := dataWriteParseRequestCatalog(fetched.content, fetched.resolvedVersion)
	if err != nil {
		return nil, "", err
	}
	types := make([]string, 0, len(catalog.entries))
	for _, entry := range catalog.entries {
		types = append(types, entry.RequestType)
	}
	return types, catalog.resolvedVersion, nil
}

func (s *mobileGuideStand) FetchRules(ctx context.Context, version string) ([]byte, error) {
	return FetchWebToMobileConversionRules(ctx, version)
}

// VersionWarning is ComponentInfoResolution.GetVersionWarning.
func (s *mobileGuideStand) VersionWarning(resolvedFrom string) *string {
	var warning string
	switch resolvedFrom {
	case componentInfoResolvedFromLatestFallback:
		warning = componentInfoLatestFallbackWarning
	case componentInfoResolvedFromEnvironmentSuperset:
		warning = componentInfoEnvironmentSupersetWarning
	default:
		return nil
	}
	return &warning
}

func (s *mobileGuideStand) Probes() mobileguide.ProbeClient {
	if s.client == nil || s.failure != nil {
		return nil
	}
	return mobileGuideProbeClient{client: s.client}
}

// mobileGuideProbeClient sends the probes' read-only requests.
type mobileGuideProbeClient struct {
	client *Client
}

// ODataGet is ExecuteGetRequest with clio's 30-second timeout: the body whatever the status.
func (p mobileGuideProbeClient) ODataGet(ctx context.Context, path string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, odataReadTimeout)
	defer cancel()
	response, _, err := p.client.doAuthenticated(requestCtx, p.client.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, p.client.serviceURL(path), nil)
		if err == nil {
			request.Header.Set("Accept", "application/json")
		}
		return request, err
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(maxResponseBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBytes {
		return nil, errResponseTooLarge
	}
	return payload, nil
}

func (p mobileGuideProbeClient) Select(ctx context.Context, query map[string]any) ([]map[string]json.RawMessage, error) {
	return p.client.esqSelect(ctx, query, 100*time.Second)
}

// AddonSchema is AddonSchemaDesignerClient.GetSchema: success false, an empty body or no schema is an error.
func (p mobileGuideProbeClient) AddonSchema(ctx context.Context, request map[string]any) (*mobileguide.AddonSchema, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload, err := p.client.postCreatioServiceJSON(ctx, "ServiceModel/AddonSchemaDesignerService.svc/GetSchema", body, 100*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return nil, errors.New("AddonSchemaDesignerService returned an empty response.")
	}
	var response struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
		Schema *struct {
			MetaData  *string `json:"metaData"`
			Resources []*struct {
				Key   string `json:"key"`
				Value []*struct {
					Value *string `json:"value"`
				} `json:"value"`
			} `json:"resources"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			return nil, errors.New(*response.ErrorInfo.Message)
		}
		return nil, errors.New("AddonSchemaDesignerService.GetSchema failed.")
	}
	if response.Schema == nil {
		return nil, errors.New("AddonSchemaDesignerService did not return a schema payload.")
	}
	schema := &mobileguide.AddonSchema{}
	if response.Schema.MetaData != nil {
		schema.MetaData = *response.Schema.MetaData
	}
	for _, resource := range response.Schema.Resources {
		if resource == nil {
			continue
		}
		item := mobileguide.AddonResource{Key: resource.Key}
		if len(resource.Value) > 0 && resource.Value[0] != nil {
			item.FirstValue = resource.Value[0].Value
		}
		schema.Resources = append(schema.Resources, item)
	}
	return schema, nil
}

// PageRuleContext is PageBusinessRuleSchemaProvider.GetSchema's identifiers: the page in the package, else the
// root of its design hierarchy, then the first two entries of the hierarchy seen from the package. clio also
// merges that hierarchy's bodies and fails when the merge does; that check is not repeated here.
func (p mobileGuideProbeClient) PageRuleContext(ctx context.Context, pageSchemaName, packageUID string) (string, string, error) {
	name := strings.TrimSpace(pageSchemaName)
	rows, err := p.client.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"UId": "UId"}, map[string]any{
		"byName":    comparisonFilter("Name", name, 1, 3),
		"byManager": comparisonFilter("ManagerName", clientUnitSchemaManagerName, 1, 3),
		"byPackage": comparisonFilter("SysPackage.UId", packageUID, 0, 3),
	}, 1))
	if err != nil {
		return "", "", fmt.Errorf("Failed to query schema metadata in target package.")
	}
	schemaUID := ""
	if len(rows) > 0 {
		schemaUID = rowString(rows[0], "UId")
	}
	if strings.TrimSpace(schemaUID) == "" {
		if schemaUID, err = p.client.rootPageSchemaUID(ctx, name, packageUID); err != nil {
			return "", "", err
		}
	}
	hierarchy, err := p.client.pageParentSchemas(ctx, schemaUID, packageUID)
	if err != nil {
		return "", "", err
	}
	if len(hierarchy) == 0 {
		return "", "", fmt.Errorf("Page schema '%s' hierarchy is empty.", pageSchemaName)
	}
	parent := ""
	if len(hierarchy) > 1 {
		parent = hierarchy[1].UID
	}
	return hierarchy[0].UID, parent, nil
}
