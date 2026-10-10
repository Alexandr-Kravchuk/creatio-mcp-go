package creatio

// The shared part of clio's process-designer write tools (create-business-process, modify-business-process,
// modify-business-process-as-new-version, set-active-business-process-version): the command envelope with
// its compile note, the CrtProcessBuilder package gate with a minimum version, and the ProcessDesignService
// call with clio's response reading.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// processWriteDesignMinVersion is the [RequiresPackage] version of the build and modify commands (clio
	// master; clio 8.1.0.134 asks for 1.6.2.1).
	processWriteDesignMinVersion = "1.6.6.77"
	// processWriteActivateMinVersion is set-active-business-process-version's [RequiresPackage] version.
	processWriteActivateMinVersion = "1.6.1.0"
	// processWriteInstallHint is BundledPackages.ProcessBuilderInstallHint.
	processWriteInstallHint = "Run 'clio install-process-builder -e <environment>' (or call the install-process-builder MCP tool) to install or update CrtProcessBuilder."
	// processWriteCompileNote is CommandExecutionResult.CompileNotRequiredNote.
	processWriteCompileNote = "compile-creatio not required"
	// processWriteCompileMarker is CommandExecutionResult.CompileRequiredWarningMarker.
	processWriteCompileMarker = "until the configuration is compiled"
	// processWriteServiceTimeout bounds one ProcessDesignService call; clio waits without a limit.
	processWriteServiceTimeout = 10 * time.Minute
)

// ProcessWriteResult is clio's CommandExecutionResult as these tools return it: the command log and, on
// success, the compile note.
type ProcessWriteResult struct {
	ExitCode int          `json:"exit-code"`
	Messages []LogMessage `json:"execution-log-messages"`
	Note     string       `json:"note,omitempty"`
}

// ProcessWriteFailure is CommandExecutionResult.FromValidationError (exit 1, one Error).
func ProcessWriteFailure(message string) ProcessWriteResult {
	return ProcessWriteResult{ExitCode: 1, Messages: []LogMessage{{MessageType: "Error", Value: message}}}
}

// ProcessWriteError is CommandExecutionResult.FromError (exit -1, one Error).
func ProcessWriteError(message string) ProcessWriteResult {
	return ProcessWriteResult{ExitCode: -1, Messages: []LogMessage{{MessageType: "Error", Value: message}}}
}

// processWriteLog collects a command's log the way clio's logger does, in order.
type processWriteLog struct {
	messages []LogMessage
}

func (l *processWriteLog) info(text string)    { l.add("Info", text) }
func (l *processWriteLog) warning(text string) { l.add("Warning", text) }
func (l *processWriteLog) error(text string)   { l.add("Error", text) }
func (l *processWriteLog) add(kind, text string) {
	l.messages = append(l.messages, LogMessage{MessageType: kind, Value: text})
}

// result closes the log with the command's exit code. A successful one gets the compile note, as the tool
// adds it after a zero exit code, unless a message already says a compile is needed.
func (l *processWriteLog) result(exitCode int) ProcessWriteResult {
	result := ProcessWriteResult{ExitCode: exitCode, Messages: l.messages}
	if result.Messages == nil {
		result.Messages = []LogMessage{}
	}
	if exitCode != 0 {
		return result
	}
	for _, message := range result.Messages {
		if strings.Contains(strings.ToLower(message.Value), processWriteCompileMarker) {
			return result
		}
	}
	result.Note = processWriteCompileNote
	return result
}

// processWriteVersion is clio's PackageVersion: System.Version (two to four parts, an absent part reads -1)
// and an optional suffix after the first '-'.
type processWriteVersion struct {
	parts  [4]int
	suffix string
	text   string
}

func processWriteParseVersion(value string) (processWriteVersion, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return processWriteVersion{}, false
	}
	numbers, suffix := value, ""
	if index := strings.Index(value, "-"); index > 0 {
		numbers, suffix = value[:index], strings.TrimSpace(value[index+1:])
	}
	fields := strings.Split(strings.TrimSpace(numbers), ".")
	if len(fields) < 2 || len(fields) > 4 {
		return processWriteVersion{}, false
	}
	version := processWriteVersion{parts: [4]int{-1, -1, -1, -1}, suffix: suffix}
	texts := make([]string, len(fields))
	for index, field := range fields {
		number, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || number < 0 {
			return processWriteVersion{}, false
		}
		version.parts[index] = number
		texts[index] = strconv.Itoa(number)
	}
	version.text = strings.Join(texts, ".")
	if suffix != "" {
		version.text += "-" + suffix
	}
	return version, true
}

// compare is PackageVersion.CompareTo: the numbers, then a version without suffix before one with it.
func (v processWriteVersion) compare(other processWriteVersion) int {
	for index := range v.parts {
		if v.parts[index] != other.parts[index] {
			if v.parts[index] > other.parts[index] {
				return 1
			}
			return -1
		}
	}
	switch {
	case v.suffix == other.suffix:
		return 0
	case strings.TrimSpace(v.suffix) == "":
		return -1
	case strings.TrimSpace(other.suffix) == "":
		return 1
	}
	return strings.Compare(v.suffix, other.suffix)
}

// processWriteInstalledVersion is RequiredPackageChecker.GetInstalledVersion: the lowest parseable version
// among the packages of that name; a package whose version does not parse counts as not installed.
func (c *Client) processWriteInstalledVersion(ctx context.Context, name string) (*processWriteVersion, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"Name": "Name", "UId": "UId", "Maintainer": "Maintainer", "Version": "Version"}, nil, 10000))
	if err != nil {
		return nil, err
	}
	var lowest *processWriteVersion
	for _, row := range rows {
		if !strings.EqualFold(rowString(row, "Name"), name) {
			continue
		}
		version, ok := processWriteParseVersion(rowString(row, "Version"))
		if !ok {
			continue
		}
		if lowest == nil || version.compare(*lowest) < 0 {
			copied := version
			lowest = &copied
		}
	}
	return lowest, nil
}

// processWriteGate is RequiredPackageChecker.EnsureRequirements for [RequiresPackage(CrtProcessBuilder,
// minVersion)]: presence, the minimum version, then the convergence check against the CrtProcessBuilder
// archive the installed clio carries. It returns the refusal text, or "" when the package qualifies.
func (c *Client) processWriteGate(ctx context.Context, minVersion string) (string, error) {
	installed, err := c.processWriteInstalledVersion(ctx, processBuilderPackageName)
	if err != nil {
		return "", err
	}
	if minVersion == "" {
		if installed == nil {
			return processBuilderMissingMessage, nil
		}
	} else {
		required, _ := processWriteParseVersion(minVersion)
		required.suffix = ""
		if installed == nil || installed.compare(required) < 0 {
			return "To use this command, you need to install the " + processBuilderPackageName + " package version " + minVersion +
				" or higher. Install or update the package in the target environment and retry.\n" + processWriteInstallHint, nil
		}
	}
	if bundled, ok := processWriteBundledVersion(); ok && bundled.suffix == "" && installed.compare(bundled) < 0 {
		return fmt.Sprintf("This clio carries %s %s, but the target environment has %s. Update the package in the target environment and retry.\n%s",
			processBuilderPackageName, bundled.text, installed.text, processWriteInstallHint), nil
	}
	return "", nil
}

// processWriteGateFailure is how BaseTool reports a gate that could not be evaluated (the package list could
// not be read): CommandExecutionResult.FromException, exit -1.
func processWriteGateFailure(err error) ProcessWriteResult {
	return ProcessWriteError(err.Error())
}

// processWriteDesign posts {"request": request} to a ProcessDesignService method and returns the
// "<method>Result" object, reading the answer the way clio's System.Text.Json DTOs do: an unreadable body
// is the method's UNKNOWN text, a JSON null "returned an empty response.", a missing result "returned an
// unexpected response shape.".
func (c *Client) processWriteDesign(ctx context.Context, method string, request *jnode, unknown string) (*jnode, error) {
	body := newObject()
	body.set("request", request)
	payload, err := c.callService(ctx, serviceCall{Route: "rest/ProcessDesignService/" + method, Body: body.stjJSON(),
		Timeout: processWriteServiceTimeout, Limit: maxResponseBytes, Label: "ProcessDesignService"})
	if err != nil {
		return nil, err
	}
	envelope, err := parseJNode(payload)
	if err != nil || (envelope.kind != jkObject && envelope.kind != jkNull) {
		return nil, errors.New(unknown)
	}
	if envelope.kind == jkNull {
		return nil, fmt.Errorf("%s returned an empty response.", method)
	}
	result := processWriteProperty(envelope, method+"Result")
	if result != nil && result.kind != jkObject && result.kind != jkNull {
		return nil, errors.New(unknown)
	}
	if result == nil || result.kind == jkNull {
		return nil, fmt.Errorf("%s returned an unexpected response shape.", method)
	}
	return result, nil
}

// processWriteProperty is a case-insensitive property read (PropertyNameCaseInsensitive); the last
// matching key wins.
func processWriteProperty(node *jnode, name string) *jnode {
	if !node.isObject() {
		return nil
	}
	var found *jnode
	for _, key := range node.keys {
		if strings.EqualFold(key, name) {
			found = node.props[key]
		}
	}
	return found
}

// processWriteFields reads the typed DTO fields of a result object. A value of the wrong JSON type is what
// System.Text.Json refuses with a JsonException, which clio reports as the UNKNOWN text.
type processWriteFields struct {
	node *jnode
	bad  bool
}

func (f *processWriteFields) boolean(name string) bool {
	value := processWriteProperty(f.node, name)
	if value == nil {
		return false
	}
	if value.kind != jkBool {
		f.bad = true
		return false
	}
	return value.flag
}

func (f *processWriteFields) optionalBool(name string) *bool {
	value := processWriteProperty(f.node, name)
	if value == nil || value.kind == jkNull {
		return nil
	}
	if value.kind != jkBool {
		f.bad = true
		return nil
	}
	flag := value.flag
	return &flag
}

func (f *processWriteFields) integer(name string) int {
	value := f.optionalInt(name)
	if node := processWriteProperty(f.node, name); node != nil && node.kind == jkNull {
		f.bad = true
	}
	if value == nil {
		return 0
	}
	return *value
}

func (f *processWriteFields) optionalInt(name string) *int {
	value := processWriteProperty(f.node, name)
	if value == nil || value.kind == jkNull {
		return nil
	}
	if value.kind != jkInteger {
		f.bad = true
		return nil
	}
	number, err := strconv.Atoi(value.text)
	if err != nil {
		f.bad = true
		return nil
	}
	return &number
}

func (f *processWriteFields) text(name string) *string {
	value := processWriteProperty(f.node, name)
	if value == nil || value.kind == jkNull {
		return nil
	}
	if value.kind != jkString {
		f.bad = true
		return nil
	}
	text := value.text
	return &text
}

func (f *processWriteFields) texts(name string) []string {
	value := processWriteProperty(f.node, name)
	if value == nil || value.kind == jkNull {
		return nil
	}
	if value.kind != jkArray {
		f.bad = true
		return nil
	}
	items := make([]string, 0, len(value.items))
	for _, item := range value.items {
		switch item.kind {
		case jkString:
			items = append(items, item.text)
		case jkNull:
			items = append(items, "")
		default:
			f.bad = true
		}
	}
	return items
}

// processWriteLayoutChange is LayoutChangeRelay; present is false when the result carries none.
type processWriteLayoutChange struct {
	present  bool
	elements []string
}

func (f *processWriteFields) layoutChange() processWriteLayoutChange {
	value := processWriteProperty(f.node, "layoutChange")
	if value == nil || value.kind == jkNull {
		return processWriteLayoutChange{}
	}
	if value.kind != jkObject {
		f.bad = true
		return processWriteLayoutChange{}
	}
	nested := processWriteFields{node: value}
	nested.text("reason")
	nested.text("summary")
	change := processWriteLayoutChange{present: true, elements: nested.texts("elements")}
	if nested.bad {
		f.bad = true
	}
	return change
}

// elementsClause is LayoutChangeRelay.ElementsClause.
func (l processWriteLayoutChange) elementsClause() string {
	if len(l.elements) == 0 {
		return ""
	}
	named := []string{}
	for _, name := range l.elements {
		if strings.TrimSpace(name) != "" {
			named = append(named, name)
		}
	}
	return " Affected elements: " + strings.Join(named, ", ") + "."
}

// relaySentence is LayoutChangeRelay.RelaySentence.
func (l processWriteLayoutChange) relaySentence() string {
	return l.elementsClause() + " ASK THE USER which they want, and send nothing until they answer: the same operations to " +
		"modify-business-process-as-new-version, which leaves this process and its diagram untouched and " +
		"creates an INACTIVE version they can look at first, or the same operations here again with " +
		"confirm-layout-change, which re-draws this process in place."
}

func processWriteDeref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// processWriteNodeText is how clio interpolates a JsonNode: a string's value, nothing for null, JSON otherwise.
func processWriteNodeText(node *jnode) string {
	if node == nil || node.kind == jkNull {
		return ""
	}
	if node.kind == jkString {
		return node.text
	}
	return string(node.stjJSON())
}
