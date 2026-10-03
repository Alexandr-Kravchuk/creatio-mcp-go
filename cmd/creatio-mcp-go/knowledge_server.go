package main

// Guidance, resources and prompts served from clio-knowledge bundles (task T6, decision D2). The bundle
// runtime lives in internal/knowledge; this file wires it into the MCP server: the initialize
// instructions, resources/list, resources/templates/list, resources/read, prompts/list and prompts/get.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// knowledgeClioCompatibilityVersion is the clio version this server answers bundle compatibility ranges
// with. A bundle is accepted when its "compatibility.clio" range contains it, so this must follow the clio
// release whose behavior the server reproduces; bump it together with the parity baseline.
const knowledgeClioCompatibilityVersion = "8.1.0"

// knowledgeMcpToolContractVersion is clio's MCP tool contract version (BindingsModule: new Version(1, 1, 0)).
var knowledgeMcpToolContractVersion = knowledge.Version{1, 1, 0}

var knowledgeRuntime = sync.OnceValue(func() *knowledge.Runtime {
	clioVersion, err := knowledge.ParseVersion(knowledgeClioCompatibilityVersion)
	if err != nil {
		panic(err)
	}
	tools := make(map[string]bool, len(knowledgeClioToolCatalog))
	for _, name := range knowledgeClioToolCatalog {
		tools[name] = true
	}
	return knowledge.NewRuntime(knowledge.Capabilities{
		ClioVersion: clioVersion, McpToolContractVersion: knowledgeMcpToolContractVersion, Tools: tools,
	})
})

// knowledgeServerOptions are the MCP server options clio's initialize answer carries: its instructions
// and the prompts and resources capabilities.
func knowledgeServerOptions() *mcp.ServerOptions {
	return &mcp.ServerOptions{Instructions: knowledgeServerInstructions, HasPrompts: true, HasResources: true}
}

// addKnowledgeHandlers answers the resource and prompt methods from the knowledge runtime and the prompt
// table. They are served by middleware rather than registered one by one, because the resource catalog
// follows whatever generation clio installs while this process runs.
func addKnowledgeHandlers(server *mcp.Server) {
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case "resources/list":
				params, _ := request.GetParams().(*mcp.ListResourcesParams)
				return knowledgeListResources(params)
			case "resources/templates/list":
				return &mcp.ListResourceTemplatesResult{ResourceTemplates: knowledgeResourceTemplates}, nil
			case "resources/read":
				params, _ := request.GetParams().(*mcp.ReadResourceParams)
				if params == nil {
					return nil, knowledgeWireError(-32602, "missing params")
				}
				return knowledgeReadResource(params.URI)
			case "prompts/list":
				return knowledgePromptListResult{&mcp.ListPromptsResult{Prompts: knowledgePromptList()}}, nil
			case "prompts/get":
				params, _ := request.GetParams().(*mcp.GetPromptParams)
				if params == nil {
					return nil, knowledgeWireError(-32602, "missing params")
				}
				return knowledgeGetPrompt(params.Name, params.Arguments)
			}
			return next(ctx, method, request)
		}
	})
}

// clio lists two static help resources ahead of the knowledge catalog, then pages the catalog 100 at a time
// with an opaque "clio-knowledge-v1:<offset>" cursor (KnowledgeResourceDiscoveryFilter).
const (
	knowledgeResourcePageSize     = 100
	knowledgeResourceCursorPrefix = "clio-knowledge-v1:"
)

var knowledgeStaticResources = []*mcp.Resource{
	{Name: "restart", URI: "docs://help/restart", Description: "Returns help article for : restart command", MIMEType: "application/octet-stream"},
	{Name: "flushdb", URI: "docs://help/flushdb", Description: "Returns help article for : flushdb command", MIMEType: "application/octet-stream"},
}

var knowledgeResourceTemplates = []*mcp.ResourceTemplate{
	{Name: "legacy-knowledge-reference", URITemplate: "docs://mcp/references/{guideName}/{referenceName}",
		Description: "Resolves a publisher-declared legacy reference URI from installed trusted knowledge.", MIMEType: "application/octet-stream"},
	{Name: "knowledge-library-item", URITemplate: "docs://knowledge/{libraryId}/{itemId}",
		Description: "Returns one verified knowledge item by exact trusted library ID and item ID.", MIMEType: "application/octet-stream"},
	{Name: "Help Article", URITemplate: "docs://help/command/{commandName}",
		Description: "Returns a help article by CLI command name or supported MCP tool alias.", MIMEType: "application/octet-stream"},
	{Name: "legacy-knowledge-guide", URITemplate: "docs://mcp/guides/{guideName}",
		Description: "Resolves a publisher-declared legacy guidance URI from installed trusted knowledge.", MIMEType: "application/octet-stream"},
	{Name: "legacy-nested-knowledge-guide", URITemplate: "docs://mcp/guides/{family}/{guideName}",
		Description: "Resolves a publisher-declared nested legacy guidance URI from installed trusted knowledge.", MIMEType: "application/octet-stream"},
}

func knowledgeListResources(params *mcp.ListResourcesParams) (*mcp.ListResourcesResult, error) {
	cursor := ""
	if params != nil {
		cursor = params.Cursor
	}
	catalog := knowledgeRuntime().Catalog()
	if strings.HasPrefix(cursor, knowledgeResourceCursorPrefix) {
		digits := cursor[len(knowledgeResourceCursorPrefix):]
		offset, err := strconv.Atoi(digits)
		if len(digits) != 10 || err != nil || offset < 0 || strings.ContainsAny(digits, "+-") {
			return nil, knowledgeWireError(-32602, "The knowledge resource cursor is invalid.")
		}
		return knowledgeResourcePage(catalog, offset, nil), nil
	}
	// Any other cursor reaches clio's static page, which has a single page and ignores it.
	existing := map[string]bool{}
	result := &mcp.ListResourcesResult{Resources: append([]*mcp.Resource{}, knowledgeStaticResources...)}
	for _, resource := range knowledgeStaticResources {
		existing[resource.URI] = true
	}
	page := knowledgeResourcePage(catalog, 0, existing)
	result.Resources = append(result.Resources, page.Resources...)
	result.NextCursor = page.NextCursor
	return result, nil
}

func knowledgeResourcePage(catalog []knowledge.Descriptor, offset int, existing map[string]bool) *mcp.ListResourcesResult {
	resources := []*mcp.Resource{}
	index := min(offset, len(catalog))
	for index < len(catalog) && len(resources) < knowledgeResourcePageSize {
		item := catalog[index]
		index++
		if existing[item.URI] {
			continue
		}
		resources = append(resources, &mcp.Resource{Name: item.Name, Title: item.Title, Description: item.Description,
			URI: item.URI, MIMEType: item.MediaType})
	}
	for index < len(catalog) && existing[catalog[index].URI] {
		index++
	}
	result := &mcp.ListResourcesResult{Resources: resources}
	if index < len(catalog) {
		result.NextCursor = fmt.Sprintf("%s%010d", knowledgeResourceCursorPrefix, index)
	}
	return result
}

var (
	knowledgeItemURI        = regexp.MustCompile(`^docs://knowledge/([^/]+)/([^/]+)$`)
	knowledgeLegacyGuideURI = regexp.MustCompile(`^docs://mcp/guides/([^/]+)(?:/([^/]+))?$`)
	knowledgeLegacyRefURI   = regexp.MustCompile(`^docs://mcp/references/([^/]+)/([^/]+)$`)
	knowledgeHelpCommandURI = regexp.MustCompile(`^docs://help/command/([^/]+)$`)
)

const resourceNotFoundCode = -32002

// knowledgeReadResource answers resources/read the way clio's resource classes do.
func knowledgeReadResource(uri string) (*mcp.ReadResourceResult, error) {
	switch {
	case knowledgeItemURI.MatchString(uri):
		parts := knowledgeItemURI.FindStringSubmatch(uri)
		// The SDK binds the template variables unescaped; clio re-escapes them to build the lookup URI.
		return knowledgeReadArticle("docs://knowledge/" + knowledge.EscapeDataString(knowledge.UnescapeDataString(parts[1])) +
			"/" + knowledge.EscapeDataString(knowledge.UnescapeDataString(parts[2])))
	case knowledgeLegacyGuideURI.MatchString(uri):
		parts := knowledgeLegacyGuideURI.FindStringSubmatch(uri)
		lookup := "docs://mcp/guides/" + knowledge.EscapeDataString(knowledge.UnescapeDataString(parts[1]))
		if parts[2] != "" {
			lookup += "/" + knowledge.EscapeDataString(knowledge.UnescapeDataString(parts[2]))
		}
		return knowledgeReadArticle(lookup)
	case knowledgeLegacyRefURI.MatchString(uri):
		parts := knowledgeLegacyRefURI.FindStringSubmatch(uri)
		return knowledgeReadArticle("docs://mcp/references/" + knowledge.EscapeDataString(knowledge.UnescapeDataString(parts[1])) +
			"/" + knowledge.EscapeDataString(knowledge.UnescapeDataString(parts[2])))
	case knowledgeHelpCommandURI.MatchString(uri):
		name := knowledge.UnescapeDataString(knowledgeHelpCommandURI.FindStringSubmatch(uri)[1])
		return knowledgeTextResource("docs://help/command/"+name, "text/plain", knowledgeCommandHelp(name)), nil
	case uri == "docs://help/restart" || uri == "docs://help/flushdb":
		name := strings.TrimPrefix(uri, "docs://help/")
		return knowledgeTextResource(uri, "text/plain", knowledgeStaticHelp(name)), nil
	}
	return nil, knowledgeWireError(resourceNotFoundCode, fmt.Sprintf("Unknown resource URI: '%s'", uri))
}

func knowledgeTextResource(uri, mimeType, text string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mimeType, Text: text}}}
}

// knowledgeReadArticle is clio's KnowledgeGuidanceResourceAdapter: an unknown identifier is the protocol's
// resource-not-found, an unavailable or ambiguous one an internal error.
func knowledgeReadArticle(uri string) (*mcp.ReadResourceResult, error) {
	lookup := knowledgeRuntime().FindByURI(uri)
	switch lookup.Status {
	case knowledge.LookupActive:
		return knowledgeTextResource(lookup.Article.URI, lookup.Article.MediaType, lookup.Article.Text), nil
	case knowledge.LookupUnavailable:
		return nil, knowledgeWireError(-32603, fmt.Sprintf(
			"[guidance-unavailable] Guidance '%s' is unavailable because no compatible verified knowledge bundle is active.", uri))
	case knowledge.LookupAmbiguous:
		return nil, knowledgeWireError(-32603, fmt.Sprintf(
			"[guidance-ambiguous] Guidance '%s' cannot be resolved deterministically. %s", uri, lookup.Diagnostic))
	}
	return nil, knowledgeWireError(resourceNotFoundCode, fmt.Sprintf(
		"[guidance-not-found] Unknown guidance resource '%s'. Use one of the URIs returned by resources/list.", uri))
}

// knowledgeUntrusted neutralizes text a knowledge repository can influence before it reaches an agent:
// line breaks and control characters collapse to spaces, fence tokens are defused, the length is capped
// and the result is fenced as data. It follows clio's SensitiveErrorTextRedactor.RedactUntrustedOrNull
// without its secret-scrubbing rule chain (that belongs to the shared redactor of task T4).
func knowledgeUntrusted(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var builder strings.Builder
	space := false
	for _, r := range text {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩') {
			r = ' '
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		builder.WriteRune(r)
	}
	flattened := strings.TrimSpace(builder.String())
	flattened = regexp.MustCompile(`(?i)\[\s*untrusted-source-text\s+(begin|end)\s*\]`).ReplaceAllString(flattened, "(fence removed)")
	if runes := []rune(flattened); len(runes) > 300 {
		flattened = string(runes[:300]) + "…"
	}
	return "[untrusted-source-text begin] " + flattened + " [untrusted-source-text end]"
}

func knowledgeJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// knowledgeWireError builds a JSON-RPC error with a specific code. The SDK keeps its wire error type
// internal and exports only the resource-not-found constructor, so a value of that same type is made
// through reflection and its exported Code and Message fields are set.
func knowledgeWireError(code int64, message string) error {
	template := mcp.ResourceNotFoundError("")
	value := reflect.New(reflect.TypeOf(template).Elem())
	value.Elem().FieldByName("Code").SetInt(code)
	value.Elem().FieldByName("Message").SetString(message)
	return value.Interface().(error)
}
