package main

// Shared argument helpers for self-registered tools: clio-compatible unknown-argument and type refusals,
// and the per-call environment selection (environment-name, plus clio's direct-connection arguments for
// the tools whose clio counterpart accepts them).

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// environmentNameAliases are the spellings clio answers with a rename to environment-name (matched
// case-insensitively, as clio's McpToolArgumentSupport.EnvironmentNameAliases is).
var environmentNameAliases = map[string]bool{"environmentname": true, "environment_name": true, "environment": true}

// connectionScope says which of clio's connection arguments a tool reads.
type connectionScope int

const (
	// scopeName: environment-name only. Any uri/login/password is not read, as in clio, where the tool's
	// argument record has no such members.
	scopeName connectionScope = iota
	// scopeDirect: environment-name plus clio's emergency uri, login and password.
	scopeDirect
	// scopeDirectOAuth: scopeDirect plus client-id, client-secret and auth-app-uri.
	scopeDirectOAuth
)

// environmentArgs are the argument names a tool in the given scope accepts for its target.
func environmentArgs(scope connectionScope) []string {
	switch scope {
	case scopeDirect:
		return []string{"environment-name", "uri", "login", "password"}
	case scopeDirectOAuth:
		return []string{"environment-name", "uri", "login", "password", "client-id", "client-secret", "auth-app-uri"}
	default:
		return []string{"environment-name"}
	}
}

// withoutEnvironmentArgs returns a copy of args without the scope's environment arguments, for a strict
// decode of the tool's own arguments.
func withoutEnvironmentArgs(args map[string]any, scope connectionScope) map[string]any {
	copied := make(map[string]any, len(args))
	for key, value := range args {
		copied[key] = value
	}
	for _, name := range environmentArgs(scope) {
		delete(copied, name)
	}
	return copied
}

// target resolves the call's environment: environment-name, then (in the wider scopes) uri/login/password
// and the OAuth client arguments, the way clio's EnvironmentSettings.Fill combines them. A non-string
// value is clio's invalid-parameter-type refusal.
func (e *environments) target(tool string, args map[string]any, scope connectionScope) (*creatio.Client, error) {
	name, err := optionalStringArg(args, tool, "environment-name")
	if err != nil {
		return nil, err
	}
	var overrides creatio.ConnectionOverrides
	if scope >= scopeDirect {
		for _, field := range []struct {
			name   string
			target *string
		}{{"uri", &overrides.URI}, {"login", &overrides.Login}, {"password", &overrides.Password}} {
			if *field.target, err = optionalStringArg(args, tool, field.name); err != nil {
				return nil, err
			}
		}
	}
	if scope >= scopeDirectOAuth {
		for _, field := range []struct {
			name   string
			target *string
		}{{"client-id", &overrides.ClientID}, {"client-secret", &overrides.ClientSecret}, {"auth-app-uri", &overrides.AuthAppURI}} {
			if *field.target, err = optionalStringArg(args, tool, field.name); err != nil {
				return nil, err
			}
		}
	}
	return e.client(name, overrides)
}

// resolve is target for tools with their own failure envelope. A binding failure (a selector that is not a
// string) comes back as err, because clio raises it before the tool runs; a resolution failure comes back as
// refusal, for the tool to report the way its clio counterpart does.
func (e *environments) resolve(tool string, args map[string]any, scope connectionScope) (client *creatio.Client, refusal, err error) {
	client, err = e.target(tool, args, scope)
	if err != nil && isEnvironmentError(err) {
		return nil, err, nil
	}
	return client, nil, err
}

// redacted is a failure as clio's SensitiveErrorTextRedactor leaves it. Tools whose clio counterpart redacts
// the failure it reports use it for a failure they put into their own envelope; the hidden-tool dispatcher
// redacts failed results again (failure_redaction.go), which changes nothing already redacted.
func redacted(err error) string {
	return redact.Text(err.Error())
}

// resolverErrorText is clio's CommandExecutionResult text for a failed resolution in the command-style
// envelope: the exception type in brackets, then its unredacted message.
func resolverErrorText(err error) string {
	var target *environmentError
	if errors.As(err, &target) {
		return "[" + target.exceptionType() + "] " + target.message
	}
	return err.Error()
}

// resolverFailureEnvelope is clio's command envelope for a failed resolution (CommandExecutionResult
// FromResolverError): one Error message carrying the exception type and the unredacted text.
func resolverFailureEnvelope(err error) *mcp.CallToolResult {
	exitCode := 1
	var target *environmentError
	if errors.As(err, &target) {
		exitCode = target.exitCode()
	}
	return structuredToolResult(creatio.NewCommandResult(exitCode, "Error", resolverErrorText(err)))
}

// unknownArgumentError mirrors clio's McpToolArgumentSupport.BuildLegacyAliasError for tools that refuse
// unknown keys: legacy spellings of environment-name get a rename hint, other unknown keys are listed with
// the accepted ones. valid lists the accepted names in the order clio's hint names them. Caller keys are
// reported in sorted order because Go maps carry no request order.
func unknownArgumentError(args map[string]any, valid ...string) string {
	known := make(map[string]bool, len(valid))
	for _, name := range valid {
		known[name] = true
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		if !known[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var renamed, unknown []string
	for _, key := range keys {
		if environmentNameAliases[strings.ToLower(key)] {
			renamed = append(renamed, fmt.Sprintf("'%s' -> 'environment-name'", describeCallerKey(key)))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", describeCallerKey(key)))
		}
	}
	var parts []string
	if len(renamed) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renamed)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". Valid: "+strings.Join(valid, ", ")+".")
	}
	return strings.Join(parts, " ")
}

// describeCallerKey strips quote characters so a caller's key cannot close the quoting around it.
func describeCallerKey(key string) string {
	return strings.NewReplacer("'", "", "\"", "").Replace(key)
}

func joinCallerKeys(keys []string) string {
	const maxEchoedKeys = 10
	if len(keys) <= maxEchoedKeys {
		return strings.Join(keys, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(keys[:maxEchoedKeys], ", "), len(keys)-maxEchoedKeys)
}

// optionalStringArg reads a string argument the way clio's binder does: absent or null is empty, any other
// JSON type is clio's invalid-parameter-type refusal.
func optionalStringArg(args map[string]any, tool, name string) (string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a string. Received an incompatible JSON value.", name, tool)
	}
	return text, nil
}
