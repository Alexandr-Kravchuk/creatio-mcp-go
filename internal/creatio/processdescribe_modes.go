package creatio

// describe-business-process's reply modes, as clio 8.1.0.142 builds them (DescribeProcessCommand.BuildReply and
// ProcessDigest, ENG-103023): the whole graph (`full`), a compact `summary` the default answers with when the
// full reply would exceed 18,000 bytes, one `element` with its flows, and the `versions` list - each written as
// compact JSON with clio's MCP result encoder, with a content checksum. The raw DataSourceFilters value of an
// element whose filter the server decoded completely is left out (ENG-99970).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	processDescribeAutoSummaryBytes = 18000
	processDescribeSummaryBudget    = 3072
	processDescribeMaxListedNames   = 30
	processDescribeBisectionSteps   = 12
	processDescribeCompactWarning   = 160
	processDescribeDecodedFilter    = "decoded into the element's filter"
)

var (
	processDescribeModes          = []string{"summary", "full", "versions", "element"}
	processDescribeLocalized      = map[string]bool{"caption": true, "description": true, "label": true, "displayValue": true, "valueDisplay": true, "recommendation": true, "hint": true, "subject": true, "results": true}
	processDescribeFactMembers    = []string{"version", "isActiveVersion", "activeVersionSchemaUId", "activeVersionName", "versionRootSchemaUId", "activeVersionSource", "versions", "versionsTruncatedAt", "versionReadWarning"}
	processDescribeCommandPrefix  = `{"exit-code":0,"execution-log-messages":[{"message-type":"Info","value":`
	processDescribeCommandSuffix  = `}],"correlation-id":"00000000-0000-0000-0000-000000000000"}`
	processDescribeElementLayout  = []string{"position", "size"}
	processDescribeFlowLayout     = []string{"geometry"}
	processDescribeFamilyStateKey = "tracing"
)

// processDescribeValidateMode is DescribeProcessCommand.ValidateMode: refused before anything is read.
func processDescribeValidateMode(mode, element string) string {
	normalized := strings.TrimSpace(mode)
	hasElement := strings.TrimSpace(element) != ""
	known := false
	for _, candidate := range processDescribeModes {
		if strings.EqualFold(candidate, normalized) {
			known = true
		}
	}
	if normalized != "" && !known {
		return fmt.Sprintf("mode '%s' is not supported; use one of %s.", normalized, strings.Join(processDescribeModes, ", "))
	}
	if strings.EqualFold(normalized, "element") && !hasElement {
		return "mode 'element' needs element=<name>."
	}
	if hasElement && normalized != "" && !strings.EqualFold(normalized, "element") {
		return fmt.Sprintf("element=<name> selects the element reply; it cannot be combined with mode '%s'.", normalized)
	}
	return ""
}

// processDescribeOmitDecodedFilters is OmitDecodedFilterPayloads: the raw DataSourceFilters value of an element
// whose decoded filter the server judged complete is left out, and the parameter says so.
func processDescribeOmitDecodedFilters(graph *jnode) {
	elements := graph.get("elements")
	if !elements.isArray() {
		return
	}
	for _, element := range elements.items {
		if !element.isObject() {
			continue
		}
		filter, complete := element.get("filter"), element.get("filterDecodedCompletely")
		parameters := element.get("parameters")
		if filter == nil || filter.kind == jkNull || complete == nil || complete.kind != jkBool || !complete.flag || !parameters.isArray() {
			continue
		}
		for _, parameter := range parameters.items {
			if !parameter.isObject() || !strings.EqualFold(processDescribeText(parameter, "name"), "DataSourceFilters") {
				continue
			}
			value := parameter.get("value")
			if value == nil || value.kind != jkString || value.text == "" {
				continue
			}
			// valueOmitted takes the place of value, where clio's model declares it.
			replaced := newObject()
			for _, key := range parameter.keys {
				if key == "value" {
					replaced.set("valueOmitted", newString(processDescribeDecodedFilter))
					continue
				}
				replaced.set(key, parameter.props[key])
			}
			*parameter = *replaced
		}
	}
}

// processDescribeModifiedOn is FormatModifiedOn: a value with a zone becomes UTC with a Z, one without is
// written without one; "" when there is none.
func processDescribeModifiedOn(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Format("2006-01-02T15:04:05")
		}
	}
	return ""
}

// processDescribeBuildReply is DescribeProcessCommand.BuildReply.
func processDescribeBuildReply(graph *jnode, mode, element string) (string, string) {
	if refusal := processDescribeValidateMode(mode, element); refusal != "" {
		return "", refusal
	}
	if strings.TrimSpace(element) != "" {
		view := processDescribeElementView(graph, element, processDescribeChecksum(graph))
		if view == nil {
			return "", processDescribeElementNotFound(graph, element)
		}
		return processDescribeWrite(view), ""
	}
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "versions" {
		return processDescribeWrite(processDescribeVersionsView(graph, processDescribeChecksum(graph))), ""
	}
	full := processDescribeWrite(graph)
	if normalized == "full" {
		return full, ""
	}
	if normalized != "summary" {
		labelled := full
		if processDescribeUTF16Len(full) > 2 {
			labelled = `{"mode":"full",` + full[1:]
		}
		// Measured as the tool reply the agent reads, in bytes, not as graph characters.
		if processDescribeUTF16Len(labelled) <= processDescribeAutoSummaryBytes && processDescribeCommandReplySize(labelled) <= processDescribeAutoSummaryBytes {
			return labelled, ""
		}
	}
	return processDescribeWrite(processDescribeSummaryReply(graph, processDescribeChecksum(graph), processDescribeUTF16Len(full))), ""
}

func processDescribeUTF16Len(text string) int {
	count := 0
	for _, r := range text {
		if r >= 0x10000 {
			count += 2
		} else {
			count++
		}
	}
	return count
}

// processDescribeCommandReplySize is McpResultSize.OfCommandReply: the bytes of the command envelope carrying
// reply as its Info message, with a correlation id of the real length.
func processDescribeCommandReplySize(reply string) int {
	var buffer bytes.Buffer
	writeMcpResultString(&buffer, reply)
	return len(processDescribeCommandPrefix) + buffer.Len() + len(processDescribeCommandSuffix)
}

// processDescribeWrite writes a view as compact JSON with clio's MCP result encoder.
func processDescribeWrite(node *jnode) string {
	var buffer bytes.Buffer
	writeMcpResultJSON(&buffer, node)
	return buffer.String()
}

func writeMcpResultJSON(buffer *bytes.Buffer, node *jnode) {
	switch {
	case node == nil:
		buffer.WriteString("null")
	case node.kind == jkString:
		writeMcpResultString(buffer, node.text)
	case node.kind == jkArray:
		buffer.WriteByte('[')
		for index, item := range node.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeMcpResultJSON(buffer, item)
		}
		buffer.WriteByte(']')
	case node.kind == jkObject:
		buffer.WriteByte('{')
		for index, key := range node.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeMcpResultString(buffer, key)
			buffer.WriteByte(':')
			writeMcpResultJSON(buffer, node.props[key])
		}
		buffer.WriteByte('}')
	default:
		node.writeJSON(buffer, true)
	}
}

// writeMcpResultString is McpResultJsonEncoder: System.Text.Json's UnsafeRelaxedJsonEscaping (which leaves HTML
// characters and non-ASCII text as they are, and escapes quotes, backslashes, controls, line and paragraph
// separators, unassigned and private-use code points and everything outside the BMP), plus every BMP Format
// character as \uXXXX.
func writeMcpResultString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			buffer.WriteString(`\"`)
			continue
		case '\\':
			buffer.WriteString(`\\`)
			continue
		case '\n':
			buffer.WriteString(`\n`)
			continue
		case '\r':
			buffer.WriteString(`\r`)
			continue
		case '\t':
			buffer.WriteString(`\t`)
			continue
		case '\b':
			buffer.WriteString(`\b`)
			continue
		case '\f':
			buffer.WriteString(`\f`)
			continue
		}
		escape := r < 0x20 || r >= 0x10000 || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Zl, unicode.Zp) ||
			!unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C)
		if !escape {
			buffer.WriteRune(r)
			continue
		}
		for _, unit := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(buffer, `\u%04X`, unit)
		}
	}
	buffer.WriteByte('"')
}

func processDescribeText(node *jnode, key string) string {
	if !node.isObject() {
		return ""
	}
	value := node.get(key)
	if value == nil || value.kind != jkString {
		return ""
	}
	return value.text
}

// processDescribeTextNode is the member as clio's typed string property writes it into a view: the text, or null.
func processDescribeTextNode(node *jnode, key string) *jnode {
	if !node.isObject() {
		return jsonNullNode()
	}
	value := node.get(key)
	if value == nil || value.kind != jkString {
		return jsonNullNode()
	}
	return newString(value.text)
}

// processDescribeChecksum is ProcessDigest.Checksum: 12 hex characters of SHA-256 over the compact graph without
// the version facts, tracing, its name and UId, every uid, the layout and the culture-resolved text.
func processDescribeChecksum(graph *jnode) string {
	copied := graph.clone()
	for _, member := range append(append(append([]string{}, processDescribeFactMembers...), processDescribeFamilyStateKey), "name", "schemaUId") {
		copied.remove(member)
	}
	processDescribeStripMembers(copied.get("elements"), processDescribeElementLayout)
	processDescribeStripMembers(copied.get("flows"), processDescribeFlowLayout)
	processDescribeStrip(copied, func(key string) bool { return key == "uid" })
	processDescribeStrip(copied, func(key string) bool {
		return processDescribeLocalized[key] || strings.HasSuffix(key, "Caption") || strings.HasSuffix(key, "Display")
	})
	digest := sha256.Sum256([]byte(processDescribeWrite(copied)))
	return hex.EncodeToString(digest[:6])
}

func processDescribeStripMembers(items *jnode, members []string) {
	if !items.isArray() {
		return
	}
	for _, item := range items.items {
		if item.isObject() {
			for _, member := range members {
				item.remove(member)
			}
		}
	}
}

func processDescribeStrip(node *jnode, drop func(string) bool) {
	switch {
	case node.isObject():
		for _, key := range append([]string{}, node.keys...) {
			if drop(key) {
				node.remove(key)
			}
		}
		for _, key := range node.keys {
			processDescribeStrip(node.props[key], drop)
		}
	case node.isArray():
		for _, item := range node.items {
			processDescribeStrip(item, drop)
		}
	}
}

// processDescribeElementView is ProcessDigest.BuildElement: nil when the process has no element by that name.
func processDescribeElementView(graph *jnode, name, checksum string) *jnode {
	wanted := strings.TrimSpace(name)
	elements := graph.get("elements")
	var found *jnode
	if elements.isArray() {
		for _, candidate := range elements.items {
			if candidate.isObject() && candidate.get("name") != nil && candidate.get("name").kind == jkString && candidate.get("name").text == wanted {
				found = candidate
				break
			}
		}
		if found == nil {
			for _, candidate := range elements.items {
				if candidate.isObject() && candidate.get("name") != nil && candidate.get("name").kind == jkString && strings.EqualFold(candidate.get("name").text, wanted) {
					found = candidate
					break
				}
			}
		}
	}
	if found == nil {
		return nil
	}
	elementName := processDescribeText(found, "name")
	flowsIn, flowsOut := newArray(), newArray()
	if flows := graph.get("flows"); flows.isArray() {
		for _, flow := range flows.items {
			if !flow.isObject() {
				continue
			}
			if target := flow.get("target"); target != nil && target.kind == jkString && target.text == elementName {
				flowsIn.items = append(flowsIn.items, flow)
			}
			if source := flow.get("source"); source != nil && source.kind == jkString && source.text == elementName {
				flowsOut.items = append(flowsOut.items, flow)
			}
		}
	}
	view := newObject()
	view.set("mode", newString("element"))
	view.set("name", processDescribeTextNode(graph, "name"))
	view.set("schemaUId", processDescribeTextNode(graph, "schemaUId"))
	view.set("checksum", newString(checksum))
	view.set("element", found)
	view.set("flowsIn", flowsIn)
	view.set("flowsOut", flowsOut)
	return view
}

func processDescribeElementNotFound(graph *jnode, element string) string {
	names := []string{}
	if elements := graph.get("elements"); elements.isArray() {
		for _, candidate := range elements.items {
			if name := strings.TrimSpace(processDescribeText(candidate, "name")); name != "" {
				names = append(names, processDescribeText(candidate, "name"))
			}
		}
	}
	listed := names
	if len(listed) > processDescribeMaxListedNames {
		listed = listed[:processDescribeMaxListedNames]
	}
	more := ""
	if len(names) > processDescribeMaxListedNames {
		more = fmt.Sprintf(" and %d more (mode=summary lists them unless it reports omitted elements; mode=full always does)",
			len(names)-processDescribeMaxListedNames)
	}
	return fmt.Sprintf("process '%s' has no element named '%s'. Elements: %s%s.", processDescribeText(graph, "name"),
		strings.TrimSpace(element), strings.Join(listed, ", "), more)
}

// processDescribeVersionsView is ProcessDigest.BuildVersions.
func processDescribeVersionsView(graph *jnode, checksum string) *jnode {
	view := newObject()
	view.set("mode", newString("versions"))
	view.set("name", processDescribeTextNode(graph, "name"))
	view.set("schemaUId", processDescribeTextNode(graph, "schemaUId"))
	view.set("checksum", newString(checksum))
	for _, member := range processDescribeFactMembers {
		if value := graph.get(member); value != nil {
			view.set(member, value.clone())
		}
	}
	return view
}

// ---- the summary ----

type processDescribeSummaryInput struct {
	graph      *jnode
	checksum   string
	elements   []*jnode
	flows      []string
	parameters []*jnode
}

func processDescribeObjects(node *jnode) []*jnode {
	items := []*jnode{}
	if node.isArray() {
		for _, item := range node.items {
			if item != nil && item.kind != jkNull {
				items = append(items, item)
			}
		}
	}
	return items
}

// processDescribeSummaryReply is BuildSummaryReply: mode first, the summary, then fullChars and the pointer.
func processDescribeSummaryReply(graph *jnode, checksum string, fullChars int) *jnode {
	envelope := newObject()
	envelope.set("fullChars", newInt(fullChars))
	envelope.set("more", newString(fmt.Sprintf("mode=full returns the whole graph (%d chars); element=<name> returns one element "+
		"in full (mappings, filters, formulas, its flows); mode=versions returns the version list.", fullChars)))
	return processDescribeFitSummary(graph, checksum, envelope)
}

// processDescribeFitSummary is ProcessDigest.FitSummary: the summary (BuildSummary when envelope is nil, the
// describe reply with mode first and the envelope's members last otherwise), fitted to 3,072 bytes.
func processDescribeFitSummary(graph *jnode, checksum string, envelope *jnode) *jnode {
	input := processDescribeSummaryInput{graph: graph, checksum: checksum, elements: processDescribeObjects(graph.get("elements")),
		parameters: processDescribeObjects(graph.get("parameters"))}
	for _, flow := range processDescribeObjects(graph.get("flows")) {
		input.flows = append(input.flows, processDescribeFlowLine(flow))
	}
	enveloped := func(summary *jnode) *jnode {
		if envelope == nil {
			return summary
		}
		reply := newObject()
		reply.set("mode", newString("summary"))
		for _, key := range summary.keys {
			reply.set(key, summary.props[key])
		}
		for _, key := range envelope.keys {
			reply.set(key, envelope.props[key].clone())
		}
		return reply
	}
	build := func(elements []*jnode, flows []string, parameters []*jnode, withCaptions, compactHeader bool) *jnode {
		return enveloped(processDescribeComposeSummary(input, elements, flows, parameters, withCaptions, compactHeader))
	}
	size := func(node *jnode) int { return len(processDescribeWrite(node)) }
	summary := build(input.elements, input.flows, input.parameters, true, false)
	if size(summary) <= processDescribeSummaryBudget {
		return summary
	}
	summary = build(input.elements, input.flows, input.parameters, false, false)
	if size(summary) <= processDescribeSummaryBudget {
		return summary
	}
	compact := false
	cut := func(share float64) *jnode {
		keep := func(count int) int { return int(float64(count) * share) }
		node := build(input.elements[:keep(len(input.elements))], input.flows[:keep(len(input.flows))],
			input.parameters[:keep(len(input.parameters))], false, compact)
		omitted := newObject()
		omitted.set("elements", newInt(len(input.elements)-keep(len(input.elements))))
		omitted.set("flows", newInt(len(input.flows)-keep(len(input.flows))))
		omitted.set("parameters", newInt(len(input.parameters)-keep(len(input.parameters))))
		node.set("omitted", omitted)
		return node
	}
	low, high := 0.0, 1.0
	best := cut(0)
	if size(best) > processDescribeSummaryBudget {
		compact = true
		best = cut(0)
	}
	for step := 0; step < processDescribeBisectionSteps; step++ {
		middle := (low + high) / 2
		candidate := cut(middle)
		if size(candidate) <= processDescribeSummaryBudget {
			best, low = candidate, middle
		} else {
			high = middle
		}
	}
	return best
}

func processDescribeComposeSummary(input processDescribeSummaryInput, elements []*jnode, flows []string, parameters []*jnode,
	withCaptions, compactHeader bool) *jnode {
	graph := input.graph
	summary := newObject()
	summary.set("name", processDescribeTextNode(graph, "name"))
	if !compactHeader {
		summary.set("caption", processDescribeTextNode(graph, "caption"))
	}
	summary.set("schemaUId", processDescribeTextNode(graph, "schemaUId"))
	if version := graph.get("version"); version != nil && version.kind == jkInteger {
		summary.set("version", version)
	}
	active := graph.get("isActiveVersion")
	if active != nil && active.kind == jkBool {
		summary.set("isActiveVersion", active)
		if !active.flag {
			for _, member := range []string{"activeVersionName", "activeVersionSchemaUId"} {
				if value := graph.get(member); value != nil && value.kind == jkString {
					summary.set(member, value)
				}
			}
		}
	}
	if versions := graph.get("versions"); versions.isArray() {
		summary.set("versionCount", newInt(len(versions.items)))
	}
	if warning := graph.get("versionReadWarning"); warning != nil && warning.kind == jkString {
		text := warning.text
		if units := utf16.Encode([]rune(text)); compactHeader && len(units) > processDescribeCompactWarning {
			text = string(utf16.Decode(units[:processDescribeCompactWarning])) + "..."
		}
		summary.set("versionReadWarning", newString(text))
	}
	summary.set("checksum", newString(input.checksum))
	elementEntries := newArray()
	signals := newArray()
	for _, element := range elements {
		entry := newObject()
		entry.set("name", processDescribeTextNode(element, "name"))
		entry.set("type", processDescribeElementType(element))
		if caption := processDescribeText(element, "caption"); withCaptions && strings.TrimSpace(caption) != "" && caption != processDescribeText(element, "name") {
			entry.set("caption", newString(caption))
		}
		elementEntries.items = append(elementEntries.items, entry)
		if signal := element.get("signal"); signal.isObject() {
			signalEntry := newObject()
			signalEntry.set("element", processDescribeTextNode(element, "name"))
			signalEntry.set("entity", processDescribeTextNode(signal, "entity"))
			signalEntry.set("on", processDescribeTextNode(signal, "on"))
			if columns := signal.get("changedColumns"); columns.isArray() && len(columns.items) > 0 {
				signalEntry.set("changedColumns", columns)
			}
			signals.items = append(signals.items, signalEntry)
		}
	}
	summary.set("elements", elementEntries)
	flowEntries := newArray()
	for _, flow := range flows {
		flowEntries.items = append(flowEntries.items, newString(flow))
	}
	summary.set("flows", flowEntries)
	if len(signals.items) > 0 {
		summary.set("startSignals", signals)
	}
	parameterEntries := newArray()
	for _, parameter := range parameters {
		kind := processDescribeTextNode(parameter, "type")
		if reference := processDescribeText(parameter, "referenceSchema"); strings.TrimSpace(reference) != "" {
			kind = newString(processDescribeText(parameter, "type") + "(" + reference + ")")
		}
		entry := newObject()
		entry.set("name", processDescribeTextNode(parameter, "name"))
		entry.set("type", kind)
		if direction := parameter.get("direction"); direction != nil && direction.kind == jkString {
			entry.set("direction", direction)
		}
		parameterEntries.items = append(parameterEntries.items, entry)
	}
	summary.set("parameters", parameterEntries)
	return summary
}

// processDescribeElementType is ProcessDigest.ElementType.
func processDescribeElementType(element *jnode) *jnode {
	if userTask := processDescribeText(element, "userTaskName"); strings.TrimSpace(userTask) != "" && strings.EqualFold(processDescribeText(element, "buildType"), "usertask") {
		return newString(userTask)
	}
	if buildType := element.get("buildType"); buildType != nil && buildType.kind == jkString {
		return newString(buildType.text)
	}
	return processDescribeTextNode(element, "type")
}

// processDescribeFlowLine is ProcessDigest.FlowLine.
func processDescribeFlowLine(flow *jnode) string {
	line := processDescribeText(flow, "source") + " -> " + processDescribeText(flow, "target")
	kind := processDescribeText(flow, "kind")
	if strings.TrimSpace(kind) == "" {
		kind = "sequence"
	}
	hasCondition := strings.TrimSpace(processDescribeText(flow, "condition")) != ""
	if strings.EqualFold(kind, "conditional") {
		decidedBy := "NO condition"
		if hasCondition {
			decidedBy = "condition"
		}
		if branches := flow.get("branchesOnActivityResult"); branches != nil && branches.kind == jkBool && branches.flag {
			decidedBy = "results"
		}
		return fmt.Sprintf("%s [%s, %s]", line, kind, decidedBy)
	}
	if strings.EqualFold(kind, "sequence") {
		if hasCondition {
			return line + " [condition text, not evaluated]"
		}
		return line
	}
	if hasCondition {
		return fmt.Sprintf("%s [%s, condition text, not evaluated]", line, kind)
	}
	return fmt.Sprintf("%s [%s]", line, kind)
}
