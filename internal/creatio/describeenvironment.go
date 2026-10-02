package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	applicationInfoRoute       = "ServiceModel/ApplicationInfoService.svc/GetApplicationInfo"
	systemEnvironmentInfoRoute = "ServiceModel/ApplicationInfoService.svc/GetSystemEnvironmentInfo"
	clioGateSysInfoRoute       = "rest/CreatioApiGateway/GetSysInfo"
	// DefaultDescribeTimeout is clio's default request timeout for remote commands.
	DefaultDescribeTimeout = 100 * time.Second
	clioGateSysInfoVersion = "2.0.0.32"
)

// describeRouteResponse is one raw answer from a fixed Creatio route. Unlike postCreatioServiceJSON it keeps
// the status code, because describe-environment tells "not Creatio" (404) apart from "unexpected" (500).
type describeRouteResponse struct {
	status       int
	payload      []byte
	needsReLogin bool
}

// callEnvironmentRoute sends one authenticated request to a fixed route; callers pass route constants only,
// never tool arguments.
func (c *Client) callEnvironmentRoute(ctx context.Context, method, route string, body []byte, timeout time.Duration) (describeRouteResponse, error) {
	switch route {
	case applicationInfoRoute, systemEnvironmentInfoRoute, clioGateSysInfoRoute:
	default:
		return describeRouteResponse{}, fmt.Errorf("unsupported environment route %q", route)
	}
	if timeout <= 0 {
		timeout = c.http.Timeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, needsReLogin, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(requestCtx, method, c.serviceURL(route), reader)
		if err != nil {
			return nil, fmt.Errorf("build %s request: %w", route, err)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		return describeRouteResponse{}, err
	}
	payload, err := readResponse(response)
	if err != nil {
		// A body cut off by the deadline is a connection failure, not a malformed answer.
		return describeRouteResponse{}, transportError{err: fmt.Errorf("%s response: %w", route, err)}
	}
	return describeRouteResponse{status: response.StatusCode, payload: payload, needsReLogin: needsReLogin}, nil
}

// UserCultureResult is the get-user-culture envelope. On failure it carries a reason and never a culture:
// the caller must ask the user rather than fall back to a default language.
type UserCultureResult struct {
	Success      bool   `json:"success"`
	Culture      string `json:"culture,omitempty"`
	ResolvedFrom string `json:"resolvedFrom"`
	Reason       string `json:"reason,omitempty"`
}

// UserCultureFailure builds the failure envelope; an empty reason reads "unknown", as in clio.
func UserCultureFailure(reason string) UserCultureResult {
	if strings.TrimSpace(reason) == "" {
		reason = "unknown"
	}
	return UserCultureResult{ResolvedFrom: "failed", Reason: reason}
}

// userCultureNamePattern accepts the language[-script][-region] names Creatio stores in SysCulture.
var userCultureNamePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z]{4})?(-([A-Za-z]{2}|[0-9]{3}))?$`)

// GetUserCulture reads the logged-in user's profile culture from applicationInfo.sysValues.userCulture.
// It never substitutes primaryCulture, the system default: that is not the user's language.
func (c *Client) GetUserCulture(ctx context.Context) UserCultureResult {
	response, err := c.callEnvironmentRoute(ctx, http.MethodPost, applicationInfoRoute, []byte("{}"), 0)
	if err != nil {
		if isAuthenticationError(err) {
			return UserCultureFailure("unauthorized")
		}
		return UserCultureFailure("unreachable")
	}
	if response.needsReLogin || response.status == http.StatusUnauthorized || response.status == http.StatusForbidden {
		return UserCultureFailure("unauthorized")
	}
	if response.status < http.StatusOK || response.status >= http.StatusMultipleChoices {
		return UserCultureFailure("unreachable")
	}
	var decoded struct {
		ApplicationInfo struct {
			SysValues struct {
				UserCulture struct {
					DisplayValue *string `json:"displayValue"`
				} `json:"userCulture"`
			} `json:"sysValues"`
		} `json:"applicationInfo"`
	}
	if err := json.Unmarshal(response.payload, &decoded); err != nil || decoded.ApplicationInfo.SysValues.UserCulture.DisplayValue == nil {
		return UserCultureFailure("userCulture-missing")
	}
	raw := strings.TrimSpace(*decoded.ApplicationInfo.SysValues.UserCulture.DisplayValue)
	if raw == "" {
		return UserCultureFailure("userCulture-missing")
	}
	culture, ok := canonicalCultureName(raw)
	if !ok {
		return UserCultureFailure("userCulture-invalid")
	}
	return UserCultureResult{Success: true, Culture: culture, ResolvedFrom: "environment"}
}

// canonicalCultureName validates the shape of a culture name and normalizes its case the way .NET's
// CultureInfo.Name does (en-us -> en-US, sr-latn-rs -> sr-Latn-RS). It checks the shape only; .NET also
// rejects well-formed names it has no data for.
func canonicalCultureName(name string) (string, bool) {
	if !userCultureNamePattern.MatchString(name) {
		return "", false
	}
	parts := strings.Split(name, "-")
	parts[0] = strings.ToLower(parts[0])
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) == 4 {
			parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
		} else {
			parts[i] = strings.ToUpper(parts[i])
		}
	}
	return strings.Join(parts, "-"), true
}

// DescribeLogMessage is one entry of clio's execution-log-messages channel.
type DescribeLogMessage struct {
	MessageType string `json:"message-type"`
	Value       string `json:"value"`
}

// DescribeResult is clio's command envelope: exit code 0 on success, 1 on an expected failure.
type DescribeResult struct {
	ExitCode int                  `json:"exit-code"`
	Messages []DescribeLogMessage `json:"execution-log-messages"`
}

// DescribeFailure is an exit-code-1 envelope with one Error message.
func DescribeFailure(message string) DescribeResult {
	return DescribeResult{ExitCode: 1, Messages: []DescribeLogMessage{{MessageType: "Error", Value: message}}}
}

// DescribeEnvironment reports the environment the way clio's describe-environment does: the
// ApplicationInfoService sysValues object, enriched best-effort with the database engine and framework
// (admin-gated GetSystemEnvironmentInfo) and with productName/licenseInfo when cliogate answers GetSysInfo.
// The report keeps Creatio's key order and is printed in Newtonsoft's indented layout, because clio
// returns it as one pre-formatted string.
func (c *Client) DescribeEnvironment(ctx context.Context, timeout time.Duration) DescribeResult {
	if timeout <= 0 {
		timeout = DefaultDescribeTimeout
	}
	report, failure := c.baseEnvironmentReport(ctx, timeout)
	if failure != "" {
		return DescribeFailure(failure)
	}
	c.enrichWithSystemEnvironmentInfo(ctx, report, timeout)
	messages := []DescribeLogMessage{}
	if warning := c.enrichFromClioGate(ctx, report, timeout); warning != "" {
		messages = append(messages, DescribeLogMessage{MessageType: "Warning", Value: warning})
	}
	text, err := report.indented()
	if err != nil {
		return DescribeFailure("The Creatio ApplicationInfoService returned an unexpected response.")
	}
	return DescribeResult{ExitCode: 0, Messages: append(messages, DescribeLogMessage{MessageType: "None", Value: text})}
}

func (c *Client) baseEnvironmentReport(ctx context.Context, timeout time.Duration) (*describeOrderedObject, string) {
	displayURI := c.displayAuthority()
	authFailure := fmt.Sprintf("Authentication failed for the Creatio application at '%s'. Verify the credentials and authentication settings.", displayURI)
	connectFailure := fmt.Sprintf("Could not connect to the Creatio application at '%s'. Verify the URL and make sure the application is running.", displayURI)
	nonCreatio := fmt.Sprintf("The URL '%s' does not appear to be a Creatio application. Verify the application URL and try again.", displayURI)
	const unexpected = "The Creatio ApplicationInfoService returned an unexpected response."

	response, err := c.callEnvironmentRoute(ctx, http.MethodPost, applicationInfoRoute, []byte("{}"), timeout)
	if err != nil {
		if isAuthenticationError(err) {
			return nil, authFailure
		}
		if isTransportError(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, connectFailure
		}
		return nil, unexpected
	}
	switch status := response.status; {
	case response.needsReLogin, status == http.StatusUnauthorized, status == http.StatusForbidden:
		return nil, authFailure
	case status == http.StatusNotFound, status == http.StatusMethodNotAllowed:
		return nil, nonCreatio
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status == http.StatusBadGateway,
		status == http.StatusServiceUnavailable, status == http.StatusGatewayTimeout:
		return nil, connectFailure
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return nil, unexpected
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(response.payload, &root); err != nil {
		if !json.Valid(response.payload) && describeIsNonJSON(response.payload) {
			return nil, nonCreatio
		}
		return nil, unexpected
	}
	var appInfo map[string]json.RawMessage
	if err := json.Unmarshal(root["applicationInfo"], &appInfo); err != nil || appInfo == nil {
		return nil, unexpected
	}
	report, err := describeParseObject(appInfo["sysValues"])
	if err != nil {
		return nil, unexpected
	}
	var coreVersion string
	if raw, ok := report.get("coreVersion"); !ok || json.Unmarshal(raw, &coreVersion) != nil || strings.TrimSpace(coreVersion) == "" {
		return nil, unexpected
	}
	return report, ""
}

// describeIsNonJSON mirrors clio: text that does not start like a JSON object, array or string is another
// application's page, while JSON-looking text that fails to parse is a malformed Creatio answer.
func describeIsNonJSON(payload []byte) bool {
	trimmed := bytes.TrimLeft(payload, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' && trimmed[0] != '"'
}

// enrichWithSystemEnvironmentInfo adds dbEngineType, frameworkKind and frameworkDescription. The operation
// needs CanManageSolution and a recent Creatio, so every failure is silent: the base report stands.
func (c *Client) enrichWithSystemEnvironmentInfo(ctx context.Context, report *describeOrderedObject, timeout time.Duration) {
	response, err := c.callEnvironmentRoute(ctx, http.MethodPost, systemEnvironmentInfoRoute, []byte("{}"), timeout)
	if err != nil || response.status != http.StatusOK {
		return
	}
	info, err := describeParseObject(response.payload)
	if err != nil {
		return
	}
	if raw, ok := info.get("success"); !ok || string(raw) != "true" {
		return
	}
	for _, field := range []string{"dbEngineType", "frameworkKind", "frameworkDescription"} {
		if raw, ok := info.get(field); ok && string(raw) != "null" {
			report.set(field, raw)
		}
	}
}

// enrichFromClioGate probes GetSysInfo as the capability check and merges the cliogate-only fields. It
// returns clio's warning when no data came back; the installed cliogate version only explains why.
func (c *Client) enrichFromClioGate(ctx context.Context, report *describeOrderedObject, timeout time.Duration) string {
	if c.mergeClioGateSysInfo(ctx, report, timeout) {
		return ""
	}
	const tail = " - ProductName and LicenseInfo are unavailable. All other fields (incl. DbEngineType and framework when CanManageSolution is granted) are reported."
	version, found, err := c.lowestClioGateVersion(ctx)
	switch {
	case err != nil:
		return "cliogate installation/version could not be determined after GetSysInfo returned no data" + tail
	case !found:
		return "cliogate " + clioGateSysInfoVersion + "+ is not installed" + tail
	case describeCompareVersions(version, clioGateSysInfoVersion) >= 0:
		return "cliogate " + version + " is installed, but GetSysInfo returned no data (the caller may lack the CanManageSolution permission)" + tail
	default:
		return "GetSysInfo returned no data; lowest detected cliogate alias version " + version + " is below required " + clioGateSysInfoVersion + tail
	}
}

func (c *Client) mergeClioGateSysInfo(ctx context.Context, report *describeOrderedObject, timeout time.Duration) bool {
	response, err := c.callEnvironmentRoute(ctx, http.MethodGet, clioGateSysInfoRoute, nil, timeout)
	if err != nil || response.status != http.StatusOK {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(response.payload, &root) != nil {
		return false
	}
	sysInfo, err := describeParseObject(root["SysInfo"])
	if err != nil {
		return false
	}
	present := func(raw json.RawMessage, ok bool) bool { return ok && string(raw) != "null" }
	if raw, ok := sysInfo.get("ProductName"); present(raw, ok) {
		report.set("productName", raw)
	}
	if raw, ok := sysInfo.get("LicenseInfo"); present(raw, ok) {
		report.set("licenseInfo", raw)
	}
	if _, has := report.get("dbEngineType"); !has {
		if raw, ok := sysInfo.get("DbEngineType"); present(raw, ok) {
			report.set("dbEngineType", raw)
		}
	}
	if _, has := report.get("frameworkDescription"); !has {
		if raw, ok := sysInfo.get("Runtime"); present(raw, ok) {
			report.set("frameworkDescription", raw)
		}
	}
	if _, has := report.get("frameworkKind"); !has {
		if raw, ok := sysInfo.get("IsNetCore"); ok && (string(raw) == "true" || string(raw) == "false") {
			kind := `"NetFramework"`
			if string(raw) == "true" {
				kind = `"Net"`
			}
			report.set("frameworkKind", json.RawMessage(kind))
		}
	}
	return true
}

// lowestClioGateVersion reads the installed cliogate package under both of its names. With both installed
// the lowest version counts, as in clio, so a stale alias cannot claim a capability it lacks.
func (c *Client) lowestClioGateVersion(ctx context.Context) (string, bool, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"Name": "Name", "Version": "Version"}, nil, 10000))
	if err != nil {
		return "", false, err
	}
	lowest, found := "", false
	for _, row := range rows {
		name := rowString(row, "Name")
		if !strings.EqualFold(name, "cliogate") && !strings.EqualFold(name, "cliogate_netcore") {
			continue
		}
		version := rowString(row, "Version")
		if !found || describeCompareVersions(version, lowest) < 0 {
			lowest, found = version, true
		}
	}
	return lowest, found, nil
}

// describeCompareVersions compares "2.0.0.32"-style versions numerically; a missing part counts as zero.
func describeCompareVersions(left, right string) int {
	l, r := strings.Split(left, "."), strings.Split(right, ".")
	for i := 0; i < max(len(l), len(r)); i++ {
		var a, b int
		if i < len(l) {
			a, _ = strconv.Atoi(l[i])
		}
		if i < len(r) {
			b, _ = strconv.Atoi(r[i])
		}
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

// displayAuthority is the scheme, host and non-default port of CREATIO_URL: no path, so a message does not
// carry more of the target than it needs.
func (c *Client) displayAuthority() string {
	parsed, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" && !(parsed.Scheme == "http" && port == "80") && !(parsed.Scheme == "https" && port == "443") {
		host += ":" + port
	}
	return strings.ToLower(parsed.Scheme) + "://" + host
}

// describeOrderedObject is a JSON object that keeps its members in document order. Go maps would sort them, and
// the report text has to list fields in the order Creatio sent them.
type describeOrderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func describeParseObject(raw json.RawMessage) (*describeOrderedObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	object := &describeOrderedObject{values: map[string]json.RawMessage{}}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object.set(key, value)
	}
	return object, nil
}

func (o *describeOrderedObject) get(key string) (json.RawMessage, bool) {
	value, ok := o.values[key]
	return value, ok
}

// set replaces an existing member in place or appends a new one, as Newtonsoft's JObject indexer does.
func (o *describeOrderedObject) set(key string, value json.RawMessage) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *describeOrderedObject) indented() (string, error) {
	var out bytes.Buffer
	out.WriteString("{")
	for i, key := range o.keys {
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString("\n  ")
		describeWriteString(&out, key)
		out.WriteString(": ")
		if err := describeWriteValue(&out, json.NewDecoder(bytes.NewReader(o.values[key])), "  "); err != nil {
			return "", err
		}
	}
	if len(o.keys) > 0 {
		out.WriteString("\n")
	}
	out.WriteString("}")
	return out.String(), nil
}

// describeWriteValue prints one JSON value in Newtonsoft's Formatting.Indented layout: two-space indent,
// "key": value, one array element per line, {} and [] for empty containers.
func describeWriteValue(out *bytes.Buffer, decoder *json.Decoder, indent string) error {
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		closing := "}"
		if value == '[' {
			closing = "]"
		}
		out.WriteString(string(value))
		inner := indent + "  "
		count := 0
		for decoder.More() {
			if count > 0 {
				out.WriteString(",")
			}
			out.WriteString("\n" + inner)
			if value == '{' {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, _ := keyToken.(string)
				describeWriteString(out, key)
				out.WriteString(": ")
			}
			if err := describeWriteValue(out, decoder, inner); err != nil {
				return err
			}
			count++
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		if count > 0 {
			out.WriteString("\n" + indent)
		}
		out.WriteString(closing)
	case string:
		describeWriteString(out, value)
	case json.Number:
		out.WriteString(describeNumber(value))
	case bool:
		out.WriteString(strconv.FormatBool(value))
	case nil:
		out.WriteString("null")
	}
	return nil
}

// describeNumber prints an integer as sent and a fraction as a .NET double, which always shows a
// decimal point ("1.50" -> "1.5", "2.0" -> "2.0").
func describeNumber(number json.Number) string {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		return text
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return text
	}
	formatted := strconv.FormatFloat(parsed, 'f', -1, 64)
	if !strings.Contains(formatted, ".") {
		formatted += ".0"
	}
	return formatted
}

// describeWriteString escapes like Newtonsoft's default StringEscapeHandling: quote, backslash and
// control characters only. Non-ASCII text and "/" stay literal, unlike Go's encoder.
func describeWriteString(out *bytes.Buffer, text string) {
	out.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x85 || r == 0x2028 || r == 0x2029 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
