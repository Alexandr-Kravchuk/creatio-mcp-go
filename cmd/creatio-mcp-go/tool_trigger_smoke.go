package main

// trigger-smoke: clio 8.1.0.142's TriggerSmokeTool over TriggerSmokeRunner.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var triggerSmokeArguments = []string{"environment-name", "entity", "values", "watch", "process", "timeout", "cleanup"}

// triggerSmokeEntityName is CreatedRecordWatcher.IsValidEntityName; triggerSmokeIdentifier is
// ODataKeyFormatter.IsSimpleIdentifier's pattern.
var (
	triggerSmokeEntityName = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,127}\z`)
	triggerSmokeIdentifier = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]*\z`)
)

func init() {
	registerTool(map[string]any{"name": "trigger-smoke"}, invokeTriggerSmoke, withAnnotations(toolAnnotations{Destructive: true}))
}

func invokeTriggerSmoke(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := t21aBinder{tool: "trigger-smoke", args: args}
	// environment-name and entity are `required` members of clio's argument record: a missing one fails the
	// binding of the whole record.
	for _, name := range []string{"environment-name", "entity"} {
		if _, ok := args[name]; !ok {
			return nil, errors.New("invalid-parameter-type: argument 'args' for MCP tool 'trigger-smoke' must be an object. Received an incompatible JSON value.")
		}
	}
	environmentName, err := bind.text("environment-name")
	if err != nil {
		return nil, err
	}
	entity, err := bind.str("entity")
	if err != nil {
		return nil, err
	}
	values, err := bind.object("values")
	if err != nil {
		return nil, err
	}
	watch, _, err := bind.strings("watch")
	if err != nil {
		return nil, err
	}
	process, err := bind.str("process")
	if err != nil {
		return nil, err
	}
	timeout, err := bind.integer("timeout")
	if err != nil {
		return nil, err
	}
	cleanup, err := bind.boolean("cleanup")
	if err != nil {
		return nil, err
	}
	failure := func(message string) (*mcp.CallToolResult, error) {
		return t21aResult(creatio.TriggerSmokeFailure(message)), nil
	}
	if message := t21aAliasError(args, triggerSmokeArguments, t21aEnvironmentAliases, true, ".",
		"Valid: environment-name, entity, values, watch, process, timeout, cleanup."); message != "" {
		return failure(message)
	}
	if message := triggerSmokeValidate(environmentName, entity, values, watch, timeout); message != "" {
		return failure(message)
	}
	client, resolution, err := envs.resolve(bind.tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if resolution != nil {
		// TriggerSmokeTool catches every resolution failure, the Safe-environment refusal included.
		return failure(redact.Text(resolution.Error()))
	}
	request := creatio.TriggerSmokeRequest{Entity: strings.TrimSpace(*entity), Values: values, TimeoutSeconds: creatio.TriggerSmokeDefaultTimeout,
		Cleanup: cleanup == nil || *cleanup}
	for _, name := range watch {
		request.Watch = append(request.Watch, *name)
	}
	if timeout != nil {
		request.TimeoutSeconds = *timeout
	}
	if process != nil && strings.TrimSpace(*process) != "" {
		trimmed := strings.TrimSpace(*process)
		request.Process = &trimmed
	}
	response := client.TriggerSmoke(ctx, request)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return t21aResult(response), nil
}

// triggerSmokeValidate is TriggerSmokeTool.Validate.
func triggerSmokeValidate(environmentName string, entity *string, values map[string]any, watch []*string, timeout *int) string {
	if strings.TrimSpace(environmentName) == "" {
		return "environment-name is required."
	}
	if entity == nil || !triggerSmokeEntityName.MatchString(strings.TrimSpace(*entity)) {
		return "entity must be an entity schema name (letters, digits, underscore)."
	}
	if len(values) == 0 {
		return "values is required: the column values of the triggering record, as OData property names (a lookup as <Column>Id " +
			"with the record GUID), for example {\"Name\": \"Smoke\", \"AccountId\": \"…\"}."
	}
	for _, key := range sortedKeys(values) {
		if !triggerSmokeIdentifier.MatchString(key) {
			return fmt.Sprintf("values keys must be plain property names; '%s' is not one.", key)
		}
	}
	if len(watch) == 0 {
		return "watch is required: list the entities the started process writes, for example [\"Activity\"]. The platform keeps no " +
			"list of what a run created, so clio reports only what appears in watched entities."
	}
	invalid := len(watch) > creatio.TriggerSmokeMaxWatched
	for _, name := range watch {
		if name == nil || !triggerSmokeEntityName.MatchString(*name) {
			invalid = true
		}
	}
	if invalid {
		return fmt.Sprintf("watch takes 1-%d entity schema names.", creatio.TriggerSmokeMaxWatched)
	}
	if timeout != nil && (*timeout < 1 || *timeout > creatio.TriggerSmokeMaxTimeout) {
		return fmt.Sprintf("timeout must be between 1 and %d seconds.", creatio.TriggerSmokeMaxTimeout)
	}
	return ""
}
