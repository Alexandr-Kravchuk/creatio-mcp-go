package creatio

import (
	"errors"
	"strings"
	"unicode"
)

// This file is a structural reader for Classic client-unit JavaScript. clio parses these bodies with the
// Acornima JavaScript parser and walks the AST; this server has no JavaScript parser, so it tokenizes the
// body (strings, templates, comments and regular-expression literals are recognised and skipped), matches
// brackets, and reads only the shapes clio's walkers look at: the define() factory's returned object, object
// literal properties, function-valued properties and the calls inside them. A body whose tokens or brackets
// do not balance is treated as a syntax error. A body that tokenizes but that Acornima would reject is not
// detected, so such a layer is read where clio would skip it.

type classicPageTokenKind byte

const (
	classicPageIdent classicPageTokenKind = iota + 1
	classicPageString
	classicPageNumber
	classicPagePunct
	classicPageTemplate
	classicPageRegex
)

type classicPageToken struct {
	kind   classicPageTokenKind
	text   string // identifier name, punctuator, or a string literal's decoded value
	offset int    // rune offset of the token in the source
}

// classicPageScript is a tokenized body with each opening bracket mapped to its closing one.
type classicPageScript struct {
	tokens []classicPageToken
	match  []int
}

var errClassicPageSyntax = errors.New("javascript syntax error")

// classicPageSyntaxError places a syntax error in the source, worded as Acornima words the common cases.
type classicPageSyntaxError struct {
	offset  int
	message string
}

func (e classicPageSyntaxError) Error() string { return e.message }

// classicPageRegexAfter lists the keywords after which '/' starts a regular expression rather than a division.
var classicPageRegexAfter = map[string]bool{"return": true, "typeof": true, "case": true, "do": true, "else": true,
	"in": true, "of": true, "new": true, "delete": true, "void": true, "throw": true, "instanceof": true,
	"yield": true, "await": true}

func classicPageParse(source string) (*classicPageScript, error) {
	tokens, err := classicPageTokenize([]rune(source))
	if err != nil {
		return nil, err
	}
	match := make([]int, len(tokens))
	stack := []int{}
	pairs := map[string]string{")": "(", "]": "[", "}": "{"}
	for index, token := range tokens {
		match[index] = -1
		if token.kind != classicPagePunct {
			continue
		}
		switch token.text {
		case "(", "[", "{":
			stack = append(stack, index)
		case ")", "]", "}":
			if len(stack) == 0 || tokens[stack[len(stack)-1]].text != pairs[token.text] {
				return nil, classicPageSyntaxError{offset: token.offset, message: "Unexpected token '" + token.text + "'"}
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			match[open], match[index] = index, open
		}
	}
	if len(stack) > 0 {
		return nil, classicPageSyntaxError{offset: len([]rune(source)), message: "Unexpected end of input"}
	}
	return &classicPageScript{tokens: tokens, match: match}, nil
}

func classicPageTokenize(runes []rune) ([]classicPageToken, error) {
	tokens := []classicPageToken{}
	for index := 0; index < len(runes); {
		ch := runes[index]
		switch {
		case unicode.IsSpace(ch) || ch == 0xFEFF:
			index++
		case ch == '/' && index+1 < len(runes) && runes[index+1] == '/':
			for index < len(runes) && runes[index] != '\n' && runes[index] != '\r' {
				index++
			}
		case ch == '/' && index+1 < len(runes) && runes[index+1] == '*':
			commentStart := index
			closed := false
			for index += 2; index+1 < len(runes); index++ {
				if runes[index] == '*' && runes[index+1] == '/' {
					index += 2
					closed = true
					break
				}
			}
			if !closed {
				return nil, classicPageSyntaxError{offset: commentStart, message: "Unterminated comment"}
			}
		case ch == '"' || ch == '\'':
			value, next, err := classicPageReadString(runes, index)
			if err != nil {
				return nil, classicPageSyntaxError{offset: index, message: "Invalid or unexpected token"}
			}
			tokens = append(tokens, classicPageToken{kind: classicPageString, text: value, offset: index})
			index = next
		case ch == '`':
			next, err := classicPageSkipTemplate(runes, index)
			if err != nil {
				return nil, classicPageSyntaxError{offset: index, message: "Unterminated template"}
			}
			tokens = append(tokens, classicPageToken{kind: classicPageTemplate, offset: index})
			index = next
		case ch == '/' && classicPageRegexAllowed(tokens):
			next, err := classicPageSkipRegex(runes, index)
			if err != nil {
				return nil, classicPageSyntaxError{offset: index, message: "Unterminated regular expression"}
			}
			tokens = append(tokens, classicPageToken{kind: classicPageRegex, offset: index})
			index = next
		case unicode.IsDigit(ch) || (ch == '.' && index+1 < len(runes) && unicode.IsDigit(runes[index+1])):
			start := index
			for index < len(runes) && (unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index]) || runes[index] == '.' || runes[index] == '_') {
				index++
			}
			tokens = append(tokens, classicPageToken{kind: classicPageNumber, text: string(runes[start:index]), offset: start})
		case unicode.IsLetter(ch) || ch == '_' || ch == '$' || ch == '\\':
			start := index
			for index < len(runes) && (unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index]) || runes[index] == '_' || runes[index] == '$' || runes[index] == '\\') {
				index++
			}
			tokens = append(tokens, classicPageToken{kind: classicPageIdent, text: string(runes[start:index]), offset: start})
		default:
			text := string(ch)
			for _, candidate := range []string{"...", "=>", "?."} {
				if strings.HasPrefix(string(runes[index:min(index+3, len(runes))]), candidate) {
					text = candidate
					break
				}
			}
			tokens = append(tokens, classicPageToken{kind: classicPagePunct, text: text, offset: index})
			index += len([]rune(text))
		}
	}
	return tokens, nil
}

func classicPageRegexAllowed(tokens []classicPageToken) bool {
	if len(tokens) == 0 {
		return true
	}
	previous := tokens[len(tokens)-1]
	switch previous.kind {
	case classicPageIdent:
		return classicPageRegexAfter[previous.text]
	case classicPagePunct:
		if classicPagePostfixUpdate(tokens) {
			// count++ / total: the operand ends with a postfix ++ or --, so the slash divides.
			return false
		}
		return previous.text != ")" && previous.text != "]"
	}
	return false
}

// classicPagePostfixUpdate reports whether the tokens end with ++ or -- applied to an operand. The tokenizer
// emits each + or - separately, so the pair is checked together with what precedes it.
func classicPagePostfixUpdate(tokens []classicPageToken) bool {
	if len(tokens) < 3 {
		return false
	}
	first, second, operand := tokens[len(tokens)-2], tokens[len(tokens)-1], tokens[len(tokens)-3]
	if first.kind != classicPagePunct || second.kind != classicPagePunct || first.text != second.text ||
		(first.text != "+" && first.text != "-") || second.offset != first.offset+1 {
		return false
	}
	switch operand.kind {
	case classicPageIdent:
		return !classicPageRegexAfter[operand.text]
	case classicPageNumber, classicPageString, classicPageTemplate:
		return true
	case classicPagePunct:
		return operand.text == ")" || operand.text == "]"
	}
	return false
}

func classicPageReadString(runes []rune, start int) (string, int, error) {
	quote := runes[start]
	var value strings.Builder
	for index := start + 1; index < len(runes); index++ {
		ch := runes[index]
		switch {
		case ch == quote:
			return value.String(), index + 1, nil
		case ch == '\n' || ch == '\r':
			return "", 0, errClassicPageSyntax
		case ch == '\\' && index+1 < len(runes):
			index++
			switch escaped := runes[index]; escaped {
			case 'n':
				value.WriteRune('\n')
			case 't':
				value.WriteRune('\t')
			case 'r':
				value.WriteRune('\r')
			case '\r', '\n':
				// A line continuation contributes nothing to the value.
			default:
				value.WriteRune(escaped)
			}
		default:
			value.WriteRune(ch)
		}
	}
	return "", 0, errClassicPageSyntax
}

// classicPageSkipTemplate skips a template literal, including nested ${...} substitutions.
func classicPageSkipTemplate(runes []rune, start int) (int, error) {
	for index := start + 1; index < len(runes); index++ {
		switch runes[index] {
		case '\\':
			index++
		case '`':
			return index + 1, nil
		case '$':
			if index+1 < len(runes) && runes[index+1] == '{' {
				next, err := classicPageSkipSubstitution(runes, index+1)
				if err != nil {
					return 0, err
				}
				index = next - 1
			}
		}
	}
	return 0, errClassicPageSyntax
}

// classicPageSkipSubstitution skips the braces of a ${...} substitution that opens at start.
func classicPageSkipSubstitution(runes []rune, start int) (int, error) {
	depth := 0
	for index := start; index < len(runes); index++ {
		switch runes[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1, nil
			}
		case '"', '\'':
			_, next, err := classicPageReadString(runes, index)
			if err != nil {
				return 0, err
			}
			index = next - 1
		case '`':
			next, err := classicPageSkipTemplate(runes, index)
			if err != nil {
				return 0, err
			}
			index = next - 1
		}
	}
	return 0, errClassicPageSyntax
}

func classicPageSkipRegex(runes []rune, start int) (int, error) {
	inClass := false
	for index := start + 1; index < len(runes); index++ {
		switch runes[index] {
		case '\\':
			index++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '\n', '\r':
			return 0, errClassicPageSyntax
		case '/':
			if !inClass {
				index++
				for index < len(runes) && (unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index])) {
					index++
				}
				return index, nil
			}
		}
	}
	return 0, errClassicPageSyntax
}

func (s *classicPageScript) is(index int, kind classicPageTokenKind, text string) bool {
	return index >= 0 && index < len(s.tokens) && s.tokens[index].kind == kind && s.tokens[index].text == text
}

func (s *classicPageScript) punct(index int, text string) bool {
	return s.is(index, classicPagePunct, text)
}

// classicPageObject is an object literal: the token range of its braces and its properties.
type classicPageObject struct {
	script     *classicPageScript
	open, end  int
	properties []classicPageProperty
}

// classicPageProperty is one property of an object literal. name is empty for a computed or numeric key and
// for a spread element. The value occupies tokens [valueStart, valueEnd).
type classicPageProperty struct {
	name                 string
	valueStart, valueEnd int
	function             *classicPageFunction
}

// classicPageFunction is a function or arrow function value; its body occupies tokens [bodyStart, bodyEnd).
type classicPageFunction struct {
	bodyStart, bodyEnd int
}

// object reads the object literal whose '{' is at open, or nil when its contents are not a property list.
func (s *classicPageScript) object(open int) *classicPageObject {
	if !s.punct(open, "{") {
		return nil
	}
	end := s.match[open]
	object := &classicPageObject{script: s, open: open, end: end}
	for _, segment := range s.splitTopLevel(open+1, end) {
		property, ok := s.property(segment[0], segment[1])
		if !ok {
			return nil
		}
		object.properties = append(object.properties, property)
	}
	return object
}

// splitTopLevel splits tokens [start, end) at commas outside nested brackets; a trailing comma is allowed.
func (s *classicPageScript) splitTopLevel(start, end int) [][2]int {
	segments := [][2]int{}
	segmentStart := start
	for index := start; index < end; index++ {
		if s.match[index] > index {
			index = s.match[index]
			continue
		}
		if s.punct(index, ",") {
			segments = append(segments, [2]int{segmentStart, index})
			segmentStart = index + 1
		}
	}
	if segmentStart < end {
		segments = append(segments, [2]int{segmentStart, end})
	}
	return segments
}

func (s *classicPageScript) property(start, end int) (classicPageProperty, bool) {
	if start >= end {
		return classicPageProperty{}, false
	}
	if s.punct(start, "...") {
		return classicPageProperty{valueStart: start + 1, valueEnd: end}, true
	}
	keyIndex := start
	// get/set/async/generator modifiers in front of a method key.
	for keyIndex+1 < end && (s.punct(keyIndex, "*") || (s.tokens[keyIndex].kind == classicPageIdent &&
		(s.tokens[keyIndex].text == "get" || s.tokens[keyIndex].text == "set" || s.tokens[keyIndex].text == "async") &&
		!s.punct(keyIndex+1, ":") && !s.punct(keyIndex+1, "(") && !s.punct(keyIndex+1, ",") && !s.punct(keyIndex+1, "}"))) {
		keyIndex++
	}
	name, afterKey := "", keyIndex+1
	switch token := s.tokens[keyIndex]; {
	case token.kind == classicPageIdent || token.kind == classicPageString:
		name = token.text
	case token.kind == classicPageNumber:
	case s.punct(keyIndex, "["):
		afterKey = s.match[keyIndex] + 1
	default:
		return classicPageProperty{}, false
	}
	switch {
	case afterKey == end:
		// Shorthand property: { name }.
		return classicPageProperty{name: name, valueStart: keyIndex, valueEnd: end}, s.tokens[keyIndex].kind == classicPageIdent
	case s.punct(afterKey, ":"):
		for index := afterKey + 1; index < end; index++ {
			if s.match[index] > index {
				index = s.match[index]
			} else if s.punct(index, ";") {
				// A statement separator cannot appear in a property value.
				return classicPageProperty{}, false
			}
		}
		property := classicPageProperty{name: name, valueStart: afterKey + 1, valueEnd: end}
		property.function = s.functionAt(afterKey+1, end)
		return property, afterKey+1 < end
	case s.punct(afterKey, "("):
		// Method shorthand: name(params) { body }.
		body := s.match[afterKey] + 1
		if !s.punct(body, "{") || s.match[body] != end-1 {
			return classicPageProperty{}, false
		}
		return classicPageProperty{name: name, valueStart: keyIndex, valueEnd: end,
			function: &classicPageFunction{bodyStart: body + 1, bodyEnd: s.match[body]}}, true
	}
	return classicPageProperty{}, false
}

// functionAt reports the function expression or arrow function that spans exactly tokens [start, end).
func (s *classicPageScript) functionAt(start, end int) *classicPageFunction {
	index := start
	if s.is(index, classicPageIdent, "async") && index+1 < end {
		index++
	}
	if s.is(index, classicPageIdent, "function") {
		index++
		if s.punct(index, "*") {
			index++
		}
		if index < end && s.tokens[index].kind == classicPageIdent {
			index++
		}
		if !s.punct(index, "(") {
			return nil
		}
		body := s.match[index] + 1
		if !s.punct(body, "{") || s.match[body] != end-1 {
			return nil
		}
		return &classicPageFunction{bodyStart: body + 1, bodyEnd: s.match[body]}
	}
	arrow := -1
	switch {
	case s.punct(index, "("):
		arrow = s.match[index] + 1
	case index < end && s.tokens[index].kind == classicPageIdent:
		arrow = index + 1
	}
	if arrow < 0 || !s.punct(arrow, "=>") {
		return nil
	}
	if s.punct(arrow+1, "{") {
		if s.match[arrow+1] != end-1 {
			return nil
		}
		return &classicPageFunction{bodyStart: arrow + 2, bodyEnd: s.match[arrow+1]}
	}
	return &classicPageFunction{bodyStart: arrow + 1, bodyEnd: end}
}

// classicPageKeywords are identifiers followed by "(...) {" that open a statement block, not a method.
var classicPageKeywords = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"with": true, "function": true, "return": true, "typeof": true, "await": true, "yield": true}

// walk visits tokens [start, end) in order, skipping the bodies of nested functions, as clio's
// DescendantsSkippingNestedFunctions does. visit receives each visited token index.
func (s *classicPageScript) walk(start, end int, visit func(int)) {
	for index := start; index < end; index++ {
		if skipTo := s.nestedFunctionEnd(index, end); skipTo > index {
			index = skipTo - 1
			continue
		}
		visit(index)
	}
}

// nestedFunctionEnd returns the index after a function that starts at index, or -1.
func (s *classicPageScript) nestedFunctionEnd(index, end int) int {
	token := s.tokens[index]
	switch {
	case token.kind == classicPageIdent && token.text == "function" && !s.punct(index-1, "."):
		next := index + 1
		if s.punct(next, "*") {
			next++
		}
		if next < end && s.tokens[next].kind == classicPageIdent {
			next++
		}
		if s.punct(next, "(") && s.punct(s.match[next]+1, "{") {
			return s.match[s.match[next]+1] + 1
		}
	case s.punct(index, "=>"):
		if s.punct(index+1, "{") {
			return s.match[index+1] + 1
		}
		next := index + 1
		for next < end {
			if s.match[next] > next {
				next = s.match[next] + 1
				continue
			}
			if s.punct(next, ",") || s.punct(next, ";") || s.punct(next, ")") || s.punct(next, "]") || s.punct(next, "}") {
				break
			}
			next++
		}
		return next
	case token.kind == classicPageIdent && !classicPageKeywords[token.text] && s.punct(index+1, "(") &&
		(s.punct(index-1, "{") || s.punct(index-1, ",")) && s.punct(s.match[index+1]+1, "{"):
		// Method shorthand inside an object literal.
		return s.match[s.match[index+1]+1] + 1
	}
	return -1
}

// objectLiteralAt reports whether the '{' at index opens an object literal rather than a block.
func (s *classicPageScript) objectLiteralAt(index int) bool {
	if !s.punct(index, "{") {
		return false
	}
	if index == 0 {
		return false
	}
	previous := s.tokens[index-1]
	if previous.kind == classicPageIdent {
		return previous.text == "return" || previous.text == "yield"
	}
	if previous.kind != classicPagePunct {
		return false
	}
	switch previous.text {
	case "(", ",", ":", "[", "=", "?", "|", "&", "!":
		return true
	}
	return false
}

func (o *classicPageObject) property(name string) *classicPageProperty {
	for index := range o.properties {
		if o.properties[index].name == name {
			return &o.properties[index]
		}
	}
	return nil
}

// stringValue returns the property's value when it is exactly one string literal.
func (o *classicPageObject) stringValue(property *classicPageProperty) (string, bool) {
	if property == nil || property.valueEnd-property.valueStart != 1 {
		return "", false
	}
	token := o.script.tokens[property.valueStart]
	return token.text, token.kind == classicPageString
}

// objectValue returns the property's value when it is exactly one object literal.
func (o *classicPageObject) objectValue(property *classicPageProperty) *classicPageObject {
	if property == nil || !o.script.punct(property.valueStart, "{") || o.script.match[property.valueStart] != property.valueEnd-1 {
		return nil
	}
	return o.script.object(property.valueStart)
}
