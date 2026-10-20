package creatio

// The read side of clio 8.1.0.142's CreatioResponseError: TryClassify (which kind of OData error a body is),
// the locally authored read diagnostics, and DescribeStructuredODataError (validated identifiers only).

import (
	"encoding/json"
	"regexp"
	"strings"
)

type odataErrorKind int

const (
	odataKindServerError odataErrorKind = iota
	odataKindUnregisteredEntity
	odataKindInvalidQuery
)

const (
	odataDescribeEmptyRead = "Creatio returned no response body for this OData read. The shared GET transport reports a " +
		"connection failure, a DNS failure and a timeout all as an empty body, so this is a transport or " +
		"session problem rather than a query-shape problem; verify the environment is reachable and the " +
		"session is valid, then retry."
	odataDescribeNonJSONRead = "Creatio did not return a JSON OData response. This points to an IIS, proxy, routing, or session " +
		"problem rather than an OData query-shape problem; verify the environment and retry only after the " +
		"endpoint is returning JSON."
)

var odataInvalidQuerySignals = []string{"The query specified in the URI is not valid", "Could not find a property named", "not found in schema"}

// odataClassify is CreatioResponseError.TryClassify for an OData payload: whether the body is a recognized
// error, and its kind. Only the routing branch (a bare {Message[,MessageDetail]} body naming the routing
// miss) is the unregistered entity.
func odataClassify(root json.RawMessage) (odataErrorKind, bool) {
	detected, ok := dataWriteDetectError(root, dataWriteODataContext)
	if !ok {
		return odataKindServerError, false
	}
	object, _ := dataWriteParseObject(root)
	if odataIsRoutingMiss(object) {
		return odataKindUnregisteredEntity, true
	}
	detail := odataCollectServerDetail(object, detected)
	lower := strings.ToLower(detail)
	for _, signal := range odataInvalidQuerySignals {
		if strings.Contains(lower, strings.ToLower(signal)) {
			return odataKindInvalidQuery, true
		}
	}
	return odataKindServerError, true
}

func odataIsRoutingMiss(object *dataWriteObject) bool {
	if object == nil {
		return false
	}
	raw, ok := object.raw("Message")
	if !ok || dataWriteJSONKind(raw) != 's' {
		return false
	}
	for _, key := range object.keys {
		if key != "Message" && key != "MessageDetail" {
			return false
		}
	}
	message, _ := object.str("Message")
	detail, _ := object.str("MessageDetail")
	return dataWriteRoutingMiss(detail) || dataWriteRoutingMiss(message)
}

// odataCollectServerDetail joins the detected headline with every nested message under error.innererror /
// internalexception (three levels at most).
func odataCollectServerDetail(object *dataWriteObject, detected string) string {
	parts := []string{detected}
	if object != nil {
		if errorObject, ok := object.object("error"); ok {
			odataAppendInnerMessages(errorObject, &parts, 0)
		}
	}
	seen := map[string]bool{}
	var distinct []string
	for _, part := range parts {
		if !seen[part] {
			seen[part] = true
			distinct = append(distinct, part)
		}
	}
	return strings.Join(distinct, " | ")
}

func odataAppendInnerMessages(node *dataWriteObject, parts *[]string, depth int) {
	if depth >= 3 {
		return
	}
	for _, name := range []string{"innererror", "internalexception"} {
		inner, ok := node.object(name)
		if !ok {
			continue
		}
		if message, ok := inner.str("message", "Message"); ok && !dataWriteBlank(message) {
			*parts = append(*parts, message)
		}
		odataAppendInnerMessages(inner, parts, depth+1)
	}
}

// odataErrorCodeFor is ODataReadTool.ErrorCodeFor.
func odataErrorCodeFor(kind odataErrorKind) string {
	switch kind {
	case odataKindUnregisteredEntity:
		return odataCodeEntityNotFound
	case odataKindInvalidQuery:
		return odataCodeInvalidQuery
	default:
		return odataCodeServerReported
	}
}

// odataDescribeServerReportedReadError is CreatioResponseError.DescribeServerReportedReadError.
func odataDescribeServerReportedReadError(kind odataErrorKind, callerInput, structured string) string {
	classification := "Creatio reported an error for this OData read. The server's own wording is not reproduced " +
		"here, because a service or proxy response is not trusted text in an MCP transcript; check " +
		"the environment's own logs for the server-side cause, then verify the entity name, the " +
		"filter and the credentials. The correlation-id on this response matches it to clio's own " +
		"log lines."
	if kind == odataKindInvalidQuery {
		classification = "Creatio rejected this OData query as invalid: a property, a column or a query option in the " +
			"request could not be resolved. The server's own wording is not reproduced here, because a " +
			"service or proxy response is not trusted text in an MCP transcript; the correlation-id on " +
			"this response matches it to clio's own log lines. If the member was added moments ago, " +
			"wait for the OData rebuild and retry ONCE before changing anything - the same rejection is " +
			"how a column or entity that exists but is not published yet reports itself, and editing " +
			"the schema again in response makes it worse. Otherwise the query is at fault: correct the " +
			"field, select, expand or order-by names, because an unchanged retry cannot succeed."
	}
	if kind == odataKindUnregisteredEntity {
		classification += " " + dataWriteUnregisteredEntityHint
	}
	if !dataWriteBlank(structured) {
		classification += " " + structured
	}
	if dataWriteBlank(callerInput) {
		return classification
	}
	return classification + " " + callerInput
}

var (
	odataErrorCodePattern       = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	odataReadUnknownPropertyPattern = regexp.MustCompile(`Could not find a property named '([A-Za-z_][A-Za-z0-9_]{0,127})' on type '([A-Za-z_][A-Za-z0-9_]{0,127}(?:\.[A-Za-z_][A-Za-z0-9_]{0,127}){0,7})'`)
	odataColumnPathPattern      = regexp.MustCompile(`Column by path ([A-Za-z_][A-Za-z0-9_]{0,63}(?:[./][A-Za-z_][A-Za-z0-9_]{0,63}){0,3}) not found in schema ([A-Za-z_][A-Za-z0-9_]{0,63})\.`)
	odataOperandPattern         = regexp.MustCompile(`Found operand types '(Edm\.[A-Za-z][A-Za-z0-9]{0,31})' and '(Edm\.[A-Za-z][A-Za-z0-9]{0,31})' for operator kind '([A-Za-z]{1,32})'`)
	odataNullPropertyPattern    = regexp.MustCompile(`^Value cannot be null\.\s{1,4}Parameter name: property\s{0,4}$`)
	odataTypedLiteralTypes      = map[string]bool{"Edm.Date": true, "Edm.DateTimeOffset": true, "Edm.Duration": true, "Edm.Guid": true, "Edm.TimeOfDay": true}
)

// odataDescribeStructuredError is CreatioResponseError.DescribeStructuredODataError.
func odataDescribeStructuredError(root *jnode) string {
	if !root.isObject() {
		return ""
	}
	errorNode := root.get("error")
	if !errorNode.isObject() {
		return ""
	}
	code := ""
	if node := errorNode.get("code"); node != nil && node.kind == jkString && node.text != "" && odataErrorCodePattern.MatchString(node.text) {
		code = node.text
	}
	var messages []string
	if headline := errorNode.get("message"); headline != nil && headline.kind == jkString {
		messages = append(messages, headline.text)
	}
	if object, ok := dataWriteParseObject(errorNode.newtonsoftJSON()); ok {
		odataAppendInnerMessages(object, &messages, 0)
	}
	fact := odataFirstStructuredFact(messages)
	if code == "" && fact == "" {
		return ""
	}
	codePart, separator := "", ""
	if code != "" {
		codePart = "code '" + code + "'"
	}
	if code != "" && fact != "" {
		separator = "; "
	}
	return "From the error payload (validated identifiers only): " + codePart + separator + fact
}

func odataFirstStructuredFact(messages []string) string {
	for _, message := range messages {
		if message == "" || toFileUTF16Len(message) > 2048 {
			continue
		}
		if match := odataReadUnknownPropertyPattern.FindStringSubmatch(message); match != nil {
			return "unknown property '" + match[1] + "' on '" + odataShortTypeName(match[2]) + "'. Correct or remove that name in " +
				"select, filters, expand or order-by."
		}
		if match := odataColumnPathPattern.FindStringSubmatch(message); match != nil {
			return "column path '" + match[1] + "' not found in schema '" + match[2] + "'. Check that name in filters and select; a lookup is " +
				"filtered through its navigation path (for example Account/Id), not its raw foreign-key column."
		}
		if match := odataOperandPattern.FindStringSubmatch(message); match != nil {
			fact := "a filter compares a '" + match[1] + "' column with a '" + match[2] + "' value (operator '" + match[3] + "')."
			if match[2] == "Edm.String" && odataTypedLiteralTypes[match[1]] {
				return fact + " odata-read sends a JSON string as a quoted string literal, so this column cannot be " +
					"compared with a string value here; read that filter with execute-esq instead."
			}
			return fact + " Send the filter value as the JSON type of that column (a number, a boolean or a string)."
		}
		if odataNullPropertyPattern.MatchString(message) {
			return "Creatio could not resolve a property while building the response (a null 'property' " +
				"argument). One measured cause is a binary (Edm.Stream) column in select; if select names " +
				"one, remove it."
		}
	}
	return ""
}

func odataShortTypeName(name string) string {
	if strings.HasPrefix(name, "Edm.") {
		return name
	}
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		return name[dot+1:]
	}
	return name
}
