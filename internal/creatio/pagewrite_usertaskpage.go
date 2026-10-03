package creatio

// create-user-task-page: clio's UserTaskPageScaffolder. Offline: in a local workspace it writes a new Classic
// parameter page (descriptor, metadata, properties, JavaScript body and caption resource) inheriting
// ProcessFlowElementPropertiesPage, links it to an existing user task (metadata FK11) and optionally stores
// SVG icons in the task's resources. Files are written the way .NET writes them (System.Text.Json indented,
// XDocument.ToString), so a workspace scaffolded here diffs like one scaffolded by clio.

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	userTaskPageParentName = "ProcessFlowElementPropertiesPage"
	userTaskPageParentUID  = "0f347363-31e5-4222-a82e-dcfeda34cbb6"
	userTaskPageSvgNS      = "http://www.w3.org/2000/svg"
	// UserTaskPageNote is clio's note on a created page.
	UserTaskPageNote = "Only workspace artifacts were changed. Deploy the package/workspace and verify process mappings after save/reopen."
)

var userTaskPageNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// UserTaskPageRequest is clio's CreateUserTaskPageArgs.
type UserTaskPageRequest struct {
	WorkspacePath, PackageName, UserTaskUID, PageName, Caption, Culture string
	SmallIconPath, LargeIconPath, TitleIconPath                         string
}

// UserTaskPageResult is clio's CommandExecutionResult with its note.
type UserTaskPageResult struct {
	CommandResult
	Note string `json:"note,omitempty"`
}

// IsGUIDText reports whether text is a GUID in the 36-character form System.Text.Json binds.
func IsGUIDText(text string) bool {
	return len(strings.TrimSpace(text)) == 36 && normalizeGUID(text) != ""
}

// CreateUserTaskPage is clio's CreateUserTaskPageTool: the created path, or the failure as a validation error.
func CreateUserTaskPage(request UserTaskPageRequest) UserTaskPageResult {
	path, err := userTaskPageCreate(request)
	if err != nil {
		return UserTaskPageResult{CommandResult: CommandFailure(err.Error())}
	}
	return UserTaskPageResult{CommandResult: NewCommandResult(0, "Info", "Created Classic parameter page: "+path), Note: UserTaskPageNote}
}

func userTaskPageValidName(name, argument string) error {
	if strings.TrimSpace(name) == "" || utf16Length(name) > 250 || !userTaskPageNamePattern.MatchString(name) {
		return fmt.Errorf("%s must be a valid schema identifier.", argument)
	}
	return nil
}

func userTaskPageReadJSON(path string) (*jnode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, userTaskPageFileError(path, err)
	}
	node, err := parseJNode([]byte(strings.TrimPrefix(string(data), "\ufeff")))
	if err != nil {
		return nil, err
	}
	if node == nil || node.kind == jkNull {
		return nil, fmt.Errorf("Invalid JSON: %s", path)
	}
	return node, nil
}

// userTaskPageFileError words a missing file as .NET's FileNotFoundException / DirectoryNotFoundException do.
func userTaskPageFileError(path string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		if _, dirErr := os.Stat(filepath.Dir(path)); dirErr != nil {
			return fmt.Errorf("Could not find a part of the path '%s'.", path)
		}
		return fmt.Errorf("Could not find file '%s'.", path)
	}
	return err
}

func userTaskPageText(node *jnode) string {
	if node != nil && node.kind == jkString {
		return node.text
	}
	return ""
}

func userTaskPageCreate(request UserTaskPageRequest) (string, error) {
	if err := userTaskPageValidName(request.PackageName, "package-name"); err != nil {
		return "", err
	}
	if err := userTaskPageValidName(request.PageName, "page-name"); err != nil {
		return "", err
	}
	taskUID := normalizeGUID(request.UserTaskUID)
	if strings.TrimSpace(request.WorkspacePath) == "" || taskUID == "" || taskUID == emptyGUID || strings.TrimSpace(request.Caption) == "" {
		return "", errors.New("workspace-path, user-task-uid and caption are required.")
	}
	cultureArg := request.Culture
	if cultureArg == "" {
		cultureArg = "en-US"
	}
	culture, problem := pageWriteCaptionCulture(cultureArg)
	if problem != "" {
		return "", fmt.Errorf("Culture is not supported. (Parameter 'name')\n%s is an invalid culture identifier.", cultureArg)
	}
	workspace, err := filepath.Abs(request.WorkspacePath)
	if err != nil {
		return "", err
	}
	settings, err := userTaskPageReadJSON(filepath.Join(workspace, ".clio", "workspaceSettings.json"))
	if err != nil {
		return "", err
	}
	member := false
	if packages := settings.get("Packages"); packages.isArray() {
		for _, item := range packages.items {
			if userTaskPageText(item) == request.PackageName {
				member = true
			}
		}
	}
	if !member {
		return "", errors.New("The package must belong to the workspace.")
	}
	packagePath := filepath.Join(workspace, "packages", request.PackageName)
	descriptor, err := userTaskPageReadJSON(filepath.Join(packagePath, "descriptor.json"))
	if err != nil {
		return "", err
	}
	packageDescriptor := descriptor.get("Descriptor")
	packageUID := normalizeGUID(userTaskPageText(packageDescriptor.get("UId")))
	if userTaskPageText(packageDescriptor.get("Name")) != request.PackageName || packageUID == "" || packageUID == emptyGUID {
		return "", errors.New("The package descriptor identity is invalid.")
	}
	schemasPath := filepath.Join(packagePath, "Schemas")
	var matches []string
	walkErr := filepath.WalkDir(schemasPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "descriptor.json" {
			return nil
		}
		candidate, err := userTaskPageReadJSON(path)
		if err != nil {
			return err
		}
		if strings.EqualFold(userTaskPageText(candidate.get("Descriptor").get("UId")), taskUID) {
			matches = append(matches, path)
		}
		return nil
	})
	if walkErr != nil {
		return "", userTaskPageFileError(schemasPath, walkErr)
	}
	if len(matches) != 1 {
		return "", errors.New("Exactly one matching user task must exist in the package.")
	}
	taskDescriptorRoot, err := userTaskPageReadJSON(matches[0])
	if err != nil {
		return "", err
	}
	taskDescriptor := taskDescriptorRoot.get("Descriptor")
	taskName := userTaskPageText(taskDescriptor.get("Name"))
	if err := userTaskPageValidName(taskName, "task name"); err != nil {
		return "", err
	}
	taskMetadataPath := filepath.Join(filepath.Dir(matches[0]), "metadata.json")
	taskRoot, err := userTaskPageReadJSON(taskMetadataPath)
	if err != nil {
		return "", err
	}
	task := taskRoot.get("MetaData").get("Schema")
	if task == nil || task.kind == jkNull {
		return "", errors.New("Task schema metadata is missing.")
	}
	if userTaskPageText(taskDescriptor.get("ManagerName")) != "ProcessUserTaskSchemaManager" ||
		userTaskPageText(task.get("ManagerName")) != "ProcessUserTaskSchemaManager" || userTaskPageText(task.get("A2")) != taskName ||
		normalizeGUID(userTaskPageText(task.get("UId"))) != taskUID || normalizeGUID(userTaskPageText(task.get("B6"))) != packageUID {
		return "", errors.New("Task descriptor, metadata and package identities must agree.")
	}
	if existing := userTaskPageText(task.get("FK11")); existing != "" && existing != emptyGUID {
		return "", errors.New("The user task already has a parameter page. Edit that page instead of replacing it.")
	}
	pagePath := filepath.Join(schemasPath, request.PageName)
	if userTaskPageDirExists(pagePath) {
		return "", errors.New("The page name already exists in the workspace; existing pages are never overwritten.")
	}
	if entries, err := os.ReadDir(filepath.Join(workspace, "packages")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && userTaskPageDirExists(filepath.Join(workspace, "packages", entry.Name(), "Schemas", request.PageName)) {
				return "", errors.New("The page name already exists in the workspace; existing pages are never overwritten.")
			}
		}
	}
	inputs, err := userTaskPageInputs(task)
	if err != nil {
		return "", err
	}
	taskResourcePath := filepath.Join(packagePath, "Resources", taskName+".ProcessUserTask", "resource."+culture+".xml")
	var taskResources *xmlNode
	if content, err := os.ReadFile(taskResourcePath); err == nil {
		if taskResources, err = xmlParse(string(content), true); err != nil {
			return "", err
		}
	} else {
		taskResources = userTaskPageNewResources(culture)
	}
	taskItems, err := userTaskPageItems(taskResources)
	if err != nil {
		return "", err
	}
	hasIcons, err := userTaskPageApplyIcons(taskItems, request)
	if err != nil {
		return "", err
	}
	pageResourcePath := filepath.Join(packagePath, "Resources", request.PageName+".ClientUnit", "resource."+culture+".xml")
	if userTaskPageDirExists(filepath.Dir(pageResourcePath)) {
		return "", errors.New("Resources already exist for the requested page; they were preserved.")
	}
	pageUID := schemaWriteNewGUID()
	stamp := fmt.Sprintf("/Date(%d)/", time.Now().UnixMilli())
	pageResources := userTaskPageNewResources(culture)
	items, _ := userTaskPageItems(pageResources)
	items.children = append(items.children, &xmlNode{kind: xmlElement, name: "Item", attrs: [][2]string{{"Name", "Caption"}, {"Value", request.Caption}}})
	descriptorJSON := toJNode(orderedFields{{"Descriptor", orderedFields{{"UId", pageUID}, {"Name", request.PageName},
		{"ModifiedOnUtc", stamp}, {"Parent", orderedFields{{"UId", userTaskPageParentUID}, {"Name", userTaskPageParentName}}},
		{"ManagerName", "ClientUnitSchemaManager"}, {"Caption", request.Caption}}}})
	artifacts := [][2]string{
		{filepath.Join(pagePath, "descriptor.json"), stjIndented(descriptorJSON)},
		{filepath.Join(pagePath, "metadata.json"), fmt.Sprintf("= MetaData.Schema.UId \"%s\"\n= MetaData.Schema.A2 \"%s\"\n= MetaData.Schema.A5 \"%s\"\n= MetaData.Schema.HD1 \"%s\"\n",
			pageUID, request.PageName, packageUID, userTaskPageParentUID)},
		{filepath.Join(pagePath, "properties.json"), `{"Properties":{"OptionalProperties":"{}","SchemaType":"EditViewModelSchema"}}`},
		{filepath.Join(pagePath, request.PageName+".js"), userTaskPageBody(request.PageName, inputs, taskItems)},
		{pageResourcePath, xmlDocumentString(pageResources)},
	}
	for _, artifact := range artifacts {
		if err := os.MkdirAll(filepath.Dir(artifact[0]), 0o755); err != nil {
			return "", err
		}
		file, err := os.OpenFile(artifact[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("The file '%s' already exists.", artifact[0])
			}
			return "", err
		}
		_, writeErr := file.WriteString(artifact[1])
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if hasIcons {
		if err := os.MkdirAll(filepath.Dir(taskResourcePath), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(taskResourcePath, []byte(xmlDocumentString(taskResources)), 0o644); err != nil {
			return "", err
		}
	}
	task.set("FK11", newString(pageUID))
	taskDescriptor.set("ModifiedOnUtc", newString(stamp))
	if err := os.WriteFile(taskMetadataPath, []byte(stjIndented(taskRoot)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(matches[0], []byte(stjIndented(taskDescriptorRoot)), 0o644); err != nil {
		return "", err
	}
	return filepath.Join(pagePath, request.PageName+".js"), nil
}

func userTaskPageDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// userTaskPageInputs is ReadInputNames: the In (0) and Variable (2) parameters, in metadata order.
func userTaskPageInputs(task *jnode) ([]string, error) {
	parameters := task.get("FJ1")
	if !parameters.isArray() {
		return nil, errors.New("Task parameter metadata is missing.")
	}
	inputs := []string{}
	seen := map[string]bool{}
	duplicate := false
	for _, parameter := range parameters.items {
		direction := 2
		if value := parameter.get("L12"); value != nil && value.kind != jkNull {
			parsed, err := strconv.Atoi(value.text)
			if value.kind != jkInteger || err != nil {
				return nil, errors.New("The requested operation requires an element of type 'Number', but the target element has type '" + tokenTypeName(value) + "'.")
			}
			direction = parsed
		}
		if direction < 0 || direction > 3 {
			return nil, errors.New("Unsupported parameter direction metadata.")
		}
		if direction != 0 && direction != 2 {
			continue
		}
		name := userTaskPageText(parameter.get("A2"))
		inputs = append(inputs, name)
	}
	for _, name := range inputs {
		if err := userTaskPageValidName(name, "parameter name"); err != nil {
			return nil, err
		}
	}
	for _, name := range inputs {
		if name == "UserTaskContainer" || name == "EditorsContainer" {
			return nil, errors.New("An input parameter name conflicts with a Classic page layout container.")
		}
		if seen[name] {
			duplicate = true
		}
		seen[name] = true
	}
	if duplicate {
		return nil, errors.New("Parameter names must be unique.")
	}
	return inputs, nil
}

func userTaskPageNewResources(culture string) *xmlNode {
	items := &xmlNode{kind: xmlElement, name: "Items"}
	group := &xmlNode{kind: xmlElement, name: "Group", attrs: [][2]string{{"Type", "String"}}, children: []*xmlNode{items}}
	root := &xmlNode{kind: xmlElement, name: "Resources", attrs: [][2]string{{"Culture", culture}}, children: []*xmlNode{group}}
	return &xmlNode{kind: xmlDocument, children: []*xmlNode{root}}
}

// userTaskPageItems is GetItems: the Items of the single String group.
func userTaskPageItems(document *xmlNode) (*xmlNode, error) {
	root := document.root()
	var group *xmlNode
	count := 0
	if root != nil {
		for _, child := range root.elements("Group") {
			if child.attr("Type") == "String" {
				group = child
				count++
			}
		}
	}
	if count != 1 {
		if count > 1 {
			return nil, errors.New("Sequence contains more than one matching element")
		}
		return nil, errors.New("Sequence contains no matching element")
	}
	items := group.elements("Items")
	if len(items) == 0 {
		return nil, errors.New("Expected a native String resource group with Items.")
	}
	return items[0], nil
}

func userTaskPageApplyIcons(items *xmlNode, request UserTaskPageRequest) (bool, error) {
	has := false
	for _, icon := range [][2]string{{"SmallSvgImage", request.SmallIconPath}, {"LargeSvgImage", request.LargeIconPath}, {"TitleSvgImage", request.TitleIconPath}} {
		if strings.TrimSpace(icon[1]) == "" {
			continue
		}
		info, err := os.Stat(icon[1])
		if err != nil {
			return false, userTaskPageFileError(icon[1], err)
		}
		if info.Size() > 1024*1024 {
			return false, errors.New("An SVG icon must be at most 1 MiB.")
		}
		content, err := os.ReadFile(icon[1])
		if err != nil {
			return false, err
		}
		if err := userTaskPageValidateSVG(content); err != nil {
			return false, err
		}
		matching := []int{}
		for index, child := range items.children {
			if child.kind == xmlElement && child.name == "Item" && child.attr("Name") == icon[0] {
				matching = append(matching, index)
			}
		}
		if len(matching) > 1 {
			return false, errors.New("Sequence contains more than one matching element")
		}
		if len(matching) == 1 {
			items.children = append(items.children[:matching[0]], items.children[matching[0]+1:]...)
		}
		items.children = append(items.children, &xmlNode{kind: xmlElement, name: "Item", attrs: [][2]string{{"Name", icon[0]},
			{"Type", "Image"}, {"ContentType", "Data"}, {"FileExtension", ".svg"}, {"Value", base64.StdEncoding.EncodeToString(content)}}})
		has = true
	}
	return has, nil
}

// userTaskPageValidateSVG is ValidateSvg: an SVG document of static basic shapes only.
func userTaskPageValidateSVG(content []byte) error {
	if len(content) > 1024*1024 {
		return errors.New("An SVG icon must be at most 1 MiB.")
	}
	text := strings.TrimLeft(strings.ToValidUTF8(string(content), "�"), "\ufeff")
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.Strict = true
	rejected := errors.New("SVG icons must use static basic shapes without styles, animation, event handlers or external references.")
	allowed := map[string]bool{"svg": true, "g": true, "path": true, "rect": true, "circle": true, "ellipse": true, "line": true,
		"polyline": true, "polygon": true, "title": true, "desc": true, "defs": true, "use": true}
	depth := 0
	rootChecked := false
	invalid := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.Directive:
			return errors.New("For security reasons DTD is prohibited in this XML document. To enable DTD processing set the DtdProcessing property on XmlReaderSettings to Parse and pass the settings into XmlReader.Create method.")
		case xml.ProcInst:
			if value.Target != "xml" {
				invalid = true
			}
		case xml.StartElement:
			if depth == 0 {
				if value.Name.Space != userTaskPageSvgNS || xmlLocalName(value.Name) != "svg" {
					return errors.New("An icon must be an SVG document.")
				}
				rootChecked = true
			} else if value.Name.Space != userTaskPageSvgNS || !allowed[xmlLocalName(value.Name)] {
				invalid = true
			}
			if depth > 0 {
				for _, attribute := range value.Attr {
					local := xmlLocalName(attribute.Name)
					if (attribute.Name.Space == "xml" || attribute.Name.Space == "http://www.w3.org/XML/1998/namespace") && local == "base" ||
						local == "style" || strings.Contains(strings.ToLower(attribute.Value), "url(") ||
						strings.HasPrefix(strings.ToLower(local), "on") || (local == "href" && !strings.HasPrefix(attribute.Value, "#")) {
						invalid = true
					}
				}
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if !rootChecked {
		return errors.New("Root element is missing.")
	}
	if invalid {
		return rejected
	}
	return nil
}

// userTaskPageBody is BuildBody: the Classic page body with one MAPPING attribute and one editor per input.
func userTaskPageBody(pageName string, inputs []string, resources *xmlNode) string {
	quote := func(value string) string { return string(toJNode(value).stjJSON()) }
	attributes := make([]string, 0, len(inputs))
	for _, name := range inputs {
		attributes = append(attributes, quote(name)+": {dataValueType: Terrasoft.DataValueType.MAPPING, type: Terrasoft.ViewModelColumnType.VIRTUAL_COLUMN, initMethod: \"initPropertySilent\", doAutoSave: true}")
	}
	diff := []string{"{operation: \"insert\", name: \"UserTaskContainer\", parentName: \"EditorsContainer\", propertyName: \"items\", values: {itemType: Terrasoft.ViewItemType.GRID_LAYOUT, items: []}}"}
	for index, name := range inputs {
		caption := name
		for _, child := range resources.children {
			if child.kind == xmlElement && child.name == "Item" && child.attr("Name") == "Parameters."+name+".Caption" {
				if value, ok := child.attrValue("Value"); ok {
					caption = value
				}
				break
			}
		}
		diff = append(diff, fmt.Sprintf("{operation: \"insert\", name: %s, parentName: \"UserTaskContainer\", propertyName: \"items\", values: {caption: %s, layout: {column: 0, row: %d, colSpan: 24}, controlConfig: {autocomplete: %s}, wrapClass: [\"top-caption-control\"]}}",
			quote(name), quote(caption), index, quote(name+"Mapping")))
	}
	return fmt.Sprintf("/** Inherits %s. Customize this Classic page after scaffolding. */\ndefine(%s, [\"terrasoft\"], function(Terrasoft) {\nreturn {attributes: {\n%s\n}, diff: /**SCHEMA_DIFF*/[\n%s\n]/**SCHEMA_DIFF*/};\n});\n",
		userTaskPageParentName, quote(pageName), strings.Join(attributes, ",\n"), strings.Join(diff, ",\n"))
}

// stjIndented writes JSON as System.Text.Json does with WriteIndented: two-space indentation, "key": value,
// the default (escaping) encoder, and the host's line break.
func stjIndented(node *jnode) string {
	var buffer bytes.Buffer
	stjIndentedWrite(&buffer, node, 0)
	return buffer.String()
}

func stjIndentedWrite(buffer *bytes.Buffer, node *jnode, depth int) {
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	indent := func(level int) {
		buffer.WriteString(newline)
		buffer.WriteString(strings.Repeat("  ", level))
	}
	switch {
	case node.isArray():
		if len(node.items) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteByte('[')
		for index, item := range node.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			stjIndentedWrite(buffer, item, depth+1)
		}
		indent(depth)
		buffer.WriteByte(']')
	case node.isObject():
		if len(node.keys) == 0 {
			buffer.WriteString("{}")
			return
		}
		buffer.WriteByte('{')
		for index, key := range node.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			writeJSONString(buffer, key, true)
			buffer.WriteString(": ")
			stjIndentedWrite(buffer, node.props[key], depth+1)
		}
		indent(depth)
		buffer.WriteByte('}')
	default:
		node.writeJSON(buffer, true)
	}
}

// --- a small XDocument: parse (insignificant whitespace dropped) and ToString() (indented, no declaration).

type xmlKind int

const (
	xmlDocument xmlKind = iota
	xmlElement
	xmlText
	xmlComment
	xmlCData
	xmlProcInst
)

type xmlNode struct {
	kind     xmlKind
	name     string
	attrs    [][2]string
	children []*xmlNode
	text     string
}

func (n *xmlNode) root() *xmlNode {
	for _, child := range n.children {
		if child.kind == xmlElement {
			return child
		}
	}
	return nil
}

func (n *xmlNode) elements(name string) []*xmlNode {
	var result []*xmlNode
	for _, child := range n.children {
		if child.kind == xmlElement && child.name == name {
			result = append(result, child)
		}
	}
	return result
}

func (n *xmlNode) attrValue(name string) (string, bool) {
	for _, attribute := range n.attrs {
		if attribute[0] == name {
			return attribute[1], true
		}
	}
	return "", false
}

func (n *xmlNode) attr(name string) string {
	value, _ := n.attrValue(name)
	return value
}

// xmlLocalName is the local part of an XML name.
func xmlLocalName(name xml.Name) string { return (name).Local }

func xmlRawName(name xml.Name) string {
	if name.Space != "" {
		return name.Space + ":" + xmlLocalName(name)
	}
	return xmlLocalName(name)
}

// xmlParse loads a document as XDocument.Load with LoadOptions.None (whitespace-only text dropped) and
// refuses a DTD as XmlReader with DtdProcessing.Prohibit does.
func xmlParse(text string, keepWhitespace bool) (*xmlNode, error) {
	decoder := xml.NewDecoder(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	decoder.Strict = true
	document := &xmlNode{kind: xmlDocument}
	stack := []*xmlNode{document}
	for {
		token, err := decoder.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		top := stack[len(stack)-1]
		switch value := token.(type) {
		case xml.StartElement:
			element := &xmlNode{kind: xmlElement, name: xmlRawName(value.Name)}
			for _, attribute := range value.Attr {
				element.attrs = append(element.attrs, [2]string{xmlRawName(attribute.Name), attribute.Value})
			}
			top.children = append(top.children, element)
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			content := string(value)
			if strings.TrimSpace(content) == "" && !keepWhitespace {
				continue
			}
			if last := len(top.children) - 1; last >= 0 && top.children[last].kind == xmlText {
				top.children[last].text += content
				continue
			}
			top.children = append(top.children, &xmlNode{kind: xmlText, text: content})
		case xml.Comment:
			top.children = append(top.children, &xmlNode{kind: xmlComment, text: string(value)})
		case xml.ProcInst:
			if value.Target == "xml" {
				continue
			}
			top.children = append(top.children, &xmlNode{kind: xmlProcInst, name: value.Target, text: string(value.Inst)})
		case xml.Directive:
			return nil, errors.New("For security reasons DTD is prohibited in this XML document. To enable DTD processing set the DtdProcessing property on XmlReaderSettings to Parse and pass the settings into XmlReader.Create method.")
		}
	}
	if document.root() == nil {
		return nil, errors.New("Root element is missing.")
	}
	return document, nil
}

// xmlDocumentString is XDocument.ToString(): indented by two spaces, no XML declaration, "<a />" for an empty
// element, and XmlWriter's escaping.
func xmlDocumentString(document *xmlNode) string {
	writer := &xmlIndentWriter{newline: "\n"}
	if runtime.GOOS == "windows" {
		writer.newline = "\r\n"
	}
	for _, child := range document.children {
		writer.node(child, 0)
	}
	return writer.buffer.String()
}

// xmlIndentWriter follows .NET's indenting XmlWriter: a node is indented unless text was already written in
// an enclosing element (mixed content, which descendants inherit; the root element starts afresh).
type xmlIndentWriter struct {
	buffer  bytes.Buffer
	newline string
	mixed   bool
}

func (w *xmlIndentWriter) indent(level int) {
	if !w.mixed && w.buffer.Len() > 0 {
		w.buffer.WriteString(w.newline + strings.Repeat("  ", level))
	}
}

func (w *xmlIndentWriter) node(node *xmlNode, level int) {
	switch node.kind {
	case xmlText:
		w.buffer.WriteString(xmlEscapeText(node.text))
		w.mixed = true
	case xmlComment:
		w.indent(level)
		w.buffer.WriteString("<!--" + node.text + "-->")
	case xmlProcInst:
		w.indent(level)
		w.buffer.WriteString("<?" + node.name + " " + node.text + "?>")
	case xmlElement:
		w.indent(level)
		w.buffer.WriteString("<" + node.name)
		for _, attribute := range node.attrs {
			w.buffer.WriteString(" " + attribute[0] + "=\"" + xmlEscapeAttribute(attribute[1]) + "\"")
		}
		if len(node.children) == 0 {
			w.buffer.WriteString(" />")
			return
		}
		w.buffer.WriteString(">")
		saved := w.mixed
		if level == 0 {
			w.mixed = false
		}
		for _, child := range node.children {
			w.node(child, level+1)
		}
		w.indent(level)
		w.buffer.WriteString("</" + node.name + ">")
		w.mixed = saved
	}
}

func xmlEscapeText(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

func xmlEscapeAttribute(text string) string {
	var buffer strings.Builder
	for _, r := range text {
		switch r {
		case '&':
			buffer.WriteString("&amp;")
		case '<':
			buffer.WriteString("&lt;")
		case '>':
			buffer.WriteString("&gt;")
		case '"':
			buffer.WriteString("&quot;")
		case '\n':
			buffer.WriteString("&#xA;")
		case '\r':
			buffer.WriteString("&#xD;")
		case '\t':
			buffer.WriteString("&#x9;")
		default:
			if r == utf8.RuneError {
				buffer.WriteRune(r)
				continue
			}
			buffer.WriteRune(r)
		}
	}
	return buffer.String()
}
