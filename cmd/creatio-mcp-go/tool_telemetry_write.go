package main

// send-telemetry and withdraw-telemetry-consent, ported from clio 8.1.0.142's SendTelemetryTool and
// WithdrawTelemetryConsentTool over internal/telemetry, which writes clio's own consent file and outbox.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// telemetryService reports this server's build as clio_version: the attribute names the producer of the
// event, and this server is not clio.
var telemetryService = sync.OnceValue(func() *telemetry.Service {
	return &telemetry.Service{Version: "creatio-mcp-go/" + serverVersion()}
})

var telemetryFlusher = sync.OnceValue(func() *telemetry.Flusher {
	return &telemetry.Flusher{Service: telemetryService(), Options: func() telemetry.Options {
		return telemetry.ResolveOptions(telemetry.ReadSettings(knowledge.SettingsPath()))
	}}
})

var telemetryRequestFields = map[string]bool{"session_id": true, "event_name": true, "coding_agent": true,
	"plugin_version": true, "duration_ms": true, "telemetry_consent": true, "workflow": true, "variant": true,
	"model": true, "input_tokens": true, "output_tokens": true, "cached_input_tokens": true}

func init() {
	registerTool(map[string]any{"name": "send-telemetry"}, invokeSendTelemetry,
		withAnnotations(toolAnnotations{OpenWorld: true}))
	registerTool(map[string]any{"name": "withdraw-telemetry-consent"}, func(context.Context, *environments, map[string]any) (*mcp.CallToolResult, error) {
		return structuredToolResult(telemetryService().Withdraw()), nil
	}, withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

// telemetryLongArg binds clio's long? members: a JSON integer, or absent/null.
func telemetryLongArg(args map[string]any, name string) (*int64, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || math.Abs(number) > 9.2e18 {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool 'send-telemetry' must be a number. Received an incompatible JSON value.", name)
	}
	converted := int64(number)
	return &converted, nil
}

func invokeSendTelemetry(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	var request telemetry.Request
	for name, target := range map[string]*string{"session_id": &request.SessionID, "event_name": &request.EventName,
		"coding_agent": &request.CodingAgent, "plugin_version": &request.PluginVersion, "telemetry_consent": &request.TelemetryConsent,
		"workflow": &request.Workflow, "variant": &request.Variant, "model": &request.Model} {
		value, err := knowledgeStringArg(args, "send-telemetry", name)
		if err != nil {
			return nil, err
		}
		*target = knowledgeText(value)
	}
	for name, target := range map[string]**int64{"duration_ms": &request.DurationMs, "input_tokens": &request.InputTokens,
		"output_tokens": &request.OutputTokens, "cached_input_tokens": &request.CachedInputTokens} {
		value, err := telemetryLongArg(args, name)
		if err != nil {
			return nil, err
		}
		*target = value
	}
	for name := range args {
		if !telemetryRequestFields[name] {
			request.Extra = append(request.Extra, name)
		}
	}
	sort.Strings(request.Extra)
	result := telemetryService().Send(request)
	if result.Success && result.Status == telemetry.StatusRecorded {
		telemetryFlusher().TryScheduleFlush()
	}
	return structuredToolResult(result), nil
}
