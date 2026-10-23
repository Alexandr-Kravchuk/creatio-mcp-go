package workspace

// xmlDocument is the part of System.Xml.XmlDocument that clio's SolutionCreator uses: LoadXml without
// preserving whitespace, find or add <Project> nodes, and Save, which writes through XmlTextWriter with
// Formatting.Indented (two spaces, the host newline, " />" for an empty element, no newline at the end).

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

type xmlNode struct {
	kind     xmlNodeKind
	name     string
	attrs    []xml.Attr
	text     string
	children []*xmlNode
}

type xmlNodeKind int

const (
	xmlElement xmlNodeKind = iota
	xmlText
	xmlComment
	xmlDeclaration
	xmlProcInst
)

type xmlDocument struct{ nodes []*xmlNode }

func parseXML(text string) (*xmlDocument, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.Strict = true
	document := &xmlDocument{}
	var stack []*xmlNode
	add := func(node *xmlNode) {
		if len(stack) == 0 {
			document.nodes = append(document.nodes, node)
			return
		}
		parent := stack[len(stack)-1]
		parent.children = append(parent.children, node)
	}
	for {
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			node := &xmlNode{kind: xmlElement, name: qualified(typed.Name), attrs: append([]xml.Attr(nil), typed.Attr...)}
			add(node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("unexpected end element")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			value := string(typed)
			if strings.TrimSpace(value) == "" {
				continue // PreserveWhitespace is false: whitespace-only text is not kept
			}
			add(&xmlNode{kind: xmlText, text: value})
		case xml.Comment:
			add(&xmlNode{kind: xmlComment, text: string(typed)})
		case xml.ProcInst:
			kind := xmlProcInst
			if typed.Target == "xml" {
				kind = xmlDeclaration
			}
			add(&xmlNode{kind: kind, name: typed.Target, text: string(typed.Inst)})
		}
	}
	if len(stack) != 0 {
		return nil, errors.New("unexpected end of document")
	}
	return document, nil
}

func qualified(name xml.Name) string {
	if name.Space != "" {
		return name.Space + ":" + name.Local
	}
	return name.Local
}

// root is the document element.
func (d *xmlDocument) root() *xmlNode {
	for _, node := range d.nodes {
		if node.kind == xmlElement {
			return node
		}
	}
	return nil
}

func (n *xmlNode) attribute(name string) (string, bool) {
	for _, attr := range n.attrs {
		if qualified(attr.Name) == name {
			return attr.Value, true
		}
	}
	return "", false
}

func (n *xmlNode) setAttribute(name, value string) {
	for index, attr := range n.attrs {
		if qualified(attr.Name) == name {
			n.attrs[index].Value = value
			return
		}
	}
	n.attrs = append(n.attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

// descendants is SelectNodes(".//name") in document order.
func (n *xmlNode) descendants(name string) []*xmlNode {
	var found []*xmlNode
	for _, child := range n.children {
		if child.kind != xmlElement {
			continue
		}
		if child.name == name {
			found = append(found, child)
		}
		found = append(found, child.descendants(name)...)
	}
	return found
}

func (n *xmlNode) child(name string) *xmlNode {
	for _, child := range n.children {
		if child.kind == xmlElement && child.name == name {
			return child
		}
	}
	return nil
}

func (n *xmlNode) appendElement(name string) *xmlNode {
	node := &xmlNode{kind: xmlElement, name: name}
	n.children = append(n.children, node)
	return node
}

// save is XmlDocument.Save(path): the text, and whether a UTF-8 byte order mark precedes it (only when the
// declaration names an encoding, which XmlDocument then writes with Encoding.UTF8).
func (d *xmlDocument) save() (string, bool) {
	var builder strings.Builder
	bom := false
	first := true
	for _, node := range d.nodes {
		if !first {
			builder.WriteString(newLine())
		}
		first = false
		if node.kind == xmlDeclaration && strings.Contains(node.text, "encoding") {
			bom = true
		}
		writeXMLNode(&builder, node, 0)
	}
	return builder.String(), bom
}

func writeXMLNode(builder *strings.Builder, node *xmlNode, depth int) {
	switch node.kind {
	case xmlDeclaration, xmlProcInst:
		builder.WriteString("<?" + node.name)
		if node.text != "" {
			builder.WriteString(" " + node.text)
		}
		builder.WriteString("?>")
	case xmlComment:
		builder.WriteString("<!--" + node.text + "-->")
	case xmlText:
		builder.WriteString(escapeXMLText(node.text))
	case xmlElement:
		builder.WriteString("<" + node.name)
		for _, attr := range node.attrs {
			builder.WriteString(" " + qualified(attr.Name) + `="` + escapeXMLAttribute(attr.Value) + `"`)
		}
		if len(node.children) == 0 {
			builder.WriteString(" />")
			return
		}
		builder.WriteString(">")
		// XmlTextWriter stops indenting inside an element as soon as it holds text (mixed content).
		mixed := false
		for _, child := range node.children {
			if child.kind == xmlText {
				mixed = true
			}
		}
		for _, child := range node.children {
			if !mixed {
				builder.WriteString(newLine() + strings.Repeat("  ", depth+1))
			}
			writeXMLNode(builder, child, depth+1)
		}
		if !mixed {
			builder.WriteString(newLine() + strings.Repeat("  ", depth))
		}
		builder.WriteString("</" + node.name + ">")
	}
}

func escapeXMLText(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

func escapeXMLAttribute(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
		"\t", "&#x9;", "\n", "&#xA;", "\r", "&#xD;").Replace(text)
}
