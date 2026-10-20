package main

// experimental, ported from clio 8.1.0.142's ExperimentalCommand: list clio's feature flags or set one in
// clio's appsettings.json (features section, written under clio's settings lock).

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// experimentalKnownFeatures are the keys clio 8.1.0.142 recognizes: the [FeatureToggle] attributes of its
// command and MCP types plus its StandaloneFeatureKeys. clio discovers them by reflection; they follow the
// clio release used as the parity baseline.
var experimentalKnownFeatures = []string{"deploy-identity", "knowledge-allow-unsequenced", "ring", "watch-compilation"}

// experimentalEnableNotices is clio's FeatureEnableNotices.
var experimentalEnableNotices = map[string]string{
	"knowledge-allow-unsequenced": "⚠️ Local development aid. While enabled, a Git knowledge source whose manifest omits " +
		"\"sequence\" is loaded by deriving the sequence from libraryVersion, and such a bundle " +
		"may replace the active generation with different content under the SAME sequence - the " +
		"content-integrity check that detects a swapped guidance corpus does not apply to it. " +
		"Rollbacks to a lower sequence are still refused. The flag is persistent and affects " +
		"every later clio run against every environment; disable it when you are done.",
}

func init() {
	registerTool(map[string]any{"name": "experimental"}, invokeExperimental, withAnnotations(toolAnnotations{Idempotent: true}))
}

func invokeExperimental(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
	name, err := knowledgeStringArg(args, "experimental", "name")
	if err != nil {
		return nil, err
	}
	enable, err := knowledgeBoolArg(args, "experimental", "enable", false)
	if err != nil {
		return nil, err
	}
	disable, err := knowledgeBoolArg(args, "experimental", "disable", false)
	if err != nil {
		return nil, err
	}
	settingsPath := knowledge.SettingsPath()
	if isBlank(knowledgeText(name)) {
		if enable || disable {
			return structuredToolResult(creatio.CommandFailure("--enable/--disable require a feature key. Use --name <key> --enable (or --disable).")), nil
		}
		settings, err := knowledge.ReadSettings(settingsPath)
		if err != nil {
			return structuredToolResult(creatio.NewCommandResult(1, "Error", err.Error())), nil
		}
		return structuredToolResult(creatio.NewCommandResult(0, "Table", experimentalTable(settings.Features))), nil
	}
	if enable == disable {
		return structuredToolResult(creatio.CommandFailure("Specify exactly one of --enable or --disable when using --name.")), nil
	}
	key := trimDotNet(*name)
	if err := knowledge.SetFeature(settingsPath, key, enable); err != nil {
		return structuredToolResult(creatio.NewCommandResult(1, "Error", err.Error())), nil
	}
	knowledgeRuntime().InvalidateSettings()
	state := "DISABLED"
	if enable {
		state = "ENABLED"
	}
	result := creatio.CommandResult{Messages: []creatio.LogMessage{{MessageType: "Info", Value: fmt.Sprintf("Feature '%s' is now %s.", key, state)}}}
	known := false
	for _, feature := range experimentalKnownFeatures {
		known = known || strings.EqualFold(feature, key)
	}
	if !known {
		result.Messages = append(result.Messages, creatio.LogMessage{MessageType: "Warning",
			Value: fmt.Sprintf("No command or MCP tool currently references the feature key '%s'.", key)})
	}
	for feature, notice := range experimentalEnableNotices {
		if enable && strings.EqualFold(feature, key) {
			result.Messages = append(result.Messages, creatio.LogMessage{MessageType: "Warning", Value: notice})
		}
	}
	return structuredToolResult(result), nil
}

// experimentalFeatureEnabled is clio's case-insensitive IsFeatureEnabled.
func experimentalFeatureEnabled(features map[string]bool, key string) bool {
	for name, enabled := range features {
		if strings.EqualFold(name, key) && enabled {
			return true
		}
	}
	return false
}

// experimentalTable is the Feature/State listing clio prints: known keys with their state, then settings
// keys nothing references marked as orphans, in case-insensitive order, as a ConsoleTables 2.7.0 table.
func experimentalTable(features map[string]bool) string {
	type row struct{ key, state string }
	var rows []row
	known := map[string]bool{}
	for _, key := range experimentalKnownFeatures {
		known[strings.ToLower(key)] = true
		state := "DISABLED"
		if experimentalFeatureEnabled(features, key) {
			state = "ENABLED"
		}
		rows = append(rows, row{key, state})
	}
	for key, enabled := range features {
		if known[strings.ToLower(key)] {
			continue
		}
		state := "DISABLED"
		if enabled {
			state = "ENABLED"
		}
		rows = append(rows, row{key, state + " (orphan - no command uses this key)"})
	}
	sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].key) < strings.ToLower(rows[j].key) })
	cells := make([][]string, len(rows))
	for i, item := range rows {
		cells[i] = []string{item.key, item.state}
	}
	return consoleTable([]string{"Feature", "State"}, cells)
}

const consoleTableMaxCell = 40

func utf16Len(text string) int { return len(utf16.Encode([]rune(text))) }

// consoleTable renders ConsoleTables 2.7.0's default format the way clio's experimental listing shows it:
// a cell longer than 40 UTF-16 units continues on an extra row (split at its last space), column widths
// come from the wrapped cells, rows are separated by dashed dividers and the footer counts every row.
func consoleTable(columns []string, rows [][]string) string {
	var wrapped [][]string
	for _, row := range rows {
		current := append([]string{}, row...)
		for {
			next := make([]string, len(current))
			long := false
			for i, cell := range current {
				units := utf16.Encode([]rune(cell))
				if len(units) <= consoleTableMaxCell {
					continue
				}
				long = true
				cut := consoleTableMaxCell
				if units[cut-1] != ' ' && units[0] != ' ' {
					for j := cut - 1; j > 0; j-- {
						if units[j] == ' ' {
							cut = j
							break
						}
					}
				}
				current[i] = strings.TrimSpace(string(utf16.Decode(units[:cut])))
				next[i] = strings.TrimSpace(string(utf16.Decode(units[cut:])))
			}
			wrapped = append(wrapped, current)
			if !long {
				break
			}
			current = next
		}
	}
	widths := make([]int, len(columns))
	for i, column := range columns {
		widths[i] = utf16Len(column)
	}
	for _, row := range wrapped {
		for i, cell := range row {
			widths[i] = max(widths[i], utf16Len(cell))
		}
	}
	format := func(cells []string) string {
		var builder strings.Builder
		for i, cell := range cells {
			builder.WriteString(" | " + cell + strings.Repeat(" ", widths[i]-utf16Len(cell)))
		}
		return builder.String() + " |"
	}
	header := format(columns)
	divider := " " + strings.Repeat("-", utf16Len(header)-1) + " "
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	var builder strings.Builder
	builder.WriteString(divider + newline + header + newline)
	for _, row := range wrapped {
		builder.WriteString(divider + newline + format(row) + newline)
	}
	builder.WriteString(divider + newline + newline + fmt.Sprintf(" Count: %d", len(wrapped)))
	return builder.String()
}
