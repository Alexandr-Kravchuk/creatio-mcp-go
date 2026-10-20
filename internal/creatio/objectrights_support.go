package creatio

// Shared by the object-rights tools clio 8.1.0.142 added (get-object-rights, set-object-rights,
// set-default-record-rights, apply-default-record-rights): the answer envelope, the line log the answer is
// built from, the request deadline, and clio's ObjectRightsSupport display rules.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// ObjectRightsResponse is clio's ObjectRightsToolResponse: success, and the command's collected log lines in
// output (exit code 0) or error (otherwise).
type ObjectRightsResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ObjectRightsFailure is ObjectRightsToolResponse.FromError / FromValidationError with the text as given.
func ObjectRightsFailure(text string) ObjectRightsResponse {
	return ObjectRightsResponse{Error: text}
}

// rightsLog collects the lines a command writes, in order, as clio's MCP logger does; info, warning and error
// lines all land in the same list.
type rightsLog struct{ lines []string }

func (l *rightsLog) info(format string, args ...any)  { l.add(format, args...) }
func (l *rightsLog) warn(format string, args ...any)  { l.add(format, args...) }
func (l *rightsLog) error(format string, args ...any) { l.add(format, args...) }

func (l *rightsLog) add(format string, args ...any) {
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}
	l.lines = append(l.lines, format)
}

// response is ObjectRightsToolResponse.From: the non-blank lines joined by "\n", redacted, as output on exit 0
// and as error otherwise.
func (l *rightsLog) response(exitCode int) ObjectRightsResponse {
	kept := []string{}
	for _, line := range l.lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	text := ""
	if len(kept) > 0 {
		text = redact.Text(strings.Join(kept, "\n"))
	}
	if exitCode == 0 {
		return ObjectRightsResponse{Success: true, Output: text}
	}
	return ObjectRightsResponse{Error: text}
}

// ---- deadline and request options (CreatioRequestOptions, RequestDeadline) ----

// rightsClock is the time source of a call, replaceable in tests.
type rightsClock struct {
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration)
}

func realRightsClock() rightsClock {
	return rightsClock{now: time.Now, sleep: func(ctx context.Context, d time.Duration) {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}}
}

// rightsDeadline is clio's RequestDeadline: a budget measured from its creation.
type rightsDeadline struct {
	budget  time.Duration
	started time.Time
	clock   rightsClock
}

func newRightsDeadline(budget time.Duration, clock rightsClock) *rightsDeadline {
	return &rightsDeadline{budget: budget, started: clock.now(), clock: clock}
}

func (d *rightsDeadline) remaining() time.Duration {
	left := d.budget - d.clock.now().Sub(d.started)
	if left < 0 {
		return 0
	}
	return left
}

func (d *rightsDeadline) spent() bool { return d.remaining() == 0 }

// rightsRequest is CreatioRequestOptions as the MCP tools use it: one attempt per request (MaxAttempts = 1),
// each at most timeout, all of them within the deadline when there is one.
type rightsRequest struct {
	timeout  time.Duration
	deadline *rightsDeadline
	clock    rightsClock
}

// next is ForNextRequest: the request timeout cut to what is left of the deadline, or clio's TimeoutException
// once the deadline is spent.
func (r rightsRequest) next() (time.Duration, error) {
	if r.deadline == nil {
		return r.timeout, nil
	}
	left := r.deadline.remaining()
	if left == 0 {
		return 0, &rightsFault{timeout: true, message: fmt.Sprintf(
			"The call's time limit of %s s is spent; the request was not sent.", rightsSeconds(r.deadline.budget))}
	}
	milliseconds := time.Duration(math.Ceil(float64(left)/float64(time.Millisecond))) * time.Millisecond
	if milliseconds < r.timeout {
		return milliseconds, nil
	}
	return r.timeout, nil
}

// rightsSeconds renders a duration as clio's "{TotalSeconds:0}": whole seconds, rounded half away from zero.
func rightsSeconds(d time.Duration) string {
	return strconv.FormatFloat(math.Round(d.Seconds()), 'f', 0, 64)
}

// ---- service failures ----

// rightsFault is a failed service call: what clio's exception says, whether it was a hang (IsTimeout), and
// whether no answer came, so a write may still have been applied (LeavesOutcomeUnknown).
type rightsFault struct {
	message  string
	timeout  bool
	noAnswer bool
}

func (f *rightsFault) Error() string { return f.message }

func rightsIsTimeout(err error) bool {
	var fault *rightsFault
	return errors.As(err, &fault) && fault.timeout
}

func rightsLeavesOutcomeUnknown(err error) bool {
	var fault *rightsFault
	return errors.As(err, &fault) && (fault.timeout || fault.noAnswer)
}

// rightsTransportFault turns a Go transport error into the text clio's client reports for it: a timeout as the
// cancelled task Creatio.Client's request ends in (measured against clio 8.1.0.142 on a request that hangs past
// its timeout), a refused connection as the socket's "Connection refused (host:port)".
func (c *Client) rightsTransportFault(err error, timeout time.Duration) error {
	if !rightsIsConnectionFailure(err) {
		return err
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &rightsFault{timeout: true, noAnswer: true, message: "A task was canceled."}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return &rightsFault{noAnswer: true, message: "Connection refused (" + rightsHostPort(c.config.BaseURL) + ")"}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &rightsFault{noAnswer: true, message: urlErr.Err.Error()}
	}
	var target transportError
	if errors.As(err, &target) {
		return &rightsFault{noAnswer: true, message: target.err.Error()}
	}
	return &rightsFault{noAnswer: true, message: err.Error()}
}

// rightsIsConnectionFailure says whether no answer came: a request, or the login before it, that never got a
// response (the client's transport error, or a url.Error inside an authentication failure).
func rightsIsConnectionFailure(err error) bool {
	var urlErr *url.Error
	return isTransportError(err) || errors.As(err, &urlErr)
}

func rightsHostPort(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(parsed.Scheme, "https") {
			port = "443"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

// rightsPost sends one POST (Creatio.Client's ExecutePostRequest with one attempt): the body whatever the status.
func (c *Client) rightsPost(ctx context.Context, route string, body []byte, request rightsRequest) (string, error) {
	timeout, err := request.next()
	if err != nil {
		return "", err
	}
	response, err := c.serviceRequest(ctx, serviceCall{Route: route, Body: body, Timeout: timeout, Limit: maxResponseBytes})
	if err != nil {
		return "", c.rightsTransportFault(err, timeout)
	}
	return strings.TrimPrefix(string(response.payload), "\uFEFF"), nil
}

// rightsPostNode is CreatioServiceClient.PostAndDeserialize over an ordered JSON tree: an empty body, or one that
// is not a JSON object, fails with clio's text.
func (c *Client) rightsPostNode(ctx context.Context, route string, body []byte, request rightsRequest) (*jnode, error) {
	text, err := c.rightsPost(ctx, route, body, request)
	if err != nil {
		return nil, err
	}
	address := c.serviceURL(route)
	if strings.TrimSpace(text) == "" {
		return nil, &rightsFault{message: "Empty response from " + address + "."}
	}
	node, parseErr := parseJNode([]byte(text))
	if parseErr != nil || (node.kind != jkObject && node.kind != jkNull) {
		if rightsLooksLikeMarkup(text) {
			return nil, &rightsFault{message: "Unexpected response from " + address +
				": an HTML page instead of JSON (a login redirect or a server error page)."}
		}
		return nil, &rightsFault{message: "Unexpected response from " + address + ": " + rightsSanitize(redact.Text(text), 200)}
	}
	return node, nil
}

func rightsLooksLikeMarkup(text string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }), "<")
}

// ---- SelectQuery (SelectQueryHelper) ----

const (
	rightsGUIDType = 0
	rightsTextType = 1
)

type rightsColumn struct {
	path, alias                   string
	orderDirection, orderPosition int
}

type rightsFilter struct {
	column        string
	value         any
	dataValueType int
}

// rightsSelectQuery is SelectQueryHelper.BuildSelectQuery.
func rightsSelectQuery(root string, columns []rightsColumn, filters []rightsFilter, rowCount int, distinct bool) *jnode {
	items := newObject()
	for _, column := range columns {
		position := column.orderPosition
		if column.orderDirection == 0 && position == 0 {
			position = -1
		}
		item := newObject()
		expression := newObject()
		expression.set("expressionType", newInt(0))
		expression.set("columnPath", newString(column.path))
		item.set("expression", expression)
		item.set("orderDirection", newInt(column.orderDirection))
		item.set("orderPosition", newInt(position))
		item.set("isVisible", newBool(true))
		items.set(column.alias, item)
	}
	filterItems := newObject()
	for index, filter := range filters {
		item := newObject()
		item.set("filterType", newInt(1))
		item.set("comparisonType", newInt(3))
		item.set("isEnabled", newBool(true))
		item.set("trimDateTimeParameterToDate", newBool(false))
		left := newObject()
		left.set("expressionType", newInt(0))
		left.set("columnPath", newString(filter.column))
		item.set("leftExpression", left)
		right := newObject()
		right.set("expressionType", newInt(2))
		parameter := newObject()
		parameter.set("value", toJNode(filter.value))
		parameter.set("dataValueType", newInt(filter.dataValueType))
		right.set("parameter", parameter)
		item.set("rightExpression", right)
		filterItems.set(fmt.Sprintf("filter%d", index), item)
	}
	query := rightsQueryEnvelope(root, distinct, rowCount)
	columnsNode := newObject()
	columnsNode.set("items", items)
	query.set("columns", columnsNode)
	filtersNode := newObject()
	filtersNode.set("filterType", newInt(6))
	filtersNode.set("isEnabled", newBool(true))
	filtersNode.set("trimDateTimeParameterToDate", newBool(false))
	filtersNode.set("logicalOperation", newInt(0))
	filtersNode.set("items", filterItems)
	query.set("filters", filtersNode)
	return query
}

func rightsQueryEnvelope(root string, distinct bool, rowCount int) *jnode {
	query := newObject()
	query.set("rootSchemaName", newString(root))
	query.set("operationType", newInt(0))
	query.set("allColumns", newBool(false))
	query.set("isDistinct", newBool(distinct))
	query.set("ignoreDisplayValues", newBool(false))
	query.set("rowCount", newInt(rowCount))
	query.set("rowsOffset", newInt(-1))
	query.set("isPageable", newBool(false))
	return query
}

// rightsCountQuery is SelectQueryHelper.BuildCountQuery: COUNT(Id) under the alias.
func rightsCountQuery(root, alias string) *jnode {
	query := rightsQueryEnvelope(root, false, 1)
	argument := newObject()
	argument.set("expressionType", newInt(0))
	argument.set("columnPath", newString("Id"))
	expression := newObject()
	expression.set("expressionType", newInt(1))
	expression.set("functionType", newInt(2))
	expression.set("aggregationType", newInt(1))
	expression.set("functionArgument", argument)
	item := newObject()
	item.set("expression", expression)
	item.set("isVisible", newBool(true))
	items := newObject()
	items.set(alias, item)
	columns := newObject()
	columns.set("items", items)
	query.set("columns", columns)
	return query
}

// rightsSelect is SelectQueryHelper.ExecuteSelectQuery: the rows of a successful answer, or clio's failure
// texts for an empty, non-JSON or rejected one.
func (c *Client) rightsSelect(ctx context.Context, query *jnode, request rightsRequest) ([]*jnode, error) {
	route := "DataService/json/SyncReply/SelectQuery"
	text, err := c.rightsPost(ctx, route, query.stjJSON(), request)
	if err != nil {
		return nil, err
	}
	address := c.serviceURL(route)
	if strings.TrimSpace(text) == "" {
		return nil, &rightsFault{message: schemaWriteEmptyBodyMessage("SelectQuery", address)}
	}
	node, parseErr := parseJNode([]byte(text))
	if parseErr == nil && node.kind == jkNull {
		return nil, &rightsFault{message: schemaWriteEmptyBodyMessage("SelectQuery", address)}
	}
	if parseErr == nil && !node.isObject() {
		parseErr = errors.New("The JSON value could not be converted to the response type.")
	}
	if parseErr != nil {
		if rightsLooksLikeMarkup(text) {
			return nil, &rightsFault{message: schemaWriteMarkupMessage("SelectQuery", address)}
		}
		return nil, &rightsFault{message: fmt.Sprintf("SelectQuery returned an unparseable response. URL: %s Parser error: %s. Response preview: %s",
			address, redact.Text(parseErr.Error()), schemaWritePreview(text))}
	}
	if success := processDescribeProperty(node, "success"); success == nil || success.kind != jkBool || !success.flag {
		message := ""
		if value := processDescribeProperty(processDescribeProperty(node, "errorInfo"), "message"); value != nil && value.kind == jkString {
			message = value.text
		}
		if strings.TrimSpace(message) == "" {
			message = "the server returned no failure details"
			if fenced := DataWriteUntrusted(text); fenced != nil {
				message = *fenced
			}
		}
		return nil, &rightsFault{message: "SelectQuery failed: " + message}
	}
	rows := processDescribeProperty(node, "rows")
	if !rows.isArray() {
		return nil, nil
	}
	return rows.items, nil
}

// ---- display rules (ObjectRightsSupport) ----

var rightsSchemaNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// rightsNormalizeSchemaName is TryNormalizeSchemaName: the trimmed name, when it is a plain schema identifier.
func rightsNormalizeSchemaName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	return name, name != "" && rightsSchemaNamePattern.MatchString(name)
}

var rightsSecurityPrefixes = []string{"SysAdmin", "SysUser", "SysSchema", "SysPackage", "SysSettings", "SysLic", "SysProcess", "Vw"}

// rightsIsSecurityObject is IsSecurityOrSystemObject.
func rightsIsSecurityObject(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range rightsSecurityPrefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return strings.HasSuffix(lower, "right") || strings.HasSuffix(lower, "rights")
}

// rightsSanitize is TextUtilities.SanitizeForDisplay: control, format and line/paragraph-separator characters
// become a space, a lone surrogate is dropped, and the text is capped at maxLength UTF-16 units with "...".
func rightsSanitize(text string, maxLength int) string {
	if text == "" {
		return text
	}
	if maxLength <= 0 {
		return "..."
	}
	units := utf16.Encode([]rune(text))
	output := make([]uint16, 0, min(len(units), maxLength+2))
	for index := 0; index < len(units) && len(output) <= maxLength; {
		unit := units[index]
		if utf16.IsSurrogate(rune(unit)) && unit < 0xDC00 && index+1 < len(units) && units[index+1] >= 0xDC00 && units[index+1] <= 0xDFFF {
			output = append(output, unit, units[index+1])
			index += 2
			continue
		}
		index++
		if unit >= 0xD800 && unit <= 0xDFFF {
			continue
		}
		r := rune(unit)
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			r = ' '
		}
		output = append(output, uint16(r))
	}
	if len(output) <= maxLength {
		return string(utf16.Decode(output))
	}
	cut := maxLength
	if last := output[cut-1]; last >= 0xD800 && last < 0xDC00 {
		cut--
	}
	return string(utf16.Decode(output[:cut])) + "..."
}

func rightsDisplay(value string) string { return rightsSanitize(value, 200) }

// rightsDisplayError is DisplayError: redacted, then kept on one line and capped at 500.
func rightsDisplayError(message string) string { return rightsSanitize(redact.Text(message), 500) }

func rightsDisplayFailure(err error) string {
	if err == nil {
		return ""
	}
	return rightsDisplayError(err.Error())
}

func rightsHasOwnTitle(name, caption string) bool {
	return strings.TrimSpace(caption) != "" && !strings.EqualFold(strings.TrimSpace(caption), name)
}

func rightsDisplayTitle(caption string) string {
	return strings.ReplaceAll(rightsSanitize(strings.TrimSpace(caption), 100), "'", "’")
}

// rightsFormatObject is FormatObject: 'Title' (Code) when the object has a title of its own, else 'Code'.
func rightsFormatObject(name, caption string) string {
	if rightsHasOwnTitle(name, caption) {
		return "'" + rightsDisplayTitle(caption) + "' (" + name + ")"
	}
	return "'" + name + "'"
}

type objectTitleMatch struct{ name, caption string }

func rightsFormatTitleMatches(matches []objectTitleMatch) string {
	parts := []string{}
	for index, match := range matches {
		if index == 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("'%s' (code: %s)", rightsDisplayTitle(match.caption), match.name))
	}
	listed := strings.Join(parts, "; ")
	if len(matches) <= 5 {
		return listed
	}
	return fmt.Sprintf("%s; and %d more", listed, len(matches)-5)
}

// ---- operation rows (ObjectRightsModel) ----

const allEmployeesID = "a29a3ba5-4b0d-de11-9a51-005056c00008"

type objectOperation int

const (
	opRead objectOperation = iota
	opCreate
	opEdit
	opDelete
)

var objectOperationNames = []string{"read", "create", "edit", "delete"}

const objectOperationsAccepted = "read,create,edit,delete"

// roleRow is RoleOperationRights.
type roleRow struct {
	granteeID   string
	granteeName string
	position    int
	ops         [4]bool
}

func (r roleRow) hasAny() bool { return r.ops[0] || r.ops[1] || r.ops[2] || r.ops[3] }

func (r roleRow) with(operations []objectOperation, value bool) roleRow {
	for _, operation := range operations {
		r.ops[operation] = value
	}
	return r
}

func (r roleRow) grantsSameAs(other *roleRow) bool {
	return other != nil && r.granteeID == other.granteeID && r.ops == other.ops
}

func (r roleRow) sameRowAs(other *roleRow) bool {
	return other != nil && r.position == other.position && r.grantsSameAs(other)
}

func (r roleRow) operationNames() []string {
	names := []string{}
	for index, granted := range r.ops {
		if granted {
			names = append(names, objectOperationNames[index])
		}
	}
	return names
}

func rightsFormatOperations(row roleRow) string {
	names := row.operationNames()
	if len(names) == 0 {
		return "no operations"
	}
	return strings.Join(names, "/")
}

func rightsFormatOperationList(operations []objectOperation) string {
	names := make([]string, len(operations))
	for index, operation := range operations {
		names[index] = objectOperationNames[operation]
	}
	return strings.Join(names, "/")
}

func rightsFormatRow(row roleRow, withID bool) string {
	id := ""
	if withID {
		id = " (" + row.granteeID + ")"
	}
	return fmt.Sprintf("[%d] %s%s: %s", row.position, rightsDisplay(row.granteeName), id, rightsFormatOperations(row))
}

func rightsFormatRows(rows []roleRow) string {
	ordered := append([]roleRow{}, rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].position < ordered[j].position })
	parts := make([]string, len(ordered))
	for index, row := range ordered {
		parts[index] = rightsFormatRow(row, false)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}

// objectRightsState is ObjectRightsState: the switch and the rows in priority order.
type objectRightsState struct {
	administered bool
	roles        []roleRow
}

func (s objectRightsState) hasAllEmployeesRow() bool {
	for _, row := range s.roles {
		if row.granteeID == allEmployeesID {
			return true
		}
	}
	return false
}

func rightsSwitch(s objectRightsState) string {
	if s.administered {
		return "ON"
	}
	return "OFF"
}

// diffRows is DiffRows: rows matched as a multiset by grantee, position and operations.
func (s objectRightsState) diffRows(other objectRightsState) (missing, extra []roleRow) {
	extra = append([]roleRow{}, other.roles...)
	for _, row := range s.roles {
		match := -1
		for index := range extra {
			if extra[index].sameRowAs(&row) {
				match = index
				break
			}
		}
		if match >= 0 {
			extra = append(extra[:match], extra[match+1:]...)
		} else {
			missing = append(missing, row)
		}
	}
	return missing, extra
}

func (s objectRightsState) sameAs(other objectRightsState) bool {
	missing, extra := s.diffRows(other)
	return s.administered == other.administered && len(missing) == 0 && len(extra) == 0
}

// ---- the record layer (DefaultRecordRightsModel) ----

type recordOperation int

const (
	recRead recordOperation = iota
	recEdit
	recDelete
)

var recordOperationNames = []string{"read", "edit", "delete"}

const recordOperationsAccepted = "read,edit,delete"

func recordLevelName(level int) string {
	switch level {
	case 0:
		return "-"
	case 1:
		return "granted"
	case 2:
		return "delegated"
	}
	return fmt.Sprintf("invalid level %d", level)
}

// recordRule is DefaultRecordRule.
type recordRule struct {
	authorID, authorName   string
	granteeID, granteeName string
	levels                 [3]int
	doNotApplyForManager   bool
}

func (r recordRule) isEmpty() bool { return r.levels == [3]int{} }

func (r recordRule) hasInvalidLevel() bool {
	for _, level := range r.levels {
		if level < 0 || level > 2 {
			return true
		}
	}
	return false
}

func (r recordRule) with(operations []recordOperation, level int) recordRule {
	for _, operation := range operations {
		r.levels[operation] = level
	}
	return r
}

func (r recordRule) samePairAs(other *recordRule) bool {
	return other != nil && r.authorID == other.authorID && r.granteeID == other.granteeID
}

func (r recordRule) sameRuleAs(other *recordRule) bool {
	return r.samePairAs(other) && r.levels == other.levels && r.doNotApplyForManager == other.doNotApplyForManager
}

// recordState is DefaultRecordRightsState.
type recordState struct {
	administered bool
	rules        []recordRule
}

func (s recordState) ruleFor(author, grantee string) *recordRule {
	for index := range s.rules {
		if s.rules[index].authorID == author && s.rules[index].granteeID == grantee {
			rule := s.rules[index]
			return &rule
		}
	}
	return nil
}

func (s recordState) rulesDifferFrom(other recordState) bool { return len(s.diffRules(other)) > 0 }

func (s recordState) diff(other recordState) []string {
	differences := []string{}
	if s.administered != other.administered {
		differences = append(differences, fmt.Sprintf("record permissions are %s, expected %s", recordSwitch(other), recordSwitch(s)))
	}
	return append(differences, s.diffRules(other)...)
}

func (s recordState) diffRules(other recordState) []string {
	differences := []string{}
	extra := append([]recordRule{}, other.rules...)
	for _, expected := range s.rules {
		match := -1
		for index := range extra {
			if extra[index].samePairAs(&expected) {
				match = index
				break
			}
		}
		if match < 0 {
			differences = append(differences, "rule "+recordRuleText(expected, false)+" is missing")
			continue
		}
		actual := extra[match]
		extra = append(extra[:match], extra[match+1:]...)
		if !actual.sameRuleAs(&expected) {
			differences = append(differences, "rule "+recordRuleText(actual, false)+", expected "+recordLevels(expected))
		}
	}
	for _, rule := range extra {
		differences = append(differences, "rule "+recordRuleText(rule, false)+" is not expected")
	}
	return differences
}

func recordSwitch(s recordState) string {
	if s.administered {
		return "ON"
	}
	return "OFF"
}

func rightsLower(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// recordLevels is DefaultRecordRightsFormat.Levels.
func recordLevels(rule recordRule) string {
	parts := make([]string, 3)
	for index, level := range rule.levels {
		parts[index] = recordOperationNames[index] + " " + recordLevelName(level)
	}
	return strings.Join(parts, ", ") + ", do not apply for manager: " + rightsLower(rule.doNotApplyForManager)
}

func recordUnitName(name, id string, withID bool) string {
	shown := name
	if shown == "" {
		shown = id
	}
	text := rightsDisplay(shown)
	if withID {
		text += " (" + id + ")"
	}
	return text
}

// recordRuleText is DefaultRecordRightsFormat.Rule.
func recordRuleText(rule recordRule, withIDs bool) string {
	return recordUnitName(rule.authorName, rule.authorID, withIDs) + " → " + recordUnitName(rule.granteeName, rule.granteeID, withIDs) +
		": " + recordLevels(rule)
}

func recordRulesText(rules []recordRule) string {
	if len(rules) == 0 {
		return "none"
	}
	parts := make([]string, len(rules))
	for index, rule := range rules {
		parts[index] = recordRuleText(rule, false)
	}
	return strings.Join(parts, "; ")
}

func recordOperationList(operations []recordOperation) string {
	names := make([]string, len(operations))
	for index, operation := range operations {
		names[index] = recordOperationNames[operation]
	}
	return strings.Join(names, "/")
}
