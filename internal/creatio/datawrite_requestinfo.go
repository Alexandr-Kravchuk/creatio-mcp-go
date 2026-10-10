package creatio

// get-request-info: clio's RequestInfoTool over RequestInfoCatalog. The request registry (RequestRegistry.json
// or MobileRequestRegistry.json) comes through the same local-file / cache / CDN chain as get-component-info,
// with the same version resolution and markers; list, detail and not-found answers follow clio's shapes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

var (
	dataWriteRequestsFlavor       = componentInfoFlavor{"requests", "RequestRegistry.json", "CLIO_REQUEST_REGISTRY_LOCAL_FILE", "requests"}
	dataWriteMobileRequestsFlavor = componentInfoFlavor{"mobile-requests", "MobileRequestRegistry.json", "CLIO_MOBILE_REQUEST_REGISTRY_LOCAL_FILE", "mobile-requests"}
	// dataWriteRequestWiringSeed seeds the type closure with the wiring contract every request binding uses.
	dataWriteRequestWiringSeed = componentInfoBindings{{"binding", json.RawMessage(`{"type":"RequestBindingConfig"}`)}}
)

// DataWriteRequestInfoResponse is clio's RequestInfoResponse, in its serialization order.
type DataWriteRequestInfoResponse struct {
	Success                     bool                            `json:"success"`
	Mode                        string                          `json:"mode"`
	Count                       int                             `json:"count"`
	Error                       *string                         `json:"error,omitempty"`
	RequestType                 *string                         `json:"requestType,omitempty"`
	Description                 *string                         `json:"description,omitempty"`
	Deprecated                  *bool                           `json:"deprecated,omitempty"`
	DeprecationReason           *string                         `json:"deprecationReason,omitempty"`
	Parameters                  *componentInfoBindings          `json:"parameters,omitempty"`
	BaseParameters              componentInfoBindings           `json:"baseParameters,omitempty"`
	References                  *ComponentInfoReferences        `json:"references,omitempty"`
	Items                       *[]DataWriteRequestInfoListItem `json:"items,omitempty"`
	Documentation               *string                         `json:"documentation,omitempty"`
	DocumentationUnavailable    *bool                           `json:"documentationUnavailable,omitempty"`
	DocumentationSource         *string                         `json:"documentationSource,omitempty"`
	DocumentationWarning        *string                         `json:"documentationWarning,omitempty"`
	ResolvedTargetVersion       *string                         `json:"resolvedTargetVersion,omitempty"`
	ResolvedFrom                *string                         `json:"resolvedFrom,omitempty"`
	VersionWarning              *string                         `json:"versionWarning,omitempty"`
	SchemaTypeWarning           *string                         `json:"schemaTypeWarning,omitempty"`
	RequiresVersionConfirmation *bool                           `json:"requiresVersionConfirmation,omitempty"`
	ResolvedFromReason          *string                         `json:"resolvedFromReason,omitempty"`
}

// DataWriteRequestInfoListItem is clio's RequestInfoListItem.
type DataWriteRequestInfoListItem struct {
	RequestType       string  `json:"requestType"`
	Description       *string `json:"description,omitempty"`
	Deprecated        *bool   `json:"deprecated,omitempty"`
	DeprecationReason *string `json:"deprecationReason,omitempty"`
}

// DataWriteRequestInfoRequest carries the tool's arguments; Resolve returns the client for environment-name/uri.
type DataWriteRequestInfoRequest struct {
	RequestType     string
	Search          string
	SchemaType      string
	Version         string
	EnvironmentName string
	URI             string
	Resolve         func() (*Client, error)
}

type dataWriteRequestEntry struct {
	RequestType       string                 `json:"requestType"`
	Description       string                 `json:"description"`
	Parameters        *componentInfoBindings `json:"parameters"`
	Deprecated        *bool                  `json:"deprecated"`
	DeprecationReason *string                `json:"deprecationReason"`
	References        *struct {
		Docs            []string              `json:"docs"`
		TypeDefinitions componentInfoBindings `json:"typeDefinitions"`
	} `json:"references"`
}

type dataWriteRequestGlobal struct {
	BaseParameters  componentInfoBindings `json:"baseParameters"`
	TypeDefinitions componentInfoBindings `json:"typeDefinitions"`
}

type dataWriteRequestCatalog struct {
	entries         []*dataWriteRequestEntry
	lookup          map[string]*dataWriteRequestEntry
	global          *dataWriteRequestGlobal
	resolvedVersion string
}

// DataWriteRequestInfoFailure is clio's CreateErrorResponse: a list-shaped failure.
func DataWriteRequestInfoFailure(message string) DataWriteRequestInfoResponse {
	return DataWriteRequestInfoResponse{Success: false, Mode: "list", Error: &message, Items: &[]DataWriteRequestInfoListItem{}}
}

// DataWriteRequestInfoGet answers get-request-info as clio's tool does: a failure it catches is reported
// redacted in the list-shaped envelope, with the schema-type warning kept.
func DataWriteRequestInfoGet(ctx context.Context, request DataWriteRequestInfoRequest) DataWriteRequestInfoResponse {
	mobile, schemaWarning := componentInfoResolveSchemaType(request.SchemaType)
	response, err := dataWriteRequestInfoBuild(ctx, request, mobile)
	if err != nil {
		response = DataWriteRequestInfoFailure(redact.Text(err.Error()))
	}
	response.SchemaTypeWarning = schemaWarning
	return response
}

func dataWriteRequestInfoBuild(ctx context.Context, request DataWriteRequestInfoRequest, mobile bool) (DataWriteRequestInfoResponse, error) {
	hasVersion := !componentInfoBlank(request.Version)
	hasEnvironment := !componentInfoBlank(request.EnvironmentName) || !componentInfoBlank(request.URI)
	if hasVersion && hasEnvironment {
		return DataWriteRequestInfoFailure("'version' and 'environment-name'/'uri' are mutually exclusive. Pass one or neither."), nil
	}
	if _, ok := componentInfoThreePartVersion(request.Version); hasVersion && !ok {
		return DataWriteRequestInfoFailure(fmt.Sprintf("'version' value '%s' is not a valid platform version. Use a 3-part semver, for example '8.3.3'.",
			request.Version)), nil
	}
	resolution, err := componentInfoResolveVersion(ctx, ComponentInfoRequest{Version: request.Version, Resolve: request.Resolve}, hasVersion, hasEnvironment)
	if err != nil {
		return DataWriteRequestInfoResponse{}, err
	}
	flavor := dataWriteRequestsFlavor
	if mobile {
		flavor = dataWriteMobileRequestsFlavor
	}
	fetched, err := componentInfoFetchRegistry(ctx, flavor, strings.TrimSpace(resolution.version))
	if err != nil {
		return DataWriteRequestInfoResponse{}, err
	}
	catalog, err := dataWriteParseRequestCatalog(fetched.content, fetched.resolvedVersion)
	if err != nil {
		return DataWriteRequestInfoResponse{}, err
	}
	markers := componentInfoMarkersFor(resolution, catalog.resolvedVersion)
	if componentInfoBlank(request.RequestType) || strings.EqualFold(request.RequestType, "list") {
		filtered := dataWriteFilterRequests(catalog.entries, request.Search)
		items := dataWriteRequestItems(filtered)
		response := DataWriteRequestInfoResponse{Success: true, Mode: "list", Count: len(filtered), Items: &items}
		dataWriteApplyMarkers(markers, &response)
		return response, nil
	}
	requested := strings.TrimSpace(request.RequestType)
	if entry, ok := catalog.lookup[componentInfoFold(requested)]; ok {
		var docs []string
		var local componentInfoBindings
		if entry.References != nil {
			docs, local = entry.References.Docs, entry.References.TypeDefinitions
		}
		documentation := componentInfoLoadDocumentation(ctx, docs, catalog.resolvedVersion)
		var global componentInfoBindings
		var base componentInfoBindings
		if catalog.global != nil {
			global, base = catalog.global.TypeDefinitions, catalog.global.BaseParameters
		}
		var parameters componentInfoBindings
		if entry.Parameters != nil {
			parameters = *entry.Parameters
		}
		response := DataWriteRequestInfoResponse{Success: true, Mode: "detail", Count: 1, RequestType: &entry.RequestType,
			Description: componentInfoNonBlank(entry.Description), Deprecated: entry.Deprecated,
			DeprecationReason: componentInfoNonBlankPtr(entry.DeprecationReason), Parameters: entry.Parameters}
		if len(base) > 0 {
			response.BaseParameters = base
		}
		if closure := componentInfoTypeClosure(parameters, dataWriteRequestWiringSeed, local, global); closure != nil {
			response.References = &ComponentInfoReferences{TypeDefinitions: closure}
		}
		response.Documentation = documentation.text
		response.DocumentationSource = documentation.source
		response.DocumentationWarning = documentation.warning
		if len(docs) > 0 && documentation.text == nil {
			unavailable := true
			response.DocumentationUnavailable = &unavailable
		}
		dataWriteApplyMarkers(markers, &response)
		return response, nil
	}
	nameMatches := dataWriteFilterRequests(catalog.entries, requested)
	var suggestions []*dataWriteRequestEntry
	var message string
	if len(nameMatches) > 0 {
		suggestions = nameMatches
		if len(suggestions) > componentInfoMaxNotFoundSuggestions {
			suggestions = suggestions[:componentInfoMaxNotFoundSuggestions]
		}
		message = fmt.Sprintf("'%s' is not a request type. Showing %d request(s) matching '%s' by name/description — pass the correct "+
			"requestType, or omit 'request-type' to list the full catalog.", requested, len(suggestions), requested)
	} else {
		suggestions = dataWriteSuggestRequests(catalog.entries, requested, request.Search)
		message = fmt.Sprintf("'%s' is not a request type. Showing the %d closest known type(s) — pass the correct requestType, "+
			"or omit 'request-type' to list the full catalog.", requested, len(suggestions))
	}
	items := dataWriteRequestItems(suggestions)
	response := DataWriteRequestInfoResponse{Success: false, Mode: "list", Error: &message, Count: len(suggestions), Items: &items}
	dataWriteApplyMarkers(markers, &response)
	return response, nil
}

func dataWriteApplyMarkers(markers componentInfoMarkers, response *DataWriteRequestInfoResponse) {
	var probe ComponentInfoResponse
	markers.apply(&probe)
	response.ResolvedTargetVersion, response.ResolvedFrom, response.VersionWarning = probe.ResolvedTargetVersion, probe.ResolvedFrom, probe.VersionWarning
	response.RequiresVersionConfirmation, response.ResolvedFromReason = probe.RequiresVersionConfirmation, probe.ResolvedFromReason
}

// dataWriteParseRequestCatalog is RequestInfoCatalog.LoadFromStream: the wrapped envelope only, duplicate
// request types refused, blank ones dropped, the rest ordered by type ignoring case.
func dataWriteParseRequestCatalog(payload []byte, resolvedVersion string) (*dataWriteRequestCatalog, error) {
	const source = "Request registry stream"
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil || root == nil {
		if err != nil && !strings.HasPrefix(strings.TrimSpace(string(payload)), "{") {
			return nil, fmt.Errorf("%s must be an object with a 'requests' array.", source)
		}
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s must be an object with a 'requests' array.", source)
	}
	requests, ok := root["requests"]
	if !ok || !strings.HasPrefix(strings.TrimSpace(string(requests)), "[") {
		return nil, fmt.Errorf("%s must be an object with a 'requests' array.", source)
	}
	var envelope struct {
		Requests   []*dataWriteRequestEntry `json:"requests"`
		References *dataWriteRequestGlobal  `json:"references"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Requests) == 0 {
		return nil, fmt.Errorf("%s is empty or invalid.", source)
	}
	valid := []*dataWriteRequestEntry{}
	for _, entry := range envelope.Requests {
		if entry != nil && !componentInfoBlank(entry.RequestType) {
			valid = append(valid, entry)
		}
	}
	if duplicates := componentInfoDuplicates(valid, func(entry *dataWriteRequestEntry) string { return entry.RequestType }); len(duplicates) > 0 {
		return nil, fmt.Errorf("%s contains duplicate request types: %s.", source, strings.Join(duplicates, ", "))
	}
	if len(valid) == 0 {
		return nil, errors.New(source + " does not contain valid request types.")
	}
	componentInfoSortStable(valid, func(entry *dataWriteRequestEntry) string { return entry.RequestType })
	catalog := &dataWriteRequestCatalog{entries: valid, lookup: map[string]*dataWriteRequestEntry{}, global: envelope.References, resolvedVersion: resolvedVersion}
	for _, entry := range valid {
		catalog.lookup[componentInfoFold(entry.RequestType)] = entry
	}
	return catalog, nil
}

// dataWriteFilterRequests is RequestInfoTool.FilterEntries: request type, description, parameter keys and the
// type/description/enum values of each parameter, compared ignoring case.
func dataWriteFilterRequests(entries []*dataWriteRequestEntry, search string) []*dataWriteRequestEntry {
	if componentInfoBlank(search) {
		return entries
	}
	query := strings.TrimSpace(search)
	result := []*dataWriteRequestEntry{}
	for _, entry := range entries {
		if componentInfoContains(entry.RequestType, query) || componentInfoContains(entry.Description, query) || dataWriteParametersMatch(entry.Parameters, query) {
			result = append(result, entry)
		}
	}
	return result
}

func dataWriteParametersMatch(parameters *componentInfoBindings, query string) bool {
	if parameters == nil {
		return false
	}
	for _, parameter := range *parameters {
		if componentInfoContains(parameter.Key, query) {
			return true
		}
		var schema map[string]any
		if json.Unmarshal(parameter.Value, &schema) != nil || schema == nil {
			continue
		}
		for _, field := range []string{"type", "description"} {
			if text, ok := schema[field].(string); ok && componentInfoContains(text, query) {
				return true
			}
		}
		if values, ok := schema["values"].([]any); ok {
			for _, value := range values {
				if text, ok := value.(string); ok && componentInfoContains(text, query) {
					return true
				}
			}
		}
	}
	return false
}

// dataWriteSuggestRequests is SuggestForUnknown: the closest types by edit distance (ignoring case), ties
// broken alphabetically, at most eight.
func dataWriteSuggestRequests(entries []*dataWriteRequestEntry, requestType, search string) []*dataWriteRequestEntry {
	pool := dataWriteFilterRequests(entries, search)
	target := strings.TrimSpace(requestType)
	ordered := append([]*dataWriteRequestEntry{}, pool...)
	distance := map[*dataWriteRequestEntry]int{}
	for _, entry := range ordered {
		distance[entry] = componentInfoLevenshtein(entry.RequestType, target)
	}
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0; j-- {
			left, right := ordered[j-1], ordered[j]
			if distance[right] < distance[left] || (distance[right] == distance[left] && componentInfoCompare(right.RequestType, left.RequestType) < 0) {
				ordered[j-1], ordered[j] = right, left
				continue
			}
			break
		}
	}
	if len(ordered) > componentInfoMaxNotFoundSuggestions {
		ordered = ordered[:componentInfoMaxNotFoundSuggestions]
	}
	return ordered
}

func dataWriteRequestItems(entries []*dataWriteRequestEntry) []DataWriteRequestInfoListItem {
	ordered := append([]*dataWriteRequestEntry{}, entries...)
	componentInfoSortStable(ordered, func(entry *dataWriteRequestEntry) string { return entry.RequestType })
	items := make([]DataWriteRequestInfoListItem, 0, len(ordered))
	for _, entry := range ordered {
		items = append(items, DataWriteRequestInfoListItem{RequestType: entry.RequestType, Description: componentInfoNonBlank(entry.Description),
			Deprecated: entry.Deprecated, DeprecationReason: componentInfoNonBlankPtr(entry.DeprecationReason)})
	}
	return items
}
