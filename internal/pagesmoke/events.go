package pagesmoke

import (
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// The page's own errors and navigations, from its CDP events (PageEventCollector), in order, so what happened
// after a given moment (a click) can be read.

const (
	maxDistinctPerPhase      = 200
	maxStoredEvents          = 20_000
	maxURLLength             = 120
	navigationsKeptAtEachEnd = 3
)

// PageConsoleError is an uncaught exception, console.error or failed request the page logged, with how often.
type PageConsoleError struct {
	Kind  string  `json:"kind"`
	Text  string  `json:"text"`
	Count int     `json:"count"`
	URL   *string `json:"url,omitzero"`
}

type loggedError struct {
	sequence int
	dropped  bool // an occurrence a cap dropped: counted as omitted
	kind     string
	text     string
	url      *string
}

type droppedRun struct {
	first  int
	count  int
	closed bool
}

type sequencedText struct {
	sequence int
	text     string
}

type eventCollector struct {
	mutex           sync.Mutex
	errors          []loggedError
	phaseKeys       map[string]bool
	droppedAtCap    []*droppedRun
	navigations     []sequencedText
	dialogs         []sequencedText
	relativeURL     func(string) string
	sequence        int
	navigationCount int
	dialogOpened    func()
}

func newEventCollector(relativeURL func(string) string) *eventCollector {
	return &eventCollector{phaseKeys: map[string]bool{}, relativeURL: relativeURL}
}

func (c *eventCollector) navigationTotal() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.navigationCount
}

// mark is a point in the event stream that also starts a new phase for the distinct-message cap.
func (c *eventCollector) mark() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.phaseKeys = map[string]bool{}
	if len(c.droppedAtCap) > 0 {
		c.droppedAtCap[len(c.droppedAtCap)-1].closed = true
	}
	return c.sequence
}

type eventObject map[string]json.RawMessage

func objectOf(raw json.RawMessage) eventObject {
	var object eventObject
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object
}

// text reads a string member; nil when absent or not a string.
func (o eventObject) text(name string) *string {
	if o == nil {
		return nil
	}
	if value, ok := stringMember(o, name); ok {
		return &value
	}
	return nil
}

func deref(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

func (c *eventCollector) onEvent(event cdpEvent) {
	params := objectOf(event.Params)
	switch event.Method {
	case "Runtime.exceptionThrown":
		c.addError("exception", readException(params), nil)
	case "Runtime.consoleAPICalled":
		if kind := deref(params.text("type")); kind == "error" || kind == "assert" {
			c.addError("console", readConsoleArguments(params), nil)
		}
	case "Log.entryAdded":
		if entry := objectOf(params["entry"]); entry != nil && deref(entry.text("level")) == "error" {
			c.addError("log", deref(entry.text("text")), entry.text("url"))
		}
	case "Page.frameNavigated":
		if frame := objectOf(params["frame"]); frame != nil {
			if _, child := frame["parentId"]; !child {
				c.addNavigation(deref(frame.text("url")))
			}
		}
	case "Page.navigatedWithinDocument":
		c.addNavigation(deref(params.text("url")))
	case "Page.javascriptDialogOpening":
		kind := "dialog"
		if value := params.text("type"); value != nil {
			kind = *value
		}
		c.addDialog(kind + ": " + deref(params.text("message")))
		if c.dialogOpened != nil {
			c.dialogOpened()
		}
	}
}

func errorKey(kind, text string, url *string) string {
	if url == nil {
		return kind + "\x00" + text + "\x00\x01"
	}
	return kind + "\x00" + text + "\x00" + *url
}

func (c *eventCollector) addError(kind, text string, url *string) {
	if blank(text) {
		return
	}
	firstLines := firstTwoLines(text)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.sequence++
	if len(c.errors) >= maxStoredEvents {
		if count := len(c.droppedAtCap); count > 0 && !c.droppedAtCap[count-1].closed {
			c.droppedAtCap[count-1].count++
			return
		}
		c.droppedAtCap = append(c.droppedAtCap, &droppedRun{first: c.sequence, count: 1})
		return
	}
	key := errorKey(kind, firstLines, url)
	if !c.phaseKeys[key] && len(c.phaseKeys) >= maxDistinctPerPhase {
		c.errors = append(c.errors, loggedError{sequence: c.sequence, dropped: true})
		return
	}
	c.phaseKeys[key] = true
	c.errors = append(c.errors, loggedError{sequence: c.sequence, kind: kind, text: firstLines, url: url})
}

// errorsBetween is ErrorsBetween: the distinct errors after `after` and up to upTo, in the order first seen, at
// most max; omitted counts the distinct ones past max and the occurrences a cap dropped.
func (c *eventCollector) errorsBetween(after, upTo, max int) ([]PageConsoleError, int) {
	c.mutex.Lock()
	var window []loggedError
	for _, logged := range c.errors {
		if logged.sequence > after && logged.sequence <= upTo {
			window = append(window, logged)
		}
	}
	droppedAtCap := 0
	for _, run := range c.droppedAtCap {
		if run.first > after && run.first <= upTo {
			droppedAtCap += run.count
		}
	}
	c.mutex.Unlock()
	dropped := droppedAtCap
	index := map[string]int{}
	distinct := []PageConsoleError{}
	for _, logged := range window {
		if logged.dropped {
			dropped++
			continue
		}
		key := errorKey(logged.kind, logged.text, logged.url)
		if position, ok := index[key]; ok {
			distinct[position].Count++
			continue
		}
		index[key] = len(distinct)
		text := "(empty)"
		if fenced := creatio.DataWriteUntrusted(logged.text); fenced != nil {
			text = *fenced
		}
		distinct = append(distinct, PageConsoleError{Kind: logged.kind, Text: text, Count: 1, URL: shortPath(logged.url)})
	}
	omitted := dropped
	if len(distinct) > max {
		omitted += len(distinct) - max
		distinct = distinct[:max]
	}
	return distinct, omitted
}

// navigationsSince folds consecutive repeats; a longer list keeps its first and last three.
func (c *eventCollector) navigationsSince(mark int) ([]string, int) {
	c.mutex.Lock()
	var urls []string
	for _, navigation := range c.navigations {
		if navigation.sequence > mark {
			urls = append(urls, navigation.text)
		}
	}
	c.mutex.Unlock()
	folded := []string{}
	for _, address := range urls {
		if len(folded) == 0 || folded[len(folded)-1] != address {
			folded = append(folded, address)
		}
	}
	const kept = navigationsKeptAtEachEnd * 2
	if len(folded) <= kept {
		return folded, 0
	}
	omitted := len(folded) - kept
	result := append([]string{}, folded[:navigationsKeptAtEachEnd]...)
	return append(result, folded[len(folded)-navigationsKeptAtEachEnd:]...), omitted
}

func (c *eventCollector) nativeDialogsSince(mark int) []string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	texts := []string{}
	for _, dialog := range c.dialogs {
		if dialog.sequence > mark && len(texts) < 3 {
			texts = append(texts, dialog.text)
		}
	}
	return texts
}

func (c *eventCollector) addNavigation(address string) {
	if blank(address) || strings.HasPrefix(strings.ToLower(address), "about:") {
		return
	}
	relative := capURL(c.relativeURL(address))
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.sequence++
	c.navigationCount++
	if len(c.navigations) < maxStoredEvents {
		c.navigations = append(c.navigations, sequencedText{c.sequence, relative})
	}
}

func (c *eventCollector) addDialog(text string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.sequence++
	if len(c.dialogs) < maxStoredEvents {
		c.dialogs = append(c.dialogs, sequencedText{c.sequence, text})
	}
}

// firstTwoLines: the message and the first stack frame place an error; the rest of a stack is noise.
func firstTwoLines(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if trimmed := dotnetTrim(line); trimmed != "" {
			lines = append(lines, trimmed)
			if len(lines) == 2 {
				break
			}
		}
	}
	return strings.Join(lines, " | ")
}

func readException(params eventObject) string {
	details := objectOf(params["exceptionDetails"])
	if details == nil {
		return ""
	}
	if exception := objectOf(details["exception"]); exception != nil {
		description := exception.text("description")
		if description == nil {
			description = exception.text("value")
		}
		if description != nil && !blank(*description) {
			return *description
		}
	}
	return deref(details.text("text"))
}

func readConsoleArguments(params eventObject) string {
	var args []json.RawMessage
	if json.Unmarshal(params["args"], &args) != nil {
		return ""
	}
	var parts []string
	for index, raw := range args {
		if index == 3 {
			break
		}
		if part := readConsoleArgument(objectOf(raw)); !blank(part) {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// readConsoleArgument: a primitive argument carries its value (as JsonElement.ToString writes it), an object only
// a description.
func readConsoleArgument(arg eventObject) string {
	value, ok := arg["value"]
	if !ok {
		return deref(arg.text("description"))
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	switch strings.TrimSpace(string(value)) {
	case "true":
		return "True"
	case "false":
		return "False"
	case "null":
		return ""
	}
	return strings.TrimSpace(string(value))
}

// shortPath reports a failed request by its path only: the host is the environment's, and a query can carry a token.
func shortPath(address *string) *string {
	if address == nil || blank(*address) {
		return nil
	}
	parsed, err := url.Parse(*address)
	if err != nil || !parsed.IsAbs() {
		return nil
	}
	path := parsed.EscapedPath()
	if path == "" && parsed.Host != "" {
		path = "/"
	}
	path = capURL(path)
	return &path
}

// capURL cuts an address to 120 UTF-16 units, marking the cut.
func capURL(address string) string {
	units := utf16.Encode([]rune(address))
	if len(units) <= maxURLLength {
		return address
	}
	return string(utf16.Decode(units[:maxURLLength-1])) + "…"
}
