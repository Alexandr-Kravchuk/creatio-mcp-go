package main

// Read-only knowledge management and consent tools. Like clio's KnowledgeManagementTools they act on
// local clio-knowledge state only (clio's settings and cache), never on Creatio, so an environment
// selector is ignored rather than refused.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name":        "list-knowledge-sources",
		"description": "Lists all configured knowledge sources, including disabled sources.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, invokeListKnowledgeSources)
	registerTool(map[string]any{
		"name":        "info-knowledge",
		"description": "Reports local trusted-source and installed-generation status; set checkUpdates=true to contact configured transports.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"source":       map[string]string{"type": "string", "description": "Configured source alias; omit to inspect all configured sources."},
			"checkUpdates": map[string]string{"type": "boolean", "description": "When true, contacts configured transports to check update availability; defaults to local-only."},
		}},
	}, invokeInfoKnowledge)
	registerTool(map[string]any{
		"name":        "list-knowledge-examples",
		"description": "Lists reference examples registered in active local knowledge catalogs, including immutable repository coordinates, without cloning repositories or contacting remote services.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"source":     map[string]string{"type": "string", "description": "Configured source alias; omit to inspect every active source."},
			"search":     map[string]string{"type": "string", "description": "Case-insensitive text matched against IDs, titles, use cases, sources, and capabilities."},
			"capability": map[string]string{"type": "string", "description": "Exact supporting-capability tag; omit to include every capability."},
			"status":     map[string]string{"type": "string", "description": "Exact catalog publication status; omit to include every status."},
		}},
	}, invokeListKnowledgeExamples)
	registerTool(map[string]any{
		"name":        "get-knowledge-feedback-policy",
		"description": "Returns configured and effective knowledge-feedback mode, destination, reporting scope, current reporting-policy hash, standing approval, and any reason auto approval is stale.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, invokeGetKnowledgeFeedbackPolicy)
	registerTool(map[string]any{
		"name":        "get-telemetry-consent",
		"description": "Reads locally persisted product telemetry consent (granted, denied, or unknown) without storing any telemetry event.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, invokeGetTelemetryConsent)
}

// knowledgeSourceInfo is clio's KnowledgeSourceInfo; null members are omitted as clio's serializer does.
type knowledgeSourceInfo struct {
	Alias                string  `json:"alias"`
	LibraryID            string  `json:"libraryId"`
	TransportType        string  `json:"transportType"`
	Location             string  `json:"location"`
	TrustedKeyID         string  `json:"trustedKeyId,omitempty"`
	TrustedPublicKeyPath string  `json:"trustedPublicKeyPath,omitempty"`
	Enabled              bool    `json:"enabled"`
	Priority             int     `json:"priority"`
	Participation        string  `json:"participation"`
	PackageID            string  `json:"packageId,omitempty"`
	RepositoryOwner      string  `json:"repositoryOwner,omitempty"`
	RepositoryName       string  `json:"repositoryName,omitempty"`
	AssetName            string  `json:"assetName,omitempty"`
	Branch               string  `json:"branch,omitempty"`
	Tag                  string  `json:"tag,omitempty"`
	Commit               string  `json:"commit,omitempty"`
	IsInstalled          bool    `json:"isInstalled"`
	IsValid              bool    `json:"isValid"`
	ActiveLibraryVersion string  `json:"activeLibraryVersion,omitempty"`
	ActiveSequence       *uint64 `json:"activeSequence,omitempty"`
	BundleDigest         string  `json:"bundleDigest,omitempty"`
	ResolvedRevision     string  `json:"resolvedRevision,omitempty"`
	ActiveContentPath    string  `json:"activeContentPath,omitempty"`
	UpdateAvailability   string  `json:"updateAvailability,omitempty"`
	Diagnostic           string  `json:"diagnostic,omitempty"`
}

func configuredSourceInfo(alias string, source knowledge.SourceConfig) knowledgeSourceInfo {
	return knowledgeSourceInfo{Alias: alias, LibraryID: source.LibraryID, TransportType: string(source.Type),
		Location: source.Location, TrustedKeyID: source.TrustedKeyID, TrustedPublicKeyPath: source.TrustedPublicKeyPath,
		Enabled: source.Enabled, Priority: source.Priority, Participation: string(source.Participation),
		PackageID: source.PackageID, RepositoryOwner: source.RepositoryOwner, RepositoryName: source.RepositoryName,
		AssetName: source.AssetName, Branch: source.Branch, Tag: source.Tag, Commit: source.Commit}
}

type knowledgeSourceListResult struct {
	Success    bool                  `json:"success"`
	Sources    []knowledgeSourceInfo `json:"sources"`
	Diagnostic string                `json:"diagnostic,omitempty"`
}

func invokeListKnowledgeSources(_ context.Context, _ *environments, _ map[string]any) (*mcp.CallToolResult, error) {
	settings, err := knowledgeRuntime().LoadSettings()
	if err != nil {
		return structuredToolResult(knowledgeSourceListResult{Sources: []knowledgeSourceInfo{}, Diagnostic: err.Error()}), nil
	}
	sources := []knowledgeSourceInfo{}
	for _, alias := range settings.Knowledge.SortedAliases() {
		sources = append(sources, configuredSourceInfo(alias, settings.Knowledge.Sources[alias]))
	}
	return structuredToolResult(knowledgeSourceListResult{Success: true, Sources: sources}), nil
}

type knowledgeInfoResult struct {
	Success          bool                  `json:"success"`
	SettingsFilePath string                `json:"settingsFilePath"`
	RootPath         string                `json:"rootPath"`
	Sources          []knowledgeSourceInfo `json:"sources"`
	Diagnostic       string                `json:"diagnostic,omitempty"`
}

func invokeInfoKnowledge(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	sourceAlias, err := optionalStringArg(args, "info-knowledge", "source")
	if err != nil {
		return nil, err
	}
	checkUpdates := false
	if value, ok := args["checkUpdates"]; ok && value != nil {
		flag, isBool := value.(bool)
		if !isBool {
			return nil, errors.New("invalid-parameter-type: argument 'checkUpdates' for MCP tool 'info-knowledge' must be a boolean. Received an incompatible JSON value.")
		}
		checkUpdates = flag
	}
	runtime := knowledgeRuntime()
	settings, err := runtime.LoadSettings()
	if err != nil {
		return nil, err
	}
	root, err := knowledge.ResolveRoot(settings.Knowledge.RootPath)
	if err != nil {
		return nil, err
	}
	result := knowledgeInfoResult{SettingsFilePath: settings.Path, RootPath: root, Sources: []knowledgeSourceInfo{}}
	var aliases []string
	if _, hasSource := args["source"]; hasSource && args["source"] != nil {
		configured, _, ok := settings.Knowledge.FindSource(sourceAlias)
		if !ok {
			result.Diagnostic = "Knowledge source '" + sourceAlias + "' is not configured."
			return structuredToolResult(result), nil
		}
		aliases = []string{configured}
	} else {
		aliases = settings.Knowledge.SortedAliases()
		if len(aliases) == 0 {
			result.Diagnostic = "No knowledge sources are configured."
			return structuredToolResult(result), nil
		}
	}
	store := knowledge.Store{Root: root}
	for _, alias := range aliases {
		result.Sources = append(result.Sources, knowledgeBuildInfo(runtime, store, alias, settings.Knowledge.Sources[alias], checkUpdates))
	}
	result.Success = true
	return structuredToolResult(result), nil
}

// knowledgeBuildInfo is KnowledgeSourceManagementService.BuildInfo for installed bundles. A Git source is
// reported from its configuration only: this server does not read repository checkouts.
func knowledgeBuildInfo(runtime *knowledge.Runtime, store knowledge.Store, alias string, source knowledge.SourceConfig, checkUpdates bool) knowledgeSourceInfo {
	info := configuredSourceInfo(alias, source)
	info.UpdateAvailability = "unknown"
	if source.Type == knowledge.SourceGit {
		info.Diagnostic = knowledgeUntrusted("Git knowledge sources are not read by creatio-mcp-go.")
		return info
	}
	current, diagnostic := store.ReadCurrent(alias)
	if current == nil {
		if diagnostic == "" {
			info.UpdateAvailability = "not-installed"
		}
		info.Diagnostic = knowledgeUntrusted(diagnostic)
		return info
	}
	info.IsInstalled = true
	contentRoot, data, readDiagnostic := store.ReadCandidate(alias, current.Active)
	if readDiagnostic != "" {
		diagnostic = readDiagnostic
	} else {
		prepared, rejection := runtime.Verify(data, current.Active.LibraryVersion, source.LibraryID)
		info.IsValid = rejection == nil && prepared.Sequence == current.Active.Sequence && prepared.LibraryID == source.LibraryID
		info.ActiveContentPath = contentRoot
		if rejection != nil {
			diagnostic = rejection.Message
		}
	}
	sequence := current.Active.Sequence
	info.ActiveLibraryVersion = current.Active.LibraryVersion
	info.ActiveSequence = &sequence
	info.BundleDigest = current.Active.BundleDigest
	info.ResolvedRevision = current.Active.ResolvedRevision
	if checkUpdates && source.Enabled {
		availability, probeDiagnostic := knowledgeProbeUpdate(runtime, alias, source, current)
		info.UpdateAvailability = availability
		if diagnostic == "" {
			diagnostic = probeDiagnostic
		}
	}
	info.Diagnostic = knowledgeUntrusted(diagnostic)
	return info
}

func invokeListKnowledgeExamples(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	var query knowledge.ExampleQuery
	for name, target := range map[string]*string{"source": &query.Source, "search": &query.Search, "capability": &query.Capability, "status": &query.Status} {
		value, err := optionalStringArg(args, "list-knowledge-examples", name)
		if err != nil {
			return nil, err
		}
		*target = value
	}
	return structuredToolResult(knowledgeRuntime().ListExamples(query, knowledgeUntrusted)), nil
}

type knowledgeFeedbackPolicyResult = knowledge.FeedbackPolicy

func invokeGetKnowledgeFeedbackPolicy(_ context.Context, _ *environments, _ map[string]any) (*mcp.CallToolResult, error) {
	var policy knowledgeFeedbackPolicyResult = knowledgeRuntime().FeedbackPolicy()
	return structuredToolResult(policy), nil
}

type telemetryConsentResult struct {
	Success          bool   `json:"success"`
	Status           string `json:"status"`
	TelemetryConsent string `json:"telemetry_consent"`
}

// invokeGetTelemetryConsent reads <clio home>/telemetry/consent.json like clio's TelemetryService.
func invokeGetTelemetryConsent(_ context.Context, _ *environments, _ map[string]any) (*mcp.CallToolResult, error) {
	consent := "unknown"
	if data, err := os.ReadFile(filepath.Join(knowledge.ClioHome(), "telemetry", "consent.json")); err == nil {
		var state struct {
			TelemetryConsent string `json:"telemetry_consent"`
		}
		if json.Unmarshal(data, &state) == nil {
			consent = state.TelemetryConsent
		}
	}
	switch consent {
	case "granted", "denied":
		return structuredToolResult(telemetryConsentResult{Success: true, Status: "known", TelemetryConsent: consent}), nil
	}
	return structuredToolResult(telemetryConsentResult{Success: true, Status: "unknown", TelemetryConsent: "unknown"}), nil
}
