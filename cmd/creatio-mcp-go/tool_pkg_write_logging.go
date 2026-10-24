package main

// add-custom-logging: clio's AddCustomLoggingTool over AddCustomLoggingCommand. The command validates every
// rule first and prints each failure as "ERROR (<validator>) - <message>", then edits the NLog files of the
// environment's EnvironmentPath, and with restart-environment restarts the application.
// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)].

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "add-custom-logging"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "add-custom-logging"
		values := map[string]string{}
		for _, name := range []string{"environment-name", "package-name", "min-level", "file-name"} {
			value, err := optionalStringArg(args, tool, name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		restart, err := pkgWriteBoolArg(args, tool, "restart-environment", false)
		if err != nil {
			return nil, err
		}
		name, packageName, fileName := values["environment-name"], values["package-name"], values["file-name"]
		minLevel := "Info"
		if raw, ok := args["min-level"]; ok && raw != nil {
			minLevel = values["min-level"]
		}

		// clio resolves the environment (InternalExecute) before the command validates its options, so an absent,
		// blank or unknown name is the resolver's refusal, whatever else the call lacks.
		client, refusal, err := pkgWriteNamedEnvironment(envs, tool, name)
		if err != nil {
			return nil, err
		}
		if refusal != nil {
			return resolverFailureEnvelope(refusal), nil
		}

		// AddCustomLoggingOptionsValidator: every rule, in order.
		var failures []string
		failure := func(validator, message string) {
			failures = append(failures, fmt.Sprintf("ERROR (%s) - %s", validator, message))
		}
		if strings.TrimSpace(name) == "" {
			failure("NotEmptyValidator", "A registered environment name is required.")
		}
		if strings.TrimSpace(packageName) == "" {
			failure("NotEmptyValidator", "Package name is required.")
		}
		if !creatio.CustomLoggingSafePackage(packageName) {
			failure("PredicateValidator", "Package name may contain only letters, digits, underscore, dot, and hyphen, and must start with a letter or underscore.")
		}
		if !creatio.CustomLoggingSupportedLevel(minLevel) {
			failure("PredicateValidator", "Min level must be one of: Trace, Debug, Info, Warn, Error, Fatal, Off.")
		}
		if !creatio.CustomLoggingSafeFile(fileName) {
			failure("PredicateValidator", "File name must be a simple file name without directories or NLog layout expressions.")
		}
		if len(failures) > 0 {
			messages := make([]creatio.LogMessage, len(failures))
			for index, text := range failures {
				messages[index] = creatio.LogMessage{MessageType: "None", Value: text}
			}
			return structuredToolResult(creatio.CommandResult{ExitCode: 1, Messages: messages}), nil
		}

		settings, _ := envs.snapshot()
		environment, ok := settings.Find(name)
		if !ok {
			return structuredToolResult(creatio.CommandFailure(fmt.Sprintf("Environment '%s' is not registered.", name))), nil
		}
		if strings.TrimSpace(environment.EnvironmentPath) == "" {
			return structuredToolResult(creatio.CommandFailure(fmt.Sprintf("Environment '%s' does not have a local EnvironmentPath. "+
				"add-custom-logging can only edit a registered local Creatio installation.", name))), nil
		}
		if restart && strings.TrimSpace(environment.URI) == "" {
			return structuredToolResult(creatio.CommandFailure(fmt.Sprintf("Environment '%s' does not have a Uri and cannot be restarted. "+
				"Configure logging without --restart-environment or update the registration.", name))), nil
		}
		result := creatio.ConfigureCustomLogging(environment.EnvironmentPath, packageName, minLevel, fileName)
		if !result.Success {
			return structuredToolResult(creatio.CommandFailure(result.ErrorMessage)), nil
		}
		format := "Configured logger '%s' with target '%s'."
		if !result.Changed {
			format = "Logger '%s' and target '%s' are already configured; no files changed."
		}
		messages := []creatio.LogMessage{
			{MessageType: "Info", Value: fmt.Sprintf(format, result.LoggerName, result.TargetName)},
			{MessageType: "Info", Value: "Configured log path: " + result.LogPath},
			{MessageType: "Info", Value: fmt.Sprintf("Configured environment '%s'.", name)},
		}
		if !restart {
			messages = append(messages, creatio.LogMessage{MessageType: "Info",
				Value: "Restart is required before the new logging route is guaranteed to be active. Re-run with --restart-environment to restart now."})
			return structuredToolResult(creatio.CommandResult{ExitCode: 0, Messages: messages}), nil
		}
		messages = append(messages, creatio.LogMessage{MessageType: "Info", Value: fmt.Sprintf("Restarting environment '%s' to activate the logging route.", name)})
		restarted := client.RestartApplication(ctx)
		return structuredToolResult(creatio.CommandResult{ExitCode: restarted.ExitCode, Messages: append(messages, restarted.Messages...)}), nil
	}, withAnnotations(toolAnnotations{Destructive: true}))
}
