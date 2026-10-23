package workspace

import "github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"

// Log collects a command's execution-log-messages in clio's order.
type Log struct{ messages []creatio.LogMessage }

func (l *Log) add(kind, value string) {
	l.messages = append(l.messages, creatio.LogMessage{MessageType: kind, Value: value})
}

func (l *Log) Info(value string)    { l.add("Info", value) }
func (l *Log) Warning(value string) { l.add("Warning", value) }
func (l *Log) Error(value string)   { l.add("Error", value) }

// Line is ILogger.WriteLine: a message without decoration.
func (l *Log) Line(value string) { l.add("None", value) }

// Append adds another command's messages, as a follow-up command writes into the same logger.
func (l *Log) Append(messages []creatio.LogMessage) { l.messages = append(l.messages, messages...) }

// Result is the command envelope with the given exit code.
func (l *Log) Result(exitCode int) creatio.CommandResult {
	messages := l.messages
	if messages == nil {
		messages = []creatio.LogMessage{}
	}
	return creatio.CommandResult{ExitCode: exitCode, Messages: messages}
}
