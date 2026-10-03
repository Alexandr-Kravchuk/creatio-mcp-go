package creatio

// CommandResult is clio's command envelope (CommandExecutionResult) as its command-style MCP tools return
// it: an exit code and the command's log. Exit code 0 is success (also an in-progress notice), 1 an
// expected, caller-actionable failure, -1 an unexpected one.
type CommandResult struct {
	ExitCode int          `json:"exit-code"`
	Messages []LogMessage `json:"execution-log-messages"`
	Note     string       `json:"note,omitempty"`
}

// LogMessage is one entry of the execution-log-messages channel. MessageType is clio's LogDecoratorType
// name: Info, Warning, Error, None and so on.
type LogMessage struct {
	MessageType string `json:"message-type"`
	Value       string `json:"value"`
}

// NewCommandResult is an envelope with one message.
func NewCommandResult(exitCode int, messageType, value string) CommandResult {
	return CommandResult{ExitCode: exitCode, Messages: []LogMessage{{MessageType: messageType, Value: value}}}
}

// CommandFailure is clio's FromValidationError: exit code 1 with one Error message.
func CommandFailure(message string) CommandResult {
	return NewCommandResult(1, "Error", message)
}

// CommandInfo is clio's FromInfo: exit code 0 with one Info message, used for an in-progress notice.
func CommandInfo(message string) CommandResult {
	return NewCommandResult(0, "Info", message)
}
