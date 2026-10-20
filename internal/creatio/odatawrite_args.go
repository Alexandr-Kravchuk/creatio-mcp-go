package creatio

// The argument handling clio 8.1.0.142 added to odata-create and odata-update: the payload synonyms of
// ODataWriteArgumentAliases and the unbound-key refusal of McpToolArgumentSupport.BuildLegacyAliasError, both
// over the keys the argument record does not bind (clio's [JsonExtensionData] bag).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ODataCreateRowsAliases are clio's synonyms of odata-create's rows.
var ODataCreateRowsAliases = []string{"payload", "values", "body", "data"}

// ODataUpdateDataAliases are clio's synonyms of odata-update's data.
var ODataUpdateDataAliases = []string{"values", "payload", "body", "fields"}

// odataCreateArgumentAliases and odataUpdateArgumentAliases are the tools' camelCase / snake_case spellings.
var (
	odataCreateArgumentAliases = map[string]string{"environmentName": "environment-name", "environment_name": "environment-name",
		"rowsFile": "rows-file", "rows_file": "rows-file", "stopOnError": "stop-on-error", "stop_on_error": "stop-on-error"}
	odataUpdateArgumentAliases = map[string]string{"environmentName": "environment-name", "environment_name": "environment-name",
		"rowsFile": "rows-file", "rows_file": "rows-file"}
)

const (
	odataCreateValidArguments = "Valid: entity, environment-name, rows, rows-file, stop-on-error."
	odataUpdateValidArguments = "Valid: entity, id, environment-name, data, rows-file, confirm."
)

// odataPayloadResolution is ODataWriteArgumentAliases.Resolution.
type odataPayloadResolution struct {
	value     json.RawMessage // the payload sent under a synonym; nil when none was used
	remaining map[string]json.RawMessage
	warning   string
	err       string
	alias     string
	canonical string
}

// warningFor is Resolution.WarningFor: a refused call accepted nothing, so it must not claim it did.
func (r odataPayloadResolution) warningFor(refused bool) []string {
	if r.warning == "" {
		return nil
	}
	if !refused {
		return []string{r.warning}
	}
	return []string{fmt.Sprintf("Argument '%s' was read as '%s', but the call was refused (see error); use '%s' in future calls.",
		r.alias, r.canonical, r.canonical)}
}

// odataDescribeCallerKey is McpToolArgumentSupport.DescribeCallerKey.
func odataDescribeCallerKey(key string) string {
	return strings.NewReplacer("'", "", "\"", "").Replace(dataWriteSanitizeForDisplay(key, 120))
}

// odataResolvePayload is ODataWriteArgumentAliases.Resolve. Go maps keep no request order, so several
// synonyms are named in sorted order where clio names them in the order they were sent.
func odataResolvePayload(extra map[string]json.RawMessage, canonical string, aliases []string,
	canonicalSupplied, rowsFileSupplied bool) odataPayloadResolution {
	var used []string
	for key := range extra {
		for _, alias := range aliases {
			if strings.EqualFold(key, alias) {
				used = append(used, key)
				break
			}
		}
	}
	if len(used) == 0 {
		return odataPayloadResolution{remaining: extra}
	}
	sort.Strings(used)
	shown := make([]string, len(used))
	for i, key := range used {
		shown[i] = "'" + odataDescribeCallerKey(key) + "'"
	}
	if canonicalSupplied || rowsFileSupplied || len(used) > 1 {
		if canonicalSupplied {
			shown = append([]string{"'" + canonical + "'"}, shown...)
		} else if rowsFileSupplied {
			shown = append([]string{"'rows-file'"}, shown...)
		}
		return odataPayloadResolution{remaining: extra, err: fmt.Sprintf("Ambiguous payload: %s were all supplied, and they name the same thing. "+
			"Send the payload once, as '%s'. Nothing was written.", strings.Join(shown, " and "), canonical)}
	}
	alias := used[0]
	displayed := odataDescribeCallerKey(alias)
	remaining := make(map[string]json.RawMessage, len(extra))
	for key, value := range extra {
		if key != alias {
			remaining[key] = value
		}
	}
	return odataPayloadResolution{value: extra[alias], remaining: remaining, alias: displayed, canonical: canonical,
		warning: fmt.Sprintf("Argument '%s' was accepted as '%s'; use '%s' in future calls.", displayed, canonical, canonical)}
}

// odataUnboundError is BuildLegacyAliasError over what is left of the overflow bag.
func odataUnboundError(extra map[string]json.RawMessage, aliases map[string]string, hint string) string {
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	return DataWriteAliasError(keys, aliases, ".", hint)
}

// odataWrapSingleObject is ODataCreateTool.WrapSingleObject: a single row object becomes a one-element array.
func odataWrapSingleObject(value json.RawMessage) json.RawMessage {
	if dataWriteJSONKind(value) != 'o' {
		return value
	}
	return json.RawMessage("[" + string(value) + "]")
}
