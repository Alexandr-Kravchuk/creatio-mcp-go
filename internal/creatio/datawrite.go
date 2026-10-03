package creatio

// Shared pieces of clio's data-write tools (odata-create/-update/-delete, execute-dataservice-batch and the
// DB-first data-binding tools), ported from clio master 914dab286:
//
//   - DataWriteDiagnostic (clio/Common/DataWriteDiagnostic.cs): the bounded write-boundary context every
//     data-write answer carries;
//   - SensitiveErrorTextRedactor.RedactUntrustedOrNull: redaction plus the untrusted-source-text fence;
//   - the subset of CreatioResponseError the write tools read (TryDetect, DescribeNonJsonResponse, the
//     foreign-key hint, the markup status probe);
//   - ODataKeyFormatter;
//   - the request paths: a write is sent ONCE (clio's ExecuteNonReplayablePostRequest / PATCH / DELETE),
//     never re-sent after a re-login, because a replayed POST duplicates the record (clio GH #1313).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// DataWriteDiagnostic is clio's DataWriteDiagnostic record.
type DataWriteDiagnostic struct {
	Operation        string  `json:"operation"`
	Entity           *string `json:"entity,omitempty"`
	ItemIndex        *int    `json:"item-index,omitempty"`
	WriteAttempted   bool    `json:"write-attempted"`
	TransportOutcome string  `json:"transport-outcome"`
	SideEffect       string  `json:"side-effect"`
	RetryAdvice      string  `json:"retry-advice"`
	Message          *string `json:"message,omitempty"`
}

var dataWriteIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// NewDataWriteDiagnostic is clio's DataWriteDiagnostic.Create.
func NewDataWriteDiagnostic(operation, entity string, itemIndex *int, attempted, responseReceived, acknowledged bool,
	message *string) *DataWriteDiagnostic {
	transport, effect := "not-attempted", "not-attempted"
	if attempted {
		transport, effect = "unknown", "unknown"
		if responseReceived {
			transport = "response-received"
		}
		if acknowledged {
			effect = "acknowledged"
		}
	}
	advice := "A write may have applied despite failure. Read affected records before deciding whether to resubmit; no automatic retry was made."
	switch effect {
	case "not-attempted":
		advice = "Correct the input or permissions before submitting."
	case "acknowledged":
		advice = "Verify important values by readback; do not repeat solely because later inspection fails."
	}
	var text string
	if message != nil {
		text = *message
		if preview := strings.Index(text, " Response: "); preview >= 0 {
			text = text[:preview]
		}
	}
	entity = dataWriteTrim(entity)
	var safeEntity *string
	if dataWriteIdentifierPattern.MatchString(entity) {
		safeEntity = &entity
	} else {
		safeEntity = DataWriteUntrusted(entity)
	}
	return &DataWriteDiagnostic{Operation: operation, Entity: safeEntity, ItemIndex: itemIndex, WriteAttempted: attempted,
		TransportOutcome: transport, SideEffect: effect, RetryAdvice: advice, Message: DataWriteUntrusted(text)}
}

// dataWriteTrim is .NET string.Trim: Unicode white space at both ends.
func dataWriteTrim(text string) string {
	return strings.TrimFunc(text, unicode.IsSpace)
}

// dataWriteBlank is .NET string.IsNullOrWhiteSpace.
func dataWriteBlank(text string) bool {
	return dataWriteTrim(text) == ""
}

const (
	dataWriteFencePrefix      = "[untrusted-source-text begin] "
	dataWriteFenceSuffix      = " [untrusted-source-text end]"
	dataWriteUntrustedLimit   = 300
	dataWriteUntrustedScanCap = dataWriteUntrustedLimit * 8
)

var dataWriteFenceToken = regexp.MustCompile(`(?i)\[\s*untrusted-source-text[^\]]*\]|untrusted-source-text\s*(?:begin|end)`)

// DataWriteUntrusted is clio's SensitiveErrorTextRedactor.RedactUntrustedOrNull: nil for blank text,
// otherwise the text unwrapped from an outer fence, cut to 2400 UTF-16 units, redacted, flattened (every
// display-hostile character becomes one space), stripped of fence tokens, capped at 300 characters and
// fenced as untrusted data.
func DataWriteUntrusted(text string) *string {
	if dataWriteBlank(text) {
		return nil
	}
	if strings.HasPrefix(text, dataWriteFencePrefix) && strings.HasSuffix(text, dataWriteFenceSuffix) &&
		len(text) >= len(dataWriteFencePrefix)+len(dataWriteFenceSuffix) {
		text = text[len(dataWriteFencePrefix) : len(text)-len(dataWriteFenceSuffix)]
	}
	units := utf16.Encode([]rune(text))
	if len(units) > dataWriteUntrustedScanCap {
		units = units[:dataWriteUntrustedScanCap]
		if last := units[len(units)-1]; last >= 0xD800 && last <= 0xDBFF {
			// A cut pair leaves a lone high surrogate, which clio's flattening turns into a space.
			units[len(units)-1] = ' '
		}
		text = string(utf16.Decode(units))
	}
	flattened := dataWriteFlatten(redact.Text(text))
	if flattened == "" {
		return nil
	}
	flattened = dataWriteFenceToken.ReplaceAllString(flattened, "(fence removed)")
	if runes := []rune(flattened); len(runes) > dataWriteUntrustedLimit {
		flattened = string(runes[:dataWriteUntrustedLimit]) + "…"
	}
	fenced := dataWriteFencePrefix + flattened + dataWriteFenceSuffix
	return &fenced
}

// dataWriteFlatten is clio's FlattenDisplayHostileRuns: control, separator, surrogate (any character
// outside the BMP, which .NET holds as a surrogate pair) and format characters become a space, runs of
// spaces collapse to one, and the ends are trimmed.
func dataWriteFlatten(text string) string {
	var builder strings.Builder
	lastSpace := false
	for _, r := range text {
		if dataWriteDisplayHostile(r) {
			r = ' '
		}
		if r == ' ' {
			if lastSpace {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		builder.WriteRune(r)
	}
	return strings.Trim(builder.String(), " ")
}

func dataWriteDisplayHostile(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp) || r > 0xFFFF ||
		(r >= 0xD800 && r <= 0xDFFF) || unicode.Is(unicode.Cf, r)
}

// dataWriteCorrelationID is clio's IOperationCorrelationIdProvider.New: twelve lower-case hex digits.
func dataWriteCorrelationID() string {
	buffer := make([]byte, 6)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

// ---- CreatioResponseError (the part the write tools read) ----

const dataWriteUnregisteredEntityHint = "The OData entity set is not queryable yet. If it was just created with create-entity-schema or " +
	"create-lookup, this is the expected ~1-2 min asynchronous OData rebuild: wait briefly and retry, " +
	"do not compile or restart. Compile and restart only if it still fails after retrying (for example " +
	"an entity deployed without compilation)."

const dataWriteNonJSONHint = "The response was not JSON, which Creatio's OData pipeline never returns by itself (even a server error " +
	"is one of the recognized JSON error shapes). This points to the request not reaching Creatio intact - " +
	"a proxy/IIS/routing error, or a session redirect - rather than a problem with the request's OData/ESQ " +
	"shape. Whether the change was actually applied is unverified; confirm with odata-read before retrying."

// dataWriteDescribeNonJSON is CreatioResponseError.DescribeNonJsonResponse.
func dataWriteDescribeNonJSON(body string) string {
	status := ""
	if code, ok := dataWriteMarkupStatus(body); ok {
		status = fmt.Sprintf(" The server answered with an %s.", dataWriteMarkupStatusPhrase(code))
	}
	return "Creatio did not return a JSON response." + status + " " + dataWriteNonJSONHint + " Response: " + dataWriteTruncate(body)
}

func dataWriteMarkupStatusPhrase(code int) string { return fmt.Sprintf("HTTP %d error page", code) }

// dataWriteTruncate is CreatioResponseError.Truncate: 500 UTF-16 units and "...".
func dataWriteTruncate(value string) string {
	if value == "" {
		return "<empty>"
	}
	units := utf16.Encode([]rune(value))
	if len(units) > 500 {
		return string(utf16.Decode(units[:500])) + "..."
	}
	return value
}

var dataWriteMarkupTitle = regexp.MustCompile(`(?i)<title[^>]{0,64}>\s{0,8}(?:HTTP\s{1,4}Error\s{1,4})?([45]\d{2})(?:\.\d{1,2})?[\s\-–:<]`)

// dataWriteMarkupStatus is CreatioResponseError.TryGetMarkupErrorStatusCode. The lookahead of clio's
// pattern is a consumed character here, which matches the same texts.
func dataWriteMarkupStatus(body string) (int, bool) {
	match := dataWriteMarkupTitle.FindStringSubmatch(body)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	return code, err == nil
}

// dataWriteIsMarkup is CreatioResponseError.IsMarkup.
func dataWriteIsMarkup(body string) bool {
	stripped := strings.ToLower(dataWriteTrimMarkupPreamble(body))
	for _, prefix := range []string{"<!doctype", "<html", "<title", "<body"} {
		if strings.HasPrefix(stripped, prefix) {
			return true
		}
	}
	return false
}

func dataWriteTrimMarkupPreamble(body string) string {
	trimBlanks := func(value string) string {
		for {
			before := len(value)
			value = strings.TrimLeftFunc(value, unicode.IsSpace)
			value = strings.TrimLeft(value, "\uFEFF​")
			if len(value) == before {
				return value
			}
		}
	}
	stripped := trimBlanks(body)
	for strings.HasPrefix(stripped, "<?") {
		end := strings.Index(stripped, "?>")
		if end < 0 {
			break
		}
		stripped = trimBlanks(stripped[end+2:])
	}
	return stripped
}

type dataWriteResponseContext int

const (
	dataWriteServiceContext dataWriteResponseContext = iota
	dataWriteODataContext
)

// dataWriteObject is a JSON object that keeps its member order, so "the first member named X" is answered
// as System.Text.Json answers it.
type dataWriteObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func dataWriteParseObject(raw []byte) (*dataWriteObject, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, false
	}
	object := &dataWriteObject{values: map[string]json.RawMessage{}}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, _ := keyToken.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		if _, seen := object.values[key]; !seen {
			object.keys = append(object.keys, key)
			object.values[key] = value
		}
	}
	return object, true
}

func (o *dataWriteObject) raw(name string) (json.RawMessage, bool) {
	value, ok := o.values[name]
	return value, ok
}

func (o *dataWriteObject) str(names ...string) (string, bool) {
	for _, name := range names {
		if raw, ok := o.values[name]; ok {
			var text string
			if json.Unmarshal(raw, &text) == nil && dataWriteJSONKind(raw) == 's' {
				return text, true
			}
		}
	}
	return "", false
}

func (o *dataWriteObject) object(name string) (*dataWriteObject, bool) {
	raw, ok := o.values[name]
	if !ok {
		return nil, false
	}
	return dataWriteParseObject(raw)
}

// dataWriteJSONKind is the JSON kind of a raw value: 'o', 'a', 's', 'n' (number), 't', 'f', 'z' (null).
func dataWriteJSONKind(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	switch trimmed[0] {
	case '{':
		return 'o'
	case '[':
		return 'a'
	case '"':
		return 's'
	case 't':
		return 't'
	case 'f':
		return 'f'
	case 'n':
		return 'z'
	default:
		return 'n'
	}
}

// dataWriteDetectError is CreatioResponseError.TryDetect.
func dataWriteDetectError(root json.RawMessage, context dataWriteResponseContext) (string, bool) {
	object, ok := dataWriteParseObject(root)
	if !ok {
		return "", false
	}
	isService := context == dataWriteServiceContext
	provenOData := false
	if !isService {
		if text, ok := object.str("@odata.context"); ok && !dataWriteBlank(text) {
			provenOData = true
		}
	}
	if message, ok := dataWriteDetectDataServiceEnvelope(object); ok {
		return message, true
	}
	if isService {
		if message, ok := dataWriteDetectBaseResponse(object); ok {
			return message, true
		}
	}
	if raw, ok := object.raw("error"); ok && dataWriteJSONKind(raw) == 'o' {
		errorObject, _ := dataWriteParseObject(raw)
		if message, ok := errorObject.raw("message"); ok && dataWriteJSONKind(message) == 's' {
			var text string
			_ = json.Unmarshal(message, &text)
			return text, true
		}
		return string(bytes.TrimSpace(raw)), true
	}
	if provenOData {
		return "", false
	}
	if _, a := object.raw("ExceptionType"); a {
		return dataWriteAspNetMessage(object), true
	}
	if _, a := object.raw("ExceptionMessage"); a {
		return dataWriteAspNetMessage(object), true
	}
	if _, a := object.raw("StackTrace"); a {
		return dataWriteAspNetMessage(object), true
	}
	return dataWriteDetectRoutingError(object, context)
}

func dataWriteAspNetMessage(object *dataWriteObject) string {
	if text, ok := object.str("ExceptionMessage", "Message"); ok {
		return text
	}
	return "Creatio returned a server error."
}

func dataWriteDetectDataServiceEnvelope(object *dataWriteObject) (string, bool) {
	for _, annotation := range []string{"@odata.context", "@odata.id", "@odata.etag"} {
		if _, ok := object.raw(annotation); ok {
			return "", false
		}
	}
	raw, ok := object.raw("Code")
	if !ok {
		raw, ok = object.raw("code")
	}
	if !ok || dataWriteJSONKind(raw) != 'n' {
		return "", false
	}
	code, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 32)
	if err != nil || code == 0 {
		return "", false
	}
	detail, _ := object.str("Exception", "exception", "Message", "message")
	if dataWriteBlank(detail) {
		return "", false
	}
	return fmt.Sprintf("Creatio returned error code %d: %s", code, detail), true
}

func dataWriteDetectBaseResponse(object *dataWriteObject) (string, bool) {
	successRaw, hasSuccess := object.raw("success")
	if !hasSuccess {
		successRaw, hasSuccess = object.raw("Success")
	}
	explicitFailure := hasSuccess && dataWriteJSONKind(successRaw) == 'f'
	explicitSuccess := hasSuccess && dataWriteJSONKind(successRaw) == 't'
	detail, haveDetail := "", false
	populated := false
	errorInfoRaw, ok := object.raw("errorInfo")
	if !ok {
		errorInfoRaw, ok = object.raw("ErrorInfo")
	}
	if ok && dataWriteJSONKind(errorInfoRaw) == 'o' {
		errorInfo, _ := dataWriteParseObject(errorInfoRaw)
		detail, haveDetail = errorInfo.str("message", "Message", "errorMessage", "ErrorMessage")
		code, _ := errorInfo.str("errorCode", "ErrorCode", "code", "Code")
		populated = !dataWriteBlank(detail) || !dataWriteBlank(code)
		if dataWriteBlank(detail) && !dataWriteBlank(code) {
			detail, haveDetail = code, true
		}
	}
	if !explicitFailure && !(populated && !explicitSuccess) {
		return "", false
	}
	if !haveDetail {
		detail, _ = object.str("errorMessage", "ErrorMessage", "message", "Message")
	}
	if dataWriteBlank(detail) {
		return "Creatio reported the request as failed without an error message.", true
	}
	return "Creatio reported the request as failed: " + detail, true
}

func dataWriteDetectRoutingError(object *dataWriteObject, context dataWriteResponseContext) (string, bool) {
	raw, ok := object.raw("Message")
	if !ok || dataWriteJSONKind(raw) != 's' {
		return "", false
	}
	for _, key := range object.keys {
		if key != "Message" && key != "MessageDetail" {
			return "", false
		}
	}
	var bare string
	_ = json.Unmarshal(raw, &bare)
	detail, _ := object.str("MessageDetail")
	if context != dataWriteODataContext && !dataWriteRoutingMiss(detail) && !dataWriteRoutingMiss(bare) {
		return "", false
	}
	primary := bare
	if detail != "" {
		primary = detail
	}
	if primary == "" {
		return "Creatio returned an empty error response.", true
	}
	if dataWriteRoutingMiss(detail) || dataWriteRoutingMiss(bare) {
		return primary + " " + dataWriteUnregisteredEntityHint, true
	}
	return primary, true
}

func dataWriteRoutingMiss(text string) bool {
	lower := strings.ToLower(text)
	return text != "" && (strings.Contains(lower, strings.ToLower("No type was found that matches the controller")) ||
		strings.Contains(lower, strings.ToLower("No HTTP resource was found that matches the request URI")))
}

// ---- the foreign-key hint (CreatioResponseError.StructuredDetail) ----

const dataWriteStructuredPrefix = "From the error payload (validated identifiers only): "

const dataWriteMissingLookupAdvice = " Inspect lookup metadata with get-entity-schema-properties and verify the supplied IDs. " +
	"If the relationship remains unclear, ask an administrator to resolve the named constraint. " +
	"Do not guess replacement IDs; follow retry-guidance before resubmitting."

var (
	dataWritePostgresInsertFK = regexp.MustCompile(`insert or update on table "([A-Za-z_][A-Za-z0-9_]{0,127})" violates foreign key constraint "([A-Za-z_][A-Za-z0-9_]{0,127})"`)
	dataWritePostgresMissing  = regexp.MustCompile(`Key \(([A-Za-z_][A-Za-z0-9_]{0,127})\)=\([^()\r\n]{1,64}\) is not present in table "([A-Za-z_][A-Za-z0-9_]{0,127})"`)
	dataWritePostgresDeleteFK = regexp.MustCompile(`update or delete on table "([A-Za-z_][A-Za-z0-9_]{0,127})" violates foreign key constraint "([A-Za-z_][A-Za-z0-9_]{0,127})" on table "([A-Za-z_][A-Za-z0-9_]{0,127})"`)
	dataWriteSQLInsertFK      = regexp.MustCompile(`The (?:INSERT|UPDATE) statement conflicted with the FOREIGN KEY constraint "([A-Za-z_][A-Za-z0-9_]{0,127})"\. The conflict occurred in database "[^"\r\n]{1,128}", table "(?:[A-Za-z_][A-Za-z0-9_]{0,127}\.)?([A-Za-z_][A-Za-z0-9_]{0,127})", column '[A-Za-z_][A-Za-z0-9_]{0,127}'`)
	dataWriteSQLDeleteFK      = regexp.MustCompile(`The (?:DELETE|UPDATE) statement conflicted with the REFERENCE constraint "([A-Za-z_][A-Za-z0-9_]{0,127})"\. The conflict occurred in database "[^"\r\n]{1,128}", table "(?:[A-Za-z_][A-Za-z0-9_]{0,127}\.)?([A-Za-z_][A-Za-z0-9_]{0,127})", column '([A-Za-z_][A-Za-z0-9_]{0,127})'`)
)

// dataWriteAppendForeignKeyHint is CreatioResponseError.AppendStructuredODataWriteError.
func dataWriteAppendForeignKeyHint(redactedServerError string, root json.RawMessage) string {
	hint := dataWriteForeignKeyHint(root)
	if hint == "" {
		return redactedServerError
	}
	if dataWriteBlank(redactedServerError) {
		return hint
	}
	return redactedServerError + " " + hint
}

func dataWriteForeignKeyHint(root json.RawMessage) string {
	object, ok := dataWriteParseObject(root)
	if !ok {
		return ""
	}
	errorObject, ok := object.object("error")
	if !ok {
		return ""
	}
	var messages []string
	if headline, ok := errorObject.raw("message"); ok && dataWriteJSONKind(headline) == 's' {
		var text string
		_ = json.Unmarshal(headline, &text)
		messages = append(messages, text)
	}
	dataWriteInnerMessages(errorObject, &messages, 0)
	for _, message := range messages {
		if message == "" || len(utf16.Encode([]rune(message))) > 2048 {
			continue
		}
		if fact := dataWriteForeignKeyFact(message); fact != "" {
			return dataWriteStructuredPrefix + fact
		}
	}
	return ""
}

func dataWriteInnerMessages(node *dataWriteObject, messages *[]string, depth int) {
	if depth >= 3 {
		return
	}
	for _, member := range []string{"innererror", "internalexception"} {
		inner, ok := node.object(member)
		if !ok {
			continue
		}
		if text, ok := inner.str("message", "Message"); ok && !dataWriteBlank(text) {
			*messages = append(*messages, text)
		}
		dataWriteInnerMessages(inner, messages, depth+1)
	}
}

func dataWriteForeignKeyFact(message string) string {
	if insert := dataWritePostgresInsertFK.FindStringSubmatch(message); insert != nil {
		cause := "a referenced record is missing. The response does not identify the foreign-key column or referenced table."
		if detail := dataWritePostgresMissing.FindStringSubmatch(message); detail != nil {
			cause = fmt.Sprintf("a value in column '%s' has no matching record in referenced table '%s'.", detail[1], detail[2])
		}
		return fmt.Sprintf("foreign key constraint '%s' on table '%s' rejected the write: %s%s", insert[2], insert[1], cause, dataWriteMissingLookupAdvice)
	}
	if deleted := dataWritePostgresDeleteFK.FindStringSubmatch(message); deleted != nil {
		return fmt.Sprintf("the record is still referenced: foreign key constraint '%s' on table '%s' points at this row of table '%s'. %s",
			deleted[2], deleted[3], deleted[1], dataWriteStillReferencedAdvice(deleted[3]))
	}
	if insert := dataWriteSQLInsertFK.FindStringSubmatch(message); insert != nil {
		return fmt.Sprintf("foreign key constraint '%s' rejected the write: a referenced record is missing from referenced table '%s'. "+
			"The response does not identify the foreign-key column.%s", insert[1], insert[2], dataWriteMissingLookupAdvice)
	}
	if deleted := dataWriteSQLDeleteFK.FindStringSubmatch(message); deleted != nil {
		return fmt.Sprintf("the record is still referenced: constraint '%s' on table '%s' (column '%s') points at this row. %s",
			deleted[1], deleted[2], deleted[3], dataWriteStillReferencedAdvice(deleted[2]))
	}
	return ""
}

func dataWriteStillReferencedAdvice(table string) string {
	return fmt.Sprintf("Inspect the referencing rows in '%s' and confirm the intended relationship. ", table) +
		"The rejected operation may be a key update or an entity event handler's write. " +
		"Do not delete or re-point records without authorization; follow retry-guidance before resubmitting."
}

// ---- ODataKeyFormatter ----

var (
	dataWriteGUIDPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	dataWriteEntityPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func dataWriteIsGUID(text string) bool { return text != "" && dataWriteGUIDPattern.MatchString(text) }

// dataWriteValidEntity is ODataKeyFormatter.IsValidEntityName.
func dataWriteValidEntity(entity string) bool {
	return !dataWriteBlank(entity) && dataWriteEntityPattern.MatchString(dataWriteTrim(entity))
}

// dataWriteSimpleIdentifier is ODataKeyFormatter.IsSimpleIdentifier.
func dataWriteSimpleIdentifier(name string) bool {
	return !dataWriteBlank(name) && name == dataWriteTrim(name) && dataWriteEntityPattern.MatchString(name)
}

// dataWriteKeyPath is ODataKeyFormatter.KeyPath.
func dataWriteKeyPath(entity, id string) string {
	key := dataWriteTrim(id)
	if !dataWriteIsGUID(key) && !dataWriteNumeric(key) {
		key = "'" + strings.ReplaceAll(key, "'", "''") + "'"
	}
	return "odata/" + dataWriteTrim(entity) + "(" + key + ")"
}

func dataWriteNumeric(text string) bool {
	if _, err := strconv.ParseInt(text, 10, 64); err == nil {
		return true
	}
	_, err := strconv.ParseFloat(text, 64)
	return err == nil
}

// ---- requests ----

// dataWriteSendOnce sends one request with the current session and never re-sends it: a write whose
// session turns out to be stale reports what came back instead of being replayed. The answer is the body
// whatever the status, as clio's IApplicationClient returns it; err is a transport or session failure.
func (c *Client) dataWriteSendOnce(ctx context.Context, method, path string, body []byte, timeout time.Duration) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	session, err := c.currentSession(requestCtx)
	if err != nil {
		return "", err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(requestCtx, method, c.serviceURL(path), reader)
	if err != nil {
		return "", err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	writeClient := c.requestClient()
	// Clio's HTTP clients disable redirects. Following 307/308 here would silently replay the write.
	writeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := c.sendWithSession(writeClient, request, session)
	if err != nil {
		return "", err
	}
	payload, err := readResponseLimit(response, maxResponseBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(string(payload), "\uFEFF"), nil
}

// dataWriteGet is clio's ExecuteGetRequest for a read the write tools make (metadata, probes): the body
// whatever the status, "" when the request failed.
func (c *Client) dataWriteGet(ctx context.Context, path string, timeout time.Duration) string {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.serviceURL(path), nil)
		if err == nil {
			request.Header.Set("Accept", "application/json")
		}
		return request, err
	})
	if err != nil {
		return ""
	}
	payload, err := readResponseLimit(response, 64<<20)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(string(payload), "\uFEFF")
}
