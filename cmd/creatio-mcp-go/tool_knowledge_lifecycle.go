package main

// The knowledge write tools of clio 8.1.0.142's KnowledgeManagementTools and KnowledgeFeedbackPolicyTools:
// install-, update-, delete-knowledge, add-, remove-, enable-, disable-knowledge-source and
// configure-knowledge-feedback-policy. Like clio they act on local clio state only (appsettings.json and
// the installation store), so an environment selector is ignored rather than refused. All writes go
// through internal/knowledge, which takes clio's own lock files.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var knowledgeManager = sync.OnceValue(func() *knowledge.Manager {
	runtime := knowledgeRuntime()
	return knowledge.NewManager("", runtime.Verify, runtime.InvalidateSettings)
})

func init() {
	registerTool(map[string]any{"name": "install-knowledge"}, invokeInstallKnowledge,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true, OpenWorld: true}))
	registerTool(map[string]any{"name": "update-knowledge"}, invokeUpdateKnowledge,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true, OpenWorld: true}))
	registerTool(map[string]any{"name": "delete-knowledge"}, invokeDeleteKnowledge,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
	registerTool(map[string]any{"name": "add-knowledge-source"}, invokeAddKnowledgeSource,
		withAnnotations(toolAnnotations{Destructive: true, OpenWorld: true}))
	registerTool(map[string]any{"name": "remove-knowledge-source"}, invokeRemoveKnowledgeSource,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
	registerTool(map[string]any{"name": "enable-knowledge-source"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return invokeSetKnowledgeSourceEnabled(args, "enable-knowledge-source", true)
	}, withAnnotations(toolAnnotations{Idempotent: true}))
	registerTool(map[string]any{"name": "disable-knowledge-source"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return invokeSetKnowledgeSourceEnabled(args, "disable-knowledge-source", false)
	}, withAnnotations(toolAnnotations{Idempotent: true}))
	registerTool(map[string]any{"name": "configure-knowledge-feedback-policy"}, invokeConfigureKnowledgeFeedbackPolicy,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

// knowledgeStringArg binds an optional string as clio's binder does: absent or null is nil.
func knowledgeStringArg(args map[string]any, tool, name string) (*string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a string. Received an incompatible JSON value.", name, tool)
	}
	return &text, nil
}

func knowledgeText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// knowledgeBoolArg binds a bool; absent or null is fallback.
func knowledgeBoolArg(args map[string]any, tool, name string, fallback bool) (bool, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return fallback, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
	}
	return flag, nil
}

// knowledgeIntArg binds a 32-bit integer; absent or null is fallback.
func knowledgeIntArg(args map[string]any, tool, name string, fallback int) (int, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < math.MinInt32 || number > math.MaxInt32 {
		return 0, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a number. Received an incompatible JSON value.", name, tool)
	}
	return int(number), nil
}

func invokeInstallKnowledge(ctx context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	source, err := knowledgeStringArg(args, "install-knowledge", "source")
	if err != nil {
		return nil, err
	}
	result, err := knowledgeManager().Install(ctx, source)
	if err != nil {
		return nil, err
	}
	return structuredToolResult(result), nil
}

func invokeUpdateKnowledge(ctx context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	source, err := knowledgeStringArg(args, "update-knowledge", "source")
	if err != nil {
		return nil, err
	}
	result, err := knowledgeManager().Update(ctx, source)
	if err != nil {
		return nil, err
	}
	return structuredToolResult(result), nil
}

func invokeDeleteKnowledge(ctx context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	source, err := knowledgeStringArg(args, "delete-knowledge", "source")
	if err != nil {
		return nil, err
	}
	confirmed, err := knowledgeBoolArg(args, "delete-knowledge", "confirmed", false)
	if err != nil {
		return nil, err
	}
	result, err := knowledgeManager().Delete(ctx, source, confirmed)
	if err != nil {
		return nil, err
	}
	return structuredToolResult(result), nil
}

func invokeAddKnowledgeSource(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "add-knowledge-source"
	bindings := map[string]**string{}
	var request knowledge.AddRequest
	var alias, libraryID, sourceType, location, participation *string
	for name, target := range map[string]**string{"alias": &alias, "libraryId": &libraryID, "type": &sourceType,
		"location": &location, "trustedKeyId": &request.TrustedKeyID, "trustedPublicKeyPath": &request.TrustedPublicKeyPath,
		"packageId": &request.PackageID, "repositoryOwner": &request.RepositoryOwner, "repositoryName": &request.RepositoryName,
		"assetName": &request.AssetName, "branch": &request.Branch, "tag": &request.Tag, "commit": &request.Commit,
		"participation": &participation} {
		bindings[name] = target
	}
	for _, name := range []string{"alias", "libraryId", "type", "location", "trustedKeyId", "trustedPublicKeyPath",
		"packageId", "repositoryOwner", "repositoryName", "assetName", "branch", "tag", "commit", "participation"} {
		value, err := knowledgeStringArg(args, tool, name)
		if err != nil {
			return nil, err
		}
		*bindings[name] = value
	}
	var err error
	if request.Enabled, err = knowledgeBoolArg(args, tool, "enabled", true); err != nil {
		return nil, err
	}
	if request.Priority, err = knowledgeIntArg(args, tool, "priority", 0); err != nil {
		return nil, err
	}
	confirmed, err := knowledgeBoolArg(args, tool, "confirmed", false)
	if err != nil {
		return nil, err
	}
	request.Alias, request.LibraryID, request.Type, request.Location = knowledgeText(alias), knowledgeText(libraryID),
		knowledgeText(sourceType), knowledgeText(location)
	request.Participation = "supplement"
	if participation != nil {
		request.Participation = *participation
	}
	if !confirmed {
		return structuredToolResult(knowledge.CommandResult{Message: "Adding a trusted knowledge source requires explicit confirmation.",
			SourceAlias: request.Alias}), nil
	}
	return structuredToolResult(knowledgeManager().Add(request)), nil
}

func invokeRemoveKnowledgeSource(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	alias, err := knowledgeStringArg(args, "remove-knowledge-source", "alias")
	if err != nil {
		return nil, err
	}
	confirmed, err := knowledgeBoolArg(args, "remove-knowledge-source", "confirmed", false)
	if err != nil {
		return nil, err
	}
	result, err := knowledgeManager().Remove(knowledgeText(alias), confirmed)
	if err != nil {
		return nil, err
	}
	return structuredToolResult(result), nil
}

func invokeSetKnowledgeSourceEnabled(args map[string]any, tool string, enabled bool) (*mcp.CallToolResult, error) {
	alias, err := knowledgeStringArg(args, tool, "alias")
	if err != nil {
		return nil, err
	}
	return structuredToolResult(knowledgeManager().SetEnabled(knowledgeText(alias), enabled)), nil
}

type knowledgeFeedbackConfigureResult struct {
	Success bool                     `json:"success"`
	Error   string                   `json:"error,omitempty"`
	Policy  knowledge.FeedbackPolicy `json:"policy"`
}

// invokeConfigureKnowledgeFeedbackPolicy is clio's KnowledgeFeedbackPolicyTools.Configure.
func invokeConfigureKnowledgeFeedbackPolicy(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "configure-knowledge-feedback-policy"
	values := map[string]*string{}
	for _, name := range []string{"mode", "destination", "reporting-scope", "expected-policy-hash", "expected-destination", "expected-reporting-scope"} {
		value, err := knowledgeStringArg(args, tool, name)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	confirmed, err := knowledgeBoolArg(args, tool, "confirmed", false)
	if err != nil {
		return nil, err
	}
	runtime := knowledgeRuntime()
	runtime.InvalidateSettings()
	current := runtime.FeedbackPolicy()
	refuse := func(message string) (*mcp.CallToolResult, error) {
		return structuredToolResult(knowledgeFeedbackConfigureResult{Error: message, Policy: current}), nil
	}
	mode := ""
	if values["mode"] != nil {
		mode = trimDotNet(*values["mode"])
	}
	selectsAuto := equalFoldInvariant(mode, knowledge.FeedbackAuto)
	retargets := current.ConfiguredMode == knowledge.FeedbackAuto && (isBlank(mode) || selectsAuto) &&
		(values["destination"] != nil || values["reporting-scope"] != nil)
	if (selectsAuto || retargets) && !confirmed {
		return refuse("Selecting auto or changing its destination/reporting scope requires confirmed=true after the user approves the displayed configuration.")
	}
	if (selectsAuto || retargets) && (isBlank(knowledgeText(values["expected-policy-hash"])) ||
		isBlank(knowledgeText(values["expected-destination"])) || isBlank(knowledgeText(values["expected-reporting-scope"]))) {
		return refuse("Confirmed approval must include expected-policy-hash, expected-destination, and expected-reporting-scope exactly as shown to the user.")
	}
	var consent *knowledge.FeedbackConsent
	if confirmed {
		consent = &knowledge.FeedbackConsent{PolicyHash: knowledgeText(values["expected-policy-hash"]),
			Destination: knowledgeText(values["expected-destination"]), ReportingScope: knowledgeText(values["expected-reporting-scope"])}
	}
	err = knowledge.ConfigureFeedback(knowledge.SettingsPath(), knowledge.FeedbackUpdate{Mode: values["mode"],
		Destination: values["destination"], ReportingScope: values["reporting-scope"]}, true, consent, current.ReportingPolicyHash)
	runtime.InvalidateSettings()
	if err != nil {
		return structuredToolResult(knowledgeFeedbackConfigureResult{Error: err.Error(), Policy: runtime.FeedbackPolicy()}), nil
	}
	return structuredToolResult(knowledgeFeedbackConfigureResult{Success: true, Policy: runtime.FeedbackPolicy()}), nil
}

// trimDotNet is String.Trim().
func trimDotNet(value string) string { return strings.TrimFunc(value, unicode.IsSpace) }

func isBlank(value string) bool { return trimDotNet(value) == "" }

func equalFoldInvariant(left, right string) bool { return strings.EqualFold(left, right) }
