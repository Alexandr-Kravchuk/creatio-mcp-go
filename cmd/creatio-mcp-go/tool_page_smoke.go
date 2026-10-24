package main

// page-smoke: clio 8.1.0.142's PageSmokeTool over PageSmokeBrowserResolver and PageSmokeRunner (internal/pagesmoke).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/pagesmoke"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var pageSmokeArguments = []string{"environment-name", "page", "record-id", "timeout", "click", "click-wait", "watch", "browser", "browser-path"}

const pageSmokeValidHint = "Valid: environment-name, page, record-id, timeout, click, click-wait, watch, browser, browser-path."

// pageSmokeAliases is the tool's ArgumentAliases: EnvironmentNameAliases plus its own, matched ignoring case.
var pageSmokeAliases = t21aWithAliases(map[string]string{"schema-name": "page", "schemaName": "page", "page-name": "page",
	"recordId": "record-id", "id": "record-id", "button": "click"})

// pageSmokeEntityName is CreatedRecordWatcher.IsValidEntityName.
var pageSmokeEntityName = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,127}\z`)

func init() {
	registerTool(map[string]any{"name": "page-smoke"}, invokePageSmoke, withAnnotations(toolAnnotations{Destructive: true}))
}

// pageSmokeBind splits the call's keys the way clio's binder does with the MCP serializer's web defaults: a
// member name matches ignoring case, every other key is extension data. When two keys spell one member, the exact
// spelling wins, else the last in sorted order (Go maps keep no request order).
func pageSmokeBind(args map[string]any) (map[string]any, map[string]any) {
	bound, extra := map[string]any{}, map[string]any{}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	exact := map[string]bool{}
	for _, key := range keys {
		member := ""
		for _, name := range pageSmokeArguments {
			if strings.EqualFold(key, name) {
				member = name
				break
			}
		}
		if member == "" {
			extra[key] = args[key]
			continue
		}
		if exact[member] {
			continue
		}
		bound[member] = args[key]
		exact[member] = key == member
	}
	return bound, extra
}

func invokePageSmoke(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, extra := pageSmokeBind(args)
	// environment-name and page are `required` members of clio's argument record: a missing one fails the binding
	// of the whole record.
	for _, name := range []string{"environment-name", "page"} {
		if _, ok := bound[name]; !ok {
			return nil, errors.New("invalid-parameter-type: argument 'args' for MCP tool 'page-smoke' must be an object. Received an incompatible JSON value.")
		}
	}
	bind := t21aBinder{tool: "page-smoke", args: bound}
	environmentName, err := bind.text("environment-name")
	if err != nil {
		return nil, err
	}
	page, err := bind.str("page")
	if err != nil {
		return nil, err
	}
	recordID, err := bind.str("record-id")
	if err != nil {
		return nil, err
	}
	timeout, err := bind.integer("timeout")
	if err != nil {
		return nil, err
	}
	click, err := bind.str("click")
	if err != nil {
		return nil, err
	}
	clickWait, err := bind.integer("click-wait")
	if err != nil {
		return nil, err
	}
	watch, _, err := bind.strings("watch")
	if err != nil {
		return nil, err
	}
	browser, err := bind.str("browser")
	if err != nil {
		return nil, err
	}
	browserPath, err := bind.str("browser-path")
	if err != nil {
		return nil, err
	}
	failure := func(message string) (*mcp.CallToolResult, error) {
		return pageSmokeResult(pagesmoke.FailureResponse(message))
	}
	if message := t21aAliasError(extra, nil, pageSmokeAliases, true, ".", pageSmokeValidHint); message != "" {
		return failure(message)
	}
	if message := pageSmokeValidate(environmentName, page, recordID, timeout, click, clickWait, watch, browser, browserPath); message != "" {
		return failure(message)
	}
	// The environment is resolved first, so a mistyped environment fails before any browser is downloaded or any
	// answer is remembered.
	client, resolution, err := envs.resolve("page-smoke", bound, scopeName)
	if err != nil {
		return failure(redact.Text(err.Error()))
	}
	if resolution != nil {
		return failure(redact.Text(resolution.Error()))
	}
	settingsPath := creatio.ClioSettingsPath()
	browserChoice, err := pagesmoke.NewResolver(settingsPath, os.Getenv).Resolve(ctx, browser, browserPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return failure(redact.Text(err.Error()))
	}
	if browserChoice.BrowserPath == "" {
		return pageSmokeResult(pagesmoke.NoBrowserResponse(browserChoice))
	}
	request := pagesmoke.Request{Page: strings.TrimSpace(*page), TimeoutSeconds: pagesmoke.DefaultTimeoutSeconds, Click: nil,
		ClickWaitSeconds: pagesmoke.DefaultClickWaitSeconds, BrowserPath: browserChoice.BrowserPath, BrowserNote: browserChoice.Note}
	if recordID != nil && strings.TrimSpace(*recordID) != "" {
		parsed, _ := parseDotnetGUID(*recordID)
		request.RecordID = parsed
	}
	if timeout != nil {
		request.TimeoutSeconds = *timeout
	}
	if click != nil && strings.TrimSpace(*click) != "" {
		trimmed := strings.TrimSpace(*click)
		request.Click = &trimmed
	}
	if clickWait != nil {
		request.ClickWaitSeconds = *clickWait
	}
	for _, name := range watch {
		request.Watch = append(request.Watch, *name)
	}
	result, err := pagesmoke.NewRunner(client, settingsPath).Run(ctx, request)
	if err != nil {
		return nil, err
	}
	return pageSmokeResult(pagesmoke.ResponseFrom(result))
}

// pageSmokeValidate is PageSmokeTool.Validate and ValidateClick, in clio's order.
func pageSmokeValidate(environmentName string, page, recordID *string, timeout *int, click *string, clickWait *int,
	watch []*string, browser, browserPath *string) string {
	if strings.TrimSpace(environmentName) == "" {
		return "environment-name is required."
	}
	if page == nil || !pageSmokeEntityName.MatchString(strings.TrimSpace(*page)) {
		return "page must be a Freedom UI form page schema name (letters, digits, underscore), for example " +
			"UsrOrders_FormPage; list-pages names them."
	}
	if recordID != nil && strings.TrimSpace(*recordID) != "" {
		if _, ok := parseDotnetGUID(*recordID); !ok {
			return "record-id must be a GUID; omit it to open the page in add mode."
		}
	}
	if timeout != nil && (*timeout < 5 || *timeout > pagesmoke.MaxTimeoutSeconds) {
		return fmt.Sprintf("timeout must be between 5 and %d seconds.", pagesmoke.MaxTimeoutSeconds)
	}
	if browser != nil && !pagesmoke.IsKnownChoice(*browser) {
		return "browser must be existing, download or none - the user's answer to browserConsent.question."
	}
	if browserPath != nil && (browser == nil || *browser != pagesmoke.ChoiceExisting) {
		return "browser-path applies only with browser=existing; take it from browserConsent.choices."
	}
	if browser != nil && *browser == pagesmoke.ChoiceExisting && (browserPath == nil || strings.TrimSpace(*browserPath) == "") {
		return "browser=existing needs browser-path: the browser-path of the choice the user picked in browserConsent.choices."
	}
	if click != nil && (strings.TrimSpace(*click) == "" || len(utf16.Encode([]rune(*click))) > 200) {
		return "click must be a button caption or element name, 1-200 characters."
	}
	if clickWait != nil && click == nil {
		return "click-wait applies only with click."
	}
	if clickWait != nil && (*clickWait < 1 || *clickWait > pagesmoke.MaxClickWaitSeconds) {
		return fmt.Sprintf("click-wait must be between 1 and %d seconds.", pagesmoke.MaxClickWaitSeconds)
	}
	if len(watch) > 0 && click == nil {
		return "watch applies only with click: it diffs the entities around the click."
	}
	invalid := len(watch) > pagesmoke.MaxWatchedEntities
	for _, name := range watch {
		if name == nil || !pageSmokeEntityName.MatchString(*name) {
			invalid = true
		}
	}
	if invalid {
		return fmt.Sprintf("watch takes 1-%d entity schema names.", pagesmoke.MaxWatchedEntities)
	}
	return ""
}

// parseDotnetGUID is Guid.TryParse followed by ToString(): the N, D, B and P formats (white space around them
// ignored), written back lowercase with hyphens.
func parseDotnetGUID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	switch {
	case len(value) == 38 && ((value[0] == '{' && value[37] == '}') || (value[0] == '(' && value[37] == ')')):
		value = value[1:37]
	}
	digits := value
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", false
		}
		digits = value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	}
	if len(digits) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(digits); err != nil {
		return "", false
	}
	lower := strings.ToLower(digits)
	return lower[:8] + "-" + lower[8:12] + "-" + lower[12:16] + "-" + lower[16:20] + "-" + lower[20:], true
}

// pageSmokeResult stamps the correlation id and writes the answer as clio's MCP server does.
func pageSmokeResult(response pagesmoke.Response) (*mcp.CallToolResult, error) {
	buffer := make([]byte, 6)
	_, _ = rand.Read(buffer)
	response.CorrelationID = hex.EncodeToString(buffer)
	text, err := creatio.ClioMcpResultText(response)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: json.RawMessage(text)}, nil
}
