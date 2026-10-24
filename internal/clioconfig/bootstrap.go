package clioconfig

// clio 8.1.0.142's SettingsBootstrapService, as its check-settings-health tool runs it: read
// appsettings.json, report what is wrong with it, and (in the MCP server, which applies repairs) create a
// missing file and stamp a pending migration. clio rewrites the whole file from its model; this port writes
// only the members the repair sets, under clio's own settings lock (knowledge.UpdateSettings).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

const (
	currentSettingsVersion  = 3
	defaultSitePortStart    = 40100
	defaultSitePortEnd      = 40199
	codeFileUnreadable      = "settings-file-unreadable"
	codeFileMissing         = "settings-file-missing"
	codeShapeMismatch       = "settings-shape-mismatch"
	codeInvalidActive       = "invalid-active-environment"
	codeSitePortRangeAdded  = "deploy-creatio-site-port-range-added"
	statusHealthy           = "healthy"
	statusIssues            = "issues-detected"
	statusBroken            = "broken"
	statusFileMissing       = "file-missing"
	invalidActiveEnvMessage = "ActiveEnvironmentKey is missing or does not point to a configured environment. " +
		"Use 'clio set-active-environment <name>' to fix this."
)

// Issue and Repair are SettingsHealthIssue and SettingsHealthRepair.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Repair struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Report is check-settings-health's answer (SettingsHealthResult); the two keys are omitted when null.
type Report struct {
	Status                       string   `json:"status"`
	SettingsFilePath             string   `json:"settings-file-path"`
	ActiveEnvironmentKey         *string  `json:"active-environment-key,omitempty"`
	ResolvedActiveEnvironmentKey *string  `json:"resolved-active-environment-key,omitempty"`
	EnvironmentCount             int      `json:"environment-count"`
	Issues                       []Issue  `json:"issues"`
	RepairsApplied               []Repair `json:"repairs-applied"`
	CanStartBootstrapTools       bool     `json:"can-start-bootstrap-tools"`
	CanExecuteEnvTools           bool     `json:"can-execute-env-tools"`
}

// ShapeMismatch is the report's settings-shape-mismatch issue, nil when there is none.
func (r Report) ShapeMismatch() *Issue {
	for index := range r.Issues {
		if r.Issues[index].Code == codeShapeMismatch {
			return &r.Issues[index]
		}
	}
	return nil
}

var readFile = os.ReadFile

// Check reads the settings file at path and reports its health. With applyRepairs a missing file is created
// and a pending migration written, as clio's MCP server does; an error is a failed write.
func Check(path string, applyRepairs bool) (Report, error) {
	data, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !applyRepairs {
			return buildReport(statusFileMissing, path, nil, nil, []Issue{{codeFileMissing, "appsettings.json does not exist."}}, nil), nil
		}
		if err := createDefaultFile(path); err != nil {
			return Report{}, err
		}
		return buildReport(statusHealthy, path, nil, nil, nil, nil), nil
	}
	if err != nil {
		return broken(path, codeFileUnreadable, "Unable to read appsettings.json: "+err.Error()), nil
	}
	root, failure := parseSettings(data)
	if failure != "" {
		return broken(path, codeFileUnreadable, failure), nil
	}
	bound := bindSettings(root)
	if environmentPath := firstEnvironmentFailure(bound.failures); environmentPath != "" {
		return broken(path, codeShapeMismatch, environmentBindFailureMessage(environmentPath)), nil
	}
	var issues []Issue
	shapeMessage := ""
	if len(bound.failures) > 0 {
		shapeMessage = shapeMismatchMessage(bound, settingsVersion(root))
		issues = append(issues, Issue{codeShapeMismatch, shapeMessage})
	}
	active := activeEnvironmentKey(root)
	environments := environmentsOf(root)
	if environments != nil && len(environments.Keys()) > 0 && (active == nil || strings.TrimSpace(*active) == "" || !hasKey(environments, *active)) {
		issues = append(issues, Issue{codeInvalidActive, invalidActiveEnvMessage})
	}
	var repairs []Repair
	if applyRepairs && shapeMessage == "" && settingsVersion(root) < currentSettingsVersion {
		repairs = migrationRepairs(root)
		if err := writeMigration(path); err != nil {
			return Report{}, err
		}
	}
	status := statusHealthy
	if len(issues) > 0 {
		status = statusIssues
	}
	return buildReport(status, path, active, root, issues, repairs), nil
}

func broken(path, code, message string) Report {
	return Report{Status: statusBroken, SettingsFilePath: path, Issues: []Issue{{code, message}}, RepairsApplied: []Repair{},
		CanStartBootstrapTools: true}
}

func buildReport(status, path string, active *string, root *knowledge.JSONObject, issues []Issue, repairs []Repair) Report {
	report := Report{Status: status, SettingsFilePath: path, ActiveEnvironmentKey: active, Issues: issues, RepairsApplied: repairs,
		CanStartBootstrapTools: true}
	if report.Issues == nil {
		report.Issues = []Issue{}
	}
	if report.RepairsApplied == nil {
		report.RepairsApplied = []Repair{}
	}
	if root == nil {
		return report
	}
	environments := environmentsOf(root)
	if environments == nil {
		return report
	}
	report.EnvironmentCount = len(environments.Keys())
	if active != nil && strings.TrimSpace(*active) != "" && hasKey(environments, *active) {
		key := *active
		report.ResolvedActiveEnvironmentKey = &key
		entry, _ := environments.Get(key)
		report.CanExecuteEnvTools = entry != nil
	}
	return report
}

// parseSettings reads the file the way the bootstrap does: a byte-order mark is skipped, comments and
// trailing commas are accepted, and the root must be an object. failure is the issue message otherwise.
func parseSettings(data []byte) (*knowledge.JSONObject, string) {
	text := string(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")))
	if strings.TrimSpace(text) == "" {
		return nil, "appsettings.json is empty or whitespace and cannot be parsed."
	}
	if !json.Valid([]byte(text)) {
		relaxed, ok := withoutCommentsAndTrailingCommas(text)
		if !ok || !json.Valid([]byte(relaxed)) {
			return nil, "appsettings.json could not be parsed: " + syntaxErrorText(text)
		}
		text = relaxed
	}
	if trimmed := strings.TrimLeft(text, " \t\r\n"); !strings.HasPrefix(trimmed, "{") {
		return nil, "appsettings.json does not contain a JSON object at its root."
	}
	root, err := knowledge.ParseJSONObject([]byte(text))
	if err != nil {
		return nil, "appsettings.json could not be parsed: " + err.Error()
	}
	return root, ""
}

// syntaxErrorText describes why text is not JSON with the line and position Json.NET's message ends with.
func syntaxErrorText(text string) string {
	var value any
	err := json.Unmarshal([]byte(text), &value)
	if err == nil {
		return "the content is not valid JSON."
	}
	message := strings.TrimSuffix(err.Error(), ".")
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		offset := int(min(syntax.Offset, int64(len(text))))
		line := strings.Count(text[:offset], "\n") + 1
		position := offset - strings.LastIndex(text[:offset], "\n") - 1
		return fmt.Sprintf("%s. Line %d, position %d.", message, line, position)
	}
	return message + "."
}

// memberFold is the member Json.NET binds a declared name to: an exact name first, then one ignoring case.
func memberFold(object *knowledge.JSONObject, name string) (string, any, bool) {
	if value, ok := object.Get(name); ok {
		return name, value, true
	}
	if key, ok := object.FindFold(name); ok {
		value, _ := object.Get(key)
		return key, value, true
	}
	return "", nil, false
}

func environmentsOf(root *knowledge.JSONObject) *knowledge.JSONObject {
	_, value, _ := memberFold(root, "Environments")
	environments, _ := value.(*knowledge.JSONObject)
	return environments
}

// activeEnvironmentKey is ActiveEnvironmentKey as the string member binds it: nil when absent or null, a
// number or boolean in its .NET text.
func activeEnvironmentKey(root *knowledge.JSONObject) *string {
	_, value, ok := memberFold(root, "ActiveEnvironmentKey")
	if !ok {
		return nil
	}
	switch value.(type) {
	case *knowledge.JSONObject, []any:
		return nil // the member failed to bind and kept its default
	}
	text, isNull := stringValue(value)
	if isNull {
		return nil
	}
	return &text
}

// hasKey is Dictionary.ContainsKey over a dictionary bound from JSON: ordinal.
func hasKey(object *knowledge.JSONObject, key string) bool {
	_, ok := object.Get(key)
	return ok
}

func settingsVersion(root *knowledge.JSONObject) int {
	_, value, _ := memberFold(root, "SettingsVersion")
	version, _, ok := readInt(value)
	if !ok {
		return 0
	}
	return version
}

func firstEnvironmentFailure(paths []string) string {
	for _, path := range paths {
		lower := strings.ToLower(path)
		if lower == "environments" || strings.HasPrefix(lower, "environments.") || strings.HasPrefix(lower, "environments[") {
			return path
		}
	}
	return ""
}

func environmentBindFailureMessage(path string) string {
	subject := "the Environments section"
	if key := environmentKeyOf(path); key != "" {
		subject = "environment '" + key + "'"
	}
	return "appsettings.json is valid JSON, but clio cannot bind " + subject + ": the member " +
		"'" + path + "' has a value of the wrong shape. Environments are not loaded at all " +
		"while this is true, because a member that silently took its default value would change what " +
		"commands do (a Safe environment would stop asking for confirmation). Correct " +
		"'" + path + "' by hand, or re-register the environment with 'clio reg-web-app'."
}

func environmentKeyOf(path string) string {
	const prefix = "environments."
	if !strings.HasPrefix(strings.ToLower(path), prefix) {
		return ""
	}
	remainder := path[len(prefix):]
	if separator := strings.IndexByte(remainder, '.'); separator >= 0 {
		remainder = remainder[:separator]
	}
	return remainder
}

func shapeMismatchMessage(bound bindResult, version int) string {
	var named []string
	for _, path := range bound.failures {
		if path != "" {
			named = append(named, path)
		}
	}
	sections := strings.Join(named, ", ")
	if sections == "" {
		return "appsettings.json is valid JSON, but this clio version (" + creatio.ClioVersion + ") " +
			"could not bind its root object. Check that the file contains a clio settings object " +
			"rather than some other JSON document. clio will not rewrite it while this is true."
	}
	preamble := "appsettings.json is valid JSON, but this clio version (" + creatio.ClioVersion +
		") cannot bind the following member(s): " + sections + ". "
	if writtenByNewerClio(bound, version) {
		return preamble + "A newer clio has written the file. Do NOT edit it - restart the resident process (the " +
			"MCP session) so it runs the new clio build, or update this one with 'clio update-cli' " +
			"when there is no session to restart. The settings themselves are intact and are left " +
			"untouched."
	}
	return preamble + "Correct " + sections + " by hand. clio will not rewrite the file while a member fails to bind, " +
		"so nothing else in it is at risk - but every setting under that member is taking its " +
		"default value until it is fixed."
}

func writtenByNewerClio(bound bindResult, version int) bool {
	if version > currentSettingsVersion {
		return true
	}
	for _, path := range bound.failures {
		section := path
		if separator := strings.IndexByte(path, '.'); separator >= 0 {
			section = path[:separator]
		}
		for unknown := range bound.unknown {
			if strings.EqualFold(unknown, section) {
				return true
			}
		}
	}
	return false
}

// migrationRepairs are the repairs ApplyMigrations reports for a file below the current version.
func migrationRepairs(root *knowledge.JSONObject) []Repair {
	if settingsVersion(root) < 2 && sitePortRangeMissing(root) {
		return []Repair{{codeSitePortRangeAdded, fmt.Sprintf("deploy-creatio default site-port-range was set to [%d, %d]",
			defaultSitePortStart, defaultSitePortEnd)}}
	}
	return []Repair{}
}

func sitePortRangeMissing(root *knowledge.JSONObject) bool {
	_, defaults, _ := memberFold(root, "deploy-creatio-defaults")
	object, ok := defaults.(*knowledge.JSONObject)
	if !ok {
		return true
	}
	_, value, _ := memberFold(object, "site-port-range")
	return value == nil
}

// writeMigration stamps the current version and, from below version 2, the default site-port range, on the
// file as it is when the lock is taken; nothing is written when that file no longer needs it.
func writeMigration(path string) error {
	return knowledge.UpdateSettings(path, func(root *knowledge.JSONObject) (bool, error) {
		if len(bindSettings(root).failures) > 0 || settingsVersion(root) >= currentSettingsVersion {
			return false, nil
		}
		if settingsVersion(root) < 2 && sitePortRangeMissing(root) {
			key, defaults, _ := memberFold(root, "deploy-creatio-defaults")
			object, ok := defaults.(*knowledge.JSONObject)
			if !ok {
				key, object = "deploy-creatio-defaults", knowledge.NewJSONObject()
				root.Set(key, object)
			}
			rangeKey, _, found := memberFold(object, "site-port-range")
			if !found {
				rangeKey = "site-port-range"
			}
			object.Set(rangeKey, []any{json.Number(fmt.Sprint(defaultSitePortStart)), json.Number(fmt.Sprint(defaultSitePortEnd))})
		}
		key, _, found := memberFold(root, "SettingsVersion")
		if !found {
			key = "SettingsVersion"
		}
		root.Set(key, json.Number(fmt.Sprint(currentSettingsVersion)))
		normalizeEnvironments(root)
		return true, nil
	})
}

// createDefaultFile writes what the bootstrap sets on a new settings model: the current version, the
// default site-port range and no environments. clio also serializes every other member's default.
func createDefaultFile(path string) error {
	return knowledge.UpdateSettings(path, func(root *knowledge.JSONObject) (bool, error) {
		if len(root.Keys()) > 0 {
			return false, nil
		}
		defaults := knowledge.NewJSONObject()
		defaults.Set("site-port-range", []any{json.Number(fmt.Sprint(defaultSitePortStart)), json.Number(fmt.Sprint(defaultSitePortEnd))})
		root.Set("deploy-creatio-defaults", defaults)
		root.Set("SettingsVersion", json.Number(fmt.Sprint(currentSettingsVersion)))
		root.Set("Environments", knowledge.NewJSONObject())
		return true, nil
	})
}

// stringValue is a string member's value: text as is, a number in its JSON text, a boolean as .NET writes
// it; isNull for null.
func stringValue(value any) (text string, isNull bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return typed, false
	case json.Number:
		return typed.String(), false
	case bool:
		if typed {
			return "True", false
		}
		return "False", false
	}
	return "", false
}
