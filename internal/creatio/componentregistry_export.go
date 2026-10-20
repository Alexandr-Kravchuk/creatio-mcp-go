package creatio

// export-component-registry, ported from clio 8.1.0.142's ExportComponentRegistryCommand: the registry the
// get-component-info transport serves (local override, clio's shared cache, CDN, 'latest' fallback) is
// written byte-for-byte to a file, and only the path and a few counters are answered.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// ComponentRegistryExportResponse is clio's ExportComponentRegistryResponse.
type ComponentRegistryExportResponse struct {
	Success                     bool    `json:"success"`
	OutputFile                  *string `json:"outputFile,omitempty"`
	ResolvedTargetVersion       *string `json:"resolvedTargetVersion,omitempty"`
	ResolvedFrom                *string `json:"resolvedFrom,omitempty"`
	ResolvedFromReason          *string `json:"resolvedFromReason,omitempty"`
	RequiresVersionConfirmation bool    `json:"requiresVersionConfirmation"`
	VersionWarning              *string `json:"versionWarning,omitempty"`
	SchemaTypeWarning           *string `json:"schemaTypeWarning,omitempty"`
	ComponentCount              int     `json:"componentCount"`
	CompositeCount              int     `json:"compositeCount"`
	InputCount                  int     `json:"inputCount"`
	Error                       *string `json:"error,omitempty"`
}

// ComponentRegistryExportRequest carries the tool's arguments; Resolve yields the environment's client.
type ComponentRegistryExportRequest struct {
	Version         string
	SchemaType      string
	OutputFile      string
	EnvironmentName string
	URI             string
	Resolve         func() (*Client, error)
}

// ComponentRegistryExportFailure is the tool's failure envelope (the message is redacted, as clio's tool does).
func ComponentRegistryExportFailure(message string, schemaTypeWarning *string) ComponentRegistryExportResponse {
	text := redact.Text(message)
	return ComponentRegistryExportResponse{Error: &text, SchemaTypeWarning: schemaTypeWarning}
}

// ExportComponentRegistry is clio's tool body: validation, version resolution, fetch, count, write.
func ExportComponentRegistry(ctx context.Context, request ComponentRegistryExportRequest) (ComponentRegistryExportResponse, error) {
	mobile, schemaWarning := componentInfoResolveSchemaType(request.SchemaType)
	hasVersion := !componentInfoBlank(request.Version)
	hasEnvironment := !componentInfoBlank(request.EnvironmentName) || !componentInfoBlank(request.URI)
	if hasVersion && hasEnvironment {
		return ComponentRegistryExportFailure("'version' and 'environment-name'/'uri' are mutually exclusive. Pass one or neither.", schemaWarning), nil
	}
	if _, ok := componentInfoThreePartVersion(request.Version); hasVersion && !ok {
		return ComponentRegistryExportFailure(fmt.Sprintf("'version' value '%s' is not a valid platform version. Use a 3-part semver, for example '8.3.3'.",
			request.Version), schemaWarning), nil
	}
	resolution, err := componentInfoResolveVersion(ctx, ComponentInfoRequest{Version: request.Version,
		EnvironmentName: request.EnvironmentName, URI: request.URI, Resolve: request.Resolve}, hasVersion, hasEnvironment)
	if err != nil {
		if ctx.Err() != nil {
			return ComponentRegistryExportResponse{}, ctx.Err()
		}
		return ComponentRegistryExportFailure(err.Error(), schemaWarning), nil
	}
	requested := strings.TrimSpace(resolution.version)
	if normalised, ok := componentInfoThreePartVersion(resolution.version); ok {
		requested = normalised
	}
	flavor := componentInfoWebFlavor
	if mobile {
		flavor = componentInfoMobileFlavor
	}
	fetched, err := componentInfoFetchRegistry(ctx, flavor, requested)
	if err != nil {
		if ctx.Err() != nil {
			return ComponentRegistryExportResponse{}, ctx.Err()
		}
		return ComponentRegistryExportFailure(err.Error(), schemaWarning), nil
	}
	resolution.version = requested
	markers := componentInfoMarkersFor(resolution, fetched.resolvedVersion)
	components, composites, inputs, err := componentRegistryCounts(fetched.content)
	if err != nil {
		return ComponentRegistryExportFailure(err.Error(), schemaWarning), nil
	}
	path, failure := componentRegistryWrite(request.OutputFile, fetched.resolvedVersion, flavor, fetched.content)
	if failure != "" {
		return ComponentRegistryExportFailure(failure, schemaWarning), nil
	}
	var shaped ComponentInfoResponse
	markers.apply(&shaped)
	confirm := shaped.RequiresVersionConfirmation != nil && *shaped.RequiresVersionConfirmation
	return ComponentRegistryExportResponse{Success: true, OutputFile: &path, ResolvedTargetVersion: shaped.ResolvedTargetVersion,
		ResolvedFrom: shaped.ResolvedFrom, ResolvedFromReason: shaped.ResolvedFromReason, RequiresVersionConfirmation: confirm,
		VersionWarning: shaped.VersionWarning, SchemaTypeWarning: schemaWarning, ComponentCount: components,
		CompositeCount: composites, InputCount: inputs}, nil
}

// componentRegistryCounts is clio's CountEntries.
func componentRegistryCounts(content []byte) (int, int, int, error) {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(content, []byte("\xEF\xBB\xBF"))))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return 0, 0, 0, err
	}
	var components []any
	kind := "Undefined"
	switch typed := root.(type) {
	case []any:
		components, kind = typed, "Array"
	case map[string]any:
		kind = "Object"
		if list, ok := typed["components"].([]any); ok {
			components = list
		}
	case string:
		kind = "String"
	case json.Number:
		kind = "Number"
	case bool:
		kind = map[bool]string{true: "True", false: "False"}[typed]
	case nil:
		kind = "Null"
	}
	if components == nil {
		return 0, 0, 0, fmt.Errorf("The fetched payload is not a component registry: its root is %s and no "+
			"'components' array was found, but a top-level array or an object with a 'components' array was "+
			"expected. Nothing was written.", kind)
	}
	composites := 0
	if object, ok := root.(map[string]any); ok {
		if list, ok := object["composites"].([]any); ok {
			composites = len(list)
		}
	}
	inputs := 0
	for _, component := range components {
		object, _ := component.(map[string]any)
		count := 0
		if members, ok := object["inputs"].(map[string]any); ok {
			count = len(members)
		}
		if count == 0 {
			if members, ok := object["properties"].(map[string]any); ok {
				count = len(members)
			}
		}
		inputs += count
	}
	return len(components), composites, inputs, nil
}

func componentRegistrySafeSegment(value string) bool {
	if strings.TrimSpace(value) == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// componentRegistryWrite is clio's WriteRegistryAsync: an explicit output-file is confined and never
// replaced; the default <anchor>/.clio-migration/component-registry/[mobile/]<version>.json is replaced
// atomically on a repeat run.
func componentRegistryWrite(explicit, resolvedVersion string, flavor componentInfoFlavor, content []byte) (string, string) {
	if strings.TrimSpace(explicit) != "" {
		path, refusal := schemaGetResolveOutput(explicit)
		if refusal != "" {
			return "", refusal
		}
		if err := schemaGetWriteAtomic(path, content); err != nil {
			return "", err.Error()
		}
		return path, ""
	}
	if !componentRegistrySafeSegment(resolvedVersion) {
		return "", fmt.Sprintf("The registry reported version '%s', which is not usable as a file name. "+
			"Pass an explicit --output-file to choose the destination yourself.", resolvedVersion)
	}
	anchor, err := pageOutputAnchor("")
	if err != nil {
		return "", err.Error()
	}
	root := filepath.Join(anchor, ".clio-migration", "component-registry", flavor.cacheSubdir)
	path := filepath.Join(root, resolvedVersion+".json")
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(root)+string(filepath.Separator)) {
		return "", fmt.Sprintf("The registry reported version '%s', which resolves outside the tool-owned "+
			"output directory. Pass an explicit --output-file to choose the destination yourself.", resolvedVersion)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return "", err.Error()
	}
	temporary := path + "." + randomHex(16) + ".tmp"
	if err := os.WriteFile(temporary, content, 0o666); err != nil {
		_ = os.Remove(temporary)
		return "", err.Error()
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", err.Error()
	}
	return path, ""
}
